package agents

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

const hostileArg = "--agent-file=/tmp/it's a $(touch x); `y` \"q\" \\b\nline\x7f"

func writeClaudeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClaudeConfigAppendsToTheGlobalEntryOwnerOnly(t *testing.T) {
	home := t.TempDir()
	global := writeClaudeConfig(t, `{"mcpServers":{"soliton":{"type":"stdio","command":"/bin/sol","args":["serve"]},"other":{"command":"/bin/o"}}}`)
	path, err := WriteClaudeMcpConfig(home, "t1", global, map[string][]string{"soliton": {hostileArg, "--two"}, "other": {"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, McpDirName, "t1", "mcp.json"); path != want {
		t.Errorf("path = %s, want %s", path, want)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		McpServers map[string]claudeMcpEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]claudeMcpEntry{
		"soliton": {Type: "stdio", Command: "/bin/sol", Args: []string{"serve", hostileArg, "--two"}},
		"other":   {Type: "stdio", Command: "/bin/o", Args: []string{"x"}},
	}
	if !reflect.DeepEqual(got.McpServers, want) {
		t.Errorf("got %+v, want %+v", got.McpServers, want)
	}
	file, _ := os.Stat(path)
	dir, _ := os.Stat(filepath.Dir(path))
	if file.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
		t.Errorf("modes: file %o, dir %o", file.Mode().Perm(), dir.Mode().Perm())
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("a temporary file was left behind: %v", entries)
	}
}

func TestClaudeConfigTightensAnExistingFolderAndFollowsChanges(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, McpDirName, "t1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	global := writeClaudeConfig(t, `{"mcpServers":{"s":{"command":"/bin/s"}}}`)
	path, err := WriteClaudeMcpConfig(home, "t1", global, map[string][]string{"s": {"first"}})
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %o", info.Mode().Perm())
	}
	if _, err := WriteClaudeMcpConfig(home, "t1", global, map[string][]string{"s": {"second"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "first") || !strings.Contains(string(raw), "second") {
		t.Errorf("file not rewritten: %s", raw)
	}
}

func TestClaudeConfigRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ config, want string }{
		"missing": {`{"mcpServers":{}}`, "no global MCP server"},
		"env":     {`{"mcpServers":{"s":{"command":"/c","env":{"KEY":"secret-value"}}}}`, "env block"},
		"http":    {`{"mcpServers":{"s":{"type":"http","url":"https://x"}}}`, "only a stdio server"},
		"nocmd":   {`{"mcpServers":{"s":{}}}`, "no command"},
		"corrupt": {`{`, "unexpected end"},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			_, err := WriteClaudeMcpConfig(home, "t1", writeClaudeConfig(t, tc.config), map[string][]string{"s": {"a"}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Error("the error repeats a secret")
			}
			if _, statErr := os.Stat(filepath.Join(home, McpDirName)); !os.IsNotExist(statErr) {
				t.Error("a refused launch wrote a file")
			}
		})
	}
	if _, err := WriteClaudeMcpConfig(t.TempDir(), "t1", filepath.Join(t.TempDir(), "absent"), map[string][]string{"s": {"a"}}); err == nil {
		t.Error("a missing global config was accepted")
	}
}

func TestMcpDirRefusesPathLikeIDs(t *testing.T) {
	for _, id := range []string{"", ".", "..", "a/b", `a\b`} {
		if _, err := McpDir("/home", id); err == nil {
			t.Errorf("id %q accepted", id)
		}
		if err := RemoveMcpConfig("/home", id); err == nil {
			t.Errorf("remove of id %q accepted", id)
		}
	}
	if _, err := McpDir("", "t1"); err == nil {
		t.Error("an empty home was accepted")
	}
}

func TestRemoveMcpConfig(t *testing.T) {
	home := t.TempDir()
	global := writeClaudeConfig(t, `{"mcpServers":{"s":{"command":"/bin/s"}}}`)
	path, err := WriteClaudeMcpConfig(home, "t1", global, map[string][]string{"s": {"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveMcpConfig(home, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file survived")
	}
	if err := RemoveMcpConfig(home, "never-had-one"); err != nil {
		t.Errorf("a thread with none: %v", err)
	}
}

func writeCodexConfig(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestCodexArgsAppendToTheGlobalEntryAndAreValidToml(t *testing.T) {
	home := writeCodexConfig(t, "[mcp_servers.soliton]\ncommand = \"/bin/sol\"\nargs = [\"serve\"]\n\n[mcp_servers.b]\ncommand = \"/bin/b\"\n")
	got, err := CodexMcpArgs(home, map[string][]string{"soliton": {hostileArg}, "b": {"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0] != "-c" || got[2] != "-c" {
		t.Fatalf("argv = %q", got)
	}
	if !strings.HasPrefix(got[1], "mcp_servers.b.args=") || !strings.HasPrefix(got[3], "mcp_servers.soliton.args=") {
		t.Fatalf("order = %q", got)
	}
	// What codex parses is `key=<TOML value>`, so decode exactly that.
	value := strings.TrimPrefix(got[3], "mcp_servers.soliton.args=")
	var decoded struct{ V []string }
	if _, err := toml.Decode("V = "+value, &decoded); err != nil {
		t.Fatalf("not TOML: %v\n%s", err, value)
	}
	if want := []string{"serve", hostileArg}; !reflect.DeepEqual(decoded.V, want) {
		t.Errorf("decoded %q, want %q", decoded.V, want)
	}
}

func TestCodexRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ config, want string }{
		"missing": {"", "no global MCP server"},
		"url":     {"[mcp_servers.s]\nurl = \"https://x\"\n", "only a stdio server"},
		"nocmd":   {"[mcp_servers.s]\nargs = []\n", "no command"},
		"corrupt": {"[[[", "config.toml"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := CodexMcpArgs(writeCodexConfig(t, tc.config), map[string][]string{"s": {"a"}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	if _, err := CodexMcpArgs(t.TempDir(), map[string][]string{"s": {"a"}}); err == nil {
		t.Error("a missing config.toml was accepted")
	}
}

func TestPiArgIsOneElementWithEquals(t *testing.T) {
	got, err := PiMcpArgs(map[string][]string{"soliton": {hostileArg}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "--sesh-mcp-args={") {
		t.Fatalf("argv = %q", got)
	}
	var decoded map[string][]string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got[0], "--sesh-mcp-args=")), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["soliton"][0] != hostileArg {
		t.Errorf("decoded %q", decoded)
	}
}

// A headed launch is a shell line: every harness's argument must come out of a real
// shell as one unchanged argv element, with nothing executed.
func TestHeadedLinePassesArgumentsThroughAShellUnchanged(t *testing.T) {
	canary := filepath.Join(t.TempDir(), "x")
	arg := "--a=$(touch " + canary + ") `touch " + canary + "`; touch " + canary + " 'q' \"d\""
	piArgs, err := PiMcpArgs(map[string][]string{"s": {arg}})
	if err != nil {
		t.Fatal(err)
	}
	codexArgs, err := CodexMcpArgs(writeCodexConfig(t, "[mcp_servers.s]\ncommand = \"/c\"\n"), map[string][]string{"s": {arg}})
	if err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"sh", "zsh", "bash"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		for _, extra := range [][]string{piArgs, codexArgs} {
			line := strings.Replace(HeadedCommand(Pi, "sid", "", "", extra), "pi", "printf '%s\\0'", 1)
			out, err := exec.Command(path, "-c", line).Output()
			if err != nil {
				t.Fatalf("%s: %v\n%s", shell, err, line)
			}
			parts := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
			if got := parts[len(parts)-len(extra):]; !reflect.DeepEqual(got, extra) {
				t.Errorf("%s: argv %q, want %q", shell, got, extra)
			}
		}
	}
	if _, err := os.Stat(canary); err == nil {
		t.Error("a metacharacter ran")
	}
}

// Every family's headless branch must carry the launch arguments, before the prompt.
func TestHeadlessTurnCarriesLaunchArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	for kind, wantBefore := range map[Kind]bool{Claude: true, Codex: true, Pi: true, MyPi: true} {
		t.Run(string(kind), func(t *testing.T) {
			bin := t.TempDir()
			log := filepath.Join(bin, "argv")
			script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + ShellEscape(log) + "; done\n"
			if err := os.WriteFile(filepath.Join(bin, string(kind)), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("SHELL", "/bin/sh")
			spec := TurnSpec{Kind: kind, ThreadID: "t", SessionID: "s", Cwd: bin, Prompt: "PROMPT", HookArgs: []string{"--launch-arg"}}
			HeadlessTurn(spec) //nolint:errcheck // the fake prints no reply; argv is what is under test
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			arg, prompt := strings.Index(string(raw), "--launch-arg"), strings.Index(string(raw), "PROMPT")
			if arg < 0 || prompt < 0 || (arg < prompt) != wantBefore {
				t.Errorf("argv:\n%s", raw)
			}
		})
	}
}
