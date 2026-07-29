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

// tailnetSentinel is the special SESH_API_ADDR host meaning "bind my own tailnet
// interface" — the daemon DISCOVERS its 100.64.0.0/10 address at bind time rather
// than DNS-resolving a name (issue #9). Resolving a name to find your own interface
// is what let NSS `myhostname` shadow MagicDNS and silently bind a LAN address,
// partitioning the machine from the mesh. myrig sets `SESH_API_ADDR=tailnet:7878`.
const tailnetSentinel = "tailnet"

// tailnetCGNAT is Tailscale's IPv4 range (100.64.0.0/10, RFC 6598 CGNAT). A node
// has exactly one address in it; the daemon binds that one.
var tailnetCGNAT = func() *net.IPNet {
	_, n, err := net.ParseCIDR("100.64.0.0/10")
	if err != nil {
		panic(err) // a compile-time-constant CIDR cannot fail to parse
	}
	return n
}()

// interfaceAddrs is net.InterfaceAddrs, injectable so tests can drive tailnetIPv4
// without a real tailnet interface.
var interfaceAddrs = net.InterfaceAddrs

// tailnetIPv4 returns this machine's own tailnet (100.64.0.0/10) IPv4 address by
// scanning the local interfaces — no DNS, no `tailscale` CLI, no external dep.
// It is LOUD about the two ways it can be unusable: zero matches (tailscaled not
// up yet — the caller retries) and, defensively, more than one match (a CGNAT-ISP
// clash — refuse to guess). This is the reliable "which interface is the tailnet
// one" answer that DNS name-resolution got wrong.
func tailnetIPv4() (string, error) {
	addrs, err := interfaceAddrs()
	if err != nil {
		return "", fmt.Errorf("enumerate interfaces: %w", err)
	}
	var found []string
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip4 := ip.To4(); ip4 != nil && tailnetCGNAT.Contains(ip4) {
			found = append(found, ip4.String())
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no tailnet address (100.64.0.0/10) on any interface — is tailscaled up?")
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("multiple tailnet addresses %v — refusing to guess; set SESH_API_ADDR to an explicit IP:port", found)
	}
}

// resolveAPIBindAddr turns SESH_API_ADDR into the concrete address to bind. The
// `tailnet` sentinel host is resolved to this machine's own tailnet IP (issue #9);
// any other host — an explicit IP or a name — is passed through unchanged (a name
// is still DNS-resolved by net.Listen, the pre-issue-#9 behavior, kept for manual
// use). Called each retry iteration so a tailnet that comes up after the daemon
// (boot ordering) is picked up.
func (d *Daemon) resolveAPIBindAddr() (string, error) {
	host, port, err := net.SplitHostPort(d.cfg.APIAddr)
	if err != nil {
		return "", fmt.Errorf("SESH_API_ADDR=%q: %w", d.cfg.APIAddr, err)
	}
	if host != tailnetSentinel {
		return d.cfg.APIAddr, nil
	}
	ip, err := tailnetIPv4()
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ip, port), nil
}

// offTailnetBind returns a non-empty reason when a bound address exposes the API
// OFF the tailnet — 0.0.0.0/:: (all interfaces, LAN+public included) or a concrete
// non-loopback, non-tailnet IP. The TCP API is the full router behind ONE bearer
// token (it can run agent turns and open a live terminal), designed to live behind
// Tailscale; an off-tailnet bind is a real exposure. Pure, so it is unit-tested; a
// name-based bind returns "" (cannot classify a hostname here — the sentinel is the
// safe path). Loopback is fine.
func offTailnetBind(bindAddr string) string {
	host, _, err := net.SplitHostPort(bindAddr)
	if err != nil {
		return ""
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() {
		return ""
	}
	if ip.IsUnspecified() {
		return "bound to all interfaces (LAN/public included)"
	}
	if ip4 := ip.To4(); ip4 != nil && !tailnetCGNAT.Contains(ip4) {
		return "not a tailnet (100.64.0.0/10) address"
	}
	return ""
}

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
// apiBindRetry until it succeeds or the daemon stops. Loud on every failure, and
// the bind state is TRACKED (apiBound/apiBindErr) so `sesh doctor` can surface a
// configured-but-never-bound API — ideapad's daemon once retried "lookup ideapad:
// no such host" every 5s for 12 DAYS while doctor said "api: ok, exposed on …",
// and the whole mesh silently showed the machine offline (ticket aeaca0d0).
func (d *Daemon) serveAPIWithRetry() {
	for {
		// Resolve the bind address EACH iteration: the `tailnet` sentinel discovers
		// the tailnet interface, which may not exist yet at boot (tailscaled coming
		// up after the daemon) — retrying re-scans until it appears.
		bindAddr, err := d.resolveAPIBindAddr()
		if err == nil {
			var ln net.Listener
			if ln, err = net.Listen("tcp", bindAddr); err == nil {
				d.apiBoundAddr.Store(bindAddr)
				d.apiBound.Store(true)
				if reason := offTailnetBind(bindAddr); reason != "" {
					log.Printf("daemon: WARNING api bound %s — %s; the token-authed API (agent turns, live terminal) is designed to live behind Tailscale", bindAddr, reason)
				}
				log.Printf("daemon: api listening on %s", bindAddr)
				d.apiSrv.Serve(ln) //nolint:errcheck — returns on Shutdown
				return
			}
		}
		d.apiBindErr.Store(err.Error())
		log.Printf("daemon: api bind for SESH_API_ADDR=%q failed, retrying in %s (the local socket is unaffected): %v", d.cfg.APIAddr, apiBindRetry, err)
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
