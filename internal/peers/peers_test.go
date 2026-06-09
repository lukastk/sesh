package peers

import (
	"path/filepath"
	"reflect"
	"testing"
)

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
