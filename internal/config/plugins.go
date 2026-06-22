package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
)

// Plugins are the daemon command-provider substrate. Each <SESH_HOME>/plugins/*.toml
// file declares one plugin: a name plus [[list]] and [[action]] capabilities the
// daemon runs ON ITS OWN HOST. This package loads + VALIDATES the manifests; the
// daemon runs the configured commands and the app surfaces them. See api/plugins.go
// for the wire shape and the endpoint contract.
//
// Example (the shipped boxyard manifest):
//
//	name = "boxyard"
//	description = "Boxyard boxes on this machine"
//
//	[[list]]
//	name = "boxes"
//	command = ["boxyard", "list", "--output-format", "json"]
//	id     = "{creation_timestamp_utc}_{box_subid}__{name}"
//	label  = "{name}"
//	groups = "groups"
//	path   = "~/dev/{creation_timestamp_utc}_{box_subid}__{name}"
//
//	[[action]]
//	name = "create-box"
//	command = ["boxyard", "new", "--name", "{name}"]
//	[[action.field]]
//	name = "name"
//	label = "Box name"
//	type = "text"
//	required = true

// PluginFieldSpec is one form input of an [[action]] capability.
type PluginFieldSpec struct {
	Name     string `toml:"name"`
	Label    string `toml:"label"`
	Type     string `toml:"type"`
	Required bool   `toml:"required"`
}

// PluginListSpec is a [[list]] capability: a command whose JSON output is mapped to
// {id,label,groups,path}. id/label/path are templates over each item's fields; groups
// names a string-array field; items is the dotted path to the array (empty = root).
type PluginListSpec struct {
	Name        string   `toml:"name"`
	Description string   `toml:"description"`
	Command     []string `toml:"command"`
	Items       string   `toml:"items"`
	ID          string   `toml:"id"`
	Label       string   `toml:"label"`
	Groups      string   `toml:"groups"`
	Path        string   `toml:"path"`
}

// PluginActionSpec is an [[action]] capability: a command with {field} placeholders
// filled from the form field values (as ARGV).
type PluginActionSpec struct {
	Name        string            `toml:"name"`
	Description string            `toml:"description"`
	Command     []string          `toml:"command"`
	Fields      []PluginFieldSpec `toml:"field"`
}

// PluginSpec is one parsed + validated manifest file.
type PluginSpec struct {
	Name        string             `toml:"name"`
	Description string             `toml:"description"`
	Lists       []PluginListSpec   `toml:"list"`
	Actions     []PluginActionSpec `toml:"action"`
	SourceFile  string             `toml:"-"` // the manifest path, for error messages
}

// knownFieldTypes are the action field input types the app knows how to render.
var knownFieldTypes = map[string]bool{"text": true, "number": true, "boolean": true}

// placeholderRe ({placeholder} tokens) is shared with the session-naming templates
// (naming.go) — the SAME token set the daemon's plugin renderer substitutes.

// PluginsDir is <home>/plugins.
func PluginsDir(home string) string { return filepath.Join(home, "plugins") }

// LoadPlugins reads + validates every <home>/plugins/*.toml manifest. A missing
// directory is a legitimate "no plugins" state (empty slice, nil error) — not a
// fallback masking a bug. A malformed manifest is a LOUD error (the daemon surfaces
// it), never silently skipped. Returns the plugins sorted by name.
func LoadPlugins(home string) ([]PluginSpec, error) {
	dir := PluginsDir(home)
	matches, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, fmt.Errorf("plugins: glob %s: %w", dir, err)
	}
	if len(matches) == 0 {
		if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
			return nil, nil
		}
		return nil, nil
	}
	sort.Strings(matches)
	out := make([]PluginSpec, 0, len(matches))
	seen := map[string]string{} // plugin name → file (for dup detection)
	for _, path := range matches {
		var spec PluginSpec
		if _, err := toml.DecodeFile(path, &spec); err != nil {
			return nil, fmt.Errorf("plugins: parse %s: %w", path, err)
		}
		spec.SourceFile = path
		if err := ValidatePlugin(spec); err != nil {
			return nil, err
		}
		if prev, dup := seen[spec.Name]; dup {
			return nil, fmt.Errorf("plugins: duplicate plugin name %q in %s and %s", spec.Name, prev, path)
		}
		seen[spec.Name] = path
		out = append(out, spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ValidatePlugin enforces the manifest invariants LOUDLY: a typo or an undeclared
// {field} placeholder is refused at load, not silently dropped.
func ValidatePlugin(s PluginSpec) error {
	where := s.SourceFile
	if s.Name == "" {
		return fmt.Errorf("plugins: %s: name is required", where)
	}
	capNames := map[string]bool{}
	declare := func(name string) error {
		if name == "" {
			return fmt.Errorf("plugins: %s (%s): capability name is required", s.Name, where)
		}
		if capNames[name] {
			return fmt.Errorf("plugins: %s (%s): duplicate capability name %q", s.Name, where, name)
		}
		capNames[name] = true
		return nil
	}
	for _, l := range s.Lists {
		if err := declare(l.Name); err != nil {
			return err
		}
		if len(l.Command) == 0 {
			return fmt.Errorf("plugins: %s/%s: command is required", s.Name, l.Name)
		}
		if l.ID == "" || l.Label == "" {
			return fmt.Errorf("plugins: %s/%s: list map requires both id and label", s.Name, l.Name)
		}
		// A list command takes no form fields, so it must carry no placeholders.
		for _, arg := range l.Command {
			if ph := placeholderRe.FindString(arg); ph != "" {
				return fmt.Errorf("plugins: %s/%s: list command must not contain placeholders (found %s)", s.Name, l.Name, ph)
			}
		}
	}
	for _, a := range s.Actions {
		if err := declare(a.Name); err != nil {
			return err
		}
		if len(a.Command) == 0 {
			return fmt.Errorf("plugins: %s/%s: command is required", s.Name, a.Name)
		}
		fieldNames := map[string]bool{}
		for _, f := range a.Fields {
			if f.Name == "" || f.Label == "" {
				return fmt.Errorf("plugins: %s/%s: every field needs a name and a label", s.Name, a.Name)
			}
			if !knownFieldTypes[f.Type] {
				return fmt.Errorf("plugins: %s/%s: field %q has unknown type %q (want text|number|boolean)", s.Name, a.Name, f.Name, f.Type)
			}
			if fieldNames[f.Name] {
				return fmt.Errorf("plugins: %s/%s: duplicate field %q", s.Name, a.Name, f.Name)
			}
			fieldNames[f.Name] = true
		}
		// Every {placeholder} in the command must reference a declared field.
		for _, arg := range a.Command {
			for _, m := range placeholderRe.FindAllStringSubmatch(arg, -1) {
				if !fieldNames[m[1]] {
					return fmt.Errorf("plugins: %s/%s: command references undeclared field {%s}", s.Name, a.Name, m[1])
				}
			}
		}
	}
	return nil
}
