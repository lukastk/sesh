package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const maxBrowserAPIOrigins = 32

// BrowserAPIConfig is the exact-origin CORS allowlist for the narrow browser
// HTTP surface. An empty map is the secure default: no browser origin receives
// CORS response headers or unauthenticated preflight handling.
type BrowserAPIConfig struct {
	AllowedOrigins map[string]bool
}

type browserAPIFileTable struct {
	AllowedOrigins []string `toml:"allowed_origins"`
}

type browserAPIConfigFile struct {
	BrowserAPI *browserAPIFileTable `toml:"browser_api"`
}

// LoadBrowserAPI reads [browser_api] from <SESH_HOME>/config.toml. Origins are
// exact configured authorities, not patterns: wildcards, paths, credentials and
// opaque/null origins are refused rather than normalized or reflected.
func LoadBrowserAPI(home string) (BrowserAPIConfig, error) {
	out := BrowserAPIConfig{AllowedOrigins: map[string]bool{}}
	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, fmt.Errorf("config: read %s: %w", ConfigPath(home), err)
	}
	var f browserAPIConfigFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return out, fmt.Errorf("config: parse %s: %w", ConfigPath(home), err)
	}
	if f.BrowserAPI == nil {
		return out, nil
	}
	if len(f.BrowserAPI.AllowedOrigins) > maxBrowserAPIOrigins {
		return out, fmt.Errorf("config: [browser_api] allowed_origins exceeds %d entries", maxBrowserAPIOrigins)
	}
	for _, origin := range f.BrowserAPI.AllowedOrigins {
		if err := validateBrowserAPIOrigin(origin); err != nil {
			return out, fmt.Errorf("config: [browser_api] allowed_origins contains %q: %w", origin, err)
		}
		if out.AllowedOrigins[origin] {
			return out, fmt.Errorf("config: [browser_api] allowed_origins contains duplicate %q", origin)
		}
		out.AllowedOrigins[origin] = true
	}
	return out, nil
}

func validateBrowserAPIOrigin(origin string) error {
	if origin == "" || origin != strings.TrimSpace(origin) || strings.Contains(origin, "*") || origin == "null" || len(origin) > 512 {
		return fmt.Errorf("must be a non-empty exact origin of at most 512 bytes (not wildcard/null/whitespace)")
	}
	u, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("invalid URL origin: %w", err)
	}
	if u.Scheme != "app" && u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be app, http, or https")
	}
	if u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return fmt.Errorf("must contain only scheme and host, without userinfo/path/query/fragment")
	}
	if strings.ContainsAny(u.Host, "\r\n") {
		return fmt.Errorf("host contains control characters")
	}
	return nil
}

// MatchAllowedOrigin returns the configured allowlist value that exactly matches
// requestOrigin. Callers use the returned configured string in response headers
// rather than reflecting an unchecked request value.
func (c BrowserAPIConfig) MatchAllowedOrigin(requestOrigin string) (string, bool) {
	for configured := range c.AllowedOrigins {
		if configured == requestOrigin {
			return configured, true
		}
	}
	return "", false
}

// AllowedOriginNames returns the exact allowlist in deterministic order.
func (c BrowserAPIConfig) AllowedOriginNames() []string {
	out := make([]string, 0, len(c.AllowedOrigins))
	for origin := range c.AllowedOrigins {
		out = append(out, origin)
	}
	sort.Strings(out)
	return out
}
