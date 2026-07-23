package store

import (
	"path/filepath"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestThreadFlagActions pins the flagged-system store semantics (schema 44):
// manual on/off/disable/enable incl. flag-on-re-enables, and AutoFlag's
// atomic guards (flag_disabled + already-flagged), all surviving a re-read.
func TestThreadFlagActions(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if err := s.InsertThread(api.Thread{ID: "tid-f", Machine: "m", SessionName: "s", AgentKind: "pi"}); err != nil {
		t.Fatalf("InsertThread: %v", err)
	}
	get := func() api.Thread {
		t.Helper()
		th, err := s.GetThread("tid-f")
		if err != nil {
			t.Fatalf("GetThread: %v", err)
		}
		return th
	}

	// AutoFlag sets flag + reason.
	if set, err := s.AutoFlag("tid-f", "turn ended"); err != nil || !set {
		t.Fatalf("AutoFlag = %v/%v, want set", set, err)
	}
	if th := get(); !th.Flagged || th.FlagReason != "turn ended" {
		t.Fatalf("after AutoFlag: %+v", th)
	}
	// Already flagged → no-op (reason preserved, not churned).
	if set, _ := s.AutoFlag("tid-f", "other"); set {
		t.Fatal("AutoFlag on a flagged thread must be a no-op")
	}
	if th := get(); th.FlagReason != "turn ended" {
		t.Fatalf("reason churned: %q", th.FlagReason)
	}

	// Manual off clears flag + reason; flags never auto-clear so this is THE clear.
	if err := s.SetThreadFlagAction("tid-f", api.FlagOff, ""); err != nil {
		t.Fatalf("off: %v", err)
	}
	if th := get(); th.Flagged || th.FlagReason != "" {
		t.Fatalf("after off: %+v", th)
	}

	// Disable suppresses AutoFlag and clears any current flag.
	if _, err := s.AutoFlag("tid-f", "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadFlagAction("tid-f", api.FlagDisable, ""); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if th := get(); th.Flagged || !th.FlagDisabled {
		t.Fatalf("after disable: %+v", th)
	}
	if set, _ := s.AutoFlag("tid-f", "suppressed"); set {
		t.Fatal("AutoFlag on a flag-disabled thread must be a no-op")
	}

	// Manual ON re-enables AND flags (the one-rule semantic).
	if err := s.SetThreadFlagAction("tid-f", api.FlagOn, ""); err != nil {
		t.Fatalf("on: %v", err)
	}
	if th := get(); !th.Flagged || th.FlagDisabled {
		t.Fatalf("manual on must flag AND re-enable: %+v", th)
	}

	// Enable alone re-allows autos without flagging.
	if err := s.SetThreadFlagAction("tid-f", api.FlagOff, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadFlagAction("tid-f", api.FlagDisable, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadFlagAction("tid-f", api.FlagEnable, ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if th := get(); th.Flagged || th.FlagDisabled {
		t.Fatalf("after enable: %+v", th)
	}

	// Loud on unknowns.
	if err := s.SetThreadFlagAction("nope", api.FlagOn, ""); err != ErrThreadNotFound {
		t.Fatalf("unknown id: %v", err)
	}
	if err := s.SetThreadFlagAction("tid-f", "bogus", ""); err == nil {
		t.Fatal("unknown action must be loud")
	}
}
