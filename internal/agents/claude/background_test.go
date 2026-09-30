package claude

import (
	"strings"
	"testing"
)

// The shape is a verbatim capture of `claude agents --json` on 2.1.286, with one
// real background entry and one interactive one.
const realAgentsJSON = `[
  {
    "pid": 2020632,
    "id": "11111111",
    "cwd": "/tmp/work",
    "kind": "background",
    "startedAt": 1790804674020,
    "sessionId": "11111111-2222-3333-4444-555555555555",
    "name": "Reply with exactly: BGHELD",
    "status": "idle",
    "state": "done"
  },
  {
    "pid": 1720039,
    "cwd": "/home/lukastk/mysetup/sesh",
    "kind": "interactive",
    "startedAt": 1790801640000,
    "sessionId": "885dba6b-6f5d-4441-bfb1-6cbfc31e16a3",
    "name": "mysetup - sesh",
    "status": "busy"
  }
]`

func TestParseBackgroundSessionsExtractsOnlyBackground(t *testing.T) {
	got, err := ParseBackgroundSessions([]byte(realAgentsJSON))
	if err != nil {
		t.Fatalf("ParseBackgroundSessions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d background sessions, want 1 (the interactive entry must not count): %+v", len(got), got)
	}
	bg := got[0]
	if bg.ID != "11111111" {
		t.Errorf("ID = %q, want the SHORT id 11111111 (the only form `claude stop` accepts)", bg.ID)
	}
	if bg.SessionID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("SessionID = %q, want the full uuid (the key a thread's leaf is matched on)", bg.SessionID)
	}
	if bg.Cwd != "/tmp/work" || bg.State != "done" || bg.Name != "Reply with exactly: BGHELD" || bg.Status != "idle" {
		t.Errorf("passthrough fields wrong: %+v", bg)
	}
}

func TestParseBackgroundSessionsEmptyRegistry(t *testing.T) {
	got, err := ParseBackgroundSessions([]byte(`[]`))
	if err != nil {
		t.Fatalf("an empty registry is a legitimate answer, not an error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d, want 0", len(got))
	}
}

// A background entry missing either id must be a LOUD error, never a skipped row:
// silently dropping it would report "nothing is held" about a registry sesh could
// not actually read — the exact silent-divergence this whole check exists to catch.
func TestParseBackgroundSessionsRefusesIncompleteEntry(t *testing.T) {
	cases := map[string]string{
		"no sessionId": `[{"kind":"background","id":"abc12345","cwd":"/tmp/w"}]`,
		"no id":        `[{"kind":"background","sessionId":"11111111-2222-3333-4444-555555555555","cwd":"/tmp/w"}]`,
		"neither":      `[{"kind":"background","cwd":"/tmp/w"}]`,
	}
	for name, in := range cases {
		got, err := ParseBackgroundSessions([]byte(in))
		if err == nil {
			t.Errorf("%s: want a loud error, got %d sessions %+v", name, len(got), got)
			continue
		}
		if !strings.Contains(err.Error(), "shape has changed") {
			t.Errorf("%s: the error must say the output shape changed (so a reader knows to look at claude, not sesh): %v", name, err)
		}
	}
}

// An entry of another kind missing those fields is NOT an error — only background
// entries are read, so an interactive row sesh never looks at cannot break it.
func TestParseBackgroundSessionsIgnoresOtherKindsEntirely(t *testing.T) {
	got, err := ParseBackgroundSessions([]byte(`[{"kind":"interactive"},{"kind":"remote_agent","id":"x"}]`))
	if err != nil {
		t.Fatalf("non-background entries must be ignored, not validated: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d, want 0", len(got))
	}
}

func TestParseBackgroundSessionsRefusesNonJSON(t *testing.T) {
	// The realistic failure: claude prints a human message instead of JSON.
	for _, in := range []string{
		"", "   \n",
		"'claude agents' requires an interactive terminal (stdout is not a TTY)",
		`{"not":"a list"}`,
	} {
		if _, err := ParseBackgroundSessions([]byte(in)); err == nil {
			t.Errorf("ParseBackgroundSessions(%q): want an error, got nil", in)
		}
	}
}

// safeSessionRef is what keeps a registry-supplied id out of shell metacharacters.
func TestSafeSessionRef(t *testing.T) {
	ok := []string{"11111111", "11111111-2222-3333-4444-555555555555", "ABCDEF", "0"}
	bad := []string{
		"", "11111111; rm -rf /", "$(whoami)", "`id`", "a b", "11111111\n", "sess/../x",
		"11111111'", `11111111"`, "abcg1234", // g is not hex
		strings.Repeat("a", 65),
	}
	for _, s := range ok {
		if !safeSessionRef(s) {
			t.Errorf("safeSessionRef(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if safeSessionRef(s) {
			t.Errorf("safeSessionRef(%q) = true, want false", s)
		}
	}
}
