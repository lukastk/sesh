package claude

import (
	"os"
	"path/filepath"
)

// ForeignProjectDir reports the project directory a session's transcript
// ACTUALLY lives in, when that is demonstrably NOT the one cwd maps to.
//
// It is deliberately ONE-DIRECTIONAL evidence: only a true result means
// anything. false means "no contradiction found", which covers three very
// different situations that the caller must not distinguish —
//
//   - the transcript is under cwd (the normal case, including claude minting a
//     new session id on compaction: same cwd, same project dir);
//   - the transcript is not on disk anywhere yet (a session id reported before
//     claude has written its first line — a race, not a lie);
//   - ~/.claude/projects is unreadable.
//
// So a caller may act on true (refuse), and must never act on false (it is not
// a positive claim of ownership). This asymmetry is the point: the cost of a
// false positive is a refused stamp, the cost of a false negative is silently
// writing one thread's conversation onto another thread's record.
//
// Why this check exists: a claude BACKGROUND agent inherits SESH_THREAD_ID from
// whatever process started claude's machine-global daemon, so it reports under
// an unrelated thread's id. Its cwd is its own, though — and claude's project
// directory is a pure function of cwd — so the transcript landing under a
// different project dir than the thread's cwd is hard proof that the reporter
// is not that thread's agent. See internal/daemon/reportstate.go.
func ForeignProjectDir(claudeHome, cwd, sessionID string) (string, bool) {
	if claudeHome == "" || cwd == "" || sessionID == "" {
		return "", false
	}
	// Under cwd: not foreign, and the cheap check, so do it first.
	if _, err := os.Stat(TranscriptPath(claudeHome, cwd, sessionID)); err == nil {
		return "", false
	}
	projects := filepath.Join(claudeHome, "projects")
	ents, err := os.ReadDir(projects)
	if err != nil {
		return "", false
	}
	want := ProjectDirName(cwd)
	for _, e := range ents {
		if !e.IsDir() || e.Name() == want {
			continue
		}
		if _, err := os.Stat(filepath.Join(projects, e.Name(), sessionID+".jsonl")); err == nil {
			return e.Name(), true
		}
	}
	return "", false
}
