package agents

// CODEX'S SHARED APP-SERVER DAEMON IS INCOMPATIBLE WITH SESH (lukastk/sesh#15).
//
// Since codex-cli 0.157 every interactive `codex` auto-starts ONE detached,
// shared app-server daemon per CODEX_HOME (feature `daemon_auto_start`, Stable,
// default on — codex-rs/tui/src/startup_orchestration.rs) and runs its turns
// there. Three sesh contracts break under it, all measured:
//
//   - `sesh thread stop` kills the PANE, but the conversation's thread writer
//     lock (<CODEX_HOME>/thread-writer-locks/<id>.lock) stays held by the daemon
//     (still held 150 s after the kill), so the next headless turn / resume fails
//     "thread … already has an active writer".
//   - `sesh thread adopt` identifies a codex pane by the rollout file ITS process
//     holds open; under the daemon the pane's process holds none.
//   - codex's README: clients share the environment of whichever launch STARTED
//     the daemon, so per-thread env (the notify reporter's SESH_THREAD_ID) cannot
//     be trusted to reach the process running the turn.
//
// So sesh pins `[features] daemon_auto_start = false` in the codex home. It is a
// config key, not `--no-daemon`, because (a) it also governs a codex the user
// starts BY HAND (which adopt later picks up) and (b) older codex versions
// tolerate an unknown feature key (0.142.5 / 0.150.0 measured: `codex features
// list` exits 0) while they REJECT an unknown flag.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

const (
	codexDaemonFeature = "daemon_auto_start"
	codexFeaturesTable = "features"
)

// codexConfigMu serialises sesh's read-modify-write of a codex config.toml within
// this process (concurrent spawns). It cannot exclude codex itself, which also
// writes the file; that window is only open on the one write that flips the key,
// since an already-false config is never rewritten.
var codexConfigMu sync.Mutex

// CodexDaemonSetting reports how the codex home's config sets daemon_auto_start:
// "unset", "false" or "true". An unparseable config is an error.
func CodexDaemonSetting(codexHome string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if os.IsNotExist(err) {
		return "unset", nil
	}
	if err != nil {
		return "", fmt.Errorf("codex config: %w", err)
	}
	v, err := decodeCodexConfig(raw)
	if err != nil {
		return "", err
	}
	return daemonSettingOf(v), nil
}

func daemonSettingOf(v map[string]any) string {
	f, ok := v[codexFeaturesTable].(map[string]any)
	if !ok {
		return "unset"
	}
	switch f[codexDaemonFeature] {
	case false:
		return "false"
	case true:
		return "true"
	case nil:
		return "unset"
	}
	return "invalid"
}

func decodeCodexConfig(raw []byte) (map[string]any, error) {
	v := map[string]any{}
	if _, err := toml.Decode(string(raw), &v); err != nil {
		return nil, fmt.Errorf("codex config is not valid TOML (sesh will not edit a file it cannot understand): %w", err)
	}
	return v, nil
}

// EnsureCodexNoDaemon sets `[features] daemon_auto_start = false` in
// <codexHome>/config.toml, changing nothing else: every other key, table and
// comment is kept, because the edit is TEXTUAL and then VERIFIED by re-parsing —
// the result must decode to exactly the original document with that one key set.
// It never writes a config that already says false. It returns what the setting
// WAS ("unset", "false", "true") so a caller can be loud when sesh overrode a user's
// explicit `true`: sesh's policy wins (it cannot work with the shared daemon), but
// never silently.
func EnsureCodexNoDaemon(codexHome string) (prev string, err error) {
	if codexHome == "" {
		return "", fmt.Errorf("codex daemon: empty codexHome")
	}
	codexConfigMu.Lock()
	defer codexConfigMu.Unlock()
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return "", fmt.Errorf("codex daemon: %w", err)
	}
	cfgPath := filepath.Join(codexHome, "config.toml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("codex daemon: read config: %w", err)
	}
	orig, err := decodeCodexConfig(raw)
	if err != nil {
		return "", fmt.Errorf("codex daemon: %s: %w", cfgPath, err)
	}
	prev = daemonSettingOf(orig)
	switch prev {
	case "false":
		return prev, nil
	case "invalid":
		return prev, fmt.Errorf("codex daemon: %s: [features].%s is not a boolean — refusing to guess", cfgPath, codexDaemonFeature)
	}

	edited, err := setCodexDaemonFalse(string(raw))
	if err != nil {
		return prev, fmt.Errorf("codex daemon: %s: %w", cfgPath, err)
	}
	// VERIFY: the edit must decode to the original document plus exactly our key.
	got, err := decodeCodexConfig([]byte(edited))
	if err != nil {
		return prev, fmt.Errorf("codex daemon: %s: the edit produced invalid TOML (not written): %w", cfgPath, err)
	}
	want := deepCopyMap(orig)
	feats, _ := want[codexFeaturesTable].(map[string]any)
	if feats == nil {
		feats = map[string]any{}
		want[codexFeaturesTable] = feats
	}
	feats[codexDaemonFeature] = false
	if !reflect.DeepEqual(got, want) {
		return prev, fmt.Errorf("codex daemon: %s: the edit changed more than [features].%s (not written) — edit it by hand", cfgPath, codexDaemonFeature)
	}

	mode := os.FileMode(0o600)
	if st, err := os.Stat(cfgPath); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(codexHome, ".config.toml.sesh-*")
	if err != nil {
		return prev, fmt.Errorf("codex daemon: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck — gone after a successful rename
	if _, err := tmp.WriteString(edited); err != nil {
		tmp.Close()
		return prev, fmt.Errorf("codex daemon: write config: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return prev, fmt.Errorf("codex daemon: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return prev, fmt.Errorf("codex daemon: %w", err)
	}
	if err := os.Rename(tmp.Name(), cfgPath); err != nil {
		return prev, fmt.Errorf("codex daemon: replace config: %w", err)
	}
	return prev, nil
}

var (
	tomlHeaderRe      = regexp.MustCompile(`^\s*\[`)
	featuresHeaderRe  = regexp.MustCompile(`^\s*\[\s*features\s*\]\s*(#.*)?$`)
	daemonKeyRe       = regexp.MustCompile(`^(\s*)daemon_auto_start(\s*)=\s*(true|false)(\s*(#.*)?)$`)
	dottedDaemonKeyRe = regexp.MustCompile(`^(\s*)features\s*\.\s*daemon_auto_start(\s*)=\s*(true|false)(\s*(#.*)?)$`)
	dottedFeaturesRe  = regexp.MustCompile(`^\s*features\s*\.`)
)

// setCodexDaemonFalse is the textual edit. It handles the three shapes a codex
// config really has — a `[features]` table, top-level dotted `features.x` keys,
// or no features at all — and refuses anything else (e.g. an inline
// `features = { … }` table) rather than rewriting it. The caller verifies the
// result by re-parsing, so a case this misjudges fails loudly, never silently.
func setCodexDaemonFalse(text string) (string, error) {
	lines := strings.Split(text, "\n")

	// 1. A [features] table: replace the key inside it, else add it under the header.
	for i, l := range lines {
		if !featuresHeaderRe.MatchString(l) {
			continue
		}
		for j := i + 1; j < len(lines) && !tomlHeaderRe.MatchString(lines[j]); j++ {
			if m := daemonKeyRe.FindStringSubmatch(lines[j]); m != nil {
				lines[j] = m[1] + codexDaemonFeature + m[2] + "= false" + m[4]
				return strings.Join(lines, "\n"), nil
			}
		}
		out := append([]string{}, lines[:i+1]...)
		out = append(out, codexDaemonFeature+" = false")
		return strings.Join(append(out, lines[i+1:]...), "\n"), nil
	}

	// 2. Top-level dotted keys (before the first table header).
	top := len(lines)
	for i, l := range lines {
		if tomlHeaderRe.MatchString(l) {
			top = i
			break
		}
	}
	lastDotted := -1
	for i := 0; i < top; i++ {
		if m := dottedDaemonKeyRe.FindStringSubmatch(lines[i]); m != nil {
			lines[i] = m[1] + "features." + codexDaemonFeature + m[2] + "= false" + m[4]
			return strings.Join(lines, "\n"), nil
		}
		if dottedFeaturesRe.MatchString(lines[i]) {
			lastDotted = i
		}
	}
	if lastDotted >= 0 {
		out := append([]string{}, lines[:lastDotted+1]...)
		out = append(out, "features."+codexDaemonFeature+" = false")
		return strings.Join(append(out, lines[lastDotted+1:]...), "\n"), nil
	}
	if strings.Contains(text, "features") {
		// features is defined in a shape this edit does not handle (inline table,
		// quoted key, …). The re-parse would catch a wrong edit, but a refusal
		// names the problem better.
		if v, err := decodeCodexConfig([]byte(text)); err == nil {
			if _, ok := v[codexFeaturesTable]; ok {
				return "", fmt.Errorf("[features] is defined in a form sesh cannot edit safely (e.g. an inline table) — add `daemon_auto_start = false` to it by hand")
			}
		}
	}

	// 3. No features at all: append a new table at the end (valid after anything).
	sep := "\n"
	if text == "" || strings.HasSuffix(text, "\n\n") {
		sep = ""
	} else if !strings.HasSuffix(text, "\n") {
		sep = "\n\n"
	}
	return text + sep + "[features]\n" + codexDaemonFeature + " = false\n", nil
}

func deepCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			out[k] = deepCopyMap(sub)
		} else {
			out[k] = v
		}
	}
	return out
}
