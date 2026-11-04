package agents

// Thread env files (api/env.go). Each thread has <SESH_HOME>/thread-env/<id>/:
//
//   - env.json: the variables, the source of truth, plus the names removed since
//     the last launch;
//   - env.sh: the same thing rendered as shell. A launch sources it, and on claude
//     so does every shell command, through the line EnvHookLine puts in claude's
//     hook env file.
//
// Hooks write that line rather than the values because claude sources EVERY hook
// env file it has: a key copied into one and then dropped from another comes back
// from the first with its stale value. The hooks exist only to refresh claude's
// cache of these files (it reads them once, then on hook events). FileChanged is
// what fires when env.sh changes, watched through SessionStart's watchPaths.
//
// A removal must be written as `unset`, not just dropped: the launched process and,
// on codex, its shell snapshot still hold the value. So Removed lists those names
// until the next launch, which starts clean.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// EnvThreadEnvFile names the thread's env.sh in every sesh-launched pane: what
// `sesh env hook` points claude at, and how the pi extension finds env.json.
const EnvThreadEnvFile = "SESH_THREAD_ENV_FILE"

const (
	// ThreadEnvDirName is the directory under SESH_HOME with one folder per thread.
	ThreadEnvDirName = "thread-env"
	threadEnvJSON    = "env.json"
	threadEnvScript  = "env.sh"
)

// ThreadEnv is a thread's env.json.
type ThreadEnv struct {
	Vars map[string]string `json:"vars"`
	// Removed names variables deleted since the last launch.
	Removed []string `json:"removed,omitempty"`
}

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// zshSpecialNames are zsh's integer, array and read-only special parameters. env.sh
// is sourced by zsh across the fleet, and assigning one of these a string does not
// merely misbehave: `export LINES='a b'` is a math error that ABORTS the source, so
// every variable after it silently disappears. Lower-case path and friends rewrite
// $PATH instead. TestZshSpecialNamesAreRefused compares this with zsh itself.
var zshSpecialNames = map[string]bool{
	"ARGC": true, "argv": true, "cdpath": true, "COLUMNS": true, "EGID": true, "EUID": true,
	"fignore": true, "fpath": true, "FUNCNEST": true, "GID": true, "HISTCMD": true,
	"HISTSIZE": true, "LINENO": true, "LINES": true, "mailpath": true, "manpath": true,
	"module_path": true, "OPTIND": true, "parameters": true, "path": true,
	"pipestatus": true, "PPID": true, "psvar": true, "RANDOM": true, "SAVEHIST": true,
	"SECONDS": true, "SHLVL": true, "status": true, "TRY_BLOCK_ERROR": true,
	"TRY_BLOCK_INTERRUPT": true, "TTYIDLE": true, "UID": true, "zsh_eval_context": true,
	"ZSH_EVAL_CONTEXT": true, "ZSH_SUBSHELL": true,
}

// ValidateEnvKey refuses a name that is not a shell variable, or one sesh owns:
// overriding a carrier would give the agent another thread's identity or tmux
// server, and CLAUDE_ENV_FILE is the hook mechanism itself.
func ValidateEnvKey(key string) error {
	switch {
	case !envKeyPattern.MatchString(key):
		return fmt.Errorf("env: %q is not a valid variable name", key)
	case strings.HasPrefix(key, "SESH_"), key == "TMUX", key == "TMUX_PANE", key == "CLAUDE_ENV_FILE":
		return fmt.Errorf("env: %s is reserved: sesh sets it for every thread", key)
	case zshSpecialNames[key]:
		return fmt.Errorf("env: %s is a special zsh parameter, which cannot hold an arbitrary value", key)
	}
	return nil
}

// ValidateEnvValue refuses a value that cannot be in an environment.
func ValidateEnvValue(key, value string) error {
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("env: the value of %s contains a NUL byte", key)
	}
	return nil
}

// ThreadEnvDir is thread id's folder. The id is checked because the path is removed
// recursively: an empty or path-like id must never resolve outside it.
func ThreadEnvDir(seshHome, threadID string) (string, error) {
	if seshHome == "" {
		return "", errors.New("thread env: empty sesh home")
	}
	if threadID == "" || threadID == "." || threadID == ".." || strings.ContainsAny(threadID, `/\`) {
		return "", fmt.Errorf("thread env: invalid thread id %q", threadID)
	}
	return filepath.Join(seshHome, ThreadEnvDirName, threadID), nil
}

// ThreadEnvScript is the path of thread id's env.sh.
func ThreadEnvScript(seshHome, threadID string) (string, error) {
	dir, err := ThreadEnvDir(seshHome, threadID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, threadEnvScript), nil
}

// ReadThreadEnv loads thread id's env.json. A thread that never had one is empty,
// not an error; an unreadable file is.
func ReadThreadEnv(seshHome, threadID string) (ThreadEnv, error) {
	dir, err := ThreadEnvDir(seshHome, threadID)
	if err != nil {
		return ThreadEnv{}, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, threadEnvJSON))
	if errors.Is(err, os.ErrNotExist) {
		return ThreadEnv{Vars: map[string]string{}}, nil
	}
	if err != nil {
		return ThreadEnv{}, fmt.Errorf("thread env: %w", err)
	}
	var e ThreadEnv
	if err := json.Unmarshal(raw, &e); err != nil {
		return ThreadEnv{}, fmt.Errorf("thread env: %s: %w", filepath.Join(dir, threadEnvJSON), err)
	}
	if e.Vars == nil {
		e.Vars = map[string]string{}
	}
	return e, nil
}

// WriteThreadEnv stores e and renders env.sh. Each file is replaced atomically, and
// env.sh last: it is the one claude watches, so env.json is current when it fires.
// An unchanged env.sh keeps its mtime, so a launch does not fire the watcher.
func WriteThreadEnv(seshHome, threadID string, e ThreadEnv) error {
	dir, err := ThreadEnvDir(seshHome, threadID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("thread env: %w", err)
	}
	sort.Strings(e.Removed)
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("thread env: %w", err)
	}
	if err := replaceFile(filepath.Join(dir, threadEnvJSON), raw); err != nil {
		return err
	}
	script := filepath.Join(dir, threadEnvScript)
	rendered := []byte(RenderThreadEnv(e))
	if cur, err := os.ReadFile(script); err == nil && string(cur) == string(rendered) {
		return nil
	}
	return replaceFile(script, rendered)
}

func replaceFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("thread env: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("thread env: %w", err)
	}
	return nil
}

// RemoveThreadEnv removes thread id's folder (a deleted thread's, or a spawn that
// failed). A thread that never had one is not an error.
func RemoveThreadEnv(seshHome, threadID string) error {
	dir, err := ThreadEnvDir(seshHome, threadID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("thread env: %w", err)
	}
	return nil
}

// RenderThreadEnv is env.sh's text: an `unset` for every removed name and an
// `export` for every variable, sorted so equal sets render equally.
func RenderThreadEnv(e ThreadEnv) string {
	var b strings.Builder
	for _, k := range e.Removed {
		fmt.Fprintf(&b, "unset %s\n", k)
	}
	for _, k := range SortedEnvKeys(e.Vars) {
		fmt.Fprintf(&b, "export %s=%s\n", k, ShellEscape(e.Vars[k]))
	}
	return b.String()
}

// SortedEnvKeys returns vars' names in order.
func SortedEnvKeys(vars map[string]string) []string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ThreadEnvHash is the version id of a set of variables ("" for none).
func ThreadEnvHash(vars map[string]string) string {
	if len(vars) == 0 {
		return ""
	}
	sum := sha256.New()
	for _, k := range SortedEnvKeys(vars) {
		fmt.Fprintf(sum, "%d:%s%d:%s", len(k), k, len(vars[k]), vars[k])
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
}

// WithThreadEnv is a launch line that sources script, for a thread with variables.
// The values reach the agent by this line rather than `tmux -e`, which at
// new-session writes them to the SESSION environment, where every other pane of
// that session reads them. exec keeps the agent the pane's own process.
func WithThreadEnv(script, command string) string {
	return ". " + ShellEscape(script) + "; exec " + command
}

// ApplyThreadEnv overlays a thread's variables on a headless turn's environment.
func ApplyThreadEnv(base []string, vars map[string]string) []string {
	out := make([]string, 0, len(base)+len(vars))
	for _, e := range base {
		name, _, _ := strings.Cut(e, "=")
		if _, replaced := vars[name]; !replaced {
			out = append(out, e)
		}
	}
	for _, k := range SortedEnvKeys(vars) {
		out = append(out, k+"="+vars[k])
	}
	return out
}

// EnvHookLine is what `sesh env hook` writes to claude's hook env file: a pointer,
// so the values have one home (see the file comment). Expanded when sourced, so no
// path is interpolated into shell. The test makes a deleted thread's still-running
// pane harmless: an error here would fail every shell command.
const EnvHookLine = `[ ! -r "$` + EnvThreadEnvFile + `" ] || . "$` + EnvThreadEnvFile + `"` + "\n"

// ParseEnvLines reads KEY=value lines, the format of `--env-file` and `sesh env
// edit`. Blank lines, # comments and a leading `export ` are allowed. A value is
// literal unless it is quoted: "…" is unquoted like a Go string (FormatEnvLines
// writes multi-line values this way), and '…' is taken as it stands.
func ParseEnvLines(text string) (map[string]string, error) {
	vars := map[string]string{}
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(trimmed, "export "), "=")
		key = strings.TrimSpace(key)
		if !ok {
			return nil, fmt.Errorf("env: line %d: expected KEY=value, got %q", i+1, key)
		}
		switch {
		case len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"':
			unquoted, err := strconv.Unquote(value)
			if err != nil {
				return nil, fmt.Errorf("env: line %d: the value of %s: %w", i+1, key, err)
			}
			value = unquoted
		case len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'':
			value = value[1 : len(value)-1]
		}
		if _, dup := vars[key]; dup {
			return nil, fmt.Errorf("env: line %d: %s is set twice", i+1, key)
		}
		vars[key] = value
	}
	return vars, nil
}

// FormatEnvLines is vars as text ParseEnvLines reads back unchanged.
func FormatEnvLines(vars map[string]string) string {
	var b strings.Builder
	for _, k := range SortedEnvKeys(vars) {
		v := vars[k]
		if v != strings.TrimSpace(v) || strings.ContainsAny(v, "\n\r\"'") {
			v = strconv.Quote(v)
		}
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	return b.String()
}
