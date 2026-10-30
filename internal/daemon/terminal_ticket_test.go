package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/tmux"
)

// Part D of the harness-display work package: CONFIRMATION, not change. The
// terminal WebSocket path has no agent-kind gate at all — it needs a thread
// record and a live pane, nothing else — so it already serves vanilla pi,
// mypi, claude and codex threads identically. These tests pin that, and pin the
// distinct refusal for a thread with no pane so a client can tell "wrong id"
// from "nothing to attach to".

// Ticket minting is kind-blind: every conversation kind (and a shell thread)
// gets the same terminal grant shape, bound to the same canonical path.
func TestTerminalTicketMintIsAgentAgnostic(t *testing.T) {
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	for _, kind := range []string{"pi", "mypi", "claude", "codex", api.ShellAgentKind} {
		id := uuid.NewString()
		thread := api.Thread{
			ID: id, Machine: "self", SessionName: "s-" + id, Cwd: t.TempDir(),
			AgentKind: kind, Name: "terminal-" + kind, Tags: []string{}, CreatedAtUnix: time.Now().Unix(),
		}
		if err := d.store.InsertThread(thread); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		body := []byte(`{"thread_id":"` + id + `","route":"terminal","protocol":"terminal-v1"}`)
		d.handleWSTicket(rec, httptest.NewRequest(http.MethodPost, "/v1/ws-ticket", bytes.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s terminal ticket = %d %s", kind, rec.Code, rec.Body.String())
		}
		var ticket api.WSTicketResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &ticket); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(ticket.Path, "/v1/threads/terminal?") ||
			!strings.Contains(ticket.Path, "id="+id) || !strings.Contains(ticket.Path, "protocol=terminal-v1") {
			t.Fatalf("%s terminal ticket path = %q", kind, ticket.Path)
		}
	}
}

// The refusal ladder is explicit and distinguishable: a malformed id, an
// unknown protocol, an unknown thread and a thread with no live pane are four
// different answers, and only the last one is about attachability.
func TestTerminalRefusalsAreDistinctPerCause(t *testing.T) {
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	get := func(query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		d.handleThreadTerminal(rec, httptest.NewRequest(http.MethodGet, "/v1/threads/terminal?"+query, nil))
		return rec
	}
	if rec := get("id=not-a-uuid"); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed id = %d %s", rec.Code, rec.Body.String())
	}
	known := uuid.NewString()
	if rec := get("id=" + known + "&protocol=rpc-v9"); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown protocol = %d %s", rec.Code, rec.Body.String())
	}
	if rec := get("id=" + known); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown thread = %d %s", rec.Code, rec.Body.String())
	}
	// Every conversation kind gets the SAME pane-less refusal, and it says what
	// to do about it.
	for _, kind := range []string{"pi", "mypi", "claude", "codex"} {
		thread := insertDisplayThread(t, d, kind, piHome, displayConversation)
		rec := get("id=" + thread.ID)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s pane-less terminal = %d %s", kind, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "has no live pane") {
			t.Fatalf("%s pane-less refusal = %s", kind, rec.Body.String())
		}
	}
}

// One REAL ticketed terminal attach over a real isolated tmux pane, on a thread
// whose recorded kind is mypi: bytes typed through the WebSocket reach the pane
// and its output comes back. The pane runs plain `cat`, so this proves the
// terminal path and deliberately does not claim to be a real-agent proof.
func TestTerminalTicketAttachesARealPaneForAnyKind(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	catBin, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("cat not available")
	}
	d := newBrowserTestDaemon(t, "self", "")
	sock := d.tmux.Socket()
	raw := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", sock, "kill-server").Run() }) //nolint:errcheck
	cwd := t.TempDir()
	if out, err := raw("-f", "/dev/null", "new-session", "-d", "-s", "attachable", "-c", cwd, "-x", "80", "-y", "24", catBin); err != nil {
		t.Fatalf("new-session: %v %s", err, out)
	}
	pane, err := raw("list-panes", "-t", "attachable", "-F", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if err := d.store.InsertThread(api.Thread{
		ID: id, Machine: "self", SessionName: "attachable", Cwd: cwd,
		AgentKind: "mypi", Name: "attachable", Tags: []string{},
		CreatedAtUnix: time.Now().Unix(), AgentSessionID: uuid.NewString(),
	}); err != nil {
		t.Fatal(err)
	}
	if out, err := raw("set-option", "-p", "-t", pane, tmux.ThreadIDOption, id); err != nil {
		t.Fatalf("mark pane: %v %s", err, out)
	}

	srv := httptest.NewServer(d.tokenAuth("secret", d.routes()))
	defer srv.Close()
	mint, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/ws-ticket",
		bytes.NewReader([]byte(`{"thread_id":"`+id+`","route":"terminal","protocol":"terminal-v1"}`)))
	mint.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(mint)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("mint = %d %s", resp.StatusCode, b)
	}
	var ticket api.WSTicketResponse
	if err := json.NewDecoder(resp.Body).Decode(&ticket); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+ticket.Path, nil)
	if err != nil {
		t.Fatalf("ticketed terminal dial: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "done") //nolint:errcheck
	// The documented post-open resize reaches the owner pty.
	if err := ws.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":91,"rows":37}`)); err != nil {
		t.Fatal(err)
	}
	if !eventuallyD(10*time.Second, func() bool {
		out, err := raw("list-clients", "-F", "#{client_width}x#{client_height}")
		return err == nil && strings.Contains(out, "91x37")
	}) {
		out, _ := raw("list-clients", "-F", "#{client_width}x#{client_height}")
		t.Fatalf("post-open resize never reached the pty; clients=%q", out)
	}
	marker := "TERMINAL-ANY-KIND-OK"
	if err := ws.Write(ctx, websocket.MessageBinary, []byte(marker+"\n")); err != nil {
		t.Fatal(err)
	}
	var seen []byte
	for !bytes.Contains(seen, []byte(marker)) {
		_, frame, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("read terminal output: %v; seen=%q", err, seen)
		}
		seen = append(seen, frame...)
		if len(seen) > 1<<20 {
			t.Fatal("terminal emitted over 1 MiB without the marker")
		}
	}
	// A ticket is single-use even on this path.
	if _, reuse, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+ticket.Path, nil); err == nil {
		t.Fatal("the terminal ticket was reusable")
	} else if reuse == nil || reuse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("terminal ticket reuse = %#v (%v)", reuse, err)
	}
}

// A pane-less thread's ticket still MINTS (the grant is about ownership, not
// attachability) and the refusal arrives at the upgrade, consuming the ticket.
// A client therefore learns "nothing to attach to" from the WS attempt, not
// from the mint — worth pinning because it is the plugin's observable order.
func TestTerminalTicketMintsBeforeThePaneCheck(t *testing.T) {
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	thread := insertDisplayThread(t, d, "codex", piHome, displayConversation)
	rec := httptest.NewRecorder()
	d.handleWSTicket(rec, httptest.NewRequest(http.MethodPost, "/v1/ws-ticket",
		bytes.NewReader([]byte(`{"thread_id":"`+thread.ID+`","route":"terminal","protocol":"terminal-v1"}`))))
	if rec.Code != http.StatusOK {
		t.Fatalf("pane-less mint = %d %s", rec.Code, rec.Body.String())
	}
	var ticket api.WSTicketResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &ticket); err != nil {
		t.Fatal(err)
	}
	upgrade := httptest.NewRequest(http.MethodGet, ticket.Path, nil)
	upgrade.Header.Set("Upgrade", "websocket")
	owner, ok := d.authenticateWSTicket(upgrade, time.Now())
	if !ok || owner != "self" {
		t.Fatalf("ticket authentication = (%q, %v)", owner, ok)
	}
	if _, reused := d.authenticateWSTicket(upgrade, time.Now()); reused {
		t.Fatal("the ticket survived its first use")
	}
	rec = httptest.NewRecorder()
	d.handleThreadTerminal(rec, httptest.NewRequest(http.MethodGet, ticket.Path, nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "has no live pane") {
		t.Fatalf("pane-less terminal upgrade = %d %s", rec.Code, rec.Body.String())
	}
}
