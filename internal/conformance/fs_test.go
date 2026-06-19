package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("fs.list", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testFsList(t, loc) })
	}
}

// testFsList exercises the generic GET /v1/fs/list primitive: it lists the immediate
// SUBDIRECTORIES of a home-rooted path on the (possibly remote) daemon, returns them
// ~-relative, excludes plain files, and refuses a path outside the home dir LOUDLY.
// The daemon runs with HOME=<a controlled tree> (withHome) so the assertions are
// deterministic and never touch the real home. This is the mechanism the Obsidian
// new-thread modal uses to fill the box (~/dev) and mysetup (~/mysetup) cwd pickers
// on platforms (mobile) with no local filesystem access.
func testFsList(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	fakeHome := t.TempDir()
	for _, d := range []string{
		"mysetup/alpha", "mysetup/beta",
		"dev/20260101_aaa111__box-one", "dev/20260102_bbb222__box-two",
	} {
		if err := os.MkdirAll(filepath.Join(fakeHome, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A plain file that must NOT appear in the listing (dirs only).
	if err := os.WriteFile(filepath.Join(fakeHome, "mysetup", "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	sb := newSandbox(t, loc, withHome(fakeHome))
	sb.startDaemon(t)

	list := func(path string) map[string]string {
		t.Helper()
		stdout, stderr, err := sb.Runner.Run(t, "fs", "list", "--path", path, "--json")
		if err != nil {
			t.Fatalf("fs list %s: %v\n%s", path, err, stderr)
		}
		var resp api.FsListResponse
		if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
			t.Fatalf("decode fs list: %v\nraw: %s", err, stdout)
		}
		m := map[string]string{}
		for _, e := range resp.Entries {
			m[e.Name] = e.Path
		}
		return m
	}

	// ~/mysetup → the two dirs, the file excluded, paths ~-relative.
	ms := list("~/mysetup")
	for _, want := range []string{"alpha", "beta"} {
		if _, ok := ms[want]; !ok {
			t.Errorf("fs list ~/mysetup missing %q: %v", want, ms)
		}
	}
	if _, ok := ms["note.txt"]; ok {
		t.Errorf("fs list returned a plain file note.txt (dirs-only expected): %v", ms)
	}
	if got := ms["alpha"]; got != "~/mysetup/alpha" {
		t.Errorf("entry path = %q, want ~-relative ~/mysetup/alpha", got)
	}

	// ~/dev → the box index dirs (the box picker's source).
	dev := list("~/dev")
	if _, ok := dev["20260101_aaa111__box-one"]; !ok {
		t.Errorf("fs list ~/dev missing box-one: %v", dev)
	}

	// A path OUTSIDE the home dir is refused loudly (not a silent empty listing).
	if _, _, err := sb.Runner.Run(t, "fs", "list", "--path", "/etc"); err == nil {
		t.Errorf("fs list /etc (outside home) was accepted")
	}
	// "../" traversal that escapes home is refused loudly too.
	if _, _, err := sb.Runner.Run(t, "fs", "list", "--path", "~/../etc"); err == nil {
		t.Errorf("fs list ~/../etc (traversal) was accepted")
	}
}
