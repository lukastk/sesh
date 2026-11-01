package main

import "testing"

// `--machine` has two very different implementations (route.go): ssh moves the
// command to the peer, http keeps it in THIS process and only redirects
// daemonClient through the local daemon's route proxy. A command whose work is
// client-side filesystem work is therefore correct only on the ssh path — on the
// http path it resolves the target machine's paths against the caller's disk.
// This test pins which side of that line each command is on, since every peer
// with an api_addr takes the http path and the failure is silent-ish (a missing
// file on a machine where the file plainly exists).
func TestHTTPRoutableKeepsFilesystemWorkOnSSH(t *testing.T) {
	filesystemLocal := [][]string{
		{"backup", "--to", "b.sqlite"},
		{"restore", "--from", "b.sqlite", "--native"},
		{"copy", "1a2b3c4d", "--to", "macbook"},
		{"import", "--from-v1", "~/.sesh"},
		{"daemon", "restart"},
		{"tmux", "nav", "--to", "macbook:sesh_x"},
		{"tmux", "stage-file", "--to", "macbook", "/tmp/f"},
	}
	for _, args := range filesystemLocal {
		if httpRoutable(args) {
			t.Errorf("%v: routed over http, but it needs remote EXECUTION (ssh)", args)
		}
	}

	// Client-only calls the peer's daemon serves identically — and the two verbs
	// that read a local file on PURPOSE and send the bytes to the peer's daemon
	// (blob put, schedule --prompt-file), which ssh would break.
	clientOnly := [][]string{
		{"thread", "list"},
		{"thread", "new", "--agent", "claude", "--cwd", "/tmp"},
		{"thread", "send", "--id", "1a2b3c4d", "--text", "hi"},
		{"blob", "put", "/tmp/local-file"},
		{"schedule", "add", "--prompt-file", "/tmp/prompt.md"},
		{"fs", "list", "--path", "~/dev"},
		{"mesh"},
		{"doctor"},
		{"tmux", "info"},
		{"tmux", "master-current", "--origin", "mymain"},
	}
	for _, args := range clientOnly {
		if !httpRoutable(args) {
			t.Errorf("%v: forced onto ssh, but the peer's daemon serves it over http", args)
		}
	}

	if httpRoutable(nil) {
		t.Error("no subcommand must not be http-routable")
	}
}
