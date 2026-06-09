package main

import (
	"errors"
	"flag"
	"fmt"

	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/peers"
)

// runPeer implements `sesh peer <add|list>` — the local mesh registry.
func runPeer(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sesh peer <add|list>")
	}
	cfg := config.Load()
	switch args[0] {
	case "add":
		return peerAdd(cfg, args[1:])
	case "list":
		return peerList(cfg)
	default:
		return fmt.Errorf("unknown peer subcommand %q", args[0])
	}
}

func peerAdd(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	machine := fs.String("machine", "", "remote machine identity (required)")
	ssh := fs.String("ssh", "", "ssh destination, e.g. user@host (required)")
	port := fs.String("port", "", "ssh port (default 22)")
	home := fs.String("home", "", "remote SESH_HOME (required)")
	binary := fs.String("binary", "sesh", "path to sesh on the remote machine")
	tmuxSocket := fs.String("tmux-socket", "", "remote mytmux socket name (for tmux nav)")
	apiAddr := fs.String("api-addr", "", "peer daemon's TCP API addr (host:port); set => reach it over HTTP instead of ssh")
	apiToken := fs.String("api-token", "", "bearer token for the peer's TCP API (literal; prefer --api-token-file)")
	apiTokenFile := fs.String("api-token-file", "", "path to a file holding the peer's TCP API bearer token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *machine == "" || *ssh == "" || *home == "" {
		return errors.New("peer add: --machine, --ssh and --home are required")
	}
	// HTTP transport requires a token (loud — never an unauthenticated peer call).
	if *apiAddr != "" && *apiToken == "" && *apiTokenFile == "" {
		return errors.New("peer add: --api-addr requires --api-token or --api-token-file")
	}
	if err := cfg.EnsureHome(); err != nil {
		return err
	}
	reg, err := peers.Load(cfg.PeersPath())
	if err != nil {
		return err
	}
	if err := reg.Add(peers.Peer{
		Machine: *machine, SSH: *ssh, Port: *port, Home: *home, Binary: *binary, TmuxSocket: *tmuxSocket,
		ApiAddr: *apiAddr, ApiToken: *apiToken, ApiTokenFile: *apiTokenFile,
	}); err != nil {
		return err
	}
	if err := reg.Save(cfg.PeersPath()); err != nil {
		return err
	}
	fmt.Printf("added peer %s (%s)\n", *machine, *ssh)
	return nil
}

func peerList(cfg config.Config) error {
	reg, err := peers.Load(cfg.PeersPath())
	if err != nil {
		return err
	}
	for _, p := range reg.List() {
		// transport is explicit per peer: http if it has a TCP API, else ssh.
		target := p.SSH
		if p.Transport() == "http" {
			target = p.ApiAddr
		}
		fmt.Printf("%s\t%s\t%s\t%s\n", p.Machine, p.Transport(), target, p.Home)
	}
	return nil
}
