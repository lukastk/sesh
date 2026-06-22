package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// session.go resolves a claude thread's CURRENT session id across COMPACTION DRIFT.
//
// THE PROBLEM: claude starts a NEW session id + a new <id>.jsonl on every compaction
// (and on a resume that compacts). sesh records the session id ONCE (spawn/adopt) and
// never updates it, so the transcript view + `resume` freeze at the LAST compaction —
// the stored file stops growing while the live conversation continues in a new file.
//
// THE SIGNAL (verified live against Claude Code data): a compaction-born file BEGINS
// (only meta lines — mode/permission-mode/file-history-snapshot — before it) with a
//
//	{"type":"system","subtype":"compact_boundary","logicalParentUuid":"<msg-uuid>",…}
//
// whose logicalParentUuid is a MESSAGE uuid that lives in the PARENT session's file
// (the last preserved message before the branch). So child.compact_boundary
// .logicalParentUuid → the file OWNING that message = the parent. We map every
// compaction-born file to its parent and follow the stored id FORWARD to the LEAF (the
// session no born file claims as parent) = the current session.
//
// On-disk, so it works for headful AND dead threads; per-chain, so concurrent sessions
// in one cwd don't get confused (each stored id follows its own lineage — a newest-file
// heuristic cannot do this). A plain resume that does NOT compact produces no
// compact_boundary and is not part of a lineage we track — by design the stored id then
// still resumes correctly (claude --resume of a prior id works); this layer only chases
// the compaction drift that breaks the transcript view.

// metaLine is the subset of a transcript line this resolver needs (the boundary link +
// the per-message uuid). json.Unmarshal ignores the many other fields.
type metaLine struct {
	Type              string `json:"type"`
	Subtype           string `json:"subtype"`
	UUID              string `json:"uuid"`
	LogicalParentUUID string `json:"logicalParentUuid"`
}

// headInfo reads the HEAD of a session file to classify it cheaply: born=true iff its
// first compaction event precedes any real conversation line (a compaction-born file),
// in which case lpu is that boundary's logicalParentUuid. A normal session (real
// conversation before any boundary — incl. an IN-SESSION compaction deep in the file)
// is born=false. It stops at the first compact_boundary OR the first user/assistant
// line, so it reads only a handful of lines.
func headInfo(path string) (born bool, lpu string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var ml metaLine
		if json.Unmarshal(b, &ml) != nil {
			continue
		}
		if ml.Type == "system" && ml.Subtype == "compact_boundary" {
			// First meaningful event is a boundary → born by compaction.
			return true, ml.LogicalParentUUID, nil
		}
		if ml.Type == "user" || ml.Type == "assistant" {
			// Real conversation came first → a normal session, not compaction-born.
			return false, "", nil
		}
	}
	return false, "", sc.Err()
}

// occurrence is where a target message uuid was found.
type occurrence struct {
	sid string
	idx int
}

// firstOccurrences scans a file once for the first line whose `uuid` is in targets,
// recording (sid, line index) into occ. A fast substring pre-check avoids JSON-parsing
// the vast majority of lines. The FIRST occurrence per (uuid) in this file is kept (an
// original message precedes any in-file replay copy).
func firstOccurrences(path, sid string, targets map[string][]byte, occ map[string][]occurrence) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	seenHere := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	idx := -1
	for sc.Scan() {
		idx++
		line := sc.Bytes()
		hit := false
		for _, tb := range targets {
			if bytes.Contains(line, tb) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		var ml metaLine
		if json.Unmarshal(bytes.TrimSpace(line), &ml) != nil {
			continue
		}
		if ml.UUID == "" || seenHere[ml.UUID] {
			continue
		}
		if _, want := targets[ml.UUID]; want {
			seenHere[ml.UUID] = true
			occ[ml.UUID] = append(occ[ml.UUID], occurrence{sid: sid, idx: idx})
		}
	}
}

// owner picks the file that OWNS message uuid (its original home), excluding the file
// that referenced it. Preference: a non-compaction-born file (the original session),
// else — when both candidates are compaction-born (a mid-chain link) — the one where the
// message sits DEEPER in the file (a replay copy is near the top, the original is in the
// parent's own later content). "" if none.
func owner(occs []occurrence, exclude string, born map[string]bool) string {
	best := ""
	bestNonBorn := false
	bestIdx := -1
	for _, o := range occs {
		if o.sid == exclude {
			continue
		}
		nonBorn := !born[o.sid]
		switch {
		case best == "":
		case nonBorn && !bestNonBorn:
		case nonBorn == bestNonBorn && o.idx > bestIdx:
		default:
			continue
		}
		best, bestNonBorn, bestIdx = o.sid, nonBorn, o.idx
	}
	return best
}

// sessionCache memoizes the resolved leaf per (claudeHome, cwd, storedID), keyed on the
// SET of session files in the project dir. A repeated poll with no new session file
// returns the cached leaf without reading any file content; a new compaction (a new
// file) changes the signature and forces a re-resolve.
var (
	sessionCacheMu sync.Mutex
	sessionCache   = map[string]sessionCacheEntry{}
)

type sessionCacheEntry struct {
	sig  string
	leaf string
}

// ResolveLeafSession follows storedID forward through the compaction chain to the
// current leaf session id. It returns storedID unchanged when there is no compaction
// to chase (no born files, or storedID has no born descendant) and when the project
// dir does not exist (e.g. a thread whose claude data is on another machine). A genuine
// read error is returned LOUDLY.
func ResolveLeafSession(claudeHome, cwd, storedID string) (string, error) {
	if claudeHome == "" || cwd == "" || storedID == "" {
		return storedID, nil
	}
	dir := ProjectDir(claudeHome, cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return storedID, nil
		}
		return storedID, err
	}
	var sids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		sids = append(sids, strings.TrimSuffix(e.Name(), ".jsonl"))
	}
	sort.Strings(sids)
	sig := strings.Join(sids, "\n")
	key := claudeHome + "\x00" + cwd + "\x00" + storedID

	sessionCacheMu.Lock()
	if e, ok := sessionCache[key]; ok && e.sig == sig {
		leaf := e.leaf
		sessionCacheMu.Unlock()
		return leaf, nil
	}
	sessionCacheMu.Unlock()

	leaf := resolveLeafUncached(dir, sids, storedID)

	sessionCacheMu.Lock()
	sessionCache[key] = sessionCacheEntry{sig: sig, leaf: leaf}
	sessionCacheMu.Unlock()
	return leaf, nil
}

// resolveLeafUncached does the actual chain walk (no cache).
func resolveLeafUncached(dir string, sids []string, storedID string) string {
	// Classify every file's head: which are compaction-born, and their parent-link lpu.
	born := map[string]string{} // sid -> logicalParentUuid
	for _, sid := range sids {
		b, lpu, err := headInfo(filepath.Join(dir, sid+".jsonl"))
		if err == nil && b && lpu != "" {
			born[sid] = lpu
		}
	}
	if len(born) == 0 {
		return storedID // no compaction anywhere → nothing to chase (cheap exit)
	}

	// Locate the owning file of each born file's logical-parent message.
	targets := map[string][]byte{}
	for _, lpu := range born {
		targets[lpu] = []byte(lpu)
	}
	occ := map[string][]occurrence{}
	for _, sid := range sids {
		firstOccurrences(filepath.Join(dir, sid+".jsonl"), sid, targets, occ)
	}

	bornSet := map[string]bool{}
	for s := range born {
		bornSet[s] = true
	}
	children := map[string][]string{}
	for child, lpu := range born {
		if p := owner(occ[lpu], child, bornSet); p != "" {
			children[p] = append(children[p], child)
		}
	}

	return followLeaf(dir, storedID, children)
}

// followLeaf walks parent→child from start to the leaf. On a FORK (a session compacted
// into more than one child — e.g. resumed twice) it follows the most-recently-modified
// child, a best-effort pick of the live branch. A cycle (should not occur) stops the
// walk where it is.
func followLeaf(dir, start string, children map[string][]string) string {
	cur := start
	seen := map[string]bool{start: true}
	for {
		kids := children[cur]
		if len(kids) == 0 {
			return cur
		}
		next := kids[0]
		if len(kids) > 1 {
			next = newestByMtime(dir, kids)
		}
		if seen[next] {
			return cur
		}
		seen[next] = true
		cur = next
	}
}

// newestByMtime returns the sid among kids whose .jsonl was modified most recently.
func newestByMtime(dir string, kids []string) string {
	best := kids[0]
	var bestMod int64 = -1
	for _, sid := range kids {
		fi, err := os.Stat(filepath.Join(dir, sid+".jsonl"))
		if err != nil {
			continue
		}
		if m := fi.ModTime().UnixNano(); m > bestMod {
			bestMod, best = m, sid
		}
	}
	return best
}
