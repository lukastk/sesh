package blobs

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAddDedupAndResolve(t *testing.T) {
	s := New(t.TempDir())
	h1, err := s.Add("shot.png", []byte("PNGDATA"))
	if err != nil {
		t.Fatal(err)
	}
	// Same bytes, different name → same hash, original name kept (dedup).
	h2, err := s.Add("other.png", []byte("PNGDATA"))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("identical bytes hashed differently: %s vs %s", h1, h2)
	}
	name, data, err := s.Bytes(h1)
	if err != nil {
		t.Fatal(err)
	}
	if name != "shot.png" || string(data) != "PNGDATA" {
		t.Errorf("got name=%q data=%q, want shot.png/PNGDATA (first name kept)", name, data)
	}
	// Resolve by a short prefix.
	got, err := s.Resolve(h1[:TokenPrefixLen])
	if err != nil || got != h1 {
		t.Errorf("resolve prefix: got %q err %v, want %s", got, err, h1)
	}
	// Path points inside the hash dir with the friendly name.
	p, err := s.Path(h1[:TokenPrefixLen])
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "shot.png" || !strings.Contains(p, h1) {
		t.Errorf("path %q should end in shot.png and contain the hash", p)
	}
}

func TestResolveAmbiguousAndMissing(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Resolve("deadbeef"); err == nil {
		t.Error("missing prefix should be a loud error")
	}
	if _, err := s.Resolve("nothex!!"); err == nil {
		t.Error("non-hex should be a loud error")
	}
}

func TestListAndRemove(t *testing.T) {
	s := New(t.TempDir())
	ha, _ := s.Add("a.txt", []byte("aaa"))
	s.Add("b.txt", []byte("bbb"))
	list, err := s.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %v (err %v), want 2", list, err)
	}
	if err := s.Remove(ha[:TokenPrefixLen]); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List()
	if len(list) != 1 || list[0].Name != "b.txt" {
		t.Errorf("after remove: %v, want only b.txt", list)
	}
}

func TestExpandAndEscape(t *testing.T) {
	s := New(t.TempDir())
	h, _ := s.Add("img.png", []byte("xy"))
	pre := h[:TokenPrefixLen]
	path, _ := s.Path(h)

	// A token expands to the absolute path; surrounding text is preserved.
	out, err := s.Expand("see @blob(" + pre + ") here")
	if err != nil {
		t.Fatal(err)
	}
	if out != "see "+path+" here" {
		t.Errorf("expand = %q, want the path substituted", out)
	}

	// The escape @@blob(...) emits the literal @blob(...) and does NOT expand.
	esc := "literally @@blob(" + pre + ") not a path"
	out, err = s.Expand(esc)
	if err != nil {
		t.Fatal(err)
	}
	if out != "literally @blob("+pre+") not a path" {
		t.Errorf("escape = %q, want a literal @blob(...) with one @ dropped", out)
	}

	// A reference to a non-existent blob is a LOUD error (no silent passthrough).
	if _, err := s.Expand("ghost @blob(999999999999)"); err == nil {
		t.Error("unknown blob token should make Expand fail loudly")
	}

	// Non-tokens are untouched (email-ish @, plain parens, no false positives).
	for _, plain := range []string{"email a@b.com", "f(x) and @blob without parens", "@blob()"} {
		if out, err := s.Expand(plain); err != nil || out != plain {
			t.Errorf("plain text %q changed to %q (err %v)", plain, out, err)
		}
	}
}

func TestReferences(t *testing.T) {
	got := References("a @blob(aaa) b @blob(bbb) c @blob(aaa) escaped @@blob(ccc)")
	want := []string{"aaa", "bbb"} // deduped; the escaped ccc is excluded
	if len(got) != len(want) || got[0] != "aaa" || got[1] != "bbb" {
		t.Errorf("References = %v, want %v (deduped, escaped excluded)", got, want)
	}
}
