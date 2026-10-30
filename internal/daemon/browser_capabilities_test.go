package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// The published schema-56 contract (sesh-input-media-contract.md): `capabilities`
// on GET /v1/browser/threads is "always present, sorted, and contains no
// duplicates". A client's strict adapter rejects the WHOLE thread page when it is
// not — so this is a wire contract, not cosmetics, and the order is worth a test
// of its own.
//
// These tests assert the PROPERTY on the real handler's real output, not a
// hard-coded order. That distinction is the point: the defect this replaces was a
// hand-sorted literal that two appended tokens silently broke, and a test pinning
// one expected sequence would have been "fixed" by updating the sequence.
func TestBrowserCapabilitiesHonourTheSortedUniqueContract(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	rec := httptest.NewRecorder()
	d.handleBrowserThreads(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("browser threads = %d %s", rec.Code, rec.Body.String())
	}
	var page api.BrowserThreadPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	assertCapabilityContract(t, "handler output", page.Capabilities)

	// Always PRESENT: the field must be emitted even with no threads, because an
	// older server is identified by its absence.
	if !strings.Contains(rec.Body.String(), `"capabilities":[`) {
		t.Fatalf("capabilities was omitted from the encoded page: %s", rec.Body.String())
	}

	// And the content is still the set this daemon actually implements.
	want := map[string]bool{
		"browser.display-v1":                   true,
		"browser.live-input-media-v1":          true,
		"browser.send-v1":                      true,
		"browser.stored-input-media-v1":        true,
		"browser.stored-media-presentation-v2": true,
	}
	if len(page.Capabilities) != len(want) {
		t.Fatalf("capabilities = %#v, want %d tokens", page.Capabilities, len(want))
	}
	for _, token := range page.Capabilities {
		if !want[token] {
			t.Fatalf("unexpected capability token %q in %#v", token, page.Capabilities)
		}
	}
}

// The producer must impose the order itself, so that a future token appended to
// the source list in any position cannot reintroduce the defect.
func TestBrowserCapabilityTokensSortAndDeduplicateWhateverTheSourceOrder(t *testing.T) {
	original := browserCapabilityNames
	t.Cleanup(func() { browserCapabilityNames = original })

	// Deliberately hostile source: reverse-sorted, with a duplicate.
	browserCapabilityNames = []string{
		"browser.zzz-last-v1", "browser.send-v1", "browser.aaa-first-v1", "browser.send-v1",
	}
	got := browserCapabilityTokens()
	assertCapabilityContract(t, "hostile source order", got)
	if len(got) != 3 || got[0] != "browser.aaa-first-v1" || got[2] != "browser.zzz-last-v1" {
		t.Fatalf("tokens = %#v, want the three distinct names in sorted order", got)
	}

	// The returned slice is a fresh allocation: a caller that sorts or truncates
	// it must not corrupt the next response.
	got[0] = "mutated"
	if next := browserCapabilityTokens(); next[0] == "mutated" {
		t.Fatal("browserCapabilityTokens shares its backing array between responses")
	}
}

// assertCapabilityContract checks exactly what the contract publishes.
func assertCapabilityContract(t *testing.T, where string, tokens []string) {
	t.Helper()
	if len(tokens) == 0 {
		t.Fatalf("%s: capabilities is empty; the contract says always present", where)
	}
	if !sort.StringsAreSorted(tokens) {
		t.Fatalf("%s: capabilities is not sorted: %#v", where, tokens)
	}
	seen := map[string]bool{}
	for _, token := range tokens {
		if token == "" {
			t.Fatalf("%s: capabilities contains an empty token: %#v", where, tokens)
		}
		if seen[token] {
			t.Fatalf("%s: capabilities contains duplicate %q: %#v", where, token, tokens)
		}
		seen[token] = true
	}
}
