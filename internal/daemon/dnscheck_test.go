package daemon

import (
	"testing"

	"github.com/lukastk/sesh/internal/peers"
)

// TestHTTPPeerHost: the DNS self-check resolves ONLY http peers' hostnames (the ones
// Go dials directly) — never ssh peers (system ssh resolves those) and never literal
// IPs (no DNS needed).
func TestHTTPPeerHost(t *testing.T) {
	cases := []struct {
		name string
		p    peers.Peer
		want string
	}{
		{"http hostname", peers.Peer{Machine: "mymain", ApiAddr: "mymain:7878"}, "mymain"},
		{"http fqdn", peers.Peer{Machine: "m", ApiAddr: "mymain.tail27f06c.ts.net:7878"}, "mymain.tail27f06c.ts.net"},
		{"http literal ipv4 skipped", peers.Peer{Machine: "m", ApiAddr: "100.106.17.33:7878"}, ""},
		{"http bare host no port", peers.Peer{Machine: "m", ApiAddr: "macstudio"}, "macstudio"},
		{"ssh peer skipped", peers.Peer{Machine: "m", SSH: "lukas@macbook", Home: "/h"}, ""},
		{"empty", peers.Peer{Machine: "m"}, ""},
	}
	for _, c := range cases {
		if got := httpPeerHost(c.p); got != c.want {
			t.Errorf("%s: httpPeerHost = %q, want %q", c.name, got, c.want)
		}
	}
}
