package main

import (
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/config"
)

// daemonClient returns the client every command uses to reach a daemon. By default
// it dials the LOCAL unix socket; when SESH_REMOTE is set it targets that REMOTE
// daemon's TCP API (with SESH_API_TOKEN) instead — so the same CLI drives a remote
// daemon directly, the same parity a mobile/Obsidian client gets. When the
// `--machine` router has pointed this process at an http PEER (SESH_ROUTE_MACHINE,
// schema 51) it reaches that peer THROUGH the local daemon's /v1/route proxy, which
// reuses the daemon's warm connection instead of dialing cold from a fresh process.
func daemonClient(cfg config.Config) *client.Client {
	if cfg.RemoteAddr != "" {
		return client.NewRemote(cfg.RemoteAddr, cfg.RemoteToken)
	}
	if cfg.RouteMachine != "" {
		return client.NewRouted(cfg.SocketPath(), cfg.RouteMachine)
	}
	return client.New(cfg.SocketPath())
}
