package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/peers"
)

func browserSendBody(t *testing.T, req api.BrowserThreadActionRequest) *bytes.Reader {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(body)
}

func decodeSendResponse(t *testing.T, rec *httptest.ResponseRecorder) api.BrowserSendResponse {
	t.Helper()
	var resp api.BrowserSendResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode send response: %v: %s", err, rec.Body.String())
	}
	return resp
}

// callSend invokes the OWNER-side send directly, the way the action handler does
// after it has resolved and authorized the owner.
func callSend(t *testing.T, d *Daemon, req api.BrowserThreadActionRequest) (*httptest.ResponseRecorder, api.BrowserSendResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	d.serveBrowserSend(rec, httptest.NewRequest(http.MethodPost, "/v1/browser/threads/action", browserSendBody(t, req)), req, d.cfg.Machine)
	if rec.Code >= 400 && !strings.HasPrefix(rec.Body.String(), "{") {
		return rec, api.BrowserSendResponse{}
	}
	return rec, decodeSendResponse(t, rec)
}

func TestBrowserSendValidatesItsOwnFields(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	text := "hello"
	id := uuid.NewString()
	name := "nope"
	for _, tc := range []struct {
		why string
		req api.BrowserThreadActionRequest
	}{
		{"no text", api.BrowserThreadActionRequest{ThreadID: id, Action: "send", RequestID: uuid.NewString()}},
		{"empty text", api.BrowserThreadActionRequest{ThreadID: id, Action: "send", Text: ptrString(""), RequestID: uuid.NewString()}},
		{"no request_id", api.BrowserThreadActionRequest{ThreadID: id, Action: "send", Text: &text}},
		{"request_id not a uuid", api.BrowserThreadActionRequest{ThreadID: id, Action: "send", Text: &text, RequestID: "abc"}},
		{"lifecycle field", api.BrowserThreadActionRequest{ThreadID: id, Action: "send", Text: &text, RequestID: uuid.NewString(), Name: &name}},
		{"wait policy", api.BrowserThreadActionRequest{ThreadID: id, Action: "send", Text: &text, RequestID: uuid.NewString(), OnTyping: api.OnTypingWait}},
		{"on_typing on a headless send", api.BrowserThreadActionRequest{ThreadID: id, Action: "send_headless", Text: &text, RequestID: uuid.NewString(), OnTyping: api.OnTypingDefer}},
	} {
		rec, _ := callSend(t, d, tc.req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d %s", tc.why, rec.Code, rec.Body.String())
		}
	}
	// A refused request_id is NOT burned: the same id works once the request is
	// well-formed, because nothing was ever delivered under it.
	reused := uuid.NewString()
	if rec, _ := callSend(t, d, api.BrowserThreadActionRequest{ThreadID: id, Action: "send", RequestID: reused}); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed send = %d", rec.Code)
	}
	rec, _ := callSend(t, d, api.BrowserThreadActionRequest{ThreadID: id, Action: "send", Text: &text, RequestID: reused})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("re-used request_id after a refusal = %d %s", rec.Code, rec.Body.String())
	}
}

func ptrString(s string) *string { return &s }

// A thread with no live pane is refused_dead for `send` — explicitly, and
// without resuming anything.
func TestBrowserSendRefusesADeadThreadAndNeverResumesIt(t *testing.T) {
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	for _, kind := range []string{"pi", "mypi", "claude", "codex"} {
		thread := insertDisplayThread(t, d, kind, piHome, displayConversation)
		text := "are you there"
		rec, resp := callSend(t, d, api.BrowserThreadActionRequest{
			ThreadID: thread.ID, Action: "send", Text: &text, RequestID: uuid.NewString(),
		})
		if rec.Code != http.StatusConflict || resp.Outcome != api.BrowserSendRefusedDead {
			t.Fatalf("%s dead send = %d %+v", kind, rec.Code, resp)
		}
		if !strings.Contains(resp.Detail, "no live pane") {
			t.Fatalf("%s refusal detail = %q", kind, resp.Detail)
		}
	}
}

// send_headless starts a real turn on an idle thread, per kind, through the
// kind's own executable — and a replayed request_id does NOT start a second.
func TestBrowserSendHeadlessStartsOneTurnPerRequestID(t *testing.T) {
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	bin := t.TempDir()
	runLog := filepath.Join(bin, "runs")
	for _, name := range []string{"pi", "mypi"} {
		script := "#!/bin/sh\nprintf '%s\\n' \"" + name + " $*\" >> " + runLog + "\necho ok-from-" + name + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SHELL", "/bin/sh")
	d := newBrowserTestDaemon(t, "self", "")

	for _, kind := range []string{"pi", "mypi"} {
		thread := insertDisplayThread(t, d, kind, piHome, displayConversation)
		text := "run a turn with " + kind
		requestID := uuid.NewString()
		req := api.BrowserThreadActionRequest{ThreadID: thread.ID, Action: "send_headless", Text: &text, RequestID: requestID}
		rec, resp := callSend(t, d, req)
		if rec.Code != http.StatusOK || resp.Outcome != api.BrowserSendSent || resp.Replayed {
			t.Fatalf("%s headless send = %d %+v", kind, rec.Code, resp)
		}
		// `sent` is not completion: the response says so in as many words.
		if !strings.Contains(resp.Detail, "completion is observed separately") {
			t.Fatalf("%s headless detail = %q", kind, resp.Detail)
		}
		if !eventuallyD(10*time.Second, func() bool { return !d.turnInFlight(thread.ID) }) {
			t.Fatalf("%s headless turn never finished", kind)
		}
		// The turn really ran the executable: the captured reply is the fake
		// binary's own output, not an error the outcome would have hidden.
		d.hlMu.Lock()
		reply := d.hlReply[thread.ID]
		d.hlMu.Unlock()
		if reply != "ok-from-"+kind {
			t.Fatalf("%s headless reply = %q", kind, reply)
		}
		// The replay returns the RECORDED outcome and starts nothing.
		replayRec, replayResp := callSend(t, d, req)
		if replayRec.Code != http.StatusOK || replayResp.Outcome != api.BrowserSendSent || !replayResp.Replayed {
			t.Fatalf("%s replay = %d %+v", kind, replayRec.Code, replayResp)
		}
	}
	runs, err := os.ReadFile(runLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(runs)), "\n")
	if len(lines) != 2 {
		t.Fatalf("headless runs = %d, want exactly one per request_id: %q", len(lines), lines)
	}
	// Each kind ran its OWN executable with pi's headless flags.
	if !strings.HasPrefix(lines[0], "pi --print --session-id ") || !strings.HasPrefix(lines[1], "mypi --print --session-id ") {
		t.Fatalf("headless runs selected the wrong executables: %q", lines)
	}
	if !strings.HasSuffix(lines[0], "run a turn with pi") || !strings.HasSuffix(lines[1], "run a turn with mypi") {
		t.Fatalf("headless prompts = %q", lines)
	}
}

// A live pane owns the conversation, so a headless turn is refused_busy.
func TestBrowserSendHeadlessRefusesALivePane(t *testing.T) {
	f := newBrowserSendFixture(t)
	text := "concurrent"
	rec, resp := callSend(t, f.d, api.BrowserThreadActionRequest{
		ThreadID: f.th.ID, Action: "send_headless", Text: &text, RequestID: uuid.NewString(),
	})
	if rec.Code != http.StatusConflict || resp.Outcome != api.BrowserSendRefusedBusy {
		t.Fatalf("headless send into a live pane = %d %+v", rec.Code, resp)
	}
	if !strings.Contains(resp.Detail, "live pane") {
		t.Fatalf("refusal detail = %q", resp.Detail)
	}
}

// newBrowserSendFixture reuses the real-tmux paste fixture (a `cat` pane under
// an argv0-"claude" symlink, with a REAL nested client whose keystrokes bump the
// session's client_activity) and gives its bare daemon the pieces the browser
// send path needs.
type browserSendFixture struct{ *pasteFixture }

func newBrowserSendFixture(t *testing.T) *browserSendFixture {
	t.Helper()
	f := newPasteFixture(t)
	f.d.browserSends = newBrowserSendRegistry()
	f.d.cfg = config.Config{Machine: "test"}
	f.d.send = config.SendConfig{RespectTyping: 30 * time.Second, RespectTypingDeadline: time.Minute}
	return &browserSendFixture{pasteFixture: f}
}

// The typing guard is honoured on the browser path: a viewer who just typed
// makes a default send DEFER (queued, nothing pasted yet) and an explicit
// on_typing=skip refuse loudly. Neither reaches the pane while typing.
func TestBrowserSendHonoursTheTypingGuard(t *testing.T) {
	f := newBrowserSendFixture(t)
	f.typeThrough()
	time.Sleep(200 * time.Millisecond)

	skipText := "BROWSER-SKIPPED"
	rec, resp := callSend(t, f.d, api.BrowserThreadActionRequest{
		ThreadID: f.th.ID, Action: "send", Text: &skipText, RequestID: uuid.NewString(), OnTyping: api.OnTypingSkip,
	})
	if rec.Code != http.StatusConflict || resp.Outcome != api.BrowserSendSkippedTyping {
		t.Fatalf("skip send while typing = %d %+v", rec.Code, resp)
	}
	if strings.Contains(f.captured(), skipText) {
		t.Fatalf("a refused browser send reached the pane:\n%s", f.captured())
	}

	deferText := "BROWSER-DEFERRED"
	rec, resp = callSend(t, f.d, api.BrowserThreadActionRequest{
		ThreadID: f.th.ID, Action: "send", Text: &deferText, RequestID: uuid.NewString(),
	})
	if rec.Code != http.StatusOK || resp.Outcome != api.BrowserSendDeferred {
		t.Fatalf("default send while typing = %d %+v", rec.Code, resp)
	}
	if resp.DeadlineUnix == 0 {
		t.Fatalf("a deferred send carried no deadline: %+v", resp)
	}
	if n := f.d.pendingPastes(f.th.ID); n != 1 {
		t.Fatalf("queued deliveries = %d, want 1", n)
	}
	if strings.Contains(f.captured(), deferText) {
		t.Fatalf("a deferred browser send reached the pane before the pane was quiet:\n%s", f.captured())
	}
}

// With a quiet pane the same send lands in the REAL pane and reports sent.
func TestBrowserSendDeliversIntoAQuietPane(t *testing.T) {
	f := newBrowserSendFixture(t)
	f.d.send = config.SendConfig{RespectTyping: 0}
	text := "BROWSER-DELIVERED"
	rec, resp := callSend(t, f.d, api.BrowserThreadActionRequest{
		ThreadID: f.th.ID, Action: "send", Text: &text, RequestID: uuid.NewString(),
	})
	if rec.Code != http.StatusOK || resp.Outcome != api.BrowserSendSent {
		t.Fatalf("quiet-pane send = %d %+v", rec.Code, resp)
	}
	if !strings.Contains(resp.Detail, "observed separately") {
		t.Fatalf("sent detail must not claim completion: %q", resp.Detail)
	}
	if !eventuallyD(5*time.Second, func() bool { return strings.Contains(f.captured(), text) }) {
		t.Fatalf("the browser send never reached the pane:\n%s", f.captured())
	}
}

// A replay of a settled live-pane send returns the recorded outcome and does NOT
// deliver a second copy; a concurrent duplicate waits for the first attempt.
func TestBrowserSendReplayDeliversOnlyOnce(t *testing.T) {
	f := newBrowserSendFixture(t)
	f.d.send = config.SendConfig{RespectTyping: 0}
	text := "BROWSER-ONCE"
	req := api.BrowserThreadActionRequest{ThreadID: f.th.ID, Action: "send", Text: &text, RequestID: uuid.NewString()}
	var wg sync.WaitGroup
	results := make([]api.BrowserSendResponse, 2)
	codes := make([]int, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			f.d.serveBrowserSend(rec, httptest.NewRequest(http.MethodPost, "/v1/browser/threads/action", browserSendBody(t, req)), req, "test")
			codes[i] = rec.Code
			var resp api.BrowserSendResponse
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			results[i] = resp
		}(i)
	}
	wg.Wait()
	for i := range results {
		if codes[i] != http.StatusOK || results[i].Outcome != api.BrowserSendSent {
			t.Fatalf("concurrent attempt %d = %d %+v", i, codes[i], results[i])
		}
	}
	if results[0].Replayed == results[1].Replayed {
		t.Fatalf("exactly one concurrent attempt must be the replay: %v / %v", results[0].Replayed, results[1].Replayed)
	}
	if !eventuallyD(5*time.Second, func() bool { return strings.Contains(f.captured(), text) }) {
		t.Fatalf("the send never reached the pane:\n%s", f.captured())
	}
	if n := strings.Count(f.captured(), text); n != 1 {
		t.Fatalf("the message was delivered %d times:\n%s", n, f.captured())
	}
	// A third, sequential replay still re-reads the record rather than sending.
	rec, resp := callSend(t, f.d, req)
	if rec.Code != http.StatusOK || !resp.Replayed {
		t.Fatalf("sequential replay = %d %+v", rec.Code, resp)
	}
	if n := strings.Count(f.captured(), text); n != 1 {
		t.Fatalf("the replay delivered again: %d copies", n)
	}
}

// One request_id must not be reused for a different thread, action or message.
func TestBrowserSendRefusesARequestIDReusedForDifferentContent(t *testing.T) {
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	first := insertDisplayThread(t, d, "pi", piHome, displayConversation)
	second := insertDisplayThread(t, d, "pi", piHome, displayConversation)
	requestID := uuid.NewString()
	text := "same id"
	if rec, _ := callSend(t, d, api.BrowserThreadActionRequest{ThreadID: first.ID, Action: "send", Text: &text, RequestID: requestID}); rec.Code != http.StatusConflict {
		t.Fatalf("first send = %d", rec.Code)
	}
	other := "different text"
	for _, req := range []api.BrowserThreadActionRequest{
		{ThreadID: second.ID, Action: "send", Text: &text, RequestID: requestID},
		{ThreadID: first.ID, Action: "send_headless", Text: &text, RequestID: requestID},
		{ThreadID: first.ID, Action: "send", Text: &other, RequestID: requestID},
	} {
		rec, _ := callSend(t, d, req)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already used for a different") {
			t.Fatalf("reused request_id = %d %s", rec.Code, rec.Body.String())
		}
	}
}

// The browser action seam itself: send verbs obey the same owner policy as every
// other verb — refused with the hub disabled, routed to the owner with it on.
func TestBrowserSendObeysTheHubOwnerPolicy(t *testing.T) {
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	owner := newBrowserTestDaemon(t, "peer", "")
	thread := insertDisplayThread(t, owner, "claude", piHome, displayConversation)
	ownerSrv := httptest.NewServer(owner.tokenAuth("peer-secret", owner.routes()))
	defer ownerSrv.Close()
	snapshot := []api.ThreadSnapshot{{Thread: thread, Head: api.Headless, Busy: api.BusyIdle}}

	text := "remote send"
	action := api.BrowserThreadActionRequest{ThreadID: thread.ID, Action: "send", Text: &text, RequestID: uuid.NewString()}
	post := func(d *Daemon) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		d.handleBrowserThreadAction(rec, httptest.NewRequest(http.MethodPost, "/v1/browser/threads/action", browserSendBody(t, action)))
		return rec
	}

	closed := newBrowserTestDaemon(t, "hub", "")
	if err := closed.view.replaceAll("peer", time.Now().Unix(), snapshot); err != nil {
		t.Fatal(err)
	}
	if rec := post(closed); rec.Code != http.StatusForbidden {
		t.Fatalf("send with the hub disabled = %d %s", rec.Code, rec.Body.String())
	}

	hub := newBrowserTestDaemon(t, "hub", "[browser_hub]\nenabled = true\ntrusted_peers = [\"peer\"]\n")
	if err := hub.view.replaceAll("peer", time.Now().Unix(), snapshot); err != nil {
		t.Fatal(err)
	}
	reg := peers.Registry{Peers: map[string]peers.Peer{"peer": {Machine: "peer", ApiAddr: strings.TrimPrefix(ownerSrv.URL, "http://"), ApiToken: "peer-secret"}}}
	if err := reg.Save(hub.cfg.PeersPath()); err != nil {
		t.Fatal(err)
	}
	rec := post(hub)
	resp := decodeSendResponse(t, rec)
	// The OWNER answered: the thread has no pane there, so the refusal is the
	// owner's own, stamped with the owner machine.
	if rec.Code != http.StatusConflict || resp.Outcome != api.BrowserSendRefusedDead || resp.OwnerMachine != "peer" {
		t.Fatalf("routed send = %d %+v", rec.Code, resp)
	}
	if strings.Contains(rec.Body.String(), "peer-secret") {
		t.Fatal("the routed response leaked the peer token")
	}
	// The hub never records the outcome: the owner holds the recovery registry.
	if len(hub.browserSends.records) != 0 {
		t.Fatalf("the hub recorded a delivery it did not perform: %d", len(hub.browserSends.records))
	}
	if len(owner.browserSends.records) != 1 {
		t.Fatalf("the owner did not record the delivery: %d", len(owner.browserSends.records))
	}
}

// An unknown action is still refused by name, and the send verbs did not widen
// the allowlist for the canonical lifecycle routes.
func TestBrowserActionStillRefusesUnknownVerbs(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	rec := httptest.NewRecorder()
	body := []byte(`{"thread_id":"` + uuid.NewString() + `","action":"exec","text":"x"}`)
	d.handleBrowserThreadAction(rec, httptest.NewRequest(http.MethodPost, "/v1/browser/threads/action", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `action \"exec\" is not allowed`) {
		t.Fatalf("unknown action = %d %s", rec.Code, rec.Body.String())
	}
}

// The browser outcome must be decided by the delivery error's TYPE, not by its
// wording: an error message is read by humans and may be reworded, while the
// outcome is read by callers and governs whether a retry is allowed.
func TestBrowserSendOutcomeClassificationIsTypedNotTextual(t *testing.T) {
	for _, tc := range []struct {
		why     string
		err     error
		outcome string
		status  int
	}{
		// Reworded past all recognition, still a refusal.
		{"refused, unrecognizable text", deliveryRefused{errors.New("totally different wording")}, api.BrowserSendRefusedDead, http.StatusConflict},
		// Reworded so it no longer says "uncertain", still uncertain.
		{"uncertain, unrecognizable text", deliveryUncertain{errors.New("totally different wording")}, api.BrowserSendUncertain, http.StatusBadGateway},
		// The classification survives wrapping by an intermediate layer.
		{"wrapped refusal", fmt.Errorf("resolve: %w", deliveryRefused{errors.New("x")}), api.BrowserSendRefusedDead, http.StatusConflict},
		{"wrapped uncertainty", fmt.Errorf("deliver: %w", deliveryUncertain{errors.New("x")}), api.BrowserSendUncertain, http.StatusBadGateway},
		// An UNCLASSIFIED error must never become a clean refusal: that would
		// invite a resend of a message the agent may already be answering.
		{"unclassified", errors.New("nothing pasted; no live pane"), api.BrowserSendUncertain, http.StatusBadGateway},
	} {
		if got := browserSendFailureOutcome(tc.err); got != tc.outcome {
			t.Fatalf("%s: outcome = %q, want %q", tc.why, got, tc.outcome)
		}
		if got := browserSendFailureStatus(tc.err); got != tc.status {
			t.Fatalf("%s: status = %d, want %d", tc.why, got, tc.status)
		}
	}
	// The last case is the discriminating one against the old implementation:
	// its text matched the retired "nothing pasted" rule and would have been
	// classified as a refusal.
	if browserSendFailureOutcome(errors.New("nothing pasted")) != api.BrowserSendUncertain {
		t.Fatal("an untyped error was classified from its text")
	}
}

// The typed wrappers must not change what a human reads: the CLI, the daemon log
// and the auto-flag reason all print these exact strings today.
func TestDeliveryErrorsPreserveTheirMessageText(t *testing.T) {
	inner := errors.New("connection refused")
	refused := deliveryRefused{fmt.Errorf("Pi RPC connect: %w; nothing pasted", inner)}
	if refused.Error() != "Pi RPC connect: connection refused; nothing pasted" {
		t.Fatalf("refusal text = %q", refused.Error())
	}
	uncertain := deliveryUncertain{fmt.Errorf("Pi RPC delivery: %w; delivery may be uncertain, do not automatically retry; nothing pasted", inner)}
	if !strings.HasPrefix(uncertain.Error(), "Pi RPC delivery: connection refused; delivery may be uncertain") {
		t.Fatalf("uncertain text = %q", uncertain.Error())
	}
	// Both unwrap to the cause, so existing errors.Is/As chains still work.
	if !errors.Is(refused, inner) || !errors.Is(uncertain, inner) {
		t.Fatal("a typed delivery error dropped its cause")
	}
}

// The REAL pi delivery path produces those types at their real sites: a missing
// RPC socket is a typed refusal, and a socket that accepts the write but refuses
// the message is a typed UNCERTAINTY that the browser reports as `uncertain`.
// The socket is a protocol-faithful fixture, not a real Pi.
func TestPiDeliveryErrorsAreTypedAtTheirRealSites(t *testing.T) {
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	thread := insertDisplayThread(t, d, "mypi", piHome, displayConversation)

	// No socket at all: openPi refuses before writing anything.
	_, err := d.openPi(thread, "%0")
	var refused deliveryRefused
	if !errors.As(err, &refused) {
		t.Fatalf("missing-socket error is not a typed refusal: %#v", err)
	}
	if !strings.Contains(err.Error(), "Pi RPC socket missing for thread "+thread.ID) ||
		!strings.Contains(err.Error(), "nothing pasted") {
		t.Fatalf("missing-socket text changed: %q", err.Error())
	}

	// A socket that passes the pane check and then refuses the message.
	tmp := shortBrowserTempDir(t)
	t.Setenv("TMPDIR", tmp)
	dir := filepath.Join(tmp, "pi-rpc-sockets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(dir, thread.AgentSessionID+".sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	tmuxSocket := d.tmux.SocketPath()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadBytes('\n')
					if err != nil {
						return
					}
					var req map[string]any
					if json.Unmarshal(line, &req) != nil {
						return
					}
					reply := `{"ok":false,"error":"refused by fixture"}`
					if _, ok := req["getTmuxInfo"]; ok {
						info, _ := json.Marshal(map[string]any{
							"ok": true,
							"tmux": map[string]any{
								"inTmux": true, "paneId": "%0", "socketPath": tmuxSocket,
							},
						})
						reply = string(info)
					}
					if _, werr := conn.Write(append([]byte(reply), '\n')); werr != nil {
						return
					}
				}
			}()
		}
	}()

	err = d.deliverHeaded(thread, "%0", "steer me")
	var uncertain deliveryUncertain
	if !errors.As(err, &uncertain) {
		t.Fatalf("a refused Pi message is not a typed uncertainty: %#v (%v)", err, err)
	}
	if got := browserSendFailureOutcome(err); got != api.BrowserSendUncertain {
		t.Fatalf("outcome for a refused Pi message = %q", got)
	}
	if !strings.Contains(err.Error(), "delivery may be uncertain, do not automatically retry") {
		t.Fatalf("uncertain text changed: %q", err.Error())
	}
}
