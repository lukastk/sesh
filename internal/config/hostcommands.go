package config

// Host commands (schema 74): named commands a machine's owner declares in
// <SESH_HOME>/host-commands/*.toml, which the daemon runs ON ITS OWN HOST when a
// client asks for one by name — over the API, routed across the mesh like fs/list,
// so a client with no shell on that machine (a phone, a browser) can still run it.
// Sesh stays policy-free: it knows nothing about what the commands do. The argv
// comes from the manifest only; a client supplies only the values of declared args,
// which are substituted INTO argv elements (never a shell string).
//
// One file per provider; the provider name is the file stem:
//
//	description = "Boxyard boxes on this machine"
//
//	[[command]]
//	name = "create-box"
//	description = "create a box here"
//	argv = ["boxyard", "new", "--box-name", "{name}"]
//	timeout = "120s"          # default 60s, at most 10m
//
//	[[command.arg]]
//	name = "name"
//	label = "Box name"
//	required = true
//	allow_dash = false        # default: a value starting with "-" is refused

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	// HostCommandDefaultTimeout applies when a command sets no timeout.
	HostCommandDefaultTimeout = 60 * time.Second
	// HostCommandMaxTimeout bounds every command's timeout.
	HostCommandMaxTimeout = 10 * time.Minute
)

// HostCommandPlaceholder matches one {arg} placeholder inside an argv element.
var HostCommandPlaceholder = regexp.MustCompile(`\{([A-Za-z0-9_-]+)\}`)

// hostCommandName is the shape of provider, command and arg names.
var hostCommandName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// HostCommandArgSpec is one value a client may supply.
type HostCommandArgSpec struct {
	Name        string `toml:"name"`
	Label       string `toml:"label"`
	Description string `toml:"description"`
	Required    bool   `toml:"required"`
	// AllowDash admits a value starting with "-". Off by default, because such a
	// value placed as its own argv element is read by most programs as an option.
	AllowDash bool `toml:"allow_dash"`
}

// HostCommandSpec is one [[command]] table.
type HostCommandSpec struct {
	Name        string               `toml:"name"`
	Description string               `toml:"description"`
	Argv        []string             `toml:"argv"`
	TimeoutRaw  string               `toml:"timeout"`
	Args        []HostCommandArgSpec `toml:"arg"`
	Timeout     time.Duration        `toml:"-"`
}

// HostCommandProvider is one manifest file.
type HostCommandProvider struct {
	Name        string            `toml:"-"` // the file stem
	Description string            `toml:"description"`
	Commands    []HostCommandSpec `toml:"command"`
	SourceFile  string            `toml:"-"`
}

// HostCommandsDir is where the manifests live.
func HostCommandsDir(home string) string { return filepath.Join(home, "host-commands") }

// LoadHostCommands reads and validates every manifest. A missing directory means
// no providers; any malformed manifest is a loud error naming its file.
func LoadHostCommands(home string) ([]HostCommandProvider, error) {
	dir := HostCommandsDir(home)
	matches, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, fmt.Errorf("host-commands: glob %s: %w", dir, err)
	}
	sort.Strings(matches)
	out := make([]HostCommandProvider, 0, len(matches))
	for _, path := range matches {
		p, err := loadHostCommandFile(path)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func loadHostCommandFile(path string) (HostCommandProvider, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return HostCommandProvider{}, fmt.Errorf("host-commands: read %s: %w", path, err)
	}
	var p HostCommandProvider
	md, err := toml.NewDecoder(bytes.NewReader(raw)).Decode(&p)
	if err != nil {
		return HostCommandProvider{}, fmt.Errorf("host-commands: parse %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return HostCommandProvider{}, fmt.Errorf("host-commands: %s: unknown keys %s", path, strings.Join(keys, ", "))
	}
	p.Name = strings.TrimSuffix(filepath.Base(path), ".toml")
	p.SourceFile = path
	if err := validateHostCommandProvider(&p); err != nil {
		return HostCommandProvider{}, err
	}
	return p, nil
}

func validateHostCommandProvider(p *HostCommandProvider) error {
	where := p.SourceFile
	if !hostCommandName.MatchString(p.Name) {
		return fmt.Errorf("host-commands: %s: the file stem %q is not a valid provider name (lowercase letters, digits, - and _)", where, p.Name)
	}
	seen := map[string]bool{}
	for i := range p.Commands {
		c := &p.Commands[i]
		if !hostCommandName.MatchString(c.Name) {
			return fmt.Errorf("host-commands: %s: command name %q is not valid (lowercase letters, digits, - and _)", where, c.Name)
		}
		if seen[c.Name] {
			return fmt.Errorf("host-commands: %s: duplicate command %q", where, c.Name)
		}
		seen[c.Name] = true
		if len(c.Argv) == 0 || c.Argv[0] == "" {
			return fmt.Errorf("host-commands: %s/%s: argv is required and its first element names the program", p.Name, c.Name)
		}
		if HostCommandPlaceholder.MatchString(c.Argv[0]) {
			return fmt.Errorf("host-commands: %s/%s: the program (argv[0]) must not contain a placeholder", p.Name, c.Name)
		}
		c.Timeout = HostCommandDefaultTimeout
		if c.TimeoutRaw != "" {
			d, err := time.ParseDuration(c.TimeoutRaw)
			if err != nil || d <= 0 {
				return fmt.Errorf("host-commands: %s/%s: timeout %q is not a positive duration", p.Name, c.Name, c.TimeoutRaw)
			}
			if d > HostCommandMaxTimeout {
				return fmt.Errorf("host-commands: %s/%s: timeout %s exceeds the %s maximum", p.Name, c.Name, d, HostCommandMaxTimeout)
			}
			c.Timeout = d
		}
		args := map[string]bool{}
		for _, a := range c.Args {
			if !hostCommandName.MatchString(a.Name) {
				return fmt.Errorf("host-commands: %s/%s: arg name %q is not valid (lowercase letters, digits, - and _)", p.Name, c.Name, a.Name)
			}
			if args[a.Name] {
				return fmt.Errorf("host-commands: %s/%s: duplicate arg %q", p.Name, c.Name, a.Name)
			}
			args[a.Name] = true
		}
		used := map[string]bool{}
		for _, el := range c.Argv {
			for _, m := range HostCommandPlaceholder.FindAllStringSubmatch(el, -1) {
				if !args[m[1]] {
					return fmt.Errorf("host-commands: %s/%s: argv references undeclared arg {%s}", p.Name, c.Name, m[1])
				}
				used[m[1]] = true
			}
		}
		for _, a := range c.Args {
			if !used[a.Name] {
				return fmt.Errorf("host-commands: %s/%s: arg %q is declared but argv never uses it", p.Name, c.Name, a.Name)
			}
		}
	}
	return nil
}
