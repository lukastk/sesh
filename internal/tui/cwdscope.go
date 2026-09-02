package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lukastk/sesh/internal/api"
)

// cwdScope is a launch-time row restriction. root is either an absolute path
// (for a directory outside the invoking user's home) or an owner-portable
// ~-relative path (for a directory inside it). The latter is compared with
// ThreadRow.CwdRel, which the owning daemon stamps, so ~/mysetup/sesh means the
// same directory on Linux and macOS even though their absolute home paths differ.
type cwdScope struct {
	root        string
	descendants bool
}

// WithCwdScope restricts every TUI view to threads at root. When descendants is
// true, rows below root are admitted too. This is deliberately independent of
// the selected view and the interactive fuzzy filter: Tab can still cycle all
// configured views, but none can escape the launch-time directory boundary.
//
// root must already be normalized by the caller to an absolute or ~-relative
// path. Rejecting other relative paths avoids interpreting them against the
// daemon's cwd (which may differ from the shell/pane that launched the TUI).
func (m Model) WithCwdScope(root string, descendants bool) (Model, error) {
	root = filepath.Clean(root)
	if root == "." || (root != "~" && !strings.HasPrefix(root, "~"+string(filepath.Separator)) && !filepath.IsAbs(root)) {
		return m, fmt.Errorf("TUI CWD scope must be absolute or ~-relative, got %q", root)
	}
	m.cwdScope = &cwdScope{root: root, descendants: descendants}
	return m, nil
}

func (s cwdScope) admits(row api.ThreadRow) bool {
	candidate := row.Cwd
	if s.root == "~" || strings.HasPrefix(s.root, "~"+string(filepath.Separator)) {
		candidate = row.CwdRel
	}
	if candidate == "" {
		return false
	}
	candidate = filepath.Clean(candidate)
	if !s.descendants {
		return candidate == s.root
	}
	return pathAtOrBelow(s.root, candidate)
}

// pathAtOrBelow is lexical and path-aware: /work/app2 is not below /work/app.
// Thread CWDs are stored lexically too (creation makes them absolute but does not
// resolve symlinks), so resolving symlinks here would make the filter disagree
// with the stored identity it is supposed to select.
func pathAtOrBelow(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (s cwdScope) describe() string {
	if s.descendants {
		return fmt.Sprintf("%s or one of its subdirectories", s.root)
	}
	return s.root
}
