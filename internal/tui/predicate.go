package tui

// The predicate mini-language (PARITY_ROADMAP A4 — v1's predicate.go ported,
// selectors re-mapped to the two-axes model + tickets). It compiles a small
// boolean expression over a thread row into a func(api.ThreadRow) bool,
// powering the `[[tui.views]]` custom views (and, later, rule columns).
//
// Grammar (recursive descent, lowest precedence first):
//
//	expr    := orExpr
//	orExpr  := andExpr ("or"  andExpr)*
//	andExpr := notExpr ("and" notExpr)*
//	notExpr := "not" notExpr | primary
//	primary := "(" expr ")" | comparison | atom
//	comparison := selector op literal
//	op      := "==" | "!=" | "~" | "!~"
//	atom    := stateKeyword
//
// A SELECTOR resolves a row to a string:
//   - head      → "headful" | "headless"
//   - busy      → "busy" | "idle"
//   - attached  → "attached" | "detached"
//   - archived  → "true" | "false"
//   - tickets   → the open-ticket count, as digits ("0", "1", …)
//   - agent, machine, name, cwd, id, tags (tags = comma-joined; ~ matches any)
//
// A bare ATOM (no operator) is a state keyword: headful, headless, busy, idle,
// attached, detached, archived, ticketed (= at least one open ticket).
// `~`/`!~` match the selector against an RE2 regex. Equality is exact and
// case-sensitive; the canonical axis values are lowercase.
//
// Everything is validated AT COMPILE TIME: an unknown selector, a bad regex, a
// missing operand, or trailing junk is a loud error the user must fix, never a
// silently-false predicate.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/lukastk/sesh/internal/api"
)

// Predicate is a compiled boolean expression over a row. The zero value
// evaluates to false; build one with CompilePredicate.
type Predicate struct {
	eval func(api.ThreadRow) bool
	src  string
}

// Eval applies the predicate to a row.
func (p Predicate) Eval(r api.ThreadRow) bool {
	if p.eval == nil {
		return false
	}
	return p.eval(r)
}

func (p Predicate) String() string { return p.src }

// CompilePredicate parses src into a Predicate. An empty/whitespace-only src
// is a loud error (a view with no predicate is a config mistake, not a
// match-nothing default).
func CompilePredicate(src string) (Predicate, error) {
	toks, err := lexPredicate(src)
	if err != nil {
		return Predicate{}, fmt.Errorf("predicate %q: %w", src, err)
	}
	if len(toks) == 0 {
		return Predicate{}, fmt.Errorf("predicate %q: empty", src)
	}
	p := &predParser{toks: toks}
	fn, err := p.parseOr()
	if err != nil {
		return Predicate{}, fmt.Errorf("predicate %q: %w", src, err)
	}
	if p.pos != len(p.toks) {
		return Predicate{}, fmt.Errorf("predicate %q: unexpected %q", src, p.toks[p.pos].text)
	}
	return Predicate{eval: fn, src: src}, nil
}

// --- lexer (v1, verbatim) ---

type ptokKind int

const (
	ptWord   ptokKind = iota // bareword / keyword / selector / unquoted literal
	ptString                 // "double-quoted literal"
	ptLParen
	ptRParen
	ptEq    // ==
	ptNe    // !=
	ptMatch // ~
	ptNMatch
)

type ptok struct {
	kind ptokKind
	text string
}

// wordByte reports whether b can appear in a bareword (selector / keyword /
// literal). Operators (= ! ~), parens, quotes and whitespace are excluded, so
// `claude`, `2026-06-07` and `~/dev` is NOT one word (the ~) — quote paths.
func wordByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '(', ')', '=', '!', '~', '"':
		return false
	}
	return true
}

func lexPredicate(s string) ([]ptok, error) {
	var toks []ptok
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			toks = append(toks, ptok{ptLParen, "("})
			i++
		case c == ')':
			toks = append(toks, ptok{ptRParen, ")"})
			i++
		case c == '=':
			if i+1 >= len(s) || s[i+1] != '=' {
				return nil, fmt.Errorf("expected == at %d", i)
			}
			toks = append(toks, ptok{ptEq, "=="})
			i += 2
		case c == '!':
			if i+1 < len(s) && s[i+1] == '=' {
				toks = append(toks, ptok{ptNe, "!="})
				i += 2
			} else if i+1 < len(s) && s[i+1] == '~' {
				toks = append(toks, ptok{ptNMatch, "!~"})
				i += 2
			} else {
				return nil, fmt.Errorf("expected != or !~ at %d", i)
			}
		case c == '~':
			toks = append(toks, ptok{ptMatch, "~"})
			i++
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("unterminated string at %d", i)
			}
			toks = append(toks, ptok{ptString, s[i+1 : j]})
			i = j + 1
		default:
			j := i
			for j < len(s) && wordByte(s[j]) {
				j++
			}
			if j == i {
				return nil, fmt.Errorf("unexpected %q at %d", s[i], i)
			}
			toks = append(toks, ptok{ptWord, s[i:j]})
			i = j
		}
	}
	return toks, nil
}

// --- parser (v1, predFn re-typed) ---

type predParser struct {
	toks []ptok
	pos  int
}

func (p *predParser) peek() (ptok, bool) {
	if p.pos < len(p.toks) {
		return p.toks[p.pos], true
	}
	return ptok{}, false
}

func (p *predParser) next() (ptok, bool) {
	t, ok := p.peek()
	if ok {
		p.pos++
	}
	return t, ok
}

func (p *predParser) isKeyword(kw string) bool {
	t, ok := p.peek()
	return ok && t.kind == ptWord && strings.EqualFold(t.text, kw)
}

type predFn = func(api.ThreadRow) bool

func (p *predParser) parseOr() (predFn, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.isKeyword("or") {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		left = func(row api.ThreadRow) bool { return l(row) || r(row) }
	}
	return left, nil
}

func (p *predParser) parseAnd() (predFn, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.isKeyword("and") {
		p.next()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		left = func(row api.ThreadRow) bool { return l(row) && r(row) }
	}
	return left, nil
}

func (p *predParser) parseNot() (predFn, error) {
	if p.isKeyword("not") {
		p.next()
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return func(row api.ThreadRow) bool { return !inner(row) }, nil
	}
	return p.parsePrimary()
}

func (p *predParser) parsePrimary() (predFn, error) {
	t, ok := p.next()
	if !ok {
		return nil, fmt.Errorf("unexpected end of expression")
	}
	if t.kind == ptLParen {
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		closing, ok := p.next()
		if !ok || closing.kind != ptRParen {
			return nil, fmt.Errorf("expected ')'")
		}
		return inner, nil
	}
	if t.kind != ptWord && t.kind != ptString {
		return nil, fmt.Errorf("unexpected %q", t.text)
	}

	// A comparison if an operator follows; otherwise a bare atom.
	if op, ok := p.peek(); ok && isCompareOp(op.kind) {
		if t.kind != ptWord || selectorFn(t.text) == nil {
			return nil, fmt.Errorf("%q is not a valid selector (left of %s; valid: %s)", t.text, op.text, strings.Join(selectorNames, ", "))
		}
		p.next() // consume op
		rhs, ok := p.next()
		if !ok || (rhs.kind != ptWord && rhs.kind != ptString) {
			return nil, fmt.Errorf("expected a value after %s", op.text)
		}
		return compareFn(t.text, op.kind, rhs.text)
	}
	return atomFn(t)
}

func isCompareOp(k ptokKind) bool {
	return k == ptEq || k == ptNe || k == ptMatch || k == ptNMatch
}

// --- selectors + atoms (the v2 mapping) ---

var selectorNames = []string{"head", "busy", "attached", "archived", "tickets", "agent", "machine", "name", "cwd", "id", "tags"}

// selectorFn resolves a selector name to a row→string function (nil = unknown).
func selectorFn(name string) func(api.ThreadRow) string {
	switch strings.ToLower(name) {
	case "head":
		return func(r api.ThreadRow) string { return string(r.Head) }
	case "busy":
		return func(r api.ThreadRow) string { return string(r.Busy) }
	case "attached":
		return func(r api.ThreadRow) string { return string(r.Attachment) }
	case "archived":
		return func(r api.ThreadRow) string { return strconv.FormatBool(r.Archived) }
	case "tickets":
		return func(r api.ThreadRow) string { return strconv.Itoa(r.TicketsOpen) }
	case "agent":
		return func(r api.ThreadRow) string { return r.AgentKind }
	case "machine":
		return func(r api.ThreadRow) string { return r.Machine }
	case "name":
		return func(r api.ThreadRow) string { return r.Name }
	case "cwd":
		return func(r api.ThreadRow) string { return r.Cwd }
	case "id":
		return func(r api.ThreadRow) string { return r.ID }
	case "tags":
		return func(r api.ThreadRow) string { return strings.Join(r.Tags, ",") }
	}
	return nil
}

func compareFn(selector string, op ptokKind, literal string) (predFn, error) {
	sel := selectorFn(selector)
	tagsAny := strings.EqualFold(selector, "tags")
	switch op {
	case ptEq, ptNe:
		want := op == ptEq
		if tagsAny {
			// tags == x means "has the tag x" (any-of), the useful reading.
			return func(r api.ThreadRow) bool {
				has := false
				for _, tg := range r.Tags {
					if tg == literal {
						has = true
					}
				}
				return has == want
			}, nil
		}
		return func(r api.ThreadRow) bool { return (sel(r) == literal) == want }, nil
	case ptMatch, ptNMatch:
		re, err := regexp.Compile(literal)
		if err != nil {
			return nil, fmt.Errorf("bad regex %q: %w", literal, err)
		}
		want := op == ptMatch
		return func(r api.ThreadRow) bool { return re.MatchString(sel(r)) == want }, nil
	}
	return nil, fmt.Errorf("unknown operator")
}

// atomFn builds an operator-less atom: a state keyword. Anything else (a lone
// field name like `agent`) is a loud error.
func atomFn(t ptok) (predFn, error) {
	if t.kind != ptWord {
		return nil, fmt.Errorf("unexpected literal %q (a bare atom must be a state keyword)", t.text)
	}
	switch strings.ToLower(t.text) {
	case "headful":
		return func(r api.ThreadRow) bool { return r.Head == api.Headful }, nil
	case "headless":
		return func(r api.ThreadRow) bool { return r.Head == api.Headless }, nil
	case "busy":
		return func(r api.ThreadRow) bool { return r.Busy == api.BusyBusy }, nil
	case "idle":
		return func(r api.ThreadRow) bool { return r.Busy == api.BusyIdle }, nil
	case "attached":
		return func(r api.ThreadRow) bool { return r.Attachment == api.Attached }, nil
	case "detached":
		return func(r api.ThreadRow) bool { return r.Attachment == api.Detached }, nil
	case "archived":
		return func(r api.ThreadRow) bool { return r.Archived }, nil
	case "ticketed":
		return func(r api.ThreadRow) bool { return r.TicketsOpen > 0 }, nil
	}
	return nil, fmt.Errorf("%q is not a state keyword (headful, headless, busy, idle, attached, detached, archived, ticketed) — comparisons need an operator (e.g. agent == pi)", t.text)
}
