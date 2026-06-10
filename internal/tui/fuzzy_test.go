package tui

import (
	"reflect"
	"testing"
)

// A non-subsequence does not match; a subsequence does.
func TestFuzzyScoreMatchOrNot(t *testing.T) {
	if r := fuzzyScore("xyz", "alpha"); r.ok {
		t.Fatalf("xyz should not match alpha")
	}
	if r := fuzzyScore("aph", "alpha"); !r.ok {
		t.Fatalf("aph is a subsequence of alpha, should match")
	}
	if r := fuzzyScore("", "anything"); !r.ok || r.score != 0 || len(r.pos) != 0 {
		t.Fatalf("empty pattern should match with score 0, no positions: %+v", r)
	}
}

// Positions are the rune indices that matched (used for highlighting).
func TestFuzzyScorePositions(t *testing.T) {
	r := fuzzyScore("st", "sesh-tui")
	if !r.ok {
		t.Fatal("st should match sesh-tui")
	}
	// greedy forward: first 's' at 0, first 't' at 5 ("sesh-[t]ui").
	if !reflect.DeepEqual(r.pos, []int{0, 5}) {
		t.Fatalf("positions = %v, want [0 5]", r.pos)
	}
}

// Ranking properties: prefix beats mid-word; boundary beats interior;
// consecutive beats scattered.
func TestFuzzyScoreRanking(t *testing.T) {
	gt := func(better, worse, pat string) {
		t.Helper()
		b := fuzzyScore(pat, better)
		w := fuzzyScore(pat, worse)
		if !b.ok || !w.ok {
			t.Fatalf("both should match %q: %q=%v %q=%v", pat, better, b.ok, worse, w.ok)
		}
		if b.score <= w.score {
			t.Fatalf("%q (%d) should outrank %q (%d) for pattern %q", better, b.score, worse, w.score, pat)
		}
	}
	gt("sesh", "assessh", "sesh")            // prefix beats embedded
	gt("my-proto", "amyproto", "proto")      // boundary (after -) beats interior
	gt("abc", "a-b-c", "abc")                // consecutive beats split-by-gaps
	gt("ProtoGarden", "approtxgarden", "pg") // camel + boundary beats scattered
}

// Smart case: lowercase pattern is case-insensitive; an uppercase rune makes
// it case-sensitive.
func TestFuzzyScoreSmartCase(t *testing.T) {
	if r := fuzzyScore("abc", "ABC"); !r.ok {
		t.Fatal("lowercase pattern should match uppercase text (case-insensitive)")
	}
	if r := fuzzyScore("ABC", "abc"); r.ok {
		t.Fatal("uppercase pattern should be case-sensitive and not match lowercase text")
	}
	if r := fuzzyScore("Abc", "Abc"); !r.ok {
		t.Fatal("case-sensitive exact should match")
	}
}
