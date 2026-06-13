package main

import "testing"

// TestNavPrevRoundTrip covers the prefix+L history file: write/read a location, the
// missing/malformed cases (ok=false), and that window 0 (a valid index) round-trips.
func TestNavPrevRoundTrip(t *testing.T) {
	home := t.TempDir()

	// Missing file → not ok.
	if _, _, _, ok := readNavPrev(home); ok {
		t.Errorf("readNavPrev on a fresh home should be ok=false")
	}

	writeNavPrev(home, "macbook", "sesh_proj <ab>", 3)
	m, s, w, ok := readNavPrev(home)
	if !ok || m != "macbook" || s != "sesh_proj <ab>" || w != 3 {
		t.Errorf("round-trip = (%q,%q,%d,%v), want (macbook, sesh_proj <ab>, 3, true)", m, s, w, ok)
	}

	// Window 0 is a real index (not "unset") and must round-trip.
	writeNavPrev(home, "mymain", "scratch", 0)
	if m, s, w, ok := readNavPrev(home); !ok || m != "mymain" || s != "scratch" || w != 0 {
		t.Errorf("window-0 round-trip = (%q,%q,%d,%v), want (mymain, scratch, 0, true)", m, s, w, ok)
	}

	// Overwrite (toggle semantics rely on the latest write winning).
	writeNavPrev(home, "macstudio", "box", 1)
	if m, _, _, _ := readNavPrev(home); m != "macstudio" {
		t.Errorf("overwrite did not take: machine=%q", m)
	}
}
