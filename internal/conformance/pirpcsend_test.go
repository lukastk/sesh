package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("thread.pi-rpc-send", matrix.Pi, loc, func(t *testing.T) { testPiRPCSend(t, loc) })
	}
}

// A real Pi with real auth and the actual extension under test, not a fake
// socket/agent. Only its resource fixtures and keepRecentTokens are test policy.
func piRPCSettings(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(home, ".pi", "agent")
	dir := t.TempDir()
	extension := os.Getenv("PI_RPC_EXTENSION_UNDER_TEST")
	if extension == "" {
		extension = filepath.Join(agentDir, "git", "github.com", "lukastk", "pi-rpc-socket", "index.ts")
	}
	if _, err := os.Stat(extension); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"auth.json", "models.json"} {
		source := filepath.Join(agentDir, name)
		if _, err := os.Stat(source); os.IsNotExist(err) {
			continue
		}
		if err := os.Symlink(source, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(agentDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err = json.Unmarshal(b, &settings); err != nil {
		t.Fatal(err)
	}
	settings["packages"] = []string{}
	settings["extensions"] = []string{extension, filepath.Join(agentDir, "extensions", "sesh-agent-state", "index.ts")}
	settings["compaction"] = map[string]any{"enabled": false, "keepRecentTokens": 512, "reserveTokens": 1024}
	settings["defaultThinkingLevel"] = "off"
	// No MCP package/discovery in this fixture; every operation is still real Pi.
	b, err = json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "settings.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testPiRPCSend(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	dir := piRPCSettings(t)
	cwd := t.TempDir()
	// Registered extension commands use the actual Pi dispatcher; a marker proves
	// execution externally, rather than treating its 'submitted' ACK as completion.
	ext := `import fs from 'node:fs'; import path from 'node:path';
export default function(pi) {
 pi.registerCommand('rpc-fixture', {description:'fixture', handler: async (_args,ctx) => {
  fs.writeFileSync(path.join(ctx.cwd,'command-result'), ctx.ui.getEditorText());
 }});
 pi.registerCommand('rpc-fail', {description:'fixture failure', handler: async () => {throw new Error('RPC-RESOURCE-ASYNC-ERROR');}});
}`
	if err := os.MkdirAll(filepath.Join(dir, "extensions"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extensions", "fixture.ts"), []byte(ext), 0600); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"prompts/rpc-template.md":   "Reply with the uppercase concatenation of TEMPLATE- and EXPANDED. Do not run tools.",
		"skills/rpc-skill/SKILL.md": "---\nname: rpc-skill\ndescription: RPC conformance fixture\n---\nReply with the uppercase concatenation of SKILL- and EXPANDED. Do not run tools.",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	sb := newSandbox(t, loc, withSandboxEnv("PI_CODING_AGENT_DIR", dir))
	sb.startDaemon(t)
	th := sb.newThread(t, "pi", "rpc-send", cwd)
	pane := sb.waitThreadReady(t, th.ID, "pi")
	command := func(text string, wantError bool) api.PiCommandResponse {
		t.Helper()
		out, stderr, err := sb.Runner.Run(t, "thread", "command", "--id", th.ID, "--text", text, "--timeout", "3m", "--json")
		if (err != nil) != wantError {
			t.Fatalf("command %q: %v\n%s\n%s", text, err, out, stderr)
		}
		if wantError {
			t.Logf("expected command refusal: %s", stderr)
			return api.PiCommandResponse{}
		}
		var response api.PiCommandResponse
		if err := json.Unmarshal([]byte(out), &response); err != nil {
			t.Fatal(err, out)
		}
		return response
	}
	command("/compact", true) // Empty real session: never report a kickoff as success.
	info := command("/session", false)
	if info.Operation.Status != "completed" || !strings.Contains(string(info.Operation.Result), th.AgentSessionID) {
		t.Fatal(info)
	}
	command("/name rpc-renamed", false)
	info = command("/session", false)
	if !strings.Contains(string(info.Operation.Result), "rpc-renamed") {
		t.Fatal(info)
	}
	// Losing a recovery id must not turn polling into a new side-effecting request.
	if _, stderr, err := sb.Runner.Run(t, "thread", "command", "--id", th.ID, "--text", "/name MUST-NOT-RENAME", "--request-id", "", "--timeout", "5s"); err == nil || !strings.Contains(stderr, "exactly one") {
		t.Fatalf("empty recovery handle was not refused before dispatch: %v %s", err, stderr)
	}
	if got := command("/name", false); !strings.Contains(string(got.Operation.Result), "rpc-renamed") {
		t.Fatal("empty recovery handle dispatched a new command", got)
	}
	// A completed operation can be recovered without re-executing its text.
	if out, stderr, err := sb.Runner.Run(t, "thread", "command", "--id", th.ID, "--request-id", info.RequestID, "--timeout", "5s", "--json"); err != nil || !strings.Contains(out, "rpc-renamed") {
		t.Fatalf("recover completed operation: %v %s %s", err, out, stderr)
	}
	command("/thinking low", false)
	command("/unknown-rpc-command", true)
	command("/reload", true)
	sb.attachViewer(t, th.SessionName)
	const draft = "DRAFT-NOT-SUBMITTED-π"
	if out, err := sb.rawTmux(t, "send-keys", "-t", "viewer_"+th.SessionName+":", "-l", draft); err != nil {
		t.Fatal(err, out)
	}
	time.Sleep(300 * time.Millisecond)
	capture := func() string { out, _ := sb.rawTmux(t, "capture-pane", "-t", pane, "-p"); return out }
	if !strings.Contains(capture(), draft) {
		t.Fatal("precondition: draft absent", capture())
	}
	if got := command("/rpc-fixture", false).Operation.Status; got != "submitted" {
		t.Fatalf("resource status = %s, want submitted", got)
	}
	marker := filepath.Join(cwd, "command-result")
	if !waitUntil(10*time.Second, func() bool { b, _ := os.ReadFile(marker); return string(b) == draft }) {
		t.Fatal("real command did not observe the preserved editor draft")
	}
	// Public dispatch cannot report asynchronous exceptions. Prove they surface in
	// the real UI, rather than advertising a completed operation to the caller.
	if got := command("/rpc-fail", false).Operation.Status; got != "submitted" {
		t.Fatal(got)
	}
	if !waitUntil(10*time.Second, func() bool { return strings.Contains(capture(), "RPC-RESOURCE-ASYNC-ERROR") }) {
		t.Fatal("resource failure disappeared", capture())
	}
	transcript := func() string {
		out, stderr, err := sb.Runner.Run(t, "thread", "transcript", "--id", th.ID, "--json")
		if err != nil {
			if strings.Contains(stderr, "no transcript on disk") {
				return ""
			}
			t.Fatalf("transcript: %v %s", err, stderr)
		}
		var tr struct {
			Lines []string `json:"lines"`
		}
		if err := json.Unmarshal([]byte(out), &tr); err != nil {
			t.Fatal(err)
		}
		return strings.Join(tr.Lines, "\n")
	}
	send := func(text string) {
		t.Helper()
		out, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", th.ID, "--text", text, "--respect-typing", "60s")
		if err != nil || !strings.Contains(out, "sent ") {
			t.Fatalf("RPC must bypass typing guard: %v %s %s", err, out, stderr)
		}
	}
	// The marker is created BY the real tool before sleeping, not inferred from
	// a tool-call line that may have been persisted before the process started.
	send("Use bash to run exactly: touch tool-started; sleep 10; touch tool-finished; python3 -c 'print(\"compaction fixture line\\n\" * 1500)'. Then reply with RPC-FIRST-DONE. Do not do anything else.")
	if !waitUntil(90*time.Second, func() bool { _, err := os.Stat(filepath.Join(cwd, "tool-started")); return err == nil }) {
		t.Fatal("real tool never started", capture())
	}
	send("After the running tool completes, reply with the uppercase concatenation of RPC- and STEER-DONE. Do not abort or run more tools.")
	if !waitUntil(150*time.Second, func() bool {
		return strings.Contains(transcript(), "RPC-STEER-DONE") && sb.threadStatus(t, th.ID).Busy == api.BusyIdle
	}) {
		t.Fatal("steer never answered", capture())
	}
	if _, err := os.Stat(filepath.Join(cwd, "tool-finished")); err != nil {
		t.Fatal("steering aborted the tool", err)
	}
	if strings.Contains(transcript(), draft) || !strings.Contains(capture(), draft) {
		t.Fatal("draft submitted or lost", capture())
	}
	// A slash-prefixed message is LITERAL: /name must reach the conversation,
	// not mutate session metadata or consume the editor draft.
	send("/name LITERAL-NOT-A-RENAME (This is literal input; reply briefly and do not use tools.)")
	if !waitUntil(90*time.Second, func() bool {
		return strings.Contains(transcript(), "LITERAL-NOT-A-RENAME") && sb.threadStatus(t, th.ID).Busy == api.BusyIdle
	}) {
		t.Fatal("literal slash did not reach the conversation")
	}
	info = command("/session", false)
	if !strings.Contains(string(info.Operation.Result), "rpc-renamed") {
		t.Fatal("slash message executed as command", info)
	}
	result := command("/compact Retain RPC-STEER-DONE in the summary.", false)
	if result.Operation.Status != "completed" {
		t.Fatal("compaction not completed", result)
	}
	if !waitUntil(10*time.Second, func() bool { return strings.Contains(transcript(), `"type":"compaction"`) }) {
		t.Fatal("reported compaction success without a persisted compaction")
	}
	if strings.Contains(transcript(), draft) || !strings.Contains(capture(), draft) {
		t.Fatal("compaction submitted or lost editor draft", capture())
	}
	for _, resource := range []struct{ command, reply string }{{"/rpc-template", "TEMPLATE-EXPANDED"}, {"/skill:rpc-skill", "SKILL-EXPANDED"}} {
		if got := command(resource.command, false).Operation.Status; got != "submitted" {
			t.Fatal(got)
		}
		if !waitUntil(90*time.Second, func() bool {
			return strings.Contains(transcript(), resource.reply) && sb.threadStatus(t, th.ID).Busy == api.BusyIdle
		}) {
			t.Fatalf("resource %s did not actually expand/run: %s", resource.command, capture())
		}
		if strings.Contains(transcript(), draft) || !strings.Contains(capture(), draft) {
			t.Fatal("resource dispatch altered draft", capture())
		}
	}
	// Initial --msg is a separate launch path and must also use RPC. A literal
	// /name arriving in the transcript discriminates against terminal dispatch.
	initial, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", "pi", "--name", "initial-rpc", "--cwd", cwd, "--no-parent", "--json", "--msg", "/name INITIAL-LITERAL (Literal message: reply briefly, no tools.)")
	if err != nil {
		t.Fatal(err, stderr)
	}
	var child api.Thread
	if err := json.Unmarshal([]byte(initial), &child); err != nil {
		t.Fatal(err)
	}
	sb.waitThreadReady(t, child.ID, "pi")
	if !waitUntil(90*time.Second, func() bool {
		out, _, err := sb.Runner.Run(t, "thread", "transcript", "--id", child.ID, "--json")
		return err == nil && strings.Contains(out, "INITIAL-LITERAL")
	}) {
		t.Fatal("initial --msg was not delivered literally over RPC")
	}
	// Socket loss while the terminal remains healthy MUST NOT paste as a fallback.
	socket, ok := findPiRPCSocket(th.AgentSessionID)
	if !ok {
		t.Fatal("socket missing before failure test")
	}
	moved := socket + ".test-away"
	if err := os.Rename(socket, moved); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(moved, socket)
	out, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", th.ID, "--text", "MUST-NOT-BE-PASTED", "--respect-typing", "0")
	if err == nil || !strings.Contains(stderr, "RPC socket missing") {
		t.Fatalf("missing socket: %v %s %s", err, out, stderr)
	}
	if strings.Contains(capture(), "MUST-NOT-BE-PASTED") || !strings.Contains(capture(), draft) {
		t.Fatal("missing socket fell back to terminal paste", capture())
	}
	t.Logf("real Pi draft, steering, commands, compaction and no-fallback proved over %s", fmt.Sprint(loc))
}
