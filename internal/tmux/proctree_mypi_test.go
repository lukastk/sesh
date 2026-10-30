package tmux

import "testing"

// The pane-process matcher must recognize a process whose argv0 really is mypi,
// keep every existing kind unchanged, and still refuse the near-misses the
// anchoring exists for. It deliberately does NOT claim to tell the two pi
// executables apart: the fork sets process.title = "pi", so a mypi pane usually
// prints "pi" here — which is why liveness compares families.
func TestAgentReMatchesMyPiWithoutLooseningTheAnchors(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    string
	}{
		{"mypi --session-id 11111111-1111-4111-8111-111111111111", "mypi"},
		{"/home/u/.local/bin/mypi --print", "mypi"},
		{"mypi", "mypi"},
		{"pi --session-id abc", "pi"},
		{"/usr/bin/pi", "pi"},
		{"claude --resume x", "claude"},
		{"codex exec", "codex"},
		// Not agents: a longer word that merely starts with a kind name, and a
		// wrapper script whose argv0 is the shell.
		{"mypinta --open", ""},
		{"pinta", ""},
		{"mypi-helper run", ""},
		{"/bin/sh /home/u/.local/bin/mypi", ""},
		{"node /opt/mypi/cli.js", ""},
	} {
		m := agentRe.FindStringSubmatch(tc.command)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != tc.want {
			t.Fatalf("agentRe(%q) = %q, want %q", tc.command, got, tc.want)
		}
	}
}

// findAgent resolves a kind through the same regex, so a mypi process under a
// pane resolves as mypi rather than as no agent at all.
func TestFindAgentResolvesMyPiUnderAPane(t *testing.T) {
	idx := &procIndex{
		byPID:    map[int]proc{10: {pid: 10, ppid: 1, command: "-zsh"}, 11: {pid: 11, ppid: 10, command: "mypi --session-id s"}},
		children: map[int][]int{10: {11}},
	}
	agent, ok := idx.findAgent(10, 4)
	if !ok || agent.Kind != "mypi" || agent.PID != 11 {
		t.Fatalf("findAgent = (%+v, %v), want the mypi child", agent, ok)
	}
}
