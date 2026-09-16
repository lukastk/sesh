// Package schedule is the pure clock behind `sesh schedule` (_dev/SCHEDULING.md
// §5): a spec in one of three forms — a 5-field cron expression, a fixed
// interval, or a one-shot instant — and Next(after) over it in a recorded time
// zone. No I/O, no time.Now: everything takes the instant to compute from, so
// it unit-tests against literal constants like the hold derivation does.
//
// The cron parser is hand-rolled (decided, SCHEDULING.md §15.4): standard
// vixie-cron 5 fields `min hour dom mon dow`, with `*`, `*/n`, `a-b`, `a-b/n`,
// lists, month/weekday names, 0 and 7 both Sunday, and the classic rule that a
// restricted day-of-month and a restricted day-of-week match when EITHER does.
// No seconds field, no @descriptors. DST: candidate instants are built in the
// zone's wall-clock terms, so a local time that does not exist (spring forward)
// is skipped and a repeated one (fall back) fires once — pinned by tests.
package schedule

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Kind is the spec form.
type Kind string

const (
	KindCron  Kind = "cron"
	KindEvery Kind = "every"
	KindAt    Kind = "at"
)

// MinInterval bounds `every`: sub-10 s schedules are refused (a runaway
// schedule at 1 s is an agent storm, and no thread action is that urgent).
const MinInterval = 10 * time.Second

// Spec is a parsed schedule clock.
type Spec struct {
	Kind Kind
	// Cron is the parsed expression (KindCron).
	Cron *Expr
	// Every is the interval (KindEvery); Anchor the instant the series is
	// counted from (creation, or --not-before).
	Every  time.Duration
	Anchor time.Time
	// At is the single instant (KindAt).
	At time.Time
	// Raw is the canonical text form, what the record stores and lists show.
	Raw string
}

// Parse reads a stored/typed spec: "cron <expr>", "every <duration>", or
// "at <YYYY-MM-DD HH:MM | RFC3339>". loc resolves wall-clock text (cron, at)
// and is recorded by the caller; anchor seeds an interval series.
func Parse(raw string, loc *time.Location, anchor time.Time) (Spec, error) {
	if loc == nil {
		return Spec{}, errors.New("schedule: a time zone is required")
	}
	kind, rest, _ := strings.Cut(strings.TrimSpace(raw), " ")
	rest = strings.TrimSpace(rest)
	switch Kind(kind) {
	case KindCron:
		expr, err := ParseCron(rest)
		if err != nil {
			return Spec{}, err
		}
		return Spec{Kind: KindCron, Cron: expr, Raw: "cron " + expr.String()}, nil
	case KindEvery:
		d, err := time.ParseDuration(rest)
		if err != nil {
			return Spec{}, fmt.Errorf("schedule: every %q: %w", rest, err)
		}
		if d < MinInterval {
			return Spec{}, fmt.Errorf("schedule: every %s is below the %s minimum", d, MinInterval)
		}
		if anchor.IsZero() {
			return Spec{}, errors.New("schedule: an interval needs an anchor instant")
		}
		return Spec{Kind: KindEvery, Every: d, Anchor: anchor.In(loc), Raw: "every " + d.String()}, nil
	case KindAt:
		at, err := ParseInstant(rest, loc)
		if err != nil {
			return Spec{}, err
		}
		return Spec{Kind: KindAt, At: at, Raw: "at " + at.Format(time.RFC3339)}, nil
	default:
		return Spec{}, fmt.Errorf("schedule: spec %q must start with cron, every or at", raw)
	}
}

// ParseInstant reads "YYYY-MM-DD HH:MM", "YYYY-MM-DDTHH:MM", or RFC3339, in loc.
func ParseInstant(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("schedule: at %q: want YYYY-MM-DD HH:MM or RFC3339", s)
}

// Next returns the first instant strictly after `after` at which the spec
// fires, in after's location (a cron is evaluated in the zone the caller passes
// via after.In(loc)). The zero time means "never again" (a one-shot that has
// passed, or a cron with no match in the next five years).
func (s Spec) Next(after time.Time) time.Time {
	switch s.Kind {
	case KindCron:
		return s.Cron.Next(after)
	case KindEvery:
		if after.Before(s.Anchor) {
			return s.Anchor
		}
		elapsed := after.Sub(s.Anchor)
		n := elapsed/s.Every + 1
		return s.Anchor.Add(n * s.Every)
	case KindAt:
		if s.At.After(after) {
			return s.At
		}
		return time.Time{}
	}
	return time.Time{}
}

// Missed counts fire instants in (from, until], i.e. occurrences a sleeping
// daemon slept through, capped so a year-old one-shot cannot spin.
func (s Spec) Missed(from, until time.Time, cap int) int {
	n := 0
	t := from
	for n < cap {
		t = s.Next(t)
		if t.IsZero() || t.After(until) {
			break
		}
		n++
	}
	return n
}

// --- cron ---

// Expr is a parsed 5-field cron expression.
type Expr struct {
	min, hour, dom, mon, dow uint64 // bit sets
	domStar, dowStar         bool   // the field was `*` (the vixie either/or rule)
	raw                      string
}

func (e *Expr) String() string { return e.raw }

type field struct {
	name     string
	min, max int
	names    map[string]int
}

var (
	monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	dowNames   = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
	cronFields = []field{
		{"minute", 0, 59, nil}, {"hour", 0, 23, nil}, {"day-of-month", 1, 31, nil},
		{"month", 1, 12, monthNames}, {"day-of-week", 0, 7, dowNames},
	}
)

// ParseCron parses `min hour dom mon dow`.
func ParseCron(s string) (*Expr, error) {
	parts := strings.Fields(s)
	if len(parts) != 5 {
		return nil, fmt.Errorf("schedule: cron %q: want 5 fields (minute hour day-of-month month day-of-week), got %d", s, len(parts))
	}
	e := &Expr{raw: strings.Join(parts, " ")}
	sets := make([]uint64, 5)
	stars := make([]bool, 5)
	for i, f := range cronFields {
		bits, star, err := parseField(parts[i], f)
		if err != nil {
			return nil, fmt.Errorf("schedule: cron %q: %s field %q: %w", s, f.name, parts[i], err)
		}
		sets[i], stars[i] = bits, star
	}
	e.min, e.hour, e.dom, e.mon, e.dow = sets[0], sets[1], sets[2], sets[3], sets[4]
	// 7 means Sunday too.
	if e.dow&(1<<7) != 0 {
		e.dow |= 1 << 0
	}
	e.domStar, e.dowStar = stars[2], stars[4]
	return e, nil
}

func parseField(s string, f field) (uint64, bool, error) {
	var bits uint64
	star := false
	for _, part := range strings.Split(s, ",") {
		rangePart, stepPart, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepPart)
			if err != nil || n <= 0 {
				return 0, false, fmt.Errorf("bad step %q", stepPart)
			}
			step = n
		}
		lo, hi := f.min, f.max
		switch {
		case rangePart == "*":
			star = !hasStep && len(s) == 1
		default:
			a, b, isRange := strings.Cut(rangePart, "-")
			var err error
			if lo, err = parseValue(a, f); err != nil {
				return 0, false, err
			}
			if isRange {
				if hi, err = parseValue(b, f); err != nil {
					return 0, false, err
				}
			} else if hasStep {
				hi = f.max // "5/10" = from 5 to max by 10
			} else {
				hi = lo
			}
			if hi < lo {
				return 0, false, fmt.Errorf("range %d-%d is backwards", lo, hi)
			}
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << uint(v)
		}
	}
	if bits == 0 {
		return 0, false, errors.New("matches nothing")
	}
	return bits, star, nil
}

func parseValue(s string, f field) (int, error) {
	if f.names != nil {
		if v, ok := f.names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", s)
	}
	if n < f.min || n > f.max {
		return 0, fmt.Errorf("%d is outside %d-%d", n, f.min, f.max)
	}
	return n, nil
}

func has(bits uint64, v int) bool { return bits&(1<<uint(v)) != 0 }

// dayMatches applies vixie's rule: when both dom and dow are restricted the
// day matches if EITHER does; otherwise the restricted one decides.
func (e *Expr) dayMatches(t time.Time) bool {
	dom := has(e.dom, t.Day())
	dow := has(e.dow, int(t.Weekday()))
	switch {
	case e.domStar && e.dowStar:
		return true
	case e.domStar:
		return dow
	case e.dowStar:
		return dom
	default:
		return dom || dow
	}
}

// Next returns the first matching instant strictly after `after`, in after's
// zone. It walks wall-clock fields coarse-to-fine (month → day → hour → minute),
// jumping past a non-matching unit rather than stepping minute by minute; the
// search is bounded at five years, past which the zero time is returned.
func (e *Expr) Next(after time.Time) time.Time {
	loc := after.Location()
	t := after.Truncate(time.Minute).Add(time.Minute)
	t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
	limit := after.AddDate(5, 0, 0)
	for t.Before(limit) {
		if !has(e.mon, int(t.Month())) {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			continue
		}
		if !e.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			continue
		}
		if !has(e.hour, t.Hour()) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
			continue
		}
		if !has(e.min, t.Minute()) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute()+1, 0, 0, loc)
			continue
		}
		// A wall-clock time that does not exist (spring forward) is normalized
		// forward by time.Date into the next hour, where the hour check above
		// rejects it on the next pass — so a 01:30 job is skipped on a day with
		// no 01:30, never fired at 02:30 (pinned by TestCronDST).
		return t
	}
	return time.Time{}
}
