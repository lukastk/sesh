package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestLoadBrowserAPIDefaultsToNoCORSOrigins(t *testing.T) {
	cfg, err := LoadBrowserAPI(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedOrigins) != 0 {
		t.Fatalf("default browser API CORS = %#v, want empty", cfg)
	}
}

func TestLoadBrowserAPIExactOrigins(t *testing.T) {
	home := t.TempDir()
	body := "[browser_api]\nallowed_origins = [\"https://example.test:8443\", \"app://obsidian.md\"]\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadBrowserAPI(home)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"app://obsidian.md", "https://example.test:8443"}
	if got := cfg.AllowedOriginNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("allowed origins = %#v, want %#v", got, want)
	}
}

func TestLoadBrowserAPIRefusesBroadOrAmbiguousOrigins(t *testing.T) {
	bad := []string{
		"*", "null", "", " app://obsidian.md", "app://obsidian.md/", "app://user@obsidian.md", "https://*.example.test",
		"app://obsidian.md/path", "app://obsidian.md?query=1", "app://obsidian.md#fragment",
		"file://local", "javascript://obsidian.md", "app:obsidian.md", "app://:123", "https://" + strings.Repeat("a", 505) + ".test",
	}
	for _, origin := range bad {
		home := t.TempDir()
		body := "[browser_api]\nallowed_origins = [" + tomlString(origin) + "]\n"
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBrowserAPI(home); err == nil {
			t.Fatalf("LoadBrowserAPI accepted unsafe origin %q", origin)
		}
	}

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[browser_api]\nallowed_origins = [\"app://obsidian.md\", \"app://obsidian.md\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBrowserAPI(home); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate origin error = %v", err)
	}
}

func TestLoadBrowserAPIBoundsOriginCount(t *testing.T) {
	home := t.TempDir()
	origins := make([]string, 0, maxBrowserAPIOrigins+1)
	for i := 0; i <= maxBrowserAPIOrigins; i++ {
		origins = append(origins, tomlString("https://origin-"+strconv.Itoa(i)+".example.test"))
	}
	body := "[browser_api]\nallowed_origins = [" + strings.Join(origins, ",") + "]\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBrowserAPI(home); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("origin-count bound error = %v", err)
	}
}

func tomlString(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return "\"" + value + "\""
}
