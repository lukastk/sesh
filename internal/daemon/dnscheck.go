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

// dnsCheckBackoff are the waits between self-check attempts. The first attempt
// races tailscale/MagicDNS coming up at daemon boot — the resolver is routinely
// not ready for several seconds — so a failure RETRIES with backoff and only the
// final failure is the loud one. Firing the scary "mesh will not work" warning
// once against a not-yet-ready resolver and never re-checking misdiagnosed a
// healthy daemon at every boot (issue #1's secondary bug).
var dnsCheckBackoff = []time.Duration{
	5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second, 120 * time.Second,
}

// checkPeerDNS resolves — in the background, retrying with backoff — the hostname of
// every HTTP-transport peer (the addresses this daemon's Go runtime dials directly for
// mesh sync / nav / routing) and logs a LOUD warning if any still don't resolve once
// the retries are exhausted (~4 min). It never blocks or fails startup; it exists so
// a broken Go resolver is caught in the log, instead of silently surfacing as every
// peer showing OFFLINE in the TUI.
//
// The canonical PERSISTENT trigger is a termux binary built with CGO_ENABLED=0: its
// pure-Go resolver reads /etc/resolv.conf (absent on termux) and can't reach Android's
// bionic/tailscale DNS, so tailscale MagicDNS names (mymain, macstudio, …) never
// resolve. A CGO_ENABLED=1 native build (termux's default) uses bionic and works.
func (d *Daemon) checkPeerDNS() {
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		return // unconfigured/unreadable peers is not this check's concern
	}
	lookup := func(host string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_, lerr := net.DefaultResolver.LookupHost(ctx, host)
		return lerr
	}
	runPeerDNSCheck(reg.List(), lookup, time.Sleep)
}

// runPeerDNSCheck is the retry engine (injectable lookup/sleep for tests). It
// re-attempts ALL http-peer hostnames each round: transient boot failures log a
// mild "retrying" line, recovery is logged explicitly, and only exhausting every
// attempt produces the loud FAILED diagnosis. Returns whether the final attempt
// had failures (for tests; logging is the production surface).
func runPeerDNSCheck(peerList []peers.Peer, lookup func(host string) error, sleep func(time.Duration)) bool {
	hadFailure := false
	for attempt := 0; ; attempt++ {
		var failed []string
		for _, p := range peerList {
			host := httpPeerHost(p)
			if host == "" {
				continue
			}
			if lerr := lookup(host); lerr != nil {
				failed = append(failed, p.Machine+" ("+host+"): "+lerr.Error())
			}
		}
		if len(failed) == 0 {
			if hadFailure {
				log.Printf("daemon: DNS self-check recovered (attempt %d): all http peer hostnames resolve", attempt+1)
			}
			return false
		}
		hadFailure = true
		if attempt >= len(dnsCheckBackoff) {
			log.Printf("daemon: DNS self-check FAILED for %d http peer(s) after %d attempts: %s — cross-machine mesh/nav/routing over http will not work. "+
				"On termux this usually means the binary was built with CGO_ENABLED=0 (the pure-Go resolver can't reach Android's bionic/tailscale DNS); rebuild natively with CGO_ENABLED=1.",
				len(failed), attempt+1, strings.Join(failed, ", "))
			return true
		}
		log.Printf("daemon: DNS self-check: %d http peer(s) not resolving yet (%s) — the resolver/tailscale may still be coming up; retrying in %v",
			len(failed), strings.Join(failed, ", "), dnsCheckBackoff[attempt])
		sleep(dnsCheckBackoff[attempt])
	}
}
