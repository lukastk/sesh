// Package claude ports v1's claude integration to Go (CONCEPT.md Q5 /
// exp 05). Re-verified against live Claude Code 2.1.158 data on macOS:
// transcript layout, agents --json shape, busy/idle markers, and the
// --session-id argv recovery from the v1 Phase 14d walker fix.
package claude

import (
	"path/filepath"
	"strings"
)

// ProjectDirName maps an absolute cwd to claude's project directory name
// under ~/.claude/projects. Rule (re-verified 2026-05-30 against live
// data): every NON-alphanumeric character becomes '-'. So '/', '_' and
// '.' all map to '-', and a leading '/' yields a leading '-'.
//
//	/Users/lukas/dev/20260527_ms6lpt__sesh
//	  -> -Users-lukas-dev-20260527-ms6lpt--sesh
//	/Users/lukas/dev/20251109_000000.7GfJI__repoyard
//	  -> -Users-lukas-dev-20251109-000000-7GfJI--repoyard
func ProjectDirName(cwd string) string {
	var b strings.Builder
	b.Grow(len(cwd))
	for _, r := range cwd {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// ProjectDir returns the full path to the project directory for cwd.
func ProjectDir(claudeHome, cwd string) string {
	return filepath.Join(claudeHome, "projects", ProjectDirName(cwd))
}

// TranscriptPath returns the JSONL transcript path for (cwd, sessionID).
func TranscriptPath(claudeHome, cwd, sessionID string) string {
	return filepath.Join(ProjectDir(claudeHome, cwd), sessionID+".jsonl")
}
