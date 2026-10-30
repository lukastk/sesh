package daemon

// GET /v1/browser/threads/display — the Sesh-owned basic display projection
// (capability browser.display-v1), owner-routed exactly like the raw transcript
// window.
//
// It answers for every harness sesh runs (claude, codex, pi, mypi) and for
// every state a thread can be in — live, headless-idle, stopped — because it
// reads only the resolved native file on the owner's disk. It NEVER starts or
// signals an agent to serve a read: there is no subprocess, no socket and no
// pane requirement anywhere on this path.
//
// It is labelled "sesh-display-v1" in the response because it is Sesh's own
// basic view, not a harness-owned history contract. For pi/mypi, Pi's
// pi.session-history projection (/v1/browser/threads/history) stays the
// authority for rich current-branch history; this route is the common fallback
// that also covers claude and codex.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

// displayCursor is the opaque page token. Fingerprint binds it to the thread,
// its agent kind and its CURRENT resolved source file, so a rotated native
// session (a claude compaction mints a new <id>.jsonl) refuses the stale cursor
// loudly instead of paging a different file's line numbers.
type displayCursor struct {
	Version     int    `json:"v"`
	Fingerprint string `json:"f"`
	Direction   string `json:"d"`
	Anchor      int    `json:"a"`
}

func encodeDisplayCursor(c displayCursor) string {
	b, _ := json.Marshal(c)
	return base64RawURL(b)
}

func decodeDisplayCursor(raw string) (displayCursor, error) {
	if len(raw) > 2048 {
		return displayCursor{}, fmt.Errorf("cursor is too long")
	}
	b, err := decodeBase64RawURL(raw)
	if err != nil {
		return displayCursor{}, fmt.Errorf("cursor is invalid")
	}
	var c displayCursor
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return displayCursor{}, fmt.Errorf("cursor is invalid")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return displayCursor{}, fmt.Errorf("cursor is invalid")
	}
	if c.Version != 1 || c.Fingerprint == "" {
		return displayCursor{}, fmt.Errorf("cursor is invalid")
	}
	switch agents.DisplayDirection(c.Direction) {
	case agents.DisplayOlder, agents.DisplayNewer:
	default:
		return displayCursor{}, fmt.Errorf("cursor is invalid")
	}
	return c, nil
}

func (d *Daemon) handleBrowserThreadDisplay(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for key, values := range q {
		if (key != "id" && key != "cursor" && key != "limit") || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "browser display: only one id and optional cursor and limit are allowed")
			return
		}
	}
	id := q.Get("id")
	if !validCanonicalUUID(id) {
		writeError(w, http.StatusBadRequest, "browser display: id must be a canonical UUID")
		return
	}
	limit := agents.DisplayDefaultLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > agents.DisplayMaxLimit {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("browser display: limit must be 1-%d", agents.DisplayMaxLimit))
			return
		}
		limit = n
	}
	cursor := displayCursor{}
	if raw := q.Get("cursor"); raw != "" {
		parsed, err := decodeDisplayCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "browser display: "+err.Error())
			return
		}
		cursor = parsed
	}
	owner, err := d.browserThreadOwner(id)
	if err != nil {
		writeError(w, browserOwnerStatus(err), err.Error())
		return
	}
	if err := d.authorizeBrowserOwner(owner); err != nil {
		writeError(w, browserOwnerStatus(err), err.Error())
		return
	}
	if owner != d.cfg.Machine {
		peerQuery := url.Values{"id": []string{id}, "limit": []string{strconv.Itoa(limit)}}
		if raw := q.Get("cursor"); raw != "" {
			peerQuery.Set("cursor", raw)
		}
		d.serveBrowserCanonical(w, r, owner, http.MethodGet, "/v1/browser/threads/display", peerQuery, nil)
		return
	}
	d.serveLocalBrowserDisplay(w, id, owner, limit, cursor)
}

func (d *Daemon) serveLocalBrowserDisplay(w http.ResponseWriter, id, owner string, limit int, cursor displayCursor) {
	th, err := d.store.GetThread(id)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conversationGate(w, th, "display") {
		return
	}
	kind := agents.Kind(th.AgentKind)
	if _, err := agents.DisplayFormat(kind); err != nil {
		writeError(w, http.StatusConflict, "browser display: "+err.Error())
		return
	}
	homes := agents.ResolveHomes(d.cfg.CodexHome)
	// The fingerprint is computed from the CURRENT resolution, before any read,
	// so a cursor minted against a previous native session file is refused here
	// rather than silently reinterpreted against the new one.
	path, found, err := agents.TranscriptPath(kind, th.AgentSessionID, th.Cwd, homes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound,
			fmt.Sprintf("browser display: no %s conversation file on this owner for session %q", th.AgentKind, th.AgentSessionID))
		return
	}
	fingerprint := displayFingerprint(id, th.AgentKind, path)
	sel := agents.DisplaySelector{Direction: agents.DisplayTail, Limit: limit}
	if cursor.Fingerprint != "" {
		if cursor.Fingerprint != fingerprint {
			writeError(w, http.StatusConflict,
				"browser display: cursor was minted against a different native session file for this thread; restart from the newest page")
			return
		}
		sel.Direction = agents.DisplayDirection(cursor.Direction)
		sel.AnchorLine = cursor.Anchor
	}
	page, err := agents.ReadDisplayWindow(kind, th.AgentSessionID, th.Cwd, homes, sel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "browser display: "+err.Error())
		return
	}
	items := make([]api.BrowserDisplayItem, 0, len(page.Items))
	for _, item := range page.Items {
		out := api.BrowserDisplayItem{
			ID: item.ID, Seq: item.Seq, Role: item.Role,
			NativeType: item.NativeType, NativeSubtype: item.NativeSubtype,
			AtUnixMS: item.AtUnixMS,
			Text:     item.Text, TextTruncated: item.TextTruncated,
			Thinking: item.Thinking, ThinkingTruncated: item.ThinkingTruncated,
			ToolsDropped: item.ToolsDropped, Injected: item.Injected,
		}
		for _, tool := range item.Tools {
			out.Tools = append(out.Tools, api.BrowserDisplayTool{
				CallID: tool.CallID, Name: tool.Name,
				Input: tool.Input, InputTruncated: tool.InputTruncated,
				Output: tool.Output, OutputTruncated: tool.OutputTruncated,
				IsError: tool.IsError,
			})
		}
		items = append(items, out)
	}
	resp := api.BrowserDisplayResponse{
		Schema: api.SchemaVersion, ID: id, OwnerMachine: owner,
		Source: browserDisplaySource, Agent: th.AgentKind, TurnStatus: page.TurnStatus,
		Items: items,
		Coverage: api.BrowserDisplayCoverage{
			Source: "resolved-current-session-file", Format: page.Format, Path: page.Path,
			RequestedLimit: limit, Direction: string(sel.Direction),
			StartLine: page.StartLine, EndLine: page.EndLine, TotalLines: page.TotalLines,
			Complete: page.Complete, ByteLimited: page.ByteLimited,
			SkippedRecords: page.SkippedRecords, UnparsedLines: page.UnparsedLines,
		},
		NewerCursor: encodeDisplayCursor(displayCursor{
			Version: 1, Fingerprint: fingerprint,
			Direction: string(agents.DisplayNewer), Anchor: page.NewerAnchor,
		}),
	}
	if page.OlderAvailable && len(page.Items) > 0 {
		resp.OlderCursor = encodeDisplayCursor(displayCursor{
			Version: 1, Fingerprint: fingerprint,
			Direction: string(agents.DisplayOlder), Anchor: page.StartLine,
		})
	}
	body, err := json.Marshal(resp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "browser display response could not be encoded")
		return
	}
	if len(body) > browserMaxForwardBody {
		writeError(w, http.StatusRequestEntityTooLarge, "browser display response cannot be returned within the 16 MiB limit")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
