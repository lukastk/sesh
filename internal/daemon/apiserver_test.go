package daemon

import (
	"net"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/config"
)

// addrList builds a []net.Addr of *net.IPNet from CIDR-ish "ip/bits" strings, the
// shape net.InterfaceAddrs returns.
func addrList(t *testing.T, cidrs ...string) []net.Addr {
	t.Helper()
	var out []net.Addr
	for _, c := range cidrs {
		ip, ipnet, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatalf("bad cidr %q: %v", c, err)
		}
		out = append(out, &net.IPNet{IP: ip, Mask: ipnet.Mask})
	}
	return out
}

// TestTailnetIPv4 pins the discovery: exactly one 100.64.0.0/10 IPv4 address is
// returned; zero (tailscaled down) and more-than-one (CGNAT clash) are LOUD
// errors, not a silent wrong pick; LAN/public/loopback/IPv6 are ignored.
func TestTailnetIPv4(t *testing.T) {
	orig := interfaceAddrs
	defer func() { interfaceAddrs = orig }()

	cases := []struct {
		name    string
		addrs   []string
		want    string
		wantErr bool
	}{
		{"one tailnet among LAN/loopback/v6", []string{"127.0.0.1/8", "192.168.1.152/24", "100.106.17.33/32", "fe80::1/64"}, "100.106.17.33", false},
		{"only LAN/public -> error (tailscaled down)", []string{"127.0.0.1/8", "192.168.1.152/24", "77.42.21.223/32"}, "", true},
		{"two tailnet addrs -> refuse to guess", []string{"100.106.17.33/32", "100.85.205.118/32"}, "", true},
		{"lower + upper edges of the range", []string{"100.64.0.0/10"}, "100.64.0.0", false},
	}
	for _, c := range cases {
		interfaceAddrs = func() ([]net.Addr, error) { return addrList(t, c.addrs...), nil }
		got, err := tailnetIPv4()
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: want error, got %q", c.name, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got (%q, %v), want %q", c.name, got, err, c.want)
		}
	}

	// A range-boundary sanity check: 100.128.0.0 is JUST outside 100.64.0.0/10.
	interfaceAddrs = func() ([]net.Addr, error) { return addrList(t, "100.128.0.1/32"), nil }
	if _, err := tailnetIPv4(); err == nil {
		t.Errorf("100.128.0.1 is outside the tailnet range but was accepted")
	}
}

// TestResolveAPIBindAddr: the `tailnet` sentinel resolves to the discovered IP on
// the configured port; an explicit IP or a name is passed through unchanged.
func TestResolveAPIBindAddr(t *testing.T) {
	orig := interfaceAddrs
	defer func() { interfaceAddrs = orig }()
	interfaceAddrs = func() ([]net.Addr, error) { return addrList(t, "100.106.17.33/32"), nil }

	cases := []struct {
		name, cfg, want string
		wantErr         bool
	}{
		{"sentinel -> discovered ip:port", "tailnet:7878", "100.106.17.33:7878", false},
		{"explicit tailnet ip passthrough", "100.106.17.33:7878", "100.106.17.33:7878", false},
		{"explicit loopback passthrough", "127.0.0.1:9000", "127.0.0.1:9000", false},
		{"hostname passthrough (net.Listen resolves it)", "mymain:7878", "mymain:7878", false},
		{"malformed -> loud", "no-port", "", true},
	}
	for _, c := range cases {
		d := &Daemon{cfg: config.Config{APIAddr: c.cfg}}
		got, err := d.resolveAPIBindAddr()
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: want error, got %q", c.name, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got (%q, %v), want %q", c.name, got, err, c.want)
		}
	}

	// Sentinel with no tailnet interface surfaces the discovery error (retried by
	// the caller), never a silent wrong bind.
	interfaceAddrs = func() ([]net.Addr, error) { return addrList(t, "192.168.1.5/24"), nil }
	d := &Daemon{cfg: config.Config{APIAddr: "tailnet:7878"}}
	if _, err := d.resolveAPIBindAddr(); err == nil {
		t.Errorf("sentinel with no tailnet interface should error, not bind a LAN address")
	}
}

// TestOffTailnetBind pins the exposure warning predicate: 0.0.0.0/:: and a
// concrete non-tailnet IP warn; a tailnet IP and loopback do not.
func TestOffTailnetBind(t *testing.T) {
	cases := []struct {
		addr string
		warn bool
	}{
		{"0.0.0.0:7878", true},
		{"[::]:7878", true},
		{"192.168.1.5:7878", true},
		{"77.42.21.223:7878", true},
		{"100.106.17.33:7878", false},
		{"127.0.0.1:7878", false},
		{"[::1]:7878", false},
		{"mymain:7878", false}, // a hostname is not classified here
	}
	for _, c := range cases {
		got := offTailnetBind(c.addr) != ""
		if got != c.warn {
			t.Errorf("offTailnetBind(%q) warned=%v, want %v (reason %q)", c.addr, got, c.warn, offTailnetBind(c.addr))
		}
	}
}

// TestNoAPIWarning pins the loud warning for a daemon running with no TCP API.
// A configured API is silent; an unconfigured one names the consequence peers see
// AND the fix; and a daemon started from inside a sesh thread pane (the hand-start
// that stripped SESH_API_ADDR on mymain) is called out by thread id.
func TestNoAPIWarning(t *testing.T) {
	if got := noAPIWarning("tailnet:7878", ""); got != "" {
		t.Errorf("configured api should warn nothing, got %q", got)
	}
	if got := noAPIWarning("tailnet:7878", "tid-1"); got != "" {
		t.Errorf("configured api should warn nothing even from a thread pane, got %q", got)
	}

	base := noAPIWarning("", "")
	for _, want := range []string{"SESH_API_ADDR", "CANNOT REACH IT", "supervisorctl restart sesh-daemon", "termux"} {
		if !strings.Contains(base, want) {
			t.Errorf("unconfigured-api warning missing %q: %s", want, base)
		}
	}
	if strings.Contains(base, "sesh thread ") {
		t.Errorf("no SESH_THREAD_ID in env must not claim a thread-pane start: %s", base)
	}

	hand := noAPIWarning("", "53c1a2e3-e0f9-4544-86c7-1394eb8ee222")
	if !strings.Contains(hand, "53c1a2e3-e0f9-4544-86c7-1394eb8ee222") {
		t.Errorf("thread-pane start must name the thread id: %s", hand)
	}
}
