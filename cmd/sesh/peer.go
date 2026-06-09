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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *machine == "" || *ssh == "" || *home == "" {
		return errors.New("peer add: --machine, --ssh and --home are required")
	}
	if err := cfg.EnsureHome(); err != nil {
		return err
	}
	reg, err := peers.Load(cfg.PeersPath())
	if err != nil {
		return err
	}
	if err := reg.Add(peers.Peer{Machine: *machine, SSH: *ssh, Port: *port, Home: *home, Binary: *binary, TmuxSocket: *tmuxSocket}); err != nil {
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
		fmt.Printf("%s\t%s\t%s\n", p.Machine, p.SSH, p.Home)
	}
	return nil
}
