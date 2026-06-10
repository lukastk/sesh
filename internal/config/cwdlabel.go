package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// CWD labels (PARITY_ROADMAP A2, v1's [[tui.cwd_rules]] re-idiomized):
// `[[cwd_label]]` rules in <SESH_HOME>/config.toml transform a thread's cwd
// into the display label the TUI's CWD column (and `sesh cwd-label`, so hooks
// and scripts render identically) shows. Same rule language as [[session_name]]
// — cwd matched in ~-relative form, first match wins, templates over named
// capture groups + {cwd} — but labels carry no thread identity (no {tid}) and
// are NOT tmux-sanitized (a label may contain '.' or ':').
//
//	[[cwd_label]]
//	match = '^~/dev/[0-9]{8}_(?P<boxid>[a-z0-9]+)__(?P<boxname>[^/]+)$'
//	label = '{boxname} <{boxid}>'
//
// No match → the ~-relative path itself (the universal "rule 4"). A broken
// rule (bad TOML/regex, unknown placeholder, empty expansion) is a LOUD error.

// CwdLabelRule is one cwd→label rule.
type CwdLabelRule struct {
	Match string `toml:"match"`
	Label string `toml:"label"`
	re    *regexp.Regexp
}

// Labeling is the compiled rule set (nil = no config: ~-relative fallback only).
type Labeling struct {
	rules []CwdLabelRule
}

type cwdLabelFile struct {
	CwdLabel []CwdLabelRule `toml:"cwd_label"`
}

// LoadCwdLabels reads and compiles the [[cwd_label]] rules from
// <home>/config.toml. Missing file or no rules = (nil, nil); a broken rule is
// a LOUD error (parity with LoadNaming).
func LoadCwdLabels(home string) (*Labeling, error) {
	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", ConfigPath(home), err)
	}
	var f cwdLabelFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", ConfigPath(home), err)
	}
	for i := range f.CwdLabel {
		r := &f.CwdLabel[i]
		if r.Match == "" || r.Label == "" {
			return nil, fmt.Errorf("config: cwd_label rule %d: match and label are both required", i+1)
		}
		re, err := regexp.Compile(r.Match)
		if err != nil {
			return nil, fmt.Errorf("config: cwd_label rule %d: bad match regex: %w", i+1, err)
		}
		r.re = re
		// Placeholders are STATICALLY checkable (named groups + {cwd}) — a typo
		// refuses the load, not the first cwd that happens to match the rule.
		valid := map[string]bool{"cwd": true}
		for _, g := range re.SubexpNames() {
			if g != "" {
				valid[g] = true
			}
		}
		for _, tok := range placeholderRe.FindAllString(r.Label, -1) {
			if key := tok[1 : len(tok)-1]; !valid[key] {
				return nil, fmt.Errorf("config: cwd_label rule %d: unknown placeholder %s (have named groups + {cwd})", i+1, tok)
			}
		}
	}
	if len(f.CwdLabel) == 0 {
		return nil, nil
	}
	return &Labeling{rules: f.CwdLabel}, nil
}

// TildeRelative renders a path ~-relative to userHome (the universal display
// fallback when no rule matches — Lukas's "rule 4").
func TildeRelative(cwd, userHome string) string {
	if userHome != "" {
		if rel, ok := strings.CutPrefix(cwd, strings.TrimRight(userHome, "/")); ok {
			return "~" + rel
		}
	}
	return cwd
}

// LabelFor applies the first matching rule to a cwd and returns the display
// label. No rule matched (or nil Labeling) → the ~-relative path. An unknown
// placeholder or empty expansion is a loud error.
func (l *Labeling) LabelFor(cwd, userHome string) (string, error) {
	matchPath := TildeRelative(cwd, userHome)
	if l == nil {
		return matchPath, nil
	}
	for i, r := range l.rules {
		m := r.re.FindStringSubmatch(matchPath)
		if m == nil {
			continue
		}
		vars := map[string]string{"cwd": matchPath}
		for gi, gname := range r.re.SubexpNames() {
			if gname != "" && gi < len(m) {
				vars[gname] = m[gi]
			}
		}
		var expandErr error
		out := placeholderRe.ReplaceAllStringFunc(r.Label, func(tok string) string {
			key := tok[1 : len(tok)-1]
			v, ok := vars[key]
			if !ok {
				expandErr = fmt.Errorf("config: cwd_label rule %d: unknown placeholder %s (have named groups + cwd)", i+1, tok)
			}
			return v
		})
		if expandErr != nil {
			return "", expandErr
		}
		out = strings.TrimSpace(out)
		if out == "" {
			return "", fmt.Errorf("config: cwd_label rule %d expanded to an empty label for cwd %s", i+1, matchPath)
		}
		return out, nil
	}
	return matchPath, nil
}
