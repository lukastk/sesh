package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// TestClipboardToolSelection: each platform reaches ITS tool. The android row is
// the termux bug — termux builds are GOOS=android, which the old linux-only branch
// never matched.
func TestClipboardToolSelection(t *testing.T) {
	has := func(bins ...string) func(string) (string, error) {
		return func(b string) (string, error) {
			for _, x := range bins {
				if x == b {
					return "/bin/" + b, nil
				}
			}
			return "", exec.ErrNotFound
		}
	}
	for _, tc := range []struct {
		goos      string
		installed []string
		want      string
		wantArgs  []string
		wantErr   string
	}{
		{goos: "darwin", installed: []string{"pbcopy"}, want: "pbcopy"},
		{goos: "android", installed: []string{"termux-clipboard-set"}, want: "termux-clipboard-set"},
		{goos: "android", wantErr: "Termux:API"},
		{goos: "linux", installed: []string{"wl-copy", "xclip"}, want: "wl-copy"},
		{goos: "linux", installed: []string{"xclip", "xsel"}, want: "xclip", wantArgs: []string{"-selection", "clipboard"}},
		{goos: "linux", installed: []string{"xsel"}, want: "xsel", wantArgs: []string{"--clipboard", "--input"}},
		{goos: "linux", installed: []string{"termux-clipboard-set"}, wantErr: "no clipboard tool found"},
		{goos: "plan9", installed: []string{"pbcopy"}, wantErr: "not supported on plan9"},
	} {
		name, args, err := clipboardTool(tc.goos, has(tc.installed...))
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s %v: err = %v, want containing %q", tc.goos, tc.installed, err, tc.wantErr)
			}
			continue
		}
		if err != nil || name != tc.want || strings.Join(args, " ") != strings.Join(tc.wantArgs, " ") {
			t.Errorf("%s %v: got %q %v err=%v, want %q %v", tc.goos, tc.installed, name, args, err, tc.want, tc.wantArgs)
		}
	}
}

// TestClipboardEnv: a process that already names a display inherits its env
// untouched; one that names none (a work-server popup) gets the graphical session
// vars; a failed session lookup is reported but does not fabricate an env.
func TestClipboardEnv(t *testing.T) {
	session := func() (map[string]string, error) {
		return map[string]string{"WAYLAND_DISPLAY": "wayland-1", "XDG_RUNTIME_DIR": "/run/user/1000"}, nil
	}
	called := false
	spy := func() (map[string]string, error) { called = true; return session() }

	if env, err := clipboardEnv("linux", []string{"HOME=/h", "WAYLAND_DISPLAY=wayland-1"}, spy); env != nil || err != nil || called {
		t.Errorf("display present: env=%v err=%v looked-up=%v, want inherit without a lookup", env, err, called)
	}
	if env, _ := clipboardEnv("linux", []string{"HOME=/h", "DISPLAY=:0"}, spy); env != nil || called {
		t.Errorf("X display present: env=%v looked-up=%v, want inherit without a lookup", env, called)
	}
	if env, _ := clipboardEnv("darwin", []string{"HOME=/h"}, spy); env != nil || called {
		t.Errorf("darwin: env=%v looked-up=%v, want inherit without a lookup", env, called)
	}

	env, err := clipboardEnv("linux", []string{"HOME=/h", "DISPLAY="}, session)
	if err != nil {
		t.Fatal(err)
	}
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{"\nHOME=/h\n", "\nWAYLAND_DISPLAY=wayland-1\n", "\nXDG_RUNTIME_DIR=/run/user/1000\n"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no-display env %v lacks %q", env, strings.TrimSpace(want))
		}
	}

	boom := errors.New("no user manager")
	if env, err := clipboardEnv("linux", []string{"HOME=/h"}, func() (map[string]string, error) { return nil, boom }); env != nil || !errors.Is(err, boom) {
		t.Errorf("lookup failure: env=%v err=%v, want nil env + the lookup error", env, err)
	}
	if env, err := clipboardEnv("linux", []string{"HOME=/h"}, func() (map[string]string, error) { return nil, nil }); env != nil || err != nil {
		t.Errorf("no session: env=%v err=%v, want inherit", env, err)
	}
}

// stubClipboardTool installs a fake clipboard tool, under the name this platform
// selects, as the ONLY tool on PATH, with a display in the env so no session
// lookup runs. body is shell; "$OUT" is a file it may write to.
func stubClipboardTool(t *testing.T, body string) (out string) {
	t.Helper()
	name, _, err := clipboardTool(runtime.GOOS, func(b string) (string, error) { return b, nil })
	if err != nil {
		t.Skipf("no clipboard tool on %s: %v", runtime.GOOS, err)
	}
	dir := t.TempDir()
	out = filepath.Join(dir, "out")
	script := "#!/bin/sh\nOUT=" + out + "\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WAYLAND_DISPLAY", "stub")
	return out
}

// TestCopyReturnsWhileToolLeavesAChildBehind is the pocket4 freeze: wl-copy forks
// a child that keeps serving the selection and inherits the tool's stdio. The copy
// must return as soon as the TOOL exits — not when that child does — and must
// still deliver the text. Against the old CombinedOutput implementation this blocks
// for the child's whole lifetime.
func TestCopyReturnsWhileToolLeavesAChildBehind(t *testing.T) {
	out := stubClipboardTool(t, `cat > "$OUT"
sleep 30 &
echo $! > "$OUT.child"`)
	t.Cleanup(func() {
		if b, err := os.ReadFile(out + ".child"); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- copyToClipboard("0b3d2774-uuid") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("copy: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("copy still blocked after 5s: it is waiting on the child the clipboard tool left running (the pocket4 wl-copy freeze)")
	}
	if got, _ := os.ReadFile(out); string(got) != "0b3d2774-uuid" {
		t.Errorf("tool received %q, want the text", got)
	}
	t.Logf("copy returned in %s", time.Since(start).Round(time.Millisecond))
}

// TestCopyFailureIsLoud: the tool's own stderr reaches the error, so a failed copy
// says WHY (e.g. wl-copy's "Failed to connect to a Wayland server").
func TestCopyFailureIsLoud(t *testing.T) {
	stubClipboardTool(t, `cat >/dev/null
echo "Failed to connect to a Wayland server" >&2
exit 1`)
	err := copyToClipboard("x")
	if err == nil || !strings.Contains(err.Error(), "Failed to connect to a Wayland server") {
		t.Fatalf("err = %v, want the tool's stderr", err)
	}
}

// TestCopyTimesOutLoudly: a tool that never returns (termux-clipboard-set without
// the Termux:API app) costs a bounded, named error.
func TestCopyTimesOutLoudly(t *testing.T) {
	stubClipboardTool(t, `exec sleep 30`)
	old := clipboardTimeout
	clipboardTimeout = 300 * time.Millisecond
	t.Cleanup(func() { clipboardTimeout = old })

	done := make(chan error, 1)
	go func() { done <- copyToClipboard("x") }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "did not finish within") {
			t.Fatalf("err = %v, want a timeout error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("copy ignored its timeout")
	}
}

// TestUUIDPopupCopyIsAsyncAndReportsAsAction: `c` never runs the tool inside
// Update (it returns a Cmd), and the outcome lands on the ACTION error line — not
// lastErr, which reads "daemon unreachable" and is wiped by the next fetch.
func TestUUIDPopupCopyIsAsyncAndReportsAsAction(t *testing.T) {
	m := Model{rows: []api.ThreadRow{descRow("abc-uuid", "", api.BusyIdle)}}
	m.uuidPopup = true
	nm, cmd := m.handleUUIDKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = nm.(Model)
	if cmd == nil {
		t.Fatal("c returned no command: the copy must run off the Update loop")
	}
	if m.uuidPopup {
		t.Error("popup still open after c")
	}

	failed, _ := m.Update(clipboardDoneMsg{what: "UUID", err: errors.New("wl-copy: exit status 1")})
	fm := failed.(Model)
	if fm.actionErr == nil || !strings.Contains(fm.actionErr.Error(), "copy uuid: wl-copy") {
		t.Errorf("actionErr = %v, want the copy failure", fm.actionErr)
	}
	if fm.lastErr != nil {
		t.Errorf("lastErr = %v: a copy failure is not a daemon-reachability error", fm.lastErr)
	}

	ok, _ := fm.Update(clipboardDoneMsg{what: "UUID"})
	om := ok.(Model)
	if om.actionErr != nil || om.note != "UUID copied to clipboard" {
		t.Errorf("success: actionErr=%v note=%q", om.actionErr, om.note)
	}

	// Any other key closes without copying.
	m.uuidPopup = true
	if _, cmd := m.handleUUIDKey(tea.KeyMsg{Type: tea.KeyEsc}); cmd != nil {
		t.Error("a non-c key started a copy")
	}
}
