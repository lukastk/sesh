package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
	"github.com/lukastk/sesh/internal/store"
)

const (
	browserDefaultPageLimit         = 50
	browserMaxPageLimit             = 100
	browserDefaultTranscriptTail    = 1000
	browserMaxTranscriptTail        = 10000
	browserMaxTranscriptWindowBytes = 8 << 20
	browserMaxRequestBody           = 1 << 20
	browserMaxForwardBody           = 16 << 20
)

func decodeBrowserJSON(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, browserMaxRequestBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return fmt.Errorf("decode browser request body: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode browser request body: trailing JSON data")
	}
	return nil
}

type browserOwnerError struct {
	status int
	msg    string
}

func (e *browserOwnerError) Error() string { return e.msg }

func browserOwnerStatus(err error) int {
	var oe *browserOwnerError
	if errors.As(err, &oe) {
		return oe.status
	}
	return http.StatusInternalServerError
}

func browserErr(status int, format string, args ...any) error {
	return &browserOwnerError{status: status, msg: fmt.Sprintf(format, args...)}
}

func (d *Daemon) routesBrowser(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/browser/threads", d.handleBrowserThreads)
	mux.HandleFunc("POST /v1/browser/threads", d.handleBrowserThreadCreate)
	mux.HandleFunc("POST /v1/browser/threads/action", d.handleBrowserThreadAction)
	mux.HandleFunc("GET /v1/browser/threads/transcript", d.handleBrowserThreadTranscript)
	mux.HandleFunc("GET /v1/browser/threads/history", d.handleBrowserStoredHistory)
	mux.HandleFunc("GET /v1/browser/threads/presentation", d.handleBrowserStoredPresentation)
	mux.HandleFunc("GET /v1/browser/threads/media-presentation", d.handleBrowserStoredMediaPresentation)
	mux.HandleFunc("/v1/browser/threads/media-presentation", d.handleBrowserStoredMediaPresentationMethodNotAllowed)
	mux.HandleFunc("HEAD /v1/browser/threads/media-presentation", d.handleBrowserStoredMediaPresentationMethodNotAllowed)
	mux.HandleFunc("GET /v1/browser/threads/input-images", d.handleBrowserStoredInputImages)
	mux.HandleFunc("GET /v1/browser/input-images", d.handleBrowserStoredInputImageBytes)
	mux.HandleFunc("HEAD /v1/browser/input-images", d.handleBrowserStoredInputImageBytes)
	mux.HandleFunc("GET /v1/browser/threads/artifacts", d.handleBrowserStoredArtifacts)
	mux.HandleFunc("GET /v1/browser/threads/artifact-bytes", d.handleBrowserStoredArtifactBytes)
	d.routesBrowserTransfers(mux)
}

// browserThreadOwner resolves one thread from the authoritative local store or
// the replicated mesh records. More than one owner is a loud conflict: choosing
// one would turn replication corruption into a potentially destructive action.
func (d *Daemon) browserThreadOwner(id string) (string, error) {
	owners := map[string]bool{}
	if th, err := d.store.GetThread(id); err == nil {
		if th.Machine != "" && th.Machine != d.cfg.Machine {
			return "", browserErr(http.StatusConflict, "browser: local thread %s claims owner %q instead of this daemon %q", id, th.Machine, d.cfg.Machine)
		}
		owners[d.cfg.Machine] = true
	} else if !errors.Is(err, store.ErrThreadNotFound) {
		return "", err
	}
	d.view.mu.RLock()
	for machine, state := range d.view.machines {
		if th, ok := state.threads[id]; ok {
			if th.Machine != "" && th.Machine != machine {
				d.view.mu.RUnlock()
				return "", browserErr(http.StatusConflict, "browser: replicated thread %s claims owner %q inside machine %q", id, th.Machine, machine)
			}
			owners[machine] = true
		}
	}
	d.view.mu.RUnlock()
	if len(owners) == 0 {
		return "", browserErr(http.StatusNotFound, "browser: thread not found: %s", id)
	}
	if len(owners) > 1 {
		names := make([]string, 0, len(owners))
		for machine := range owners {
			names = append(names, machine)
		}
		sort.Strings(names)
		return "", browserErr(http.StatusConflict, "browser: thread %s has multiple owners %v; refusing to route", id, names)
	}
	for owner := range owners {
		return owner, nil
	}
	panic("unreachable")
}

func (d *Daemon) authorizeBrowserOwner(owner string) error {
	if owner == d.cfg.Machine {
		return nil
	}
	if !d.browserHub.Enabled {
		return browserErr(http.StatusForbidden, "browser hub is disabled; remote owner %q is not allowed", owner)
	}
	if !d.browserHub.TrustedPeers[owner] {
		return browserErr(http.StatusForbidden, "browser hub does not trust peer %q", owner)
	}
	if d.view.knownOffline()[owner] {
		return browserErr(http.StatusServiceUnavailable, "browser hub peer %q is offline", owner)
	}
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		return err
	}
	peer, ok := reg.Get(owner)
	if !ok {
		return browserErr(http.StatusBadGateway, "browser hub trusted peer %q is not registered", owner)
	}
	if peer.Transport() != "http" {
		return browserErr(http.StatusBadGateway, "browser hub peer %q must use the HTTP transport; arbitrary SSH relaying is not allowed", owner)
	}
	return nil
}

func (d *Daemon) browserPeer(owner string) (peers.Peer, error) {
	if err := d.authorizeBrowserOwner(owner); err != nil {
		return peers.Peer{}, err
	}
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		return peers.Peer{}, err
	}
	peer, ok := reg.Get(owner)
	if !ok {
		return peers.Peer{}, browserErr(http.StatusBadGateway, "browser hub peer %q is not registered", owner)
	}
	return peer, nil
}

type browserListFilter struct {
	Query    string   `json:"q"`
	Machine  string   `json:"m"`
	Tags     []string `json:"t"`
	Archived string   `json:"a"`
}

type browserCursor struct {
	Version int    `json:"v"`
	Filter  string `json:"f"`
	Updated int64  `json:"u"`
	ID      string `json:"i"`
}

func filterFingerprint(filter browserListFilter) string {
	b, _ := json.Marshal(filter)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

func encodeBrowserCursor(cursor browserCursor) string {
	b, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeBrowserCursor(raw, fingerprint string) (browserCursor, error) {
	if raw == "" {
		return browserCursor{}, nil
	}
	if len(raw) > 2048 {
		return browserCursor{}, fmt.Errorf("cursor is too long")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return browserCursor{}, fmt.Errorf("cursor is invalid")
	}
	var cursor browserCursor
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cursor); err != nil || cursor.Version != 1 || cursor.Filter != fingerprint || cursor.ID == "" {
		return browserCursor{}, fmt.Errorf("cursor does not match this thread query")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return browserCursor{}, fmt.Errorf("cursor is invalid")
	}
	return cursor, nil
}

func (d *Daemon) handleBrowserThreads(w http.ResponseWriter, r *http.Request) {
	if d.browserHub.Enabled {
		d.noteMeshDemand()
	}
	q := r.URL.Query()
	filter := browserListFilter{
		Query:    strings.TrimSpace(q.Get("query")),
		Machine:  q.Get("machine"),
		Tags:     append([]string(nil), q["tag"]...),
		Archived: q.Get("archived"),
	}
	if filter.Archived == "" {
		filter.Archived = "exclude"
	}
	if len(filter.Query) > 256 || len(filter.Machine) > 128 || len(filter.Tags) > 16 {
		writeError(w, http.StatusBadRequest, "browser threads: query, machine or tag filters exceed their limits")
		return
	}
	for _, tag := range filter.Tags {
		if tag == "" || len(tag) > 128 {
			writeError(w, http.StatusBadRequest, "browser threads: tags must be 1-128 bytes")
			return
		}
	}
	sort.Strings(filter.Tags)
	switch filter.Archived {
	case "exclude", "include", "only":
	default:
		writeError(w, http.StatusBadRequest, "browser threads: archived must be exclude, include or only")
		return
	}
	limit := browserDefaultPageLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > browserMaxPageLimit {
			writeError(w, http.StatusBadRequest, "browser threads: limit must be 1-100")
			return
		}
		limit = n
	}
	fingerprint := filterFingerprint(filter)
	cursor, err := decodeBrowserCursor(q.Get("cursor"), fingerprint)
	if err != nil {
		writeError(w, http.StatusBadRequest, "browser threads: "+err.Error())
		return
	}

	rows, err := d.browserThreadRows()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	filtered := rows[:0]
	for _, row := range rows {
		if browserThreadMatches(row, filter) {
			filtered = append(filtered, row)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].UpdatedAtUnix != filtered[j].UpdatedAtUnix {
			return filtered[i].UpdatedAtUnix > filtered[j].UpdatedAtUnix
		}
		return filtered[i].ID < filtered[j].ID
	})
	start := 0
	if cursor.ID != "" {
		start = len(filtered)
		for i, row := range filtered {
			if row.UpdatedAtUnix < cursor.Updated || (row.UpdatedAtUnix == cursor.Updated && row.ID > cursor.ID) {
				start = i
				break
			}
		}
	}
	end := start + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	page := append([]api.BrowserThreadSummary(nil), filtered[start:end]...)
	resp := api.BrowserThreadPage{
		Schema: api.SchemaVersion, Capabilities: []string{"browser.live-input-media-v1", "browser.stored-input-media-v1", "browser.stored-media-presentation-v2"}, Threads: page,
	}
	if end < len(filtered) && len(page) > 0 {
		last := page[len(page)-1]
		resp.NextCursor = encodeBrowserCursor(browserCursor{Version: 1, Filter: fingerprint, Updated: last.UpdatedAtUnix, ID: last.ID})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (d *Daemon) browserThreadRows() ([]api.BrowserThreadSummary, error) {
	now := time.Now().Unix()
	localRecords, err := d.store.ListThreads(true)
	if err != nil {
		return nil, err
	}
	localSnap := map[string]api.ThreadSnapshot{}
	for _, snap := range d.maint.snapshot().Threads {
		localSnap[snap.ID] = snap
	}
	owners := map[string]string{}
	rows := make([]api.BrowserThreadSummary, 0, len(localRecords))
	for _, th := range localRecords {
		if previous := owners[th.ID]; previous != "" {
			return nil, browserErr(http.StatusConflict, "browser: duplicate thread %s on %s and %s", th.ID, previous, d.cfg.Machine)
		}
		owners[th.ID] = d.cfg.Machine
		snap, ok := localSnap[th.ID]
		if !ok {
			snap.Thread = th
		}
		rows = append(rows, browserSummary(snap, d.cfg.Machine, true, now, false))
	}
	if !d.browserHub.Enabled {
		return rows, nil
	}
	d.view.mu.RLock()
	defer d.view.mu.RUnlock()
	for machine, state := range d.view.machines {
		if !d.browserHub.TrustedPeers[machine] {
			continue
		}
		for _, snap := range state.threads {
			if previous := owners[snap.ID]; previous != "" {
				return nil, browserErr(http.StatusConflict, "browser: duplicate thread %s on %s and %s", snap.ID, previous, machine)
			}
			owners[snap.ID] = machine
			staleAfter := int64((2 * d.mesh.idleInterval) / time.Second)
			if staleAfter < 30 {
				staleAfter = 30
			}
			stale := state.reachable && state.syncedAt > 0 && now-state.syncedAt > staleAfter
			rows = append(rows, browserSummary(snap, machine, state.reachable, state.syncedAt, stale))
		}
	}
	return rows, nil
}

func browserSummary(snap api.ThreadSnapshot, machine string, reachable bool, syncedAt int64, stale bool) api.BrowserThreadSummary {
	updated := snap.CreatedAtUnix
	if snap.LastActiveUnix > updated {
		updated = snap.LastActiveUnix
	}
	if snap.ArchivedAtUnix > updated {
		updated = snap.ArchivedAtUnix
	}
	state := "unknown"
	switch {
	case !reachable:
		state = "offline"
	case stale:
		state = "stale"
	case snap.Busy == api.BusyBusy:
		state = "running"
	case snap.Head == api.Headful && snap.Busy == api.BusyIdle:
		state = "idle"
	case snap.Head == api.Headless && snap.Busy == api.BusyIdle:
		state = "stopped"
	}
	tags := append([]string(nil), snap.Tags...)
	if tags == nil {
		tags = []string{}
	}
	return api.BrowserThreadSummary{
		ID: snap.ID, Name: snap.Name, Agent: snap.AgentKind, Machine: machine, Cwd: snap.Cwd,
		State: state, UpdatedAtUnix: updated, ParentID: snap.Parent, Tags: tags,
		Archived:          snap.Archived,
		RuntimeAvailable:  reachable && !stale && (snap.Head == api.Headful || snap.Busy == api.BusyBusy),
		TerminalAvailable: reachable && !stale && snap.Head == api.Headful,
		Reachable:         reachable, SyncedAtUnix: syncedAt, AgentSessionID: snap.AgentSessionID,
	}
}

func browserThreadMatches(row api.BrowserThreadSummary, filter browserListFilter) bool {
	if filter.Machine != "" && row.Machine != filter.Machine {
		return false
	}
	switch filter.Archived {
	case "exclude":
		if row.Archived {
			return false
		}
	case "only":
		if !row.Archived {
			return false
		}
	}
	for _, want := range filter.Tags {
		found := false
		for _, have := range row.Tags {
			if have == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if filter.Query == "" {
		return true
	}
	needle := strings.ToLower(filter.Query)
	hay := []string{row.Name, row.ID, row.Agent, row.Machine, row.Cwd, row.ParentID, row.AgentSessionID, strings.Join(row.Tags, " ")}
	for _, value := range hay {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}

func (d *Daemon) handleBrowserThreadCreate(w http.ResponseWriter, r *http.Request) {
	var req api.BrowserThreadCreateRequest
	if err := decodeBrowserJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	owner := req.Machine
	if owner == "" {
		owner = d.cfg.Machine
	}
	if err := d.authorizeBrowserOwner(owner); err != nil {
		writeError(w, browserOwnerStatus(err), err.Error())
		return
	}
	body, _ := json.Marshal(req.Request)
	d.serveBrowserCanonical(w, r, owner, http.MethodPost, "/v1/threads", nil, body)
}

func (d *Daemon) handleBrowserThreadAction(w http.ResponseWriter, r *http.Request) {
	var req api.BrowserThreadActionRequest
	if err := decodeBrowserJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !validCanonicalUUID(req.ThreadID) {
		writeError(w, http.StatusBadRequest, "browser action: thread_id must be a canonical UUID")
		return
	}
	path, body, err := browserActionRoute(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	owner, err := d.browserThreadOwner(req.ThreadID)
	if err != nil {
		writeError(w, browserOwnerStatus(err), err.Error())
		return
	}
	if err := d.authorizeBrowserOwner(owner); err != nil {
		writeError(w, browserOwnerStatus(err), err.Error())
		return
	}
	d.serveBrowserCanonical(w, r, owner, http.MethodPost, path, nil, body)
}

func browserActionRoute(req api.BrowserThreadActionRequest) (string, []byte, error) {
	force := false
	if req.Force != nil {
		force = *req.Force
	}
	marshal := func(v any) []byte { b, _ := json.Marshal(v); return b }
	switch req.Action {
	case "rename":
		if req.Name == nil || req.Parent != nil || req.Archived != nil || req.Force != nil || len(req.Add)+len(req.Remove) != 0 {
			return "", nil, fmt.Errorf("browser action rename requires only name")
		}
		return "/v1/threads/rename", marshal(api.RenameThreadRequest{ID: req.ThreadID, Name: *req.Name}), nil
	case "tag":
		if req.Name != nil || req.Parent != nil || req.Archived != nil || req.Force != nil || len(req.Add)+len(req.Remove) == 0 {
			return "", nil, fmt.Errorf("browser action tag requires add and/or remove only")
		}
		return "/v1/threads/tag", marshal(api.TagThreadRequest{ID: req.ThreadID, Add: req.Add, Remove: req.Remove}), nil
	case "reparent":
		if req.Parent == nil || req.Name != nil || req.Archived != nil || req.Force != nil || len(req.Add)+len(req.Remove) != 0 {
			return "", nil, fmt.Errorf("browser action reparent requires only parent")
		}
		return "/v1/threads/reparent", marshal(api.ReparentThreadRequest{ID: req.ThreadID, Parent: *req.Parent}), nil
	case "archive":
		if req.Archived == nil || req.Name != nil || req.Parent != nil || req.Force != nil || len(req.Add)+len(req.Remove) != 0 {
			return "", nil, fmt.Errorf("browser action archive requires only archived")
		}
		return "/v1/threads/archive", marshal(api.ArchiveThreadRequest{ID: req.ThreadID, Archived: *req.Archived}), nil
	case "stop":
		if req.Name != nil || req.Parent != nil || req.Archived != nil || len(req.Add)+len(req.Remove) != 0 {
			return "", nil, fmt.Errorf("browser action stop accepts only optional force")
		}
		return "/v1/threads/stop", marshal(api.StopThreadRequest{ID: req.ThreadID, Force: force}), nil
	case "resume":
		if req.Name != nil || req.Parent != nil || req.Archived != nil || len(req.Add)+len(req.Remove) != 0 {
			return "", nil, fmt.Errorf("browser action resume accepts only optional force")
		}
		return "/v1/threads/resume", marshal(api.ThreadResumeRequest{ID: req.ThreadID, Force: force}), nil
	default:
		return "", nil, fmt.Errorf("browser action %q is not allowed", req.Action)
	}
}

func (d *Daemon) handleBrowserThreadTranscript(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for key, values := range q {
		if (key != "id" && key != "tail") || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "browser transcript: only one id and optional tail are allowed")
			return
		}
	}
	id := q.Get("id")
	if !validCanonicalUUID(id) {
		writeError(w, http.StatusBadRequest, "browser transcript: id must be a canonical UUID")
		return
	}
	tail := browserDefaultTranscriptTail
	if raw := q.Get("tail"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > browserMaxTranscriptTail {
			writeError(w, http.StatusBadRequest, "browser transcript: tail must be 0-10000")
			return
		}
		tail = n
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
		peerQuery := url.Values{"id": []string{id}, "tail": []string{strconv.Itoa(tail)}}
		d.serveBrowserCanonical(w, r, owner, http.MethodGet, "/v1/browser/threads/transcript", peerQuery, nil)
		return
	}
	d.serveLocalBrowserTranscript(w, id, tail)
}

func (d *Daemon) serveLocalBrowserTranscript(w http.ResponseWriter, id string, tail int) {
	th, err := d.store.GetThread(id)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conversationGate(w, th, "transcript") {
		return
	}
	homes := agents.ResolveHomes(d.cfg.CodexHome)
	kind := agents.Kind(th.AgentKind)
	window, err := agents.ReadTranscriptWindow(kind, th.AgentSessionID, th.Cwd, homes, tail, browserMaxTranscriptWindowBytes)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	reply, count, err := agents.LastReplyPath(kind, window.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := api.BrowserTranscriptResponse{
		Schema: api.SchemaVersion, ID: id, Path: window.Path, Format: "native-jsonl",
		Lines: window.Lines, LastReply: reply, ReplyCount: count,
		Coverage: api.BrowserTranscriptCoverage{
			Source: "resolved-current-session-file", RequestedTail: tail,
			StartLine: window.StartLine, EndLine: window.EndLine, TotalLines: window.TotalLines,
			Complete: window.StartLine == 0, ByteLimited: window.ByteLimited,
		},
	}
	body, err := json.Marshal(resp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "browser transcript response could not be encoded")
		return
	}
	if len(body) > browserMaxForwardBody {
		writeError(w, http.StatusRequestEntityTooLarge, "browser transcript response cannot be returned within the 16 MiB limit")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// serveBrowserCanonical invokes one fixed canonical daemon route locally or on
// one trusted HTTP peer. Callers supply only hard-coded paths; this is not a
// general proxy and deliberately has no public path/URL parameter.
func (d *Daemon) serveBrowserCanonical(w http.ResponseWriter, r *http.Request, owner, method, path string, query url.Values, body []byte) {
	if owner == d.cfg.Machine {
		target := path
		if len(query) > 0 {
			target += "?" + query.Encode()
		}
		req := httptest.NewRequest(method, target, bytes.NewReader(body)).WithContext(r.Context())
		rec := httptest.NewRecorder()
		d.routes().ServeHTTP(rec, req)
		copyBrowserResponse(w, rec.Result())
		return
	}
	peer, err := d.browserPeer(owner)
	if err != nil {
		writeError(w, browserOwnerStatus(err), err.Error())
		return
	}
	token, err := peer.ResolveAPIToken()
	if err != nil {
		writeError(w, http.StatusBadGateway, "browser hub peer authentication is unavailable")
		return
	}
	target := "http://" + peer.ApiAddr + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeProxyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "browser hub could not build the peer request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "browser hub peer request failed")
		return
	}
	copyBrowserResponse(w, resp)
}

func copyBrowserResponse(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, browserMaxForwardBody+1))
	if err != nil || len(body) > browserMaxForwardBody {
		writeError(w, http.StatusBadGateway, "browser hub response exceeded its size limit")
		return
	}
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}
