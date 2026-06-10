package pi

import (
	"os"
	"path/filepath"
	"strings"
)

// ResolveUUIDByPane finds the live pi session whose rpc socket reports the
// given tmux pane, and returns its uuid (the socket filename). pi serves a
// socket at <socketsDir>/<uuid>.sock and reports its own tmux pane via
// getTmuxInfo, so this is authoritative and store-independent: enumerate the
// sockets, dial each, and match paneId + socketPath. Returns "" if no live pi
// reports that pane (exp15 §2).
func ResolveUUIDByPane(socketsDir, targetPane, targetSocket string) (string, error) {
	entries, err := os.ReadDir(socketsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	target := realpath(targetSocket)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sock") {
			continue
		}
		sock := filepath.Join(socketsDir, e.Name())
		if ProbeLive(sock) != Live {
			continue // skip stale socket files
		}
		c, err := Dial(sock)
		if err != nil {
			continue
		}
		ti, err := c.GetTmuxInfo()
		c.Close()
		if err != nil || !ti.InTmux {
			continue
		}
		if ti.PaneID == targetPane && realpath(ti.SocketPath) == target {
			return strings.TrimSuffix(e.Name(), ".sock"), nil
		}
	}
	return "", nil
}

// realpath resolves symlinks so a caller-supplied /tmp/... socket matches pi's
// reported /private/tmp/... (macOS), the same normalization the walker uses.
func realpath(p string) string {
	if rp, err := filepath.EvalSymlinks(p); err == nil {
		return rp
	}
	return p
}
