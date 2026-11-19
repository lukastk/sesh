package store

import (
	"testing"
	"time"
)

// The headless queue is FIFO per thread, keeps seqs across a drop and a restore,
// and survives a reopen (a daemon restart): losing a queued message is the bug
// schema 75 exists to prevent.
func TestHeadlessQueueIsFIFOAndDurable(t *testing.T) {
	s := openTestStore(t)
	now := time.Unix(1_700_000_000, 0)
	a, err := s.EnqueueHeadless("t1", "thread send", "first", now)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.EnqueueHeadless("t1", "thread send", "second", now)
	other, _ := s.EnqueueHeadless("t2", "thread send", "elsewhere", now)
	if !(a < b && b < other) {
		t.Fatalf("seqs not increasing: %d %d %d", a, b, other)
	}

	head, ok, err := s.HeadlessQueueHead("t1")
	if err != nil || !ok || head.Text != "first" || head.Seq != a {
		t.Fatalf("head = %+v ok=%v err=%v, want the first message", head, ok, err)
	}

	// Pop-then-restore keeps the message at the front with its seq.
	if _, err := s.DeleteHeadless("t1", head.Seq); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreHeadless(head); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.HeadlessQueue("t1")
	if err != nil || len(msgs) != 2 || msgs[0].Seq != a || msgs[1].Seq != b {
		t.Fatalf("queue after restore = %+v err=%v, want [first second]", msgs, err)
	}

	// A drop is scoped to its thread: another thread's seq is not found.
	if found, _ := s.DeleteHeadless("t1", other); found {
		t.Fatal("drop removed another thread's message")
	}
	if found, _ := s.DeleteHeadless("t1", b); !found {
		t.Fatal("drop of a queued message reported not found")
	}

	counts, err := s.ThreadsWithHeadlessQueue()
	if err != nil || counts["t1"] != 1 || counts["t2"] != 1 || len(counts) != 2 {
		t.Fatalf("counts = %v err=%v, want t1:1 t2:1", counts, err)
	}
}

func TestHeadlessQueueSurvivesReopen(t *testing.T) {
	path := t.TempDir() + "/sesh.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueHeadless("t1", "thread send", "kept", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	msgs, err := s.HeadlessQueue("t1")
	if err != nil || len(msgs) != 1 || msgs[0].Text != "kept" {
		t.Fatalf("after reopen = %+v err=%v, want the kept message", msgs, err)
	}
}

// A held paste keeps its text until it is delivered or dropped, a failed one stays
// visible as undelivered, and a restart fails every paste still held (schema 77).
func TestHeldPasteLifecycle(t *testing.T) {
	s := openTestStore(t)
	now := time.Unix(1_700_000_000, 0)
	delivered, _ := s.InsertHeldPaste("t1", "thread send", "delivered text", now)
	failed, _ := s.InsertHeldPaste("t1", "thread send", "failed text", now)
	lost, _ := s.InsertHeldPaste("t1", "thread send", "lost text", now)

	if err := s.DeleteHeldPaste(delivered); err != nil {
		t.Fatal(err)
	}
	if err := s.FailHeldPaste(failed, "pane in use for 10m"); err != nil {
		t.Fatal(err)
	}
	und, err := s.UndeliveredPastes("t1")
	if err != nil || len(und) != 1 || und[0].Seq != failed || und[0].Text != "failed text" || und[0].Reason != "pane in use for 10m" {
		t.Fatalf("undelivered = %+v err=%v, want the failed paste with its text and reason", und, err)
	}
	got, ok, err := s.GetUndeliveredPaste("t1", failed)
	if err != nil || !ok || got.Text != "failed text" {
		t.Fatalf("get undelivered = %+v ok=%v err=%v", got, ok, err)
	}
	if _, ok, _ := s.GetUndeliveredPaste("t1", lost); ok {
		t.Fatal("a paste still held is not undelivered")
	}

	restarted, err := s.LostHeldPastes("the daemon restarted while the message was held")
	if err != nil || len(restarted) != 1 || restarted[0].Seq != lost || restarted[0].Text != "lost text" {
		t.Fatalf("lost at restart = %+v err=%v, want the held paste", restarted, err)
	}
	und, _ = s.UndeliveredPastes("t1")
	if len(und) != 2 {
		t.Fatalf("after restart undelivered = %d, want 2 (text kept for both)", len(und))
	}
}
