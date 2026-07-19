package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/matrix"
)

// route.parity (ssh) and route.parity.http exercise the `--machine` ROUTING plane
// over BOTH transports with the SAME body, so the grid enforces SSH↔HTTP routing
// parity. Representative client-only operations across layers (thread, ticket, tmux)
// are routed to a peer and verified to have landed on the PEER's daemon. Routing is
// a single generic code path, so this is agent-agnostic and need not be multiplied
// per agent. Carve-outs (daemon lifecycle, tmux nav/stage-file) stay ssh by design.
func init() {
	for _, tr := range meshTransports {
		tr := tr
		matrix.RegisterTest("route.parity"+tr.suffix, matrix.AgentAgnostic, matrix.Remote,
			func(t *testing.T) { testRouteParity(t, tr) })
	}
}

// setupRoutedPeer starts a peer daemon and a separate client machine that routes to
// it via `--machine`, over tr's transport. For http the peer's TCP API is exposed and
// it is registered with --api-addr/--api-token AND a deliberately BROKEN ssh dest, so
// a green http cell PROVES the command routed over HTTP (a silent ssh attempt would
// fail). Returns a sandbox whose Runner routes to the peer.
func setupRoutedPeer(t *testing.T, tr meshTransport) *Sandbox {
	t.Helper()
	ensureSSHLocalhost(t)
	bin := seshBin(t)
	stamp := time.Now().UnixNano()

	sshDest := "localhost"
	var peer *Sandbox
	var apiAddr, apiToken string
	switch tr.name {
	case "http":
		apiAddr = freePort(t)
		apiToken = fmt.Sprintf("route-token-%d", stamp)
		peer = newSandbox(t, matrix.Local, withAPI(apiAddr, apiToken))
		sshDest = "http-only.invalid" // ssh cannot connect — only HTTP can route
	case "ssh":
		peer = newSandbox(t, matrix.Local)
	default:
		t.Fatalf("unknown transport %q", tr.name)
	}
	peer.startDaemon(t)

	clientEnv := map[string]string{
		"SESH_HOME":          shortSandboxHome(t), // ssh routing opens a ControlMaster under it — see shortSandboxHome
		"SESH_MACHINE":       fmt.Sprintf("rclient-%d", stamp),
		"SESH_MASTER_SOCKET": fmt.Sprintf("sesh-test-rcmaster-%d", stamp),
	}
	add := []string{"peer", "add", "--machine", peer.Machine, "--ssh", sshDest, "--home", peer.Home, "--binary", bin, "--tmux-socket", peer.TmuxSocket}
	if tr.name == "http" {
		add = append(add, "--api-addr", apiAddr, "--api-token", apiToken)
	}
	if _, stderr, err := (&localRunner{bin: bin, env: clientEnv}).Run(t, add...); err != nil {
		t.Fatalf("peer add (%s): %v\n%s", tr.name, err, stderr)
	}
	peer.Runner = &routingRunner{bin: bin, env: clientEnv, peerMachine: peer.Machine}
	return peer
}

func testRouteParity(t *testing.T, tr meshTransport) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := setupRoutedPeer(t, tr)
	// Independent cross-check: a client straight to the PEER's daemon (unix socket),
	// to assert each routed command's real effect actually landed on the peer — not
	// just that the routed call returned ok.
	peerDaemon := client.New(sb.Home + "/daemon.sock")
	ctx := context.Background()

	// thread layer: `thread new` routed -> the record lands on the peer.
	th := sb.newHeadlessThread(t, "pi", "routed")
	if !threadOnPeer(t, peerDaemon, th.ID) {
		t.Fatalf("routed `thread new` did not land on the peer over %s", tr.name)
	}

	// ticket layer: `ticket create` routed -> the ticket lands on the peer.
	tkOut, stderr, err := sb.Runner.Run(t, "ticket", "create", "--name", "routed-ticket", "--json")
	if err != nil {
		t.Fatalf("routed ticket create over %s: %v\n%s", tr.name, err, stderr)
	}
	var tk api.Ticket
	if err := json.Unmarshal([]byte(tkOut), &tk); err != nil {
		t.Fatalf("decode routed ticket: %v\nraw: %s", err, tkOut)
	}
	tickets, err := peerDaemon.TicketList(ctx, "")
	if err != nil {
		t.Fatalf("peer ticket list: %v", err)
	}
	var haveTicket bool
	for _, x := range tickets.Tickets {
		if x.ID == tk.ID {
			haveTicket = true
		}
	}
	if !haveTicket {
		t.Errorf("routed ticket did not land on the peer over %s", tr.name)
	}

	// tmux layer: a client-only read routed over the transport works (emits JSONL).
	if _, stderr, err := sb.Runner.Run(t, "tmux", "info"); err != nil {
		t.Errorf("routed `tmux info` over %s: %v\n%s", tr.name, err, stderr)
	}

	// mutation round-trip: `thread delete` routed -> the record is gone from the peer.
	if _, stderr, err := sb.Runner.Run(t, "thread", "delete", "--id", th.ID); err != nil {
		t.Fatalf("routed thread delete over %s: %v\n%s", tr.name, err, stderr)
	}
	if threadOnPeer(t, peerDaemon, th.ID) {
		t.Errorf("routed `thread delete` left the record on the peer over %s", tr.name)
	}
}

func threadOnPeer(t *testing.T, c *client.Client, id string) bool {
	t.Helper()
	list, err := c.ThreadList(context.Background(), false, false)
	if err != nil {
		return false
	}
	return threadInResp(list.Threads, id)
}
