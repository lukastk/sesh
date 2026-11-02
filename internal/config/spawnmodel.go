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

// SpawnModelAgents are the agent tables a default model may be written to — the
// same set LoadSpawn accepts as [spawn.<agent>].
var SpawnModelAgents = []string{"claude", "codex", "pi", "mypi"}

// SetSpawnModel writes (model != "") or removes (model == "") the default model
// of one agent: the `model` key of its [spawn.<agent>] table in
// <home>/config.toml. It returns the file it actually wrote.
//
// It is a TEXT edit of that one key, not a decode/re-encode of the file: the
// file is hand-maintained and heavily commented, and a TOML round-trip would
// drop every comment. An existing `model = …` line is replaced in place; a
// missing one is inserted under the table header; a missing table is appended.
// Every other line is preserved byte for byte (the replaced `model` line itself
// is rewritten whole, so a trailing comment on it does not survive).
//
// It writes THROUGH a symlink, never over it. config.toml may be a symlink into
// a synced dotfiles checkout so one machine's setting reaches the others; a
// temp-file + rename onto the link path would replace the link with a plain
// file and silently detach this machine. So the link is resolved first and the
// temp file is created next to the TARGET.
//
// Forms it cannot edit surgically are refused, never guessed at: an agent table
// written as an inline table (`claude = { … }` under [spawn]) or as dotted keys
// (`claude.model = …`). After writing, the file is re-read through LoadSpawn and
// must yield exactly the requested default — anything else is an error.
func SetSpawnModel(home, agent, model string) (string, error) {
	known := false
	for _, a := range SpawnModelAgents {
		if a == agent {
			known = true
		}
	}
	if !known {
		return "", fmt.Errorf("config: unknown agent %q for a default model (valid: %s)", agent, strings.Join(SpawnModelAgents, ", "))
	}
	if model != "" {
		if strings.TrimSpace(model) != model {
			return "", fmt.Errorf("config: model %q has leading or trailing whitespace", model)
		}
		for _, r := range model {
			if r < 0x20 || r == 0x7f {
				return "", fmt.Errorf("config: model %q contains a control character", model)
			}
		}
	}

	linkPath := ConfigPath(home)
	target, err := filepath.EvalSymlinks(linkPath)
	var raw []byte
	switch {
	case err == nil:
		if raw, err = os.ReadFile(target); err != nil {
			return "", fmt.Errorf("config: read %s: %w", target, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// No file yet is a legitimate state (a fresh machine) — unless it is a
		// DANGLING symlink, which means the synced target is missing: writing a
		// plain file here would hide that, so refuse.
		if fi, lerr := os.Lstat(linkPath); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("config: %s is a symlink to a missing file; restore its target first", linkPath)
		}
		if model == "" {
			return linkPath, nil // nothing to remove
		}
		target = linkPath
	default:
		return "", fmt.Errorf("config: resolve %s: %w", linkPath, err)
	}

	edited, err := editSpawnModel(string(raw), agent, model)
	if err != nil {
		return "", fmt.Errorf("config: %s: %w", target, err)
	}
	if edited == string(raw) {
		return target, nil
	}
	if err := writeFileAtomic(target, []byte(edited)); err != nil {
		return "", err
	}
	got, err := LoadSpawn(home)
	if err != nil {
		return "", fmt.Errorf("config: %s no longer loads after writing the default model: %w", target, err)
	}
	if got.ModelFor(agent) != model {
		return "", fmt.Errorf("config: wrote %s but it reads back default model %q for %s, not %q", target, got.ModelFor(agent), agent, model)
	}
	return target, nil
}

// headerRe matches any table or array-of-tables header line; tableName pulls
// out the (possibly quoted, possibly spaced) dotted name.
var headerRe = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*(#.*)?$`)

var modelKeyRe = regexp.MustCompile(`^(\s*)model\s*=`)

// normTableName canonicalises `spawn . "claude"` to `spawn.claude`.
func normTableName(name string) string {
	parts := strings.Split(name, ".")
	for i, p := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(p), `"'`)
	}
	return strings.Join(parts, ".")
}

func editSpawnModel(text, agent, model string) (string, error) {
	// Refuse the forms a line edit cannot handle. Decoding to a generic map is
	// enough to see whether [spawn].<agent> exists at all; whether it exists AS A
	// HEADER TABLE is what the line scan below establishes.
	var doc map[string]any
	if _, err := toml.Decode(text, &doc); err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	want := "spawn." + agent
	lines := strings.SplitAfter(text, "\n")
	header := -1
	for i, l := range lines {
		m := headerRe.FindStringSubmatch(strings.TrimRight(l, "\r\n"))
		if m != nil && !strings.HasPrefix(strings.TrimSpace(l), "[[") && normTableName(m[1]) == want {
			header = i
			break
		}
	}
	spawn, _ := doc["spawn"].(map[string]any)
	if _, defined := spawn[agent]; defined && header < 0 {
		return "", fmt.Errorf("[spawn.%s] is written as an inline table or dotted keys, which sesh cannot edit in place; rewrite it as a [spawn.%s] table", agent, agent)
	}

	value := "model = " + tomlBasicString(model) + "\n"
	if header < 0 {
		if model == "" {
			return text, nil
		}
		// Put the new table with its family: straight after the last content line
		// of the last [spawn] / [spawn.*] section, ahead of any blank lines or
		// comments that introduce the next table. With no [spawn] family at all,
		// append it.
		at := -1
		for i := 0; i < len(lines); i++ {
			m := headerRe.FindStringSubmatch(strings.TrimRight(lines[i], "\r\n"))
			if m == nil {
				continue
			}
			if n := normTableName(m[1]); n == "spawn" || strings.HasPrefix(n, "spawn.") {
				at = i
				for j := i + 1; j < len(lines) && !headerRe.MatchString(strings.TrimRight(lines[j], "\r\n")); j++ {
					if t := strings.TrimSpace(lines[j]); t != "" && !strings.HasPrefix(t, "#") {
						at = j
					}
				}
			}
		}
		block := "\n[spawn." + agent + "]\n" + value
		if at < 0 {
			out := text
			if out != "" && !strings.HasSuffix(out, "\n") {
				out += "\n"
			}
			return out + block, nil
		}
		if !strings.HasSuffix(lines[at], "\n") {
			lines[at] += "\n"
		}
		out := append(append(lines[:at+1:at+1], block), lines[at+1:]...)
		return strings.Join(out, ""), nil
	}
	end := len(lines)
	for i := header + 1; i < len(lines); i++ {
		if headerRe.MatchString(strings.TrimRight(lines[i], "\r\n")) {
			end = i
			break
		}
	}
	for i := header + 1; i < end; i++ {
		m := modelKeyRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		if model == "" {
			rest := append(lines[:i:i], lines[i+1:]...)
			// A table left holding nothing (only blank lines) goes too, with the
			// blank separator line ahead of it, so set-then-unset leaves no residue.
			// A table that still has keys or comments is kept.
			empty := true
			for j := header + 1; j < end-1; j++ {
				if strings.TrimSpace(rest[j]) != "" {
					empty = false
				}
			}
			if empty {
				from := header
				if from > 0 && strings.TrimSpace(rest[from-1]) == "" {
					from--
				}
				rest = append(rest[:from:from], rest[header+1:]...)
			}
			return strings.Join(rest, ""), nil
		}
		lines[i] = m[1] + value
		return strings.Join(lines, ""), nil
	}
	if model == "" {
		return text, nil
	}
	if !strings.HasSuffix(lines[header], "\n") {
		lines[header] += "\n"
	}
	out := append(append(lines[:header+1:header+1], value), lines[header+1:]...)
	return strings.Join(out, ""), nil
}

// tomlBasicString renders s as a TOML basic string. SetSpawnModel has already
// refused control characters, so only the quote and the backslash need escaping.
func tomlBasicString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// writeFileAtomic replaces path's contents via a temp file in the same
// directory, keeping the file's permission bits.
func writeFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config.toml.*")
	if err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmpName, mode)
	}
	if werr == nil {
		werr = os.Rename(tmpName, path)
	}
	if werr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("config: write %s: %w", path, werr)
	}
	return nil
}
