package agents

// The Sesh-owned BASIC display projection: one bounded, paged, common view over
// any harness's own conversation file. It is deliberately not a second history
// model —
//
//   - it reads ONLY the resolved native file on the owner's disk. It never
//     starts, signals or asks an agent anything, so it answers for live,
//     headless-idle and stopped threads alike;
//   - it projects one item per native RECORD, with that record's own role, text,
//     thinking and tool interactions (see internal/agents/convo). Records a
//     format carries in bulk but that hold no conversation (claude's attachment
//     and api-request lines, codex's duplicate item_completed events, pi's
//     model_change) are counted in coverage, not rendered;
//   - it claims file coverage only. One native file is not an agent's complete
//     logical branch history, and Complete says the window covers the file, not
//     the conversation. For pi/mypi, Pi's own pi.session-history projection
//     remains the authority for rich history; this is the fallback that also
//     works for claude and codex.
//
// Bounds are per field and per page, and every truncation is flagged rather than
// silent.

import (
	"bufio"
	"fmt"
	"os"

	"github.com/lukastk/sesh/internal/agents/claude"
	"github.com/lukastk/sesh/internal/agents/codexfs"
	"github.com/lukastk/sesh/internal/agents/convo"
	"github.com/lukastk/sesh/internal/agents/pi"
)

// Page and field bounds. The item range matches the native stored-history page
// budget (1–500, default 200); the retained-byte budget matches the raw
// transcript window's 8 MiB so neither read can outgrow the other.
const (
	DisplayDefaultLimit = 200
	DisplayMaxLimit     = 500
	DisplayMaxBytes     = 8 << 20

	displayMaxText       = 64 << 10
	displayMaxThinking   = 16 << 10
	displayMaxToolInput  = 8 << 10
	displayMaxToolOutput = 16 << 10
	displayMaxTools      = 16
	// displayStatusTailLines is how much of the file's END the turn status is
	// derived from. It is always the tail, never the returned page: paging back
	// through history must not report a stale busy/idle.
	displayStatusTailLines = 400
)

// DisplayDirection selects which window of the file a request wants.
type DisplayDirection string

const (
	// DisplayTail is the newest page: the last Limit items in the file.
	DisplayTail DisplayDirection = "tail"
	// DisplayOlder pages BACK: the last Limit items whose source line is below
	// the anchor.
	DisplayOlder DisplayDirection = "older"
	// DisplayNewer polls FORWARD: the first Limit items whose source line is
	// above the anchor.
	DisplayNewer DisplayDirection = "newer"
)

// DisplaySelector is one page request. AnchorLine is a zero-based source line
// index and is ignored for DisplayTail.
type DisplaySelector struct {
	Direction  DisplayDirection
	AnchorLine int
	Limit      int
}

// DisplayTool is one bounded tool interaction.
type DisplayTool struct {
	CallID          string
	Name            string
	Input           string
	InputTruncated  bool
	Output          string
	OutputTruncated bool
	IsError         bool
}

// DisplayItem is one projected native record. Seq is its zero-based source line
// index — the cursor anchor, and the ordering key. ID is the harness's own
// record id where it has one, else "L<seq>"; both are stable for an append-only
// session file.
type DisplayItem struct {
	ID                string
	Seq               int
	Role              string
	NativeType        string
	NativeSubtype     string
	AtUnixMS          int64
	Text              string
	TextTruncated     bool
	Thinking          string
	ThinkingTruncated bool
	Tools             []DisplayTool
	ToolsDropped      int
	Injected          bool
}

// DisplayPage is one bounded page plus exact coverage of what produced it.
type DisplayPage struct {
	Path   string
	Format string
	Items  []DisplayItem
	// TurnStatus is busy|idle|unknown from the harness's own DeriveTurnStatus
	// over the file TAIL, independent of which page was requested.
	TurnStatus string
	// StartLine is the first returned item's source line and EndLine the last
	// one's plus one (both 0 on an empty page). TotalLines is the file's line
	// count at read time.
	StartLine  int
	EndLine    int
	TotalLines int
	// Complete means every projected item in the file is in this page.
	Complete bool
	// ByteLimited means the retained-byte budget, not the item limit, bounded
	// the page.
	ByteLimited bool
	// OlderAvailable means projected items exist before StartLine.
	OlderAvailable bool
	// NewerAnchor is the line a forward poll resumes STRICTLY ABOVE; -1 means
	// "from the start of the file". It never advances past an unparsed final
	// line (a partial write), and never past an item a filled page held back.
	NewerAnchor int
	// SkippedRecords counts records the format decoder read but that carry no
	// conversation content; UnparsedLines counts lines the decoder could not
	// read at all (invalid JSON, or a shape the format does not allow).
	SkippedRecords int
	UnparsedLines  int
}

// DisplayFormat names the native file format behind a kind's projection.
func DisplayFormat(kind Kind) (string, error) {
	switch Family(kind) {
	case Claude:
		return "claude-transcript-jsonl", nil
	case Pi:
		return "pi-session-jsonl", nil
	case Codex:
		return "codex-rollout-jsonl", nil
	default:
		return "", fmt.Errorf("display: unknown agent %q", kind)
	}
}

// displayRecord dispatches one raw line to the harness's own decoder.
func displayRecord(kind Kind, raw []byte) (convo.Record, bool, error) {
	switch Family(kind) {
	case Claude:
		return claude.DisplayRecord(raw)
	case Pi:
		return pi.DisplayRecord(raw)
	case Codex:
		return codexfs.DisplayRecord(raw)
	default:
		return convo.Record{}, false, fmt.Errorf("display: unknown agent %q", kind)
	}
}

// displayTurnStatus runs the harness's own DeriveTurnStatus over raw tail lines.
// A line the format's parser rejects is skipped: the status is a best reading of
// the markers present, and "unknown" is a real answer.
func displayTurnStatus(kind Kind, tail [][]byte) (string, error) {
	switch Family(kind) {
	case Claude:
		lines := make([]claude.Line, 0, len(tail))
		for _, raw := range tail {
			if l, ok, err := claude.ParseLine(raw); err == nil && ok {
				lines = append(lines, l)
			}
		}
		return string(claude.DeriveTurnStatus(lines)), nil
	case Pi:
		lines := make([]pi.Line, 0, len(tail))
		for _, raw := range tail {
			if l, ok, err := pi.ParseLine(raw); err == nil && ok {
				lines = append(lines, l)
			}
		}
		return string(pi.DeriveTurnStatus(lines)), nil
	case Codex:
		lines := make([]codexfs.Line, 0, len(tail))
		for _, raw := range tail {
			if l, ok, err := codexfs.ParseLine(raw); err == nil && ok {
				lines = append(lines, l)
			}
		}
		return string(codexfs.DeriveTurnStatus(lines)), nil
	default:
		return "", fmt.Errorf("display: unknown agent %q", kind)
	}
}

// ReadDisplayWindow resolves a thread's own conversation file and returns one
// bounded page of the display projection. A missing file is a LOUD error: the
// caller asked about a conversation that has none on this owner.
func ReadDisplayWindow(kind Kind, agentSessionID, cwd string, homes Homes, sel DisplaySelector) (DisplayPage, error) {
	format, err := DisplayFormat(kind)
	if err != nil {
		return DisplayPage{}, err
	}
	if sel.Limit < 1 || sel.Limit > DisplayMaxLimit {
		return DisplayPage{}, fmt.Errorf("display: limit must be 1-%d", DisplayMaxLimit)
	}
	switch sel.Direction {
	case DisplayTail, DisplayOlder, DisplayNewer:
	default:
		return DisplayPage{}, fmt.Errorf("display: unknown direction %q", sel.Direction)
	}
	// A forward poll accepts -1 ("from the start"); paging back from line 0 is a
	// legitimately empty page, so DisplayOlder accepts 0 but not less.
	if sel.Direction == DisplayOlder && sel.AnchorLine < 0 {
		return DisplayPage{}, fmt.Errorf("display: older cursor anchor must be non-negative")
	}
	if sel.Direction == DisplayNewer && sel.AnchorLine < -1 {
		return DisplayPage{}, fmt.Errorf("display: newer cursor anchor must be -1 or a line index")
	}
	path, found, err := TranscriptPath(kind, agentSessionID, cwd, homes)
	if err != nil {
		return DisplayPage{}, err
	}
	if !found {
		return DisplayPage{}, fmt.Errorf("no transcript on disk for this %s conversation (session %s)", kind, agentSessionID)
	}
	page, err := readDisplayWindow(kind, path, sel)
	page.Path = path
	page.Format = format
	return page, err
}

func readDisplayWindow(kind Kind, path string, sel DisplaySelector) (DisplayPage, error) {
	f, err := os.Open(path)
	if err != nil {
		return DisplayPage{}, err
	}
	defer f.Close()

	page := DisplayPage{}
	// accounted is the highest window line that decoded as JSON. A forward poll
	// resumes strictly above it, so an unparsed FINAL line — a partial write at
	// the tail — is re-read next poll instead of being skipped.
	accounted := -1
	if sel.Direction == DisplayNewer {
		accounted = sel.AnchorLine
	}
	var (
		items        []DisplayItem
		retained     int
		droppedFront bool
		droppedLimit bool
		full         bool
		statusTail   [][]byte
		line         int
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 32*1024*1024)
	for sc.Scan() {
		raw := sc.Bytes()
		// The status tail is a ring over the file's last lines; the copy is
		// required because the scanner reuses its buffer.
		statusTail = append(statusTail, append([]byte(nil), raw...))
		if len(statusTail) > displayStatusTailLines {
			statusTail = statusTail[1:]
		}
		current := line
		line++
		// A filled forward page keeps scanning only to count the file and fill
		// the status tail: the items above it belong to the next poll, and
		// `accounted` deliberately stops here so none of them is skipped.
		if full || !displayLineInWindow(sel, current) {
			continue
		}
		rec, ok, parseErr := displayRecord(kind, raw)
		if parseErr != nil {
			page.UnparsedLines++
			continue
		}
		accounted = current
		if !ok || rec.Empty() {
			page.SkippedRecords++
			continue
		}
		item := boundDisplayItem(rec, current)
		size := displayItemBytes(item)
		if sel.Direction == DisplayNewer {
			if len(items) > 0 && retained+size > DisplayMaxBytes {
				page.ByteLimited = true
				full, droppedLimit = true, true
				accounted = items[len(items)-1].Seq
				continue
			}
			items = append(items, item)
			retained += size
			if len(items) == sel.Limit {
				full, droppedLimit = true, true
				accounted = item.Seq
			}
			continue
		}
		items = append(items, item)
		retained += size
		for len(items) > sel.Limit || (len(items) > 1 && retained > DisplayMaxBytes) {
			if retained > DisplayMaxBytes {
				page.ByteLimited = true
			}
			retained -= displayItemBytes(items[0])
			items = items[1:]
			droppedFront, droppedLimit = true, true
		}
	}
	if err := sc.Err(); err != nil {
		return DisplayPage{}, err
	}
	page.TotalLines = line
	page.Items = items
	if len(items) > 0 {
		page.StartLine = items[0].Seq
		page.EndLine = items[len(items)-1].Seq + 1
	}
	switch sel.Direction {
	case DisplayOlder:
		// Nothing at or above the requested anchor was examined, so a forward
		// poll from this page must not claim those lines.
		page.NewerAnchor = sel.AnchorLine - 1
	default:
		page.NewerAnchor = accounted
	}
	page.Complete = sel.Direction == DisplayTail && !droppedLimit
	page.OlderAvailable = droppedFront || (sel.Direction == DisplayNewer && len(items) > 0 && items[0].Seq > 0)
	status, err := displayTurnStatus(kind, statusTail)
	if err != nil {
		return DisplayPage{}, err
	}
	page.TurnStatus = status
	return page, nil
}

// displayLineInWindow reports whether a source line belongs to the requested
// window. Selection is by SOURCE LINE, and the item limit is applied after
// projection, so a page of non-conversational records never pushes real items
// out of view.
func displayLineInWindow(sel DisplaySelector, line int) bool {
	switch sel.Direction {
	case DisplayOlder:
		return line < sel.AnchorLine
	case DisplayNewer:
		return line > sel.AnchorLine
	default:
		return true
	}
}

// boundDisplayItem applies the per-field bounds, flagging every truncation.
func boundDisplayItem(rec convo.Record, line int) DisplayItem {
	id := rec.ID
	if id == "" {
		id = fmt.Sprintf("L%d", line)
	}
	item := DisplayItem{
		ID: id, Seq: line, Role: rec.Role,
		NativeType: rec.NativeType, NativeSubtype: rec.NativeSubtype,
		AtUnixMS: rec.AtUnixMS, Injected: rec.Injected,
	}
	item.Text, item.TextTruncated = truncateDisplay(rec.Text, displayMaxText)
	item.Thinking, item.ThinkingTruncated = truncateDisplay(rec.Thinking, displayMaxThinking)
	tools := rec.Tools
	if len(tools) > displayMaxTools {
		item.ToolsDropped = len(tools) - displayMaxTools
		tools = tools[:displayMaxTools]
	}
	for _, t := range tools {
		out := DisplayTool{CallID: t.CallID, Name: t.Name, IsError: t.IsError}
		out.Input, out.InputTruncated = truncateDisplay(t.Input, displayMaxToolInput)
		out.Output, out.OutputTruncated = truncateDisplay(t.Output, displayMaxToolOutput)
		item.Tools = append(item.Tools, out)
	}
	return item
}

// truncateDisplay cuts a field to max bytes on a UTF-8 boundary, reporting
// whether it cut. Nothing is appended: the flag is the signal, not an ellipsis
// the client would have to parse back out.
func truncateDisplay(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// utf8Start reports whether b can start a UTF-8 sequence (i.e. is not a
// continuation byte).
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func displayItemBytes(item DisplayItem) int {
	n := len(item.ID) + len(item.Role) + len(item.NativeType) + len(item.NativeSubtype) + len(item.Text) + len(item.Thinking)
	for _, t := range item.Tools {
		n += len(t.CallID) + len(t.Name) + len(t.Input) + len(t.Output)
	}
	return n
}
