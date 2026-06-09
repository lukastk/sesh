package conformance

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("api.tcp-auth", matrix.AgentAgnostic, matrix.Local, testAPITcpAuth)
	matrix.RegisterTest("api.tcp-parity", matrix.AgentAgnostic, matrix.Local, testAPITcpParity)
}

// apiDaemon is a daemon started with its TCP API enabled (SESH_API_ADDR + token).
type apiDaemon struct {
	addr   string
	token  string
	runner *localRunner
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// startAPIDaemon starts an isolated daemon whose TCP API is exposed on a free port
// with a token. Cleaned up with the test.
func startAPIDaemon(t *testing.T) *apiDaemon {
	t.Helper()
	bin := seshBin(t)
	home := t.TempDir()
	stamp := time.Now().UnixNano()
	socket := fmt.Sprintf("sesh-test-api-%d", stamp)
	addr := freePort(t)
	token := fmt.Sprintf("test-token-%d", stamp)
	env := map[string]string{
		"SESH_HOME":          home,
		"SESH_MACHINE":       fmt.Sprintf("api-%d", stamp),
		"SESH_TMUX_SOCKET":   socket,
		"SESH_MASTER_SOCKET": fmt.Sprintf("sesh-test-apimaster-%d", stamp),
		"SESH_CODEX_HOME":    setupCodexHome(t),
		"SESH_API_ADDR":      addr,
		"SESH_API_TOKEN":     token,
	}
	r := &localRunner{bin: bin, env: env}
	if _, stderr, err := r.Run(t, "daemon", "start"); err != nil {
		t.Fatalf("start api daemon: %v\n%s", err, stderr)
	}
	t.Cleanup(func() {
		r.Run(t, "daemon", "stop")                              //nolint:errcheck
		exec.Command("tmux", "-L", socket, "kill-server").Run() //nolint:errcheck
	})
	// Wait for the TCP listener to answer.
	if !waitUntil(10*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}) {
		t.Fatalf("api daemon never listened on %s", addr)
	}
	return &apiDaemon{addr: addr, token: token, runner: r}
}

func httpStatus(t *testing.T, url, token string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// testAPITcpAuth asserts the network API is gated by a bearer token (and refuses to
// run exposed without one).
func testAPITcpAuth(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	d := startAPIDaemon(t)
	url := "http://" + d.addr + "/v1/status"

	if got := httpStatus(t, url, ""); got != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", got)
	}
	if got := httpStatus(t, url, "wrong-token"); got != http.StatusUnauthorized {
		t.Errorf("wrong token: status %d, want 401", got)
	}
	if got := httpStatus(t, url, d.token); got != http.StatusOK {
		t.Errorf("correct token: status %d, want 200", got)
	}

	// Refuse to run exposed without a token (loud, not a silent unauthenticated API).
	bin := seshBin(t)
	noTok := &localRunner{bin: bin, env: map[string]string{
		"SESH_HOME":     t.TempDir(),
		"SESH_MACHINE":  "api-notoken",
		"SESH_API_ADDR": "127.0.0.1:1", // never bound — the token check fails first
	}}
	if _, stderr, err := noTok.Run(t, "daemon", "run"); err == nil {
		t.Errorf("daemon ran with SESH_API_ADDR but no token (should refuse)")
	} else if !strings.Contains(stderr, "unauthenticated") {
		t.Errorf("expected a loud refusal mentioning the unauthenticated API, got: %q", stderr)
	}
}

// testAPITcpParity drives EVERY layer over the TCP API with a token, proving the
// network surface has full parity with the local one (it is the same router).
func testAPITcpParity(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	d := startAPIDaemon(t)
	rc := client.NewRemote(d.addr, d.token)
	ctx := context.Background()

	// thread layer (use headless pi — fast, no tmux).
	th, err := rc.ThreadNew(ctx, api.NewThreadRequest{Agent: "pi", Name: "apiparity", Cwd: "/tmp", Headless: true})
	if err != nil {
		t.Fatalf("ThreadNew over TCP: %v", err)
	}
	list, err := rc.ThreadList(ctx, false, false)
	if err != nil || !threadInResp(list.Threads, th.Thread.ID) {
		t.Fatalf("ThreadList over TCP missing the thread (err=%v)", err)
	}
	// Snapshot is maintainer-backed (eventually consistent), so poll.
	if !waitUntil(5*time.Second, func() bool {
		snap, err := rc.Snapshot(ctx)
		return err == nil && snapHas(snap.Threads, th.Thread.ID)
	}) {
		t.Fatalf("Snapshot over TCP never showed the thread")
	}
	if err := rc.ThreadSendHeadless(ctx, th.Thread.ID, "say hi"); err != nil {
		t.Fatalf("ThreadSendHeadless over TCP: %v", err)
	}

	// ticket layer.
	tk, err := rc.TicketCreate(ctx, api.CreateTicketRequest{Name: "api-ticket"})
	if err != nil {
		t.Fatalf("TicketCreate over TCP: %v", err)
	}
	tickets, err := rc.TicketList(ctx, "")
	if err != nil {
		t.Fatalf("TicketList over TCP: %v", err)
	}
	var haveTicket bool
	for _, x := range tickets.Tickets {
		if x.ID == tk.Ticket.ID {
			haveTicket = true
		}
	}
	if !haveTicket {
		t.Errorf("TicketList over TCP missing the created ticket")
	}

	// tmux layer.
	if _, err := rc.TmuxInfo(ctx, ""); err != nil {
		t.Errorf("TmuxInfo over TCP: %v", err)
	}

	// mesh + grid layer.
	mesh, err := rc.Mesh(ctx)
	if err != nil || len(mesh.Machines) == 0 {
		t.Errorf("Mesh over TCP empty/err: %v", err)
	}
	if _, err := rc.ThreadGrid(ctx, false, false); err != nil {
		t.Errorf("ThreadGrid over TCP: %v", err)
	}

	// mutation round-trips: delete the thread, confirm it's gone — over TCP.
	if err := rc.ThreadDelete(ctx, th.Thread.ID, true); err != nil {
		t.Fatalf("ThreadDelete over TCP: %v", err)
	}
	list, err = rc.ThreadList(ctx, false, false)
	if err != nil || threadInResp(list.Threads, th.Thread.ID) {
		t.Errorf("thread still listed after delete over TCP (err=%v)", err)
	}
}

func threadInResp(threads []api.Thread, id string) bool {
	for _, t := range threads {
		if t.ID == id {
			return true
		}
	}
	return false
}

func snapHas(rows []api.ThreadSnapshot, id string) bool {
	for _, r := range rows {
		if r.ID == id {
			return true
		}
	}
	return false
}
