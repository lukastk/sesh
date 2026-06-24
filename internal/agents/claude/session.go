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

// session.go resolves a claude thread's CURRENT session id across SESSION-ID DRIFT.
//
// THE PROBLEM: claude does NOT keep one stable session id for a conversation. It starts
// a NEW session id + a new <id>.jsonl on (a) every compaction, and (b) every resume /
// rewind — `claude --resume <id>` copies the conversation into a fresh file and
// continues THERE. sesh records the session id ONCE (spawn/adopt) and never updates it,
// so the transcript view + `resume` freeze at the LAST drift point: the stored file
// stops growing while the live conversation continues in a new file. Resolve-on-read:
// the record keeps the stable anchor; this layer follows the anchor FORWARD to the live
// leaf. Two drift signals, two linkages, unified into one forward walk:
//
// (1) COMPACTION (verified live): a compaction-born file BEGINS (only meta lines —
// mode/permission-mode/file-history-snapshot — before it) with a
//
//	{"type":"system","subtype":"compact_boundary","logicalParentUuid":"<msg-uuid>",…}
//
// whose logicalParentUuid is a MESSAGE uuid that lives in the PARENT session's file
// (the last preserved message before the branch). So child.compact_boundary
// .logicalParentUuid → the file OWNING that message = the parent. We map every
// compaction-born file to its parent and follow the chain to its LEAF.
//
// (2) RESUME / REWIND (verified live — the "duplicate session" bug): a resume/rewind
// fork is a normal (NOT compaction-born) file that COPIES the conversation, PRESERVING
// each message's uuid (only the per-line sessionId is rewritten) — so it shares its
// FIRST conversation-message uuid (the conversation ROOT) with every other fork of the
// same conversation. Files sharing a root are one lineage; the LIVE tip is the one most
// recently extended (latest last-message timestamp). A frozen pre-fork file (e.g. the
// stale anchor) has an earlier last-message timestamp than the fork that continued.
//
// The walk follows the compaction chain to its leaf, then hops to that conversation's
// live resume tip, repeating until stable. On-disk, so it works for headful AND dead
// threads; lineage is keyed on the compaction-boundary link and the conversation-root
// uuid (both precise — a newest-file heuristic cannot tell concurrent conversations in
// one cwd apart, but a shared root uuid can).

// metaLine is the subset of a transcript line this resolver needs (the boundary link,
// the per-message uuid, and the message timestamp). json.Unmarshal ignores the many
// other fields.
type metaLine struct {
	Type              string `json:"type"`
	Subtype           string `json:"subtype"`
	UUID              string `json:"uuid"`
	LogicalParentUUID string `json:"logicalParentUuid"`
	Timestamp         string `json:"timestamp"`
}

// fileInfo summarizes a session file for lineage resolution.
type fileInfo struct {
	born   bool   // first meaningful event is a compact_boundary (compaction-born)
	lpu    string // born: the boundary's logicalParentUuid (its compaction-parent link)
	root   string // non-born: first conversation (user/assistant) message uuid
	lastTS string // non-born: last conversation message timestamp (ISO-8601, sorts chronologically)
	msgs   int    // non-born: conversation message count (a tie-breaker)
}

// scanFile classifies a session file. born=true iff its first meaningful event is a
// compact_boundary (before any real conversation line) — then lpu is that boundary's
// logicalParentUuid and we return early (a born file's content is reached via the
// boundary chain, not by root grouping). A normal session (real conversation before any
// boundary — incl. an IN-SESSION compaction deep in the file) is born=false; for it we
// scan to the end to record the conversation root uuid (shared by resume/rewind forks),
// the last message timestamp (the live tip is the most-recently-extended fork), and the
// message count.
func scanFile(path string) (fileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return fileInfo{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var fi fileInfo
	classified := false
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var ml metaLine
		if json.Unmarshal(b, &ml) != nil {
			continue
		}
		isMsg := ml.Type == "user" || ml.Type == "assistant"
		if !classified {
			if ml.Type == "system" && ml.Subtype == "compact_boundary" {
				// First meaningful event is a boundary → born by compaction.
				return fileInfo{born: true, lpu: ml.LogicalParentUUID}, nil
			}
			if !isMsg {
				continue // meta line before any conversation
			}
			classified = true
			fi.root = ml.UUID
		}
		if isMsg {
			fi.msgs++
			if ml.Timestamp != "" {
				fi.lastTS = ml.Timestamp
			}
		}
	}
	return fi, sc.Err()
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

// ResolveLeafSession follows storedID forward through the compaction chain AND the
// resume/rewind fork lineage to the current leaf session id. It returns storedID
// unchanged when there is no drift to chase (no born files and no sibling fork) and when
// the project dir does not exist (e.g. a thread whose claude data is on another machine).
// A genuine read error is returned LOUDLY. The result is cached on the SET of session
// files in the project dir — a new compaction or resume creates a new file, changing the
// set and forcing a re-resolve; within a stable set the leaf does not change.
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

// resolveLeafUncached does the actual lineage walk (no cache).
func resolveLeafUncached(dir string, sids []string, storedID string) string {
	// Scan every file once: born+lpu for compaction-born files, root+lastTS+msgs for
	// normal files.
	infos := map[string]fileInfo{}
	for _, sid := range sids {
		fi, err := scanFile(filepath.Join(dir, sid+".jsonl"))
		if err != nil {
			continue
		}
		infos[sid] = fi
	}

	// (1) Compaction lineage: each born file -> its parent (the file owning the message
	// its boundary's logicalParentUuid names).
	born := map[string]string{} // sid -> logicalParentUuid
	for sid, fi := range infos {
		if fi.born && fi.lpu != "" {
			born[sid] = fi.lpu
		}
	}
	children := map[string][]string{}
	if len(born) > 0 {
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
		for child, lpu := range born {
			if p := owner(occ[lpu], child, bornSet); p != "" {
				children[p] = append(children[p], child)
			}
		}
	}

	// (2) Resume/rewind lineage: non-born files sharing a conversation-root uuid are
	// forks of one conversation; the live tip is the most-recently-extended.
	byRoot := map[string][]string{}
	for sid, fi := range infos {
		if !fi.born && fi.root != "" {
			byRoot[fi.root] = append(byRoot[fi.root], sid)
		}
	}

	if len(born) == 0 && !hasMultiFork(byRoot) {
		return storedID // no drift to chase (cheap exit)
	}

	// Follow the compaction chain to its leaf, then hop to that conversation's live
	// resume tip; repeat until stable. Each step advances to a strictly newer file, so
	// it converges; the file count bounds it as a backstop against an unexpected cycle.
	cur := storedID
	for range sids {
		next := followLeaf(dir, cur, children)
		next = resumeTip(next, infos, byRoot)
		if next == cur {
			break
		}
		cur = next
	}
	return cur
}

// hasMultiFork reports whether any conversation root is shared by more than one file
// (i.e. there is a resume/rewind fork to chase).
func hasMultiFork(byRoot map[string][]string) bool {
	for _, grp := range byRoot {
		if len(grp) > 1 {
			return true
		}
	}
	return false
}

// resumeTip returns the live tip of sid's resume/rewind conversation: among the non-born
// files sharing sid's conversation-root uuid, the one most-recently extended. Returns
// sid unchanged when it is compaction-born or the sole file of its root.
func resumeTip(sid string, infos map[string]fileInfo, byRoot map[string][]string) string {
	fi, ok := infos[sid]
	if !ok || fi.born || fi.root == "" {
		return sid
	}
	grp := byRoot[fi.root]
	if len(grp) <= 1 {
		return sid
	}
	best, bestFi := sid, fi
	for _, cand := range grp {
		if cand == best {
			continue
		}
		if cfi := infos[cand]; moreCurrent(cfi, cand, bestFi, best) {
			best, bestFi = cand, cfi
		}
	}
	return best
}

// moreCurrent reports whether file a (id aID) is a later conversation tip than b (id
// bID): later last-message timestamp; ties broken by more messages, then larger id (for
// determinism).
func moreCurrent(a fileInfo, aID string, b fileInfo, bID string) bool {
	if a.lastTS != b.lastTS {
		return a.lastTS > b.lastTS
	}
	if a.msgs != b.msgs {
		return a.msgs > b.msgs
	}
	return aID > bID
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
