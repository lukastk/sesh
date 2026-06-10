package tui

import "testing"

// trunc must count RUNES: byte slicing would split multi-byte characters
// (session names contain ─, →, CJK, etc.) and emit invalid UTF-8.
func TestTruncRuneSafe(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello!", 5, "hell…"},
		{"日本語のなまえ", 4, "日本語…"},
		{"caféteria", 5, "café…"},
	}
	for _, c := range cases {
		if got := trunc(c.in, c.n); got != c.want {
			t.Errorf("trunc(%q,%d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
