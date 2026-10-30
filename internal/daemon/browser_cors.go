package daemon

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

const browserArtifactExposeHeaders = "Accept-Ranges, Content-Disposition, Content-Length, Content-Range, ETag, Last-Modified, X-Content-Type-Options, X-Sesh-Artifact-SHA256"
const browserInputImageExposeHeaders = "Accept-Ranges, Content-Length, ETag, X-Content-Type-Options, X-Sesh-Input-Image-Digest-Status, X-Sesh-Input-Image-SHA256"

type browserCORSMethodPolicy struct {
	requestHeaders map[string]string
	exposeHeaders  string
}

type browserCORSRoutePolicy map[string]browserCORSMethodPolicy

func corsHeaders(names ...string) map[string]string {
	out := make(map[string]string, len(names))
	for _, name := range names {
		out[strings.ToLower(name)] = name
	}
	return out
}

var browserCORSRoutes = map[string]browserCORSRoutePolicy{
	"/v1/browser/threads": {
		http.MethodGet:  {requestHeaders: corsHeaders("Authorization")},
		http.MethodPost: {requestHeaders: corsHeaders("Authorization", "Content-Type")},
	},
	"/v1/browser/threads/action": {
		http.MethodPost: {requestHeaders: corsHeaders("Authorization", "Content-Type")},
	},
	"/v1/browser/threads/transcript": {
		http.MethodGet: {requestHeaders: corsHeaders("Authorization")},
	},
	"/v1/browser/threads/display": {
		http.MethodGet: {requestHeaders: corsHeaders("Authorization")},
	},
	"/v1/browser/threads/history": {
		http.MethodGet: {requestHeaders: corsHeaders("Authorization")},
	},
	"/v1/browser/threads/presentation": {
		http.MethodGet: {requestHeaders: corsHeaders("Authorization")},
	},
	"/v1/browser/threads/media-presentation": {
		http.MethodGet: {requestHeaders: corsHeaders("Authorization")},
	},
	"/v1/browser/threads/input-images": {
		http.MethodGet: {requestHeaders: corsHeaders("Authorization")},
	},
	"/v1/browser/input-images": {
		http.MethodGet:  {requestHeaders: corsHeaders("Authorization"), exposeHeaders: browserInputImageExposeHeaders},
		http.MethodHead: {requestHeaders: corsHeaders("Authorization"), exposeHeaders: browserInputImageExposeHeaders},
	},
	"/v1/browser/threads/artifacts": {
		http.MethodGet: {requestHeaders: corsHeaders("Authorization")},
	},
	"/v1/browser/threads/artifact-bytes": {
		http.MethodGet: {requestHeaders: corsHeaders("Authorization"), exposeHeaders: browserArtifactExposeHeaders},
	},
	"/v1/browser/attachments/start": {
		http.MethodPost: {requestHeaders: corsHeaders("Authorization", "Content-Type")},
	},
	"/v1/browser/attachments": {
		http.MethodGet:    {requestHeaders: corsHeaders("Authorization")},
		http.MethodDelete: {requestHeaders: corsHeaders("Authorization")},
	},
	"/v1/browser/attachments/chunk": {
		http.MethodPut: {requestHeaders: corsHeaders("Authorization", "Content-Type", "X-Sesh-Chunk-SHA256")},
	},
	"/v1/browser/attachments/complete": {
		http.MethodPost: {requestHeaders: corsHeaders("Authorization", "Content-Type")},
	},
	"/v1/browser/attachments/prepare": {
		http.MethodPost: {requestHeaders: corsHeaders("Authorization", "Content-Type")},
	},
	"/v1/browser/artifacts": {
		http.MethodGet:  {requestHeaders: corsHeaders("Authorization", "Range"), exposeHeaders: browserArtifactExposeHeaders},
		http.MethodHead: {requestHeaders: corsHeaders("Authorization", "Range"), exposeHeaders: browserArtifactExposeHeaders},
	},
	"/v1/browser/artifact-mappings/query": {
		http.MethodPost: {requestHeaders: corsHeaders("Authorization", "Content-Type")},
	},
	"/v1/ws-ticket": {
		http.MethodPost: {requestHeaders: corsHeaders("Authorization", "Content-Type")},
	},
}

// browserCORS is the outermost TCP middleware. It handles unauthenticated
// preflight only for the exact fixed route table above; actual requests still
// flow through tokenAuth and all owner/peer checks. Unix routes do not use it.
func (d *Daemon) browserCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, routeKnown := browserCORSRoutes[r.URL.Path]
		origins := r.Header.Values("Origin")
		if r.Method == http.MethodOptions && routeKnown && len(origins) > 0 {
			d.handleBrowserCORSPreflight(w, r, route, origins)
			return
		}
		policy, methodKnown := route[r.Method]
		if routeKnown && methodKnown && len(origins) > 0 {
			addVary(w.Header(), "Origin")
			w.Header().Set("Cache-Control", "no-store")
			if len(origins) == 1 {
				if allowedOrigin, allowed := d.browserAPI.MatchAllowedOrigin(origins[0]); allowed {
					w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
					if policy.exposeHeaders != "" {
						w.Header().Set("Access-Control-Expose-Headers", policy.exposeHeaders)
					}
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (d *Daemon) handleBrowserCORSPreflight(w http.ResponseWriter, r *http.Request, route browserCORSRoutePolicy, origins []string) {
	addVary(w.Header(), "Origin")
	addVary(w.Header(), "Access-Control-Request-Method")
	addVary(w.Header(), "Access-Control-Request-Headers")
	w.Header().Set("Cache-Control", "no-store")
	if len(origins) != 1 {
		http.Error(w, "CORS origin is not allowed", http.StatusForbidden)
		return
	}
	allowedOrigin, allowed := d.browserAPI.MatchAllowedOrigin(origins[0])
	if !allowed {
		http.Error(w, "CORS origin is not allowed", http.StatusForbidden)
		return
	}
	if r.Header.Get("Access-Control-Request-Private-Network") != "" {
		http.Error(w, "private-network CORS is not allowed", http.StatusForbidden)
		return
	}
	requestedMethods := r.Header.Values("Access-Control-Request-Method")
	if len(requestedMethods) != 1 {
		http.Error(w, "CORS method is not allowed for this route", http.StatusForbidden)
		return
	}
	requestedMethod := requestedMethods[0]
	policy, ok := route[requestedMethod]
	if !ok {
		http.Error(w, "CORS method is not allowed for this route", http.StatusForbidden)
		return
	}
	requestedHeaders, err := parseBrowserCORSRequestHeaders(r.Header.Values("Access-Control-Request-Headers"), policy.requestHeaders)
	if err != nil {
		http.Error(w, "CORS request headers are not allowed for this route", http.StatusForbidden)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
	w.Header().Set("Access-Control-Allow-Methods", requestedMethod)
	if len(requestedHeaders) > 0 {
		w.Header().Set("Access-Control-Allow-Headers", strings.Join(requestedHeaders, ", "))
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseBrowserCORSRequestHeaders(values []string, allowed map[string]string) ([]string, error) {
	requested := map[string]string{}
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			name := strings.TrimSpace(token)
			if name == "" {
				return nil, fmt.Errorf("empty requested header")
			}
			lower := strings.ToLower(name)
			canonical, ok := allowed[lower]
			if !ok {
				return nil, fmt.Errorf("header %q is not allowed", name)
			}
			requested[lower] = canonical
		}
	}
	keys := make([]string, 0, len(requested))
	for key := range requested {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, requested[key])
	}
	return out, nil
}

func addVary(header http.Header, value string) {
	for _, line := range header.Values("Vary") {
		for _, existing := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(existing), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}
