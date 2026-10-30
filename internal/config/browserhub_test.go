package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadBrowserHubDisabledByDefaultAndExplicitAllowlist(t *testing.T) {
	home := t.TempDir()
	cfg, err := LoadBrowserHub(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || len(cfg.TrustedPeers) != 0 {
		t.Fatalf("default browser hub = %#v, want disabled/empty", cfg)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[browser_hub]\nenabled = true\ntrusted_peers = [\"peer-b\", \"peer-a\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadBrowserHub(home)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || !cfg.TrustedPeers["peer-a"] || !cfg.TrustedPeers["peer-b"] {
		t.Fatalf("loaded browser hub = %#v", cfg)
	}
	if got := strings.Join(cfg.TrustedPeerNames(), ","); got != "peer-a,peer-b" {
		t.Fatalf("trusted names = %q", got)
	}
}

func TestLoadBrowserHubRefusesImplicitOrAmbiguousPolicy(t *testing.T) {
	for _, body := range []string{
		"[browser_hub]\ntrusted_peers = [\"peer\"]\n",
		"[browser_hub]\nenabled = true\ntrusted_peers = [\"peer\", \"peer\"]\n",
		"[browser_hub]\nenabled = true\ntrusted_peers = [\"\"]\n",
	} {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBrowserHub(home); err == nil {
			t.Fatalf("LoadBrowserHub accepted unsafe config:\n%s", body)
		}
	}
}
