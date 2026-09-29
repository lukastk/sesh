package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
)

// ROUTED CALLS THROUGH THE LOCAL DAEMON (schema 51, issue #12).
//
// A `sesh <cmd> --machine X` for an http peer used to point the CLI process straight at
// X's TCP API. Every CLI invocation is a fresh process, so every routed call paid a
// fresh TCP connection (one extra tailnet round trip), and before that a full GET
// /v1/mesh (~2,400 threads, 2.4 MB of JSON on this fleet) just to read ONE machine's
// reachability bit. Measured 2026-09-29 (probe with sesh's own client packages):
//
//	                       offline check   fresh conn call   warm conn call
//	mymain  -> macbook       34-41 ms          87 ms            45-47 ms
//	termux  -> mymain        45-102 ms        100 ms            50 ms
//
// The daemon already holds a WARM keep-alive connection to every http peer — its mesh
// sync polls them through http.DefaultTransport (1 Hz in demand, idle_interval <
// DefaultTransport's 90 s IdleConnTimeout otherwise). So the CLI now asks its LOCAL
// daemon over the unix socket, and the daemon forwards the request on that pool and
// answers the reachability question from memory. Nothing about the peer's TRANSPORT
// changes: only http peers are proxied (an ssh peer is refused here, and the CLI never
// sends one — it keeps its real ssh hop), and an unreachable peer still fails loudly.
//
// UNIX SOCKET ONLY. This handler is mounted by unixRoutes, never on the TCP API: a
// token holder must not be able to relay through one machine to another, and routing
// never nests (a route inside a route is refused), so a request cannot loop.
//
// HEADERS. Every response this handler produces carries api.RoutedByHeader — so a 404
// WITHOUT it can only mean the local daemon predates schema 51 (the client turns that
// into a loud "restart your daemon"). The handler's OWN refusals (the peer never
// answered) additionally carry api.RouteRefusedHeader, so the client reports their
// message whatever method made the call; the peer's own answers never carry it.

// routeProxyTimeout bounds one proxied request end to end. It matches NewRemote's
// client timeout — the ceiling the direct-dial path had — so a hung peer fails in the
// same time as before. The caller's own context still cancels it sooner.
const routeProxyTimeout = 15 * time.Second

// unixRoutes is the unix-socket surface: every regular route plus the routing proxy.
func (d *Daemon) unixRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/route/{machine}/{path...}", d.handleRoute)
	mux.Handle("/", d.routes())
	return mux
}

// handleRoute serves ANY /v1/route/<machine>/<path>: forward the request (method,
// path, query, body) to <machine>'s TCP API as /<path>, authenticated with that peer's
// token, over the daemon's shared keep-alive transport.
func (d *Daemon) handleRoute(w http.ResponseWriter, r *http.Request) {
	machine, path := r.PathValue("machine"), r.PathValue("path")
	if machine == d.cfg.Machine {
		refuseRoute(w, machine, http.StatusBadRequest, fmt.Sprintf("route: %q is this machine — call it directly, not through /v1/route", machine))
		return
	}
	if !strings.HasPrefix(path, "v1/") {
		refuseRoute(w, machine, http.StatusBadRequest, fmt.Sprintf("route: %q is not an API path (want v1/...)", path))
		return
	}
	if strings.HasPrefix(path, "v1/route/") {
		refuseRoute(w, machine, http.StatusBadRequest, "route: nested routing is refused (a routed request may not itself route)")
		return
	}
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		refuseRoute(w, machine, http.StatusInternalServerError, "route: load peers: "+err.Error())
		return
	}
	p, ok := reg.Get(machine)
	if !ok {
		refuseRoute(w, machine, http.StatusNotFound, fmt.Sprintf("unknown machine %q: no peer registered (see `sesh peer add`)", machine))
		return
	}
	if p.Transport() != "http" {
		refuseRoute(w, machine, http.StatusBadRequest, fmt.Sprintf("route: machine %q is an %s peer — only http peers are routed through the daemon (the CLI runs ssh peers over a real ssh hop)", machine, p.Transport()))
		return
	}
	// The same gate the CLI applied before (peerKnownOffline), now read from memory
	// instead of by fetching the whole mesh: refuse only a machine the cache
	// DEFINITIVELY knows is down; a never-synced peer is still attempted.
	if d.view.knownOffline()[machine] {
		refuseRoute(w, machine, http.StatusServiceUnavailable, fmt.Sprintf("machine %q is offline per the local mesh cache; not routing (check `sesh mesh`, retry when it is back)", machine))
		return
	}
	token, err := p.ResolveAPIToken()
	if err != nil {
		refuseRoute(w, machine, http.StatusInternalServerError, "route: "+err.Error())
		return
	}
	target := &url.URL{Scheme: "http", Host: p.ApiAddr}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/" + path
			pr.Out.URL.RawPath = ""
			pr.Out.Header.Set("Authorization", "Bearer "+token)
		},
		// The SHARED keep-alive pool mesh sync already keeps warm to this peer —
		// the whole point. (A private transport would start cold.)
		Transport:     http.DefaultTransport,
		FlushInterval: -1, // stream: a long-running routed call's output is not buffered
		ModifyResponse: func(resp *http.Response) error {
			// The PEER's own answer: routed-by, never refused — passed through as-is.
			resp.Header.Set(api.RoutedByHeader, machine)
			resp.Header.Del(api.RouteRefusedHeader)
			// A routed MUTATION should show up in local reads within ~an RTT, so
			// re-sync this peer's cached view now (what the CLI's post-route nudge
			// did). Reads change nothing, so they no longer trigger a peer sync.
			if r.Method != http.MethodGet && r.Method != http.MethodHead && resp.StatusCode < 300 {
				d.noteMeshDemand()
				d.mesh.launchSync(p)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			refuseRoute(w, machine, http.StatusBadGateway, fmt.Sprintf("route to %s (%s): %v", machine, p.ApiAddr, err))
		},
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeProxyTimeout)
	defer cancel()
	proxy.ServeHTTP(w, r.WithContext(ctx))
}

// refuseRoute answers for the route handler ITSELF (the peer never answered): both
// headers set, so the routed client reports the message whatever method called.
func refuseRoute(w http.ResponseWriter, machine string, code int, msg string) {
	w.Header().Set(api.RoutedByHeader, machine)
	w.Header().Set(api.RouteRefusedHeader, "1")
	writeError(w, code, msg)
}
