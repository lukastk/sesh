package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/config"
)

func corsTestHandler(t *testing.T, allowed ...string) (http.Handler, *int) {
	t.Helper()
	origins := map[string]bool{}
	for _, origin := range allowed {
		origins[origin] = true
	}
	d := &Daemon{browserAPI: config.BrowserAPIConfig{AllowedOrigins: origins}}
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	return d.browserCORS(d.tokenAuth("test-token", next)), &calls
}

func corsPreflight(path, origin, method, headers string) *http.Request {
	req := httptest.NewRequest(http.MethodOptions, path, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if method != "" {
		req.Header.Set("Access-Control-Request-Method", method)
	}
	if headers != "" {
		req.Header.Set("Access-Control-Request-Headers", headers)
	}
	return req
}

func TestBrowserCORSDefaultsEmptyAndPreflightIsNarrow(t *testing.T) {
	handler, calls := corsTestHandler(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, corsPreflight("/v1/browser/threads", "app://obsidian.md", http.MethodGet, "Authorization"))
	if rec.Code != http.StatusForbidden || *calls != 0 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("empty-origin preflight = %d calls=%d headers=%#v", rec.Code, *calls, rec.Header())
	}
	if rec.Header().Get("Cache-Control") != "no-store" || !varyContains(rec.Header(), "Origin") || !varyContains(rec.Header(), "Access-Control-Request-Method") || !varyContains(rec.Header(), "Access-Control-Request-Headers") {
		t.Fatalf("preflight cache boundary = %#v", rec.Header())
	}

	handler, calls = corsTestHandler(t, "app://obsidian.md")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/v1/browser/threads", nil))
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Access-Control-Allow-Origin") != "" || *calls != 0 {
		t.Fatalf("non-CORS OPTIONS bypassed auth = %d calls=%d headers=%#v", rec.Code, *calls, rec.Header())
	}
	for _, path := range []string{
		"/v1/route/peer/v1/status",
		"/v1/runtime/artifacts/start",
		"/v1/threads/rpc",
		"/v1/health",
		"/v1/browser/threads/extra",
	} {
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, corsPreflight(path, "app://obsidian.md", http.MethodGet, "Authorization"))
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("Access-Control-Allow-Origin") != "" || *calls != 0 {
			t.Fatalf("non-CORS route %s = %d calls=%d headers=%#v", path, rec.Code, *calls, rec.Header())
		}
	}
}

func TestBrowserCORSAllowsExactObsidianPreflightWithoutAuthentication(t *testing.T) {
	handler, calls := corsTestHandler(t, "app://obsidian.md")
	rec := httptest.NewRecorder()
	req := corsPreflight("/v1/ws-ticket", "app://obsidian.md", http.MethodPost, "content-type, AUTHORIZATION")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || *calls != 0 {
		t.Fatalf("ticket preflight = %d calls=%d body=%q", rec.Code, *calls, rec.Body.String())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "app://obsidian.md" || rec.Header().Get("Access-Control-Allow-Methods") != http.MethodPost || rec.Header().Get("Access-Control-Allow-Headers") != "Authorization, Content-Type" {
		t.Fatalf("ticket preflight headers = %#v", rec.Header())
	}
	for _, forbidden := range []string{"Access-Control-Allow-Credentials", "Access-Control-Allow-Private-Network"} {
		if rec.Header().Get(forbidden) != "" {
			t.Fatalf("preflight granted %s: %#v", forbidden, rec.Header())
		}
	}
	if strings.Contains(strings.Join(rec.Header().Values("Access-Control-Allow-Headers"), ","), "*") || rec.Header().Get("Access-Control-Allow-Origin") == "*" {
		t.Fatalf("preflight used wildcard: %#v", rec.Header())
	}
}

func TestBrowserCORSRejectsWrongMethodHeaderOriginAndPrivateNetwork(t *testing.T) {
	handler, calls := corsTestHandler(t, "app://obsidian.md")
	cases := []struct {
		name    string
		request *http.Request
	}{
		{"wrong origin", corsPreflight("/v1/browser/threads", "https://evil.example", http.MethodGet, "Authorization")},
		{"wrong method", corsPreflight("/v1/browser/threads/history", "app://obsidian.md", http.MethodPost, "Authorization")},
		{"wrong header", corsPreflight("/v1/browser/threads", "app://obsidian.md", http.MethodGet, "X-Unsafe")},
		{"range on non-artifact", corsPreflight("/v1/browser/threads", "app://obsidian.md", http.MethodGet, "Authorization, Range")},
	}
	privateNetwork := corsPreflight("/v1/browser/threads", "app://obsidian.md", http.MethodGet, "Authorization")
	privateNetwork.Header.Set("Access-Control-Request-Private-Network", "true")
	cases = append(cases, struct {
		name    string
		request *http.Request
	}{"private network", privateNetwork})
	duplicateMethod := corsPreflight("/v1/browser/threads", "app://obsidian.md", http.MethodGet, "Authorization")
	duplicateMethod.Header.Add("Access-Control-Request-Method", http.MethodPost)
	cases = append(cases, struct {
		name    string
		request *http.Request
	}{"duplicate method", duplicateMethod})
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, tc.request)
		if rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" || *calls != 0 {
			t.Fatalf("%s preflight = %d calls=%d headers=%#v", tc.name, rec.Code, *calls, rec.Header())
		}
	}
}

func TestBrowserCORSActualRequestsRemainBearerAuthenticated(t *testing.T) {
	handler, calls := corsTestHandler(t, "app://obsidian.md")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/browser/threads", nil)
	req.Header.Set("Origin", "app://obsidian.md")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || *calls != 0 || rec.Header().Get("Access-Control-Allow-Origin") != "app://obsidian.md" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unauthenticated actual request = %d calls=%d headers=%#v", rec.Code, *calls, rec.Header())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/browser/threads", nil)
	req.Header.Set("Origin", "app://obsidian.md")
	req.Header.Set("Authorization", "Bearer test-token")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || *calls != 1 || rec.Header().Get("Access-Control-Allow-Origin") != "app://obsidian.md" || rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("authenticated actual request = %d calls=%d headers=%#v", rec.Code, *calls, rec.Header())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/browser/threads", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Authorization", "Bearer test-token")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || *calls != 2 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("disallowed actual origin was reflected = %d calls=%d headers=%#v", rec.Code, *calls, rec.Header())
	}
}

func TestBrowserCORSArtifactHeadersAndRouteSpecificRequestHeaders(t *testing.T) {
	handler, calls := corsTestHandler(t, "app://obsidian.md")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, corsPreflight("/v1/browser/attachments/chunk", "app://obsidian.md", http.MethodPut, "x-sesh-chunk-sha256, authorization, content-type"))
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Headers") != "Authorization, Content-Type, X-Sesh-Chunk-SHA256" || *calls != 0 {
		t.Fatalf("chunk preflight = %d calls=%d headers=%#v", rec.Code, *calls, rec.Header())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, corsPreflight("/v1/browser/artifacts", "app://obsidian.md", http.MethodGet, "range, authorization"))
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Headers") != "Authorization, Range" {
		t.Fatalf("artifact range preflight = %d headers=%#v", rec.Code, rec.Header())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, corsPreflight("/v1/browser/artifact-mappings/query", "app://obsidian.md", http.MethodPost, "content-type, authorization"))
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Headers") != "Authorization, Content-Type" {
		t.Fatalf("artifact mapping query preflight = %d headers=%#v", rec.Code, rec.Header())
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, corsPreflight("/v1/browser/artifact-mappings/query", "app://obsidian.md", http.MethodPost, "authorization, range"))
	if rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("artifact mapping query accepted Range = %d headers=%#v", rec.Code, rec.Header())
	}

	for _, path := range []string{"/v1/browser/artifacts?thread_id=x&id=y", "/v1/browser/threads/artifact-bytes?id=x&publication_id=y"} {
		rec = httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Origin", "app://obsidian.md")
		req.Header.Set("Authorization", "Bearer test-token")
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Expose-Headers") != browserArtifactExposeHeaders {
			t.Fatalf("artifact exposed headers for %s = %d %#v", path, rec.Code, rec.Header())
		}
	}
	if *calls != 2 {
		t.Fatalf("artifact actual calls = %d, want 2", *calls)
	}
}

func varyContains(header http.Header, want string) bool {
	for _, value := range header.Values("Vary") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), want) {
				return true
			}
		}
	}
	return false
}

// The display route is a browser read, so it needs its own exact entry in the
// fixed CORS table — otherwise an app-origin client cannot call it at all. The
// grant stays narrow: GET + Authorization, with no write method and no
// unauthenticated actual request.
func TestBrowserCORSCoversTheDisplayRouteNarrowly(t *testing.T) {
	handler, calls := corsTestHandler(t, "app://obsidian.md")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, corsPreflight("/v1/browser/threads/display", "app://obsidian.md", http.MethodGet, "Authorization"))
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "app://obsidian.md" {
		t.Fatalf("display preflight = %d headers=%#v", rec.Code, rec.Header())
	}
	if rec.Header().Get("Access-Control-Allow-Headers") != "Authorization" {
		t.Fatalf("display preflight headers = %q", rec.Header().Get("Access-Control-Allow-Headers"))
	}
	if *calls != 0 {
		t.Fatalf("preflight reached the handler %d times", *calls)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, corsPreflight("/v1/browser/threads/display", "app://obsidian.md", http.MethodPost, "Authorization"))
	if rec.Code == http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("display accepted a write preflight: %d %#v", rec.Code, rec.Header())
	}
	// The actual read is still bearer-authenticated.
	req := httptest.NewRequest(http.MethodGet, "/v1/browser/threads/display?id=x", nil)
	req.Header.Set("Origin", "app://obsidian.md")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated display read = %d", rec.Code)
	}
}
