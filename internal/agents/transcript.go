package agents

// The transcript-resolution layer (PARITY_ROADMAP D0, v1's TranscriptPath/
// LastReply/ReadTranscript adapted): locate and read an agent's own
// conversation file for a thread. The per-agent formats live in the ported
// v1 subpackages (claude/, pi/, codexfs/). KEY v2 adaptation: the transcript
// belongs to the thread's AGENT SESSION ID (api.Thread.AgentSessionID), not
// the thread id — in v1 the two were the same uuid.
//
// Reads are OWNER-SIDE: transcript content is not mesh-replicated; remote
// reads route to the owning daemon. Everything here is read-only; mutation
// (fork/backup) arrives with D3/D4.

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/lukastk/sesh/internal/agents/claude"
	codex "github.com/lukastk/sesh/internal/agents/codexfs"
	"github.com/lukastk/sesh/internal/agents/pi"
)

// Homes locates each agent's state dir.
type Homes struct {
	Claude string
	Pi     string
	Codex  string
}

// ResolveHomes resolves the agent state dirs from the environment, with the
// agents' own defaults. codexHome lets the daemon pass its SESH_CODEX_HOME
// ("" = $CODEX_HOME / ~/.codex).
func ResolveHomes(codexHome string) Homes {
	uh, _ := os.UserHomeDir()
	h := Homes{
		Claude: os.Getenv("CLAUDE_CONFIG_DIR"),
		Pi:     filepath.Join(uh, ".pi", "agent"),
		Codex:  codexHome,
	}
	if h.Claude == "" {
		h.Claude = filepath.Join(uh, ".claude")
	}
	if h.Codex == "" {
		h.Codex = os.Getenv("CODEX_HOME")
	}
	if h.Codex == "" {
		h.Codex = filepath.Join(uh, ".codex")
	}
	return h
}

// TranscriptPath resolves a thread conversation's transcript file.
// found=false means it isn't on disk yet (no turn taken, or another machine's
// thread) — a legitimate state, not an error.
func TranscriptPath(kind Kind, agentSessionID, cwd string, homes Homes) (path string, found bool, err error) {
	if agentSessionID == "" {
		// A codex thread before its first turn has no minted session id —
		// there IS no transcript yet.
		return "", false, nil
	}
	switch kind {
	case Claude:
		if cwd == "" {
			return "", false, nil
		}
		p := claude.TranscriptPath(homes.Claude, cwd, agentSessionID)
		if _, statErr := os.Stat(p); statErr != nil {
			return p, false, nil
		}
		return p, true, nil
	case Pi:
		if cwd == "" {
			return "", false, nil
		}
		p, err := pi.FindSession(homes.Pi, cwd, agentSessionID)
		return p, p != "", err
	case Codex:
		p, err := codex.FindRolloutByID(homes.Codex, agentSessionID)
		return p, p != "", err
	default:
		return "", false, fmt.Errorf("unknown agent %q", kind)
	}
}

// LastReply returns the text of the conversation's last assistant reply plus
// the count of assistant replies so far (a monotone marker consumers can
// dedup on — the subscribe engine's contract). A missing transcript is a
// LOUD error: the caller asked about a conversation that has none.
func LastReply(kind Kind, agentSessionID, cwd string, homes Homes) (string, int, error) {
	path, found, err := TranscriptPath(kind, agentSessionID, cwd, homes)
	if err != nil {
		return "", 0, err
	}
	if !found {
		return "", 0, fmt.Errorf("no transcript on disk for this %s conversation (session %s)", kind, agentSessionID)
	}
	switch kind {
	case Claude:
		return claude.LastReply(path)
	case Pi:
		return pi.LastReply(path)
	case Codex:
		return codex.LastReply(path)
	}
	return "", 0, fmt.Errorf("unknown agent %q", kind)
}

// maxTranscriptLines caps a whole-transcript read so one response can't grow
// unbounded; the drop is logged (no silent truncation).
const maxTranscriptLines = 100_000

// ReadTranscript returns the transcript's raw lines: the last tailN when
// tailN >= 0, else the whole file (capped at maxTranscriptLines).
func ReadTranscript(kind Kind, agentSessionID, cwd string, homes Homes, tailN int) ([]string, error) {
	path, found, err := TranscriptPath(kind, agentSessionID, cwd, homes)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("no transcript on disk for this %s conversation (session %s)", kind, agentSessionID)
	}
	lines, err := readTranscriptLines(path)
	if err != nil {
		return nil, err
	}
	if tailN >= 0 {
		if tailN < len(lines) {
			lines = lines[len(lines)-tailN:]
		}
		return lines, nil
	}
	if len(lines) > maxTranscriptLines {
		log.Printf("transcript %s: %d lines; returning the last %d (capped)", agentSessionID, len(lines), maxTranscriptLines)
		lines = lines[len(lines)-maxTranscriptLines:]
	}
	return lines, nil
}

func readTranscriptLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 32*1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

// NativePath resolves the DETERMINISTIC on-disk location a conversation's
// transcript would occupy — a restore DESTINATION (TranscriptPath's found
// reflects existence, false for a file about to be written). Only claude's
// layout is deterministic from (cwd, session id); pi/codex filenames embed a
// timestamp and return a loud error.
func NativePath(kind Kind, agentSessionID, cwd string, homes Homes) (string, error) {
	switch kind {
	case Claude:
		if cwd == "" || agentSessionID == "" {
			return "", fmt.Errorf("claude native path needs a cwd + session id")
		}
		return claude.TranscriptPath(homes.Claude, cwd, agentSessionID), nil
	case Pi, Codex:
		return "", fmt.Errorf("native path for %s is not deterministic (filename embeds a timestamp)", kind)
	default:
		return "", fmt.Errorf("unknown agent %q", kind)
	}
}
