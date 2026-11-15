package config

import (
	"os"
	"testing"
	"time"
)

func writeSendConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(ConfigPath(home), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestLoadSendDefaults(t *testing.T) {
	got, err := LoadSend(t.TempDir()) // no file at all
	if err != nil {
		t.Fatal(err)
	}
	if got.RespectTyping != DefaultRespectTyping || got.RespectTypingDeadline != DefaultRespectTypingDeadline {
		t.Fatalf("defaults: %+v", got)
	}
	got, err = LoadSend(writeSendConfig(t, "[tui]\ncolumns = [\"name\"]\n")) // file without [send]
	if err != nil || got.RespectTyping != DefaultRespectTyping {
		t.Fatalf("missing table: %+v %v", got, err)
	}
}

func TestLoadSendValues(t *testing.T) {
	got, err := LoadSend(writeSendConfig(t, "[send]\nrespect_typing = \"0s\"\nrespect_typing_deadline = \"2m\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got.RespectTyping != 0 || got.RespectTypingDeadline != 2*time.Minute {
		t.Fatalf("values: %+v", got)
	}
}

// TestLoadSendBrowserWindow: browser_respect_typing defaults to respect_typing (with
// or without a file), and may be set on its own, e.g. "0s" so a UI user's own
// messages never wait while agent deliveries keep the guard.
func TestLoadSendBrowserWindow(t *testing.T) {
	cases := []struct {
		body              string
		window, browserWd time.Duration
	}{
		{"", DefaultRespectTyping, DefaultRespectTyping},
		{"[send]\nrespect_typing = \"30s\"\n", 30 * time.Second, 30 * time.Second},
		{"[send]\nbrowser_respect_typing = \"0s\"\n", DefaultRespectTyping, 0},
		{"[send]\nrespect_typing = \"0s\"\nbrowser_respect_typing = \"5s\"\n", 0, 5 * time.Second},
	}
	for _, c := range cases {
		home := t.TempDir()
		if c.body != "" {
			home = writeSendConfig(t, c.body)
		}
		got, err := LoadSend(home)
		if err != nil || got.RespectTyping != c.window || got.BrowserRespectTyping != c.browserWd {
			t.Errorf("%q: got %+v %v, want window %s browser %s", c.body, got, err, c.window, c.browserWd)
		}
	}
	if _, err := LoadSend(writeSendConfig(t, "[send]\nbrowser_respect_typing = \"-1s\"\n")); err == nil {
		t.Error("a negative browser_respect_typing must be refused loudly")
	}
}

func TestLoadSendLoud(t *testing.T) {
	for _, body := range []string{
		"[send]\nrespect_typing = \"soon\"\n",
		"[send]\nrespect_typing = \"-5s\"\n",
		"[send]\nrespect_typing_deadline = \"0s\"\n",
	} {
		if _, err := LoadSend(writeSendConfig(t, body)); err == nil {
			t.Errorf("config %q must be refused loudly", body)
		}
	}
}

func TestLoadSchedules(t *testing.T) {
	got, err := LoadSchedules(t.TempDir())
	if err != nil || !got.Enabled || got.DisableAfterFailures != DefaultScheduleFailureBreaker {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	got, err = LoadSchedules(writeSendConfig(t, "[schedules]\nenabled = false\ndisable_after_failures = 0\n"))
	if err != nil || got.Enabled || got.DisableAfterFailures != 0 {
		t.Fatalf("values: %+v %v", got, err)
	}
	if _, err := LoadSchedules(writeSendConfig(t, "[schedules]\ndisable_after_failures = -1\n")); err == nil {
		t.Fatal("negative breaker must be refused")
	}
}
