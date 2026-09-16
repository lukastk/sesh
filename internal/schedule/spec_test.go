package schedule

import (
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zone %s unavailable: %v", name, err)
	}
	return loc
}

func at(t *testing.T, loc *time.Location, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCronNextTable(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		expr, after, want string
	}{
		{"* * * * *", "2026-09-16 10:00", "2026-09-16 10:01"},
		{"*/15 * * * *", "2026-09-16 10:00", "2026-09-16 10:15"},
		{"*/15 * * * *", "2026-09-16 10:14", "2026-09-16 10:15"},
		{"*/15 * * * *", "2026-09-16 10:15", "2026-09-16 10:30"}, // strictly after
		{"0 9 * * *", "2026-09-16 10:00", "2026-09-17 09:00"},
		{"0 9 * * 1-5", "2026-09-18 10:00", "2026-09-21 09:00"}, // Fri → Mon
		{"0 9 * * mon-fri", "2026-09-19 08:00", "2026-09-21 09:00"},
		{"30 2 1 * *", "2026-09-16 10:00", "2026-10-01 02:30"},
		{"0 0 29 2 *", "2026-01-01 00:00", "2028-02-29 00:00"},  // leap day
		{"0 12 15 * 1", "2026-09-16 00:00", "2026-09-21 12:00"}, // dom OR dow: Mon 21st before the 15th of Oct
		{"0 12 15 * 1", "2026-09-21 12:00", "2026-09-28 12:00"}, // next Monday
		{"5 4 * * sun", "2026-09-16 00:00", "2026-09-20 04:05"},
		{"5 4 * * 7", "2026-09-16 00:00", "2026-09-20 04:05"}, // 7 = Sunday
		{"0 0 1 jan *", "2026-09-16 00:00", "2027-01-01 00:00"},
		{"1,31 * * * *", "2026-09-16 10:02", "2026-09-16 10:31"},
		{"10-12 * * * *", "2026-09-16 10:11", "2026-09-16 10:12"},
		{"0-59/20 * * * *", "2026-09-16 10:21", "2026-09-16 10:40"},
	}
	for _, c := range cases {
		expr, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		got := expr.Next(at(t, utc, c.after))
		if got.Format("2006-01-02 15:04") != c.want {
			t.Errorf("%s after %s: got %s, want %s", c.expr, c.after, got.Format("2006-01-02 15:04"), c.want)
		}
	}
}

func TestCronParseLoud(t *testing.T) {
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *", "5-1 * * * *", "a * * * *", "* * * * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("cron %q must be refused", bad)
		}
	}
}

func TestCronNeverMatchesIsZero(t *testing.T) {
	expr, err := ParseCron("0 0 31 2 *") // February 31st
	if err != nil {
		t.Fatal(err)
	}
	if got := expr.Next(at(t, time.UTC, "2026-01-01 00:00")); !got.IsZero() {
		t.Fatalf("Feb 31 must never fire, got %s", got)
	}
}

// TestCronDST pins the two DST rules in a real zone: a wall-clock time that
// does not exist on spring-forward day is SKIPPED (never fired an hour late),
// and a repeated one on fall-back day fires ONCE.
func TestCronDST(t *testing.T) {
	london := mustLoc(t, "Europe/London")
	expr, err := ParseCron("30 1 * * *")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-03-29: clocks go 01:00 GMT → 02:00 BST; 01:30 does not exist.
	got := expr.Next(at(t, london, "2026-03-28 12:00"))
	if got.Format("2006-01-02 15:04") != "2026-03-30 01:30" {
		t.Fatalf("spring forward: got %s, want the 30th (the 29th has no 01:30)", got.Format("2006-01-02 15:04 MST"))
	}
	// 2026-10-25: 01:30 BST then 01:30 GMT; fire once.
	first := expr.Next(at(t, london, "2026-10-24 12:00"))
	if first.Format("2006-01-02 15:04") != "2026-10-25 01:30" {
		t.Fatalf("fall back first: got %s", first.Format("2006-01-02 15:04 MST"))
	}
	second := expr.Next(first)
	if second.Format("2006-01-02 15:04") != "2026-10-26 01:30" {
		t.Fatalf("fall back must fire once: next after %s is %s, want the 26th", first.Format("15:04 MST"), second.Format("2006-01-02 15:04 MST"))
	}
	// A daily 09:00 keeps its WALL-CLOCK time across the change (the zone is
	// what is recorded, not an offset).
	nine, _ := ParseCron("0 9 * * *")
	a := nine.Next(at(t, london, "2026-03-28 08:00"))
	b := nine.Next(a)
	if a.Hour() != 9 || b.Hour() != 9 || b.Sub(a) != 23*time.Hour {
		t.Fatalf("daily across spring forward: %s then %s (%s apart)", a, b, b.Sub(a))
	}
}

func TestSpecEvery(t *testing.T) {
	anchor := at(t, time.UTC, "2026-09-16 10:00")
	s, err := Parse("every 15m", time.UTC, anchor)
	if err != nil {
		t.Fatal(err)
	}
	if s.Raw != "every 15m0s" {
		t.Fatalf("raw = %q", s.Raw)
	}
	if got := s.Next(anchor.Add(-time.Hour)); !got.Equal(anchor) {
		t.Fatalf("before the anchor: got %s, want the anchor", got)
	}
	if got := s.Next(anchor); !got.Equal(anchor.Add(15 * time.Minute)) {
		t.Fatalf("at the anchor: got %s", got)
	}
	if got := s.Next(anchor.Add(31 * time.Minute)); !got.Equal(anchor.Add(45 * time.Minute)) {
		t.Fatalf("mid-series: got %s", got)
	}
	if _, err := Parse("every 5s", time.UTC, anchor); err == nil || !strings.Contains(err.Error(), "minimum") {
		t.Fatalf("sub-minimum interval must be refused: %v", err)
	}
	if _, err := Parse("every soon", time.UTC, anchor); err == nil {
		t.Fatal("bad duration must be refused")
	}
}

func TestSpecAt(t *testing.T) {
	loc := mustLoc(t, "Europe/London")
	now := at(t, loc, "2026-09-16 10:00")
	s, err := Parse("at 2026-09-17 09:00", loc, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Next(now); got.Format("2006-01-02 15:04 MST") != "2026-09-17 09:00 BST" {
		t.Fatalf("at: got %s", got.Format("2006-01-02 15:04 MST"))
	}
	if got := s.Next(s.At); !got.IsZero() {
		t.Fatalf("a passed one-shot must never fire again, got %s", got)
	}
	if _, err := Parse("at tomorrow", loc, now); err == nil {
		t.Fatal("bad instant must be refused")
	}
	if _, err := Parse("soon", loc, now); err == nil {
		t.Fatal("unknown kind must be refused")
	}
}

func TestSpecMissed(t *testing.T) {
	anchor := at(t, time.UTC, "2026-09-16 10:00")
	s, _ := Parse("every 10m", time.UTC, anchor)
	// The daemon was down from 10:05 to 10:47: 10:10, 10:20, 10:30, 10:40 slept through.
	if n := s.Missed(at(t, time.UTC, "2026-09-16 10:05"), at(t, time.UTC, "2026-09-16 10:47"), 1000); n != 4 {
		t.Fatalf("missed = %d, want 4", n)
	}
	if n := s.Missed(at(t, time.UTC, "2026-09-16 10:05"), at(t, time.UTC, "2026-09-16 10:07"), 1000); n != 0 {
		t.Fatalf("no occurrence in the gap: missed = %d", n)
	}
	if n := s.Missed(anchor, anchor.AddDate(1, 0, 0), 50); n != 50 {
		t.Fatalf("cap must bound the count, got %d", n)
	}
}
