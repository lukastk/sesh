package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("thread.backup-http", matrix.AgentAgnostic, matrix.Remote, testBackupRoutingHTTP)
}

// testBackupRoutingHTTP is the transport twin of the transcript-file verbs: it
// proves `--machine` MOVES backup/restore/copy/import to the target machine even
// when the peer's transport is http, instead of running them here against the
// target's paths.
//
// The two routing implementations are not interchangeable for these verbs: ssh
// re-execs the command on the peer, while http keeps the process here and only
// redirects its daemon calls. restore's work is filesystem work (open --from,
// write --to-dir/--native), so the http path would resolve the peer's paths on the
// CALLER's disk — which is why `sesh copy <id> --to <peer>` always failed once
// peers carried an api_addr (sesh#17): the staged backup had arrived on the peer
// and the restore looked for it here.
//
// The peer is registered as http (api_addr set) with a DELIBERATELY BROKEN ssh
// dest, so the proof is unambiguous on a single host, where both sandboxes share a
// filesystem: the routed restore must FAIL at the ssh hop, and must never report
// one of the local-execution fingerprints (opening --from here). A client-only read
// over the same peer still succeeds, which is what rules out "the peer was simply
// unreachable".
func testBackupRoutingHTTP(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	self := newSandbox(t, matrix.Local)
	self.startDaemon(t)
	addr, token := freePort(t), "backuphttp-token"
	peer := newSandbox(t, matrix.Local, withAPI(addr, token))
	peer.startDaemon(t)

	if _, stderr, err := self.Runner.Run(t, "peer", "add", "--machine", peer.Machine,
		"--ssh", "http-only.invalid", "--home", peer.Home, "--binary", seshBin(t),
		"--tmux-socket", peer.TmuxSocket, "--api-addr", addr, "--api-token", token); err != nil {
		t.Fatalf("peer add (http): %v\n%s", err, stderr)
	}

	// Control: the peer IS reachable, and a client-only verb rides http (broken ssh).
	if _, stderr, err := self.Runner.Run(t, "thread", "list", "--machine", peer.Machine, "--json"); err != nil {
		t.Fatalf("http-routed thread list: %v\n%s", err, stderr)
	}

	// A file that exists HERE and is not a backup database: executed locally, restore
	// opens it and fails inside SQLite; executed on the peer, it never gets that far.
	dir := t.TempDir()
	from := filepath.Join(dir, "local-only.sqlite")
	if err := os.WriteFile(from, []byte("not a sqlite database\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")

	out, stderr, err := self.Runner.Run(t, "restore", "--from", from, "--to-dir", outDir,
		"--id", "deadbeef", "--machine", peer.Machine)
	if err == nil {
		t.Fatalf("routed restore SUCCEEDED with the peer's ssh dest broken — it ran on the caller\n%s", out)
	}
	for _, fingerprint := range []string{"not a database", "no such backup file", "is not a sesh backup"} {
		if strings.Contains(out+stderr, fingerprint) {
			t.Errorf("routed restore resolved --from on the CALLER (sesh#17): %q in\n%s%s", fingerprint, out, stderr)
		}
	}
	if entries, derr := os.ReadDir(outDir); derr == nil && len(entries) > 0 {
		t.Errorf("routed restore wrote %d file(s) into the caller's --to-dir", len(entries))
	}
}
