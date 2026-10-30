package config

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/BurntSushi/toml"
)

// BrowserHubConfig is the explicit operator opt-in for cross-peer browser
// operations. Disabled means browser endpoints may address only this daemon's
// own threads. TrustedPeers is an allowlist, not a discovery mechanism: every
// named machine must also exist in peers.json and expose the HTTP transport.
type BrowserHubConfig struct {
	Enabled      bool
	TrustedPeers map[string]bool
}

type browserHubFileTable struct {
	Enabled      *bool    `toml:"enabled"`
	TrustedPeers []string `toml:"trusted_peers"`
}

type browserHubConfigFile struct {
	BrowserHub *browserHubFileTable `toml:"browser_hub"`
}

// LoadBrowserHub reads [browser_hub] from <SESH_HOME>/config.toml. Cross-peer
// access is disabled by default. A malformed or ambiguous table is loud: a
// security boundary must never silently fall back to a broader policy.
func LoadBrowserHub(home string) (BrowserHubConfig, error) {
	out := BrowserHubConfig{TrustedPeers: map[string]bool{}}
	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, fmt.Errorf("config: read %s: %w", ConfigPath(home), err)
	}
	var f browserHubConfigFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return out, fmt.Errorf("config: parse %s: %w", ConfigPath(home), err)
	}
	if f.BrowserHub == nil {
		return out, nil
	}
	if f.BrowserHub.Enabled != nil {
		out.Enabled = *f.BrowserHub.Enabled
	}
	for _, machine := range f.BrowserHub.TrustedPeers {
		if machine == "" {
			return out, fmt.Errorf("config: [browser_hub] trusted_peers contains an empty machine name")
		}
		if out.TrustedPeers[machine] {
			return out, fmt.Errorf("config: [browser_hub] trusted_peers contains duplicate %q", machine)
		}
		out.TrustedPeers[machine] = true
	}
	if !out.Enabled && len(out.TrustedPeers) > 0 {
		return out, fmt.Errorf("config: [browser_hub] trusted_peers is set while enabled=false — remove the allowlist or opt in explicitly")
	}
	return out, nil
}

// TrustedPeerNames returns the configured allowlist in deterministic order.
func (c BrowserHubConfig) TrustedPeerNames() []string {
	out := make([]string, 0, len(c.TrustedPeers))
	for machine := range c.TrustedPeers {
		out = append(out, machine)
	}
	sort.Strings(out)
	return out
}
