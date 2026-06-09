package daemon

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
)

// startAPI starts the optional TCP API listener — the network surface for remote
// clients (mobile / Obsidian) and direct cross-machine access. It serves the
// IDENTICAL full router as the unix socket, wrapped in bearer-token auth, so the
// network API has complete parity with the local one BY CONSTRUCTION (every
// endpoint is exposed; there is no second, partial surface to drift). It refuses to
// expose an unauthenticated API. No-op unless SESH_API_ADDR is set.
func (d *Daemon) startAPI() error {
	if d.cfg.APIAddr == "" {
		return nil
	}
	if d.cfg.APIToken == "" {
		return fmt.Errorf("daemon: SESH_API_ADDR=%q is set but no SESH_API_TOKEN — refusing to expose an unauthenticated network API", d.cfg.APIAddr)
	}
	ln, err := net.Listen("tcp", d.cfg.APIAddr)
	if err != nil {
		return fmt.Errorf("daemon: api listen %s: %w", d.cfg.APIAddr, err)
	}
	d.apiSrv = &http.Server{Handler: tokenAuth(d.cfg.APIToken, d.routes())}
	go d.apiSrv.Serve(ln) //nolint:errcheck — Serve returns on Shutdown
	return nil
}

func (d *Daemon) stopAPI(ctx context.Context) {
	if d.apiSrv != nil {
		d.apiSrv.Shutdown(ctx) //nolint:errcheck — best-effort
	}
}

// tokenAuth requires `Authorization: Bearer <token>` on every request, with a
// constant-time comparison (no length/timing oracle). The unix socket is exempt
// (local trust); only the TCP listener is wrapped.
func tokenAuth(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeEq(int32(len(got)), int32(len(want))) != 1 ||
			subtle.ConstantTimeCompare(got, want) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized: a valid bearer token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
