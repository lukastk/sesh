package daemon

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/lukastk/sesh/internal/agents"
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

// noAPIWarning returns the loud warning for a daemon running with NO TCP API, or
// "" when one is configured. Pure (env passed in) so it is unit-tested.
//
// An unset SESH_API_ADDR is LEGITIMATE for an inbound-less leaf (termux syncs
// outward only) — so this is a warning, not a refusal. But it is also the exact
// shape of a real fleet outage, and the failure is INVISIBLE from the affected
// machine: outbound sync keeps working, so its own `sesh mesh` and `sesh doctor`
// stay green while every OTHER machine marks it unreachable and (since the
// offline-hiding default) HIDES its threads in the TUI. That asymmetry let mymain
// sit off the mesh for 9 hours with nothing anywhere saying so.
//
// The usual cause is a hand-started `sesh daemon run`: started from an agent/shell
// pane it inherits that shell's environment instead of the service manager's, which
// is where SESH_API_ADDR lives. A daemon started from inside a sesh thread pane
// carries SESH_THREAD_ID, so say so outright when we can see it.
func noAPIWarning(apiAddr, threadID string) string {
	if apiAddr != "" {
		return ""
	}
	msg := "daemon: WARNING SESH_API_ADDR is not set — this daemon serves NO TCP API, so peers CANNOT REACH IT: " +
		"inbound mesh sync, cross-machine routing and remote clients are all down. Outbound sync still works, so THIS " +
		"machine's own `sesh mesh`/`sesh doctor` look healthy while every OTHER machine shows it unreachable and hides " +
		"its threads. Correct ONLY for an inbound-less leaf (e.g. termux); otherwise the daemon was started without its " +
		"service environment — restart it via the service manager (e.g. `supervisorctl restart sesh-daemon`), not by hand."
	if threadID != "" {
		msg += " NB this daemon was started from inside sesh thread " + threadID +
			" — a `sesh daemon run` launched by hand from an agent pane inherits that pane's environment, which is the usual cause."
	}
	return msg
}

// startAPI starts the optional TCP API listener — the network surface for remote
// clients (mobile / Obsidian) and direct cross-machine access. It serves the regular
// daemon router behind bearer-token auth. The generic /v1/route proxy is deliberately
// added only by unixRoutes; browser-hub endpoints expose only their fixed allowlist.
// It refuses to expose an unauthenticated API. No-op unless SESH_API_ADDR is set.
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
		log.Print(noAPIWarning(d.cfg.APIAddr, os.Getenv(agents.EnvThreadID)))
		return nil
	}
	if d.cfg.APIToken == "" {
		return fmt.Errorf("daemon: SESH_API_ADDR=%q is set but no SESH_API_TOKEN — refusing to expose an unauthenticated network API", d.cfg.APIAddr)
	}
	d.apiSrv = &http.Server{Handler: d.browserCORS(d.tokenAuth(d.cfg.APIToken, d.routes()))}
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

// wsTokenRoutes are the WebSocket upgrade endpoints that retain legacy bearer-token
// query authentication. New browser clients mint a scoped ?ticket= instead.
var wsTokenRoutes = map[string]bool{
	"/v1/threads/rpc":      true,
	"/v1/threads/terminal": true,
}

// tokenAuth protects the TCP listener. Ordinary HTTP remains header-only. The two
// legacy WS routes still accept ?token= for compatibility; a scoped ?ticket= is
// independently consumed and bound to its exact path/thread/protocol/owner.
type browserAuthorityContextKey struct{}

func (d *Daemon) tokenAuth(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	wantQuery := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		ok := subtle.ConstantTimeEq(int32(len(got)), int32(len(want))) == 1 &&
			subtle.ConstantTimeCompare(got, want) == 1
		headerOK := ok
		if !ok && wsTokenRoutes[r.URL.Path] {
			q := []byte(r.URL.Query().Get("token"))
			ok = subtle.ConstantTimeEq(int32(len(q)), int32(len(wantQuery))) == 1 &&
				subtle.ConstantTimeCompare(q, wantQuery) == 1
		}
		if !ok && wsTokenRoutes[r.URL.Path] && len(r.URL.Query()["ticket"]) > 0 {
			if owner, accepted := d.authenticateWSTicket(r, time.Now()); accepted {
				r = r.WithContext(context.WithValue(r.Context(), wsOwnerContextKey{}, owner))
				ok = true
			}
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized: a valid bearer token or websocket ticket is required")
			return
		}
		if headerOK {
			sum := sha256.Sum256([]byte(token))
			r = r.WithContext(context.WithValue(r.Context(), browserAuthorityContextKey{}, "bearer:"+hex.EncodeToString(sum[:16])))
		}
		next.ServeHTTP(w, r)
	})
}
