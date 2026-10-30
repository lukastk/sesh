package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMyPiKindParsingAndFamily(t *testing.T) {
	kind, err := ParseKind("mypi")
	if err != nil || kind != MyPi {
		t.Fatalf("ParseKind(mypi) = (%q, %v)", kind, err)
	}
	if !Valid(MyPi) {
		t.Fatal("mypi is not a valid kind")
	}
	// A near-miss must stay loud, and the refusal must NAME mypi so a typo is
	// actionable rather than mysterious.
	_, err = ParseKind("my-pi")
	if err == nil || !strings.Contains(err.Error(), "mypi") {
		t.Fatalf("ParseKind(my-pi) = %v", err)
	}
	if Family(MyPi) != Pi {
		t.Fatalf("Family(mypi) = %q, want pi", Family(MyPi))
	}
	for _, k := range []Kind{Pi, Claude, Codex} {
		if Family(k) != k {
			t.Fatalf("Family(%q) = %q, want itself", k, Family(k))
		}
	}
	// The executable is the kind's own name, resolved from PATH — never pi's.
	if Executable(MyPi) != "mypi" || Executable(Pi) != "pi" {
		t.Fatalf("executables = %q / %q", Executable(MyPi), Executable(Pi))
	}
}

// A sesh-spawned mypi pane reports process title "pi" (the fork leaves pi's
// APP_NAME alone), so liveness MUST match on the family. A literal comparison
// here is what would publish every live mypi thread as headless·idle.
func TestPaneKindMatchesComparesPiFamily(t *testing.T) {
	for _, tc := range []struct {
		recorded, detected Kind
		want               bool
	}{
		{MyPi, Pi, true},
		{MyPi, MyPi, true},
		{Pi, MyPi, true},
		{Pi, Pi, true},
		{MyPi, Claude, false},
		{Claude, MyPi, false},
		{Codex, Pi, false},
	} {
		if got := PaneKindMatches(tc.recorded, tc.detected); got != tc.want {
			t.Fatalf("PaneKindMatches(%q, %q) = %v, want %v", tc.recorded, tc.detected, got, tc.want)
		}
	}
}

func TestMyPiLaunchAndResumeArgsUseTheMyPiExecutable(t *testing.T) {
	headed := HeadedCommand(MyPi, "11111111-1111-4111-8111-111111111111", "sonnet", "yolo", []string{"--extra"})
	if !strings.HasPrefix(headed, "mypi --session-id 11111111-1111-4111-8111-111111111111 --model ") {
		t.Fatalf("headed mypi command = %q", headed)
	}
	// pi has no permission system, so yolo adds no flag for mypi either; the
	// configured extra args still pass through, shell-escaped.
	if strings.Contains(headed, "--dangerously") || !strings.HasSuffix(headed, "'--extra'") {
		t.Fatalf("headed mypi command = %q", headed)
	}
	resumed := ResumeCommand(MyPi, "22222222-2222-4222-8222-222222222222", "", "", nil)
	if resumed != "mypi --session-id 22222222-2222-4222-8222-222222222222" {
		t.Fatalf("resume mypi command = %q", resumed)
	}
	// Same shape as pi, different executable: that is the whole difference.
	if got := ResumeCommand(Pi, "22222222-2222-4222-8222-222222222222", "", "", nil); got != "pi --session-id 22222222-2222-4222-8222-222222222222" {
		t.Fatalf("resume pi command = %q", got)
	}
}

// A mypi thread's conversation lives in pi's own store, so the resolver must
// find a pi-format session file for it with no mypi-specific home.
func TestMyPiTranscriptResolvesUnderThePiHome(t *testing.T) {
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	cwd := "/tmp/mypi-project"
	session := "33333333-3333-4333-8333-333333333333"
	dir := filepath.Join(piHome, "sessions", "--tmp-mypi-project--")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "2026-10-30T00-00-00-000Z_"+session+".jsonl")
	body := `{"type":"session","version":3,"id":"` + session + `","cwd":"` + cwd + `"}` + "\n" +
		`{"type":"message","id":"m1","timestamp":"2026-10-30T00:00:01.000Z","message":{"role":"assistant","content":[{"type":"text","text":"from mypi"}],"stopReason":"stop"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	homes := ResolveHomes("")
	got, found, err := TranscriptPath(MyPi, session, cwd, homes)
	if err != nil || !found || got != path {
		t.Fatalf("TranscriptPath(mypi) = (%q, %v, %v), want %q", got, found, err, path)
	}
	reply, count, err := LastReply(MyPi, session, cwd, homes)
	if err != nil || reply != "from mypi" || count != 1 {
		t.Fatalf("LastReply(mypi) = (%q, %d, %v)", reply, count, err)
	}
	if _, err := NativePath(MyPi, session, cwd, homes); err == nil {
		t.Fatal("mypi native path must stay a loud error (pi filenames embed a timestamp)")
	}
}

// The headless turn must run the mypi EXECUTABLE with pi's flags. The fake
// binary lives on a private PATH; runHeadless goes through $SHELL -c, so this
// is the same resolution a real turn performs.
func TestMyPiHeadlessTurnRunsTheMyPiExecutable(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv")
	for _, name := range []string{"mypi", "pi"} {
		script := "#!/bin/sh\nprintf '%s' \"" + name + "\" > " + argvLog + "\nfor a in \"$@\"; do printf ' %s' \"$a\" >> " + argvLog + "; done\necho reply-from-" + name + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SHELL", "/bin/sh")

	session := "44444444-4444-4444-8444-444444444444"
	reply, newSession, err := HeadlessTurn(TurnSpec{
		Kind: MyPi, ThreadID: "55555555-5555-4555-8555-555555555555", SessionID: session,
		Cwd: dir, Prompt: "hello mypi", Model: "sonnet",
	})
	if err != nil {
		t.Fatalf("mypi headless turn: %v", err)
	}
	if reply != "reply-from-mypi" || newSession != session {
		t.Fatalf("mypi headless turn = (%q, %q)", reply, newSession)
	}
	argv, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	want := "mypi --print --session-id " + session + " --model sonnet hello mypi"
	if string(argv) != want {
		t.Fatalf("mypi headless argv = %q, want %q", argv, want)
	}
}

// An absent mypi is a LOUD failure, never a silent fall back to pi.
func TestMyPiHeadlessTurnFailsLoudlyWhenTheExecutableIsMissing(t *testing.T) {
	dir := t.TempDir()
	piOnly := filepath.Join(dir, "pi")
	if err := os.WriteFile(piOnly, []byte("#!/bin/sh\necho reply-from-pi\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("SHELL", "/bin/sh")
	reply, _, err := HeadlessTurn(TurnSpec{
		Kind: MyPi, ThreadID: "66666666-6666-4666-8666-666666666666",
		SessionID: "77777777-7777-4777-8777-777777777777", Cwd: dir, Prompt: "x",
	})
	if err == nil {
		t.Fatalf("a missing mypi produced a reply instead of an error: %q", reply)
	}
	if reply != "" || strings.Contains(reply, "reply-from-pi") {
		t.Fatalf("a missing mypi fell back to pi: %q", reply)
	}
	if !strings.Contains(err.Error(), "mypi") {
		t.Fatalf("missing-mypi error does not name the executable: %v", err)
	}
}
