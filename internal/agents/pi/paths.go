// Package pi ports v1's pi integration to Go (CONCEPT.md Q5 / exp 06):
// the pi-rpc-socket client (live channel) AND the JSONL transcript watch
// (for TUI-typed turns the socket doesn't broadcast). Re-verified against
// live pi 0.78.0 storage on macOS and v1 exp 03's documented wire shape.
package pi

import (
	"os"
	"path/filepath"
	"strings"
)

// SessionsRoot is pi's default session store.
func SessionsRoot(home string) string { return filepath.Join(home, "sessions") }

// CwdDirName maps a cwd to pi's per-cwd subdir name: replace '/' with '-'
// and wrap in '--…--'. NOTE this is LOSSY (v1 exp00 §1.2) — you cannot
// recover the original path, so always read the authoritative cwd from
// the first JSONL ("session") record, not the dir name.
func CwdDirName(cwd string) string {
	return "--" + strings.ReplaceAll(strings.TrimPrefix(cwd, "/"), "/", "-") + "--"
}

// --- pi-rpc-socket discovery (v1 exp03 §1) ----------------------------

// sunPathMax is the macOS unix-socket sun_path limit. The pi extension
// picks the first sockets-dir candidate whose path plus ~50 reserved
// bytes (for "<sessionId>.sock") fits. On macOS the default $TMPDIR
// (/var/folders/...) overflows, so it falls through to /tmp.
const (
	sunPathMax    = 104
	reservedBytes = 50
	socketsSubdir = "pi-rpc-sockets"
)

// DiscoverSocketsDir replicates the extension's pickSocketsDir(): try
// $TMPDIR, then /tmp, then /var/tmp; choose the first whose
// dir+reserved fits sun_path. Mirrors v1's pi_rpc_client.ts.
func DiscoverSocketsDir(tmpdir string) string {
	candidates := []string{}
	if tmpdir != "" {
		candidates = append(candidates, tmpdir)
	}
	candidates = append(candidates, "/tmp", "/var/tmp")
	for _, c := range candidates {
		dir := filepath.Join(c, socketsSubdir)
		if len(dir)+reservedBytes <= sunPathMax {
			return dir
		}
	}
	// Last resort: /tmp (short, always fits).
	return filepath.Join("/tmp", socketsSubdir)
}

// SocketPath returns the per-session socket path under socketsDir.
func SocketPath(socketsDir, sessionID string) string {
	return filepath.Join(socketsDir, sessionID+".sock")
}

// DefaultSocketsDir uses the live $TMPDIR.
func DefaultSocketsDir() string { return DiscoverSocketsDir(os.Getenv("TMPDIR")) }

// FindSession locates a pi session's transcript file under the cwd's session
// dir (filenames embed a timestamp prefix, so we match on the suffix).
// ("", nil) = not on disk yet.
func FindSession(home, cwd, sessionID string) (string, error) {
	dir := filepath.Join(SessionsRoot(home), CwdDirName(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, "_"+sessionID+".jsonl") || strings.HasSuffix(n, sessionID+".jsonl") {
			return filepath.Join(dir, n), nil
		}
	}
	return "", nil
}
