package main

import (
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/config"
)

// daemonClient returns the client every command uses to reach a daemon. By default
// it dials the LOCAL unix socket; when SESH_REMOTE is set it targets that REMOTE
// daemon's TCP API (with SESH_API_TOKEN) instead — so the same CLI drives a remote
// daemon directly, the same parity a mobile/Obsidian client gets.
func daemonClient(cfg config.Config) *client.Client {
	if cfg.RemoteAddr != "" {
		return client.NewRemote(cfg.RemoteAddr, cfg.RemoteToken)
	}
	return client.New(cfg.SocketPath())
}
