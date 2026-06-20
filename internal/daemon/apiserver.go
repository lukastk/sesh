package daemon

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

// apiBindRetry is how often the API listener retries a failed bind.
const apiBindRetry = 5 * time.Second

// startAPI starts the optional TCP API listener — the network surface for remote
// clients (mobile / Obsidian) and direct cross-machine access. It serves the
// IDENTICAL full router as the unix socket, wrapped in bearer-token auth, so the
// network API has complete parity with the local one BY CONSTRUCTION (every
// endpoint is exposed; there is no second, partial surface to drift). It refuses to
// expose an unauthenticated API. No-op unless SESH_API_ADDR is set.
//
// A missing token is a MISCONFIGURATION → fatal. But the bind itself is retried in
// the background (serveAPIWithRetry), because the common failure is TRANSIENT: at
// boot the daemon can start before tailscaled is up, so the tailscale name in
// SESH_API_ADDR doesn't resolve yet ("lookup <host>: no such host"). Crashing over
// that would take the LOCAL unix socket down too (and supervisor's "exited too
// quickly" then gives up permanently) — so the daemon's core stays up and the network
// API comes online as soon as the address is bindable. Never silent: each failure is
// logged loudly.
func (d *Daemon) startAPI() error {
	if d.cfg.APIAddr == "" {
		return nil
	}
	if d.cfg.APIToken == "" {
		return fmt.Errorf("daemon: SESH_API_ADDR=%q is set but no SESH_API_TOKEN — refusing to expose an unauthenticated network API", d.cfg.APIAddr)
	}
	d.apiSrv = &http.Server{Handler: tokenAuth(d.cfg.APIToken, d.routes())}
	d.apiStop = make(chan struct{})
	go d.serveAPIWithRetry()
	return nil
}

// serveAPIWithRetry binds and serves the TCP API, retrying a failed bind every
// apiBindRetry until it succeeds or the daemon stops. Loud on every failure.
func (d *Daemon) serveAPIWithRetry() {
	for {
		ln, err := net.Listen("tcp", d.cfg.APIAddr)
		if err == nil {
			log.Printf("daemon: api listening on %s", d.cfg.APIAddr)
			d.apiSrv.Serve(ln) //nolint:errcheck — returns on Shutdown
			return
		}
		log.Printf("daemon: api listen %s failed, retrying in %s (the local socket is unaffected): %v", d.cfg.APIAddr, apiBindRetry, err)
		select {
		case <-time.After(apiBindRetry):
		case <-d.apiStop:
			return
		}
	}
}

func (d *Daemon) stopAPI(ctx context.Context) {
	if d.apiStop != nil {
		close(d.apiStop)
	}
	if d.apiSrv != nil {
		d.apiSrv.Shutdown(ctx) //nolint:errcheck — best-effort
	}
}

// wsTokenRoutes are the WebSocket upgrade endpoints that ALSO accept the bearer
// token via a `?token=` query param (see tokenAuth). A browser/WebView WebSocket
// cannot set request headers on the handshake, so the query param is the standard
// way to authenticate it. Kept to exactly the two streaming routes — the rest of the
// API is header-only.
var wsTokenRoutes = map[string]bool{
	"/v1/threads/rpc":      true,
	"/v1/threads/terminal": true,
}

// tokenAuth requires a valid bearer token on every request, compared in constant
// time (no length/timing oracle). The token is normally supplied as
// `Authorization: Bearer <token>`. For the two WebSocket routes in wsTokenRoutes it
// MAY ALSO be supplied as a `?token=<token>` query param, because a browser/WebView
// WebSocket handshake cannot carry custom headers (the only way the Android app and
// any browser client can authenticate the rpc/terminal sockets).
//
// SECURITY TRADE-OFF: a token in the query string can leak where a header does not —
// access logs, proxy logs, browser history. We accept this NARROWLY: it is enabled
// only for the two WS upgrade routes (never the general HTTP API), and the TCP API is
// designed to live behind Tailscale (a private, authenticated network) where the only
// thing that could log the URL is the daemon itself. This is the same trade-off every
// browser-WebSocket-with-auth deployment makes; the header path is unchanged and
// preferred wherever headers are possible.
//
// The unix socket is exempt from auth entirely (local trust); only the TCP listener
// is wrapped.
func tokenAuth(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	wantQuery := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		ok := subtle.ConstantTimeEq(int32(len(got)), int32(len(want))) == 1 &&
			subtle.ConstantTimeCompare(got, want) == 1
		// WS upgrade routes additionally accept ?token= (constant-time, same as the header).
		if !ok && wsTokenRoutes[r.URL.Path] {
			q := []byte(r.URL.Query().Get("token"))
			ok = subtle.ConstantTimeEq(int32(len(q)), int32(len(wantQuery))) == 1 &&
				subtle.ConstantTimeCompare(q, wantQuery) == 1
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized: a valid bearer token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
