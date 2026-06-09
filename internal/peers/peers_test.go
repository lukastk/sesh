package peers

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestPeerTransport: a peer with an ApiAddr is reached over http; without, ssh.
func TestPeerTransport(t *testing.T) {
	if got := (Peer{SSH: "localhost"}).Transport(); got != "ssh" {
		t.Errorf("no api_addr: Transport() = %q, want ssh", got)
	}
	if got := (Peer{SSH: "localhost", ApiAddr: "100.1.2.3:7373"}).Transport(); got != "http" {
		t.Errorf("with api_addr: Transport() = %q, want http", got)
	}
}

// TestResolveAPIToken: literal wins; else read the file; else a LOUD error (never a
// silent empty token for an http peer).
func TestResolveAPIToken(t *testing.T) {
	if tok, err := (Peer{ApiToken: "lit"}).ResolveAPIToken(); err != nil || tok != "lit" {
		t.Errorf("literal: got %q,%v want lit,nil", tok, err)
	}

	f := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(f, []byte("  filetoken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, err := (Peer{ApiTokenFile: f}).ResolveAPIToken(); err != nil || tok != "filetoken" {
		t.Errorf("file: got %q,%v want filetoken,nil", tok, err)
	}

	// api_addr but no token anywhere => loud error.
	if _, err := (Peer{Machine: "m", ApiAddr: "x:1"}).ResolveAPIToken(); err == nil {
		t.Errorf("missing token: expected a loud error, got nil")
	}
	// empty token file => loud error (not a silent empty token).
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Peer{Machine: "m", ApiTokenFile: empty}).ResolveAPIToken(); err == nil {
		t.Errorf("empty token file: expected a loud error, got nil")
	}
}

// TestPeerAPIRoundTrip ensures the http-transport fields survive Add -> Save -> Load.
func TestPeerAPIRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	reg, _ := Load(path)
	if err := reg.Add(Peer{Machine: "mac", SSH: "lukas@mac", Home: "/h", Binary: "sesh", ApiAddr: "100.9.9.9:7373", ApiTokenFile: "/run/tok"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := Load(path)
	p, ok := reloaded.Get("mac")
	if !ok {
		t.Fatal("peer not found after reload")
	}
	if p.ApiAddr != "100.9.9.9:7373" || p.ApiTokenFile != "/run/tok" || p.Transport() != "http" {
		t.Errorf("api fields lost in round-trip: %+v (transport=%s)", p, p.Transport())
	}
}

func TestPeerSSHArgs(t *testing.T) {
	cases := []struct {
		port string
		want []string
	}{
		{"", nil},   // default port => no -p
		{"22", nil}, // explicit default => no -p
		{"8022", []string{"-p", "8022"}},
	}
	for _, c := range cases {
		got := Peer{SSH: "lukas@host", Port: c.port}.SSHArgs()
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Port=%q: SSHArgs() = %v, want %v", c.port, got, c.want)
		}
	}
}

// TestPeerPortRoundTrip ensures the port survives Add -> Save -> Load (so routing
// to a non-22 machine actually reaches it).
func TestPeerPortRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	reg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(Peer{Machine: "android", SSH: "lukas@android-main", Port: "8022", Home: "/data/sesh", Binary: "sesh"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := reloaded.Get("android")
	if !ok {
		t.Fatal("peer not found after reload")
	}
	if p.Port != "8022" {
		t.Errorf("port = %q after round-trip, want 8022", p.Port)
	}
	if got := p.SSHArgs(); !reflect.DeepEqual(got, []string{"-p", "8022"}) {
		t.Errorf("reloaded SSHArgs() = %v, want [-p 8022]", got)
	}
}
