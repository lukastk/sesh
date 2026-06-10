package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Session naming is configurable POLICY over a fixed mechanism: `[[session_name]]`
// rules in <SESH_HOME>/config.toml map a thread's cwd (matched in ~-relative form)
// to a templated tmux session name. sesh ships NO rules — without a config (or when
// no rule matches) the default `sesh_<sanitized-name>` convention applies. The rule
// FILE is owned by the user's dotfiles (myrig), like the tmux confs.
//
//	[[session_name]]
//	match = '^~/dev/[0-9]{8}_(?P<boxid>[a-z0-9]+)__(?P<boxname>[^/]+)$'
//	name  = '{boxname} <{boxid}> ({tid8})'
//
// First matching rule wins. Templates may use the rule's NAMED capture groups plus
// the built-ins {tid} (thread uuid), {tid8} (first 8 chars), {name} (thread name),
// {cwd} (~-relative cwd). An unknown placeholder is a LOUD error (a typo must never
// silently produce a wrong name). tmux forbids ':' and '.' in session names — they
// are sanitized to '_' after expansion.

// SessionNameRule is one cwd→name rule.
type SessionNameRule struct {
	Match string `toml:"match"`
	Name  string `toml:"name"`
	re    *regexp.Regexp
}

// Naming is the compiled rule set (nil = no config, default naming).
type Naming struct {
	rules []SessionNameRule
}

type seshConfigFile struct {
	SessionName []SessionNameRule `toml:"session_name"`
}

// ConfigPath returns the sesh config file path for a SESH_HOME.
func ConfigPath(home string) string { return filepath.Join(home, "config.toml") }

// LoadNaming reads and compiles the session-name rules from <home>/config.toml.
// A missing file is the legitimate no-rules state (nil, nil); a present-but-broken
// file (bad TOML, bad regex, empty fields) is a LOUD error — the daemon refuses to
// start on a misconfigured naming policy rather than silently falling back.
func LoadNaming(home string) (*Naming, error) {
	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", ConfigPath(home), err)
	}
	var f seshConfigFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", ConfigPath(home), err)
	}
	for i := range f.SessionName {
		r := &f.SessionName[i]
		if r.Match == "" || r.Name == "" {
			return nil, fmt.Errorf("config: session_name rule %d: match and name are both required", i+1)
		}
		re, err := regexp.Compile(r.Match)
		if err != nil {
			return nil, fmt.Errorf("config: session_name rule %d: bad match regex: %w", i+1, err)
		}
		r.re = re
	}
	if len(f.SessionName) == 0 {
		return nil, nil
	}
	return &Naming{rules: f.SessionName}, nil
}

// placeholderRe finds {placeholder} tokens in a name template.
var placeholderRe = regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)

// SessionNameFor applies the first matching rule to a thread's cwd and returns the
// expanded session name. matched=false means no rule applied (use the default
// convention). An unknown placeholder or an empty expansion is a loud error.
func (n *Naming) SessionNameFor(cwd, threadID, threadName, userHome string) (name string, matched bool, err error) {
	if n == nil {
		return "", false, nil
	}
	// Match in ~-relative form so rules are portable across machines/homes.
	matchPath := cwd
	if userHome != "" {
		if rel, ok := strings.CutPrefix(cwd, strings.TrimRight(userHome, "/")); ok {
			matchPath = "~" + rel
		}
	}
	tid8 := threadID
	if len(tid8) > 8 {
		tid8 = tid8[:8]
	}
	for i, r := range n.rules {
		m := r.re.FindStringSubmatch(matchPath)
		if m == nil {
			continue
		}
		vars := map[string]string{
			"tid":  threadID,
			"tid8": tid8,
			"name": threadName,
			"cwd":  matchPath,
		}
		for gi, gname := range r.re.SubexpNames() {
			if gname != "" && gi < len(m) {
				vars[gname] = m[gi]
			}
		}
		var expandErr error
		out := placeholderRe.ReplaceAllStringFunc(r.Name, func(tok string) string {
			key := tok[1 : len(tok)-1]
			v, ok := vars[key]
			if !ok {
				expandErr = fmt.Errorf("config: session_name rule %d: unknown placeholder %s (have named groups + tid/tid8/name/cwd)", i+1, tok)
			}
			return v
		})
		if expandErr != nil {
			return "", false, expandErr
		}
		// tmux forbids ':' and '.' in session names.
		out = strings.NewReplacer(":", "_", ".", "_").Replace(out)
		out = strings.TrimSpace(out)
		if out == "" {
			return "", false, fmt.Errorf("config: session_name rule %d expanded to an empty name for cwd %s", i+1, matchPath)
		}
		return out, true, nil
	}
	return "", false, nil
}
