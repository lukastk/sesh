package daemon

import (
	"context"
	"log"
	"net"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/peers"
)

// httpPeerHost returns the hostname THIS daemon's Go runtime will dial for an http
// peer (its ApiAddr host) — or "" when there is nothing to DNS-resolve: a non-http
// (ssh) peer (those resolve via the system ssh, not Go), an http peer with no addr,
// or one whose ApiAddr is already a literal IP. Factored out so it can be unit-tested
// without touching the network.
func httpPeerHost(p peers.Peer) string {
	if p.Transport() != "http" || p.ApiAddr == "" {
		return ""
	}
	host := p.ApiAddr
	if h, _, err := net.SplitHostPort(p.ApiAddr); err == nil {
		host = h
	}
	if host == "" || net.ParseIP(host) != nil {
		return ""
	}
	return host
}

// checkPeerDNS resolves — once, at startup, in the background — the hostname of every
// HTTP-transport peer (the addresses this daemon's Go runtime dials directly for mesh
// sync / nav / routing) and logs a LOUD warning for any that don't resolve. It never
// blocks or fails startup; it exists so a broken Go resolver is caught IMMEDIATELY in
// the log, instead of silently surfacing as every peer showing OFFLINE in the TUI.
//
// The canonical trigger is a termux binary built with CGO_ENABLED=0: its pure-Go
// resolver reads /etc/resolv.conf (absent on termux) and can't reach Android's
// bionic/tailscale DNS, so tailscale MagicDNS names (mymain, macstudio, …) never
// resolve. A CGO_ENABLED=1 native build (termux's default) uses bionic and works.
func (d *Daemon) checkPeerDNS() {
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		return // unconfigured/unreadable peers is not this check's concern
	}
	var failed []string
	for _, p := range reg.Peers {
		host := httpPeerHost(p)
		if host == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		_, lerr := net.DefaultResolver.LookupHost(ctx, host)
		cancel()
		if lerr != nil {
			failed = append(failed, p.Machine+" ("+host+")")
			log.Printf("daemon: DNS self-check: cannot resolve http peer %s host %q: %v", p.Machine, host, lerr)
		}
	}
	if len(failed) > 0 {
		log.Printf("daemon: DNS self-check FAILED for %d http peer(s): %s — cross-machine mesh/nav/routing over http will not work. "+
			"On termux this usually means the binary was built with CGO_ENABLED=0 (the pure-Go resolver can't reach Android's bionic/tailscale DNS); rebuild natively with CGO_ENABLED=1.",
			len(failed), strings.Join(failed, ", "))
	}
}
