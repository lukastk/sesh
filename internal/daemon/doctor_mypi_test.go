package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// A machine without the patched fork is a normal machine. `sesh doctor` must say
// so as a WARN on the mypi row alone: it must not fail the run (the CLI exits
// non-zero only on `fail`), must not change any other harness's row, and must
// say what it actually costs — mypi launches, nothing else.
func TestDoctorWarnsOnlyForTheMissingHarness(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	// A login shell whose profile prepends a bin dir holding every harness
	// EXCEPT mypi. The doctor resolves agents exactly this way ($SHELL -lc
	// 'command -v …'), so this exercises the real resolution, not a stub.
	root := t.TempDir()
	home, bin := filepath.Join(root, "home"), filepath.Join(root, "bin")
	for _, dir := range []string{home, bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"claude", "codex", "pi", "tmux", "ssh"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte("PATH="+bin+":$PATH\nexport PATH\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/sh")

	d := newBrowserTestDaemon(t, "self", "")
	rec := httptest.NewRecorder()
	d.handleDoctor(rec, httptest.NewRequest(http.MethodGet, "/v1/doctor", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("doctor = %d %s", rec.Code, rec.Body.String())
	}
	var resp api.DoctorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	rows := map[string]api.DoctorCheck{}
	for _, ch := range resp.Checks {
		rows[ch.Name] = ch
	}
	mypi, ok := rows["agent:mypi"]
	if !ok {
		t.Fatalf("doctor has no mypi row: %#v", resp.Checks)
	}
	if mypi.Status != "warn" {
		t.Fatalf("missing mypi row = %q %q, want warn", mypi.Status, mypi.Detail)
	}
	if !strings.Contains(mypi.Detail, "mypi") || !strings.Contains(mypi.Detail, "other harnesses are unaffected") {
		t.Fatalf("missing mypi detail is not actionable: %q", mypi.Detail)
	}
	// Every other harness resolved, and its row is untouched by mypi's absence.
	for _, name := range []string{"agent:claude", "agent:codex", "agent:pi", "agent:tmux", "agent:ssh"} {
		row, ok := rows[name]
		if !ok {
			t.Fatalf("doctor has no %s row", name)
		}
		if row.Status != "ok" || !strings.HasPrefix(row.Detail, bin+"/") {
			t.Fatalf("%s = %q %q, want ok at the fixture path", name, row.Status, row.Detail)
		}
	}
	// The run as a whole does not FAIL: that is what keeps `sesh doctor`'s exit
	// code zero on a machine that simply has no mypi.
	for _, ch := range resp.Checks {
		if ch.Status == "fail" {
			t.Fatalf("a missing mypi produced a failing check: %s %q", ch.Name, ch.Detail)
		}
	}
}
