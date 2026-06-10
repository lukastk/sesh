package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/daemon"
)

// runDaemon implements `sesh daemon <run|start|stop|status>`.
//
//	run     foreground server loop (what `start` spawns; used directly by tests)
//	start   spawn `run` detached, wait until it answers health
//	stop    ask the running daemon to shut down, wait until the socket is gone
//	status  print the running daemon's status (--json for machine-readable)
func runDaemon(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sesh daemon <run|start|stop|status>")
	}
	cfg := config.Load()
	sub, rest := args[0], args[1:]
	switch sub {
	case "run":
		return daemonRun(cfg)
	case "start":
		return daemonStart(cfg)
	case "restart":
		// stop-then-start; "wasn't running" is a legitimate restart precondition
		// (printed, not hidden), any other stop failure aborts loudly.
		if err := daemonStop(cfg); err != nil {
			if !strings.Contains(err.Error(), "no daemon running") {
				return err
			}
			fmt.Println("(daemon was not running)")
		}
		return daemonStart(cfg)
	case "stop":
		return daemonStop(cfg)
	case "status":
		return daemonStatus(cfg, rest)
	default:
		return fmt.Errorf("unknown daemon subcommand %q (want: run|start|stop|status)", sub)
	}
}

func daemonRun(cfg config.Config) error {
	d, err := daemon.New(cfg)
	if err != nil {
		return err
	}

	// Translate SIGINT/SIGTERM into a graceful shutdown.
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigc
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.Shutdown(ctx)
	}()

	return d.Serve()
}

func daemonStart(cfg config.Config) error {
	if err := cfg.EnsureHome(); err != nil {
		return err
	}
	c := daemonClient(cfg)
	if err := c.Health(context.Background()); err == nil {
		return fmt.Errorf("daemon already running on %s", cfg.SocketPath())
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own executable: %w", err)
	}
	logPath := filepath.Join(cfg.Home, "daemon.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer logf.Close()

	cmd := exec.Command(exe, "daemon", "run")
	cmd.Env = os.Environ() // inherit SESH_HOME/SESH_MACHINE for an identical config
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach into its own session
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn daemon: %w", err)
	}
	_ = cmd.Process.Release()

	// Wait until it answers, or the log explains why it died.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := c.Health(context.Background()); err == nil {
			fmt.Printf("daemon started on %s (machine=%s)\n", cfg.SocketPath(), cfg.Machine)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	out, _ := os.ReadFile(logPath)
	return fmt.Errorf("daemon did not become healthy within 10s; log tail:\n%s", tail(out, 2000))
}

func daemonStop(cfg config.Config) error {
	c := daemonClient(cfg)
	if err := c.Health(context.Background()); err != nil {
		return fmt.Errorf("no daemon running on %s", cfg.SocketPath())
	}
	if err := c.Shutdown(context.Background()); err != nil {
		return fmt.Errorf("request shutdown: %w", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := c.Health(context.Background()); err != nil {
			fmt.Println("daemon stopped")
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("daemon did not stop within 10s")
}

func daemonStatus(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := daemonClient(cfg)
	st, err := c.Status(context.Background())
	if err != nil {
		return fmt.Errorf("no daemon running on %s (%w)", cfg.SocketPath(), err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	fmt.Printf("machine:        %s\n", st.Machine)
	fmt.Printf("pid:            %d\n", st.PID)
	fmt.Printf("version:        %s\n", st.Version)
	fmt.Printf("uptime:         %ds\n", st.UptimeSeconds)
	fmt.Printf("db:             %s\n", st.DBPath)
	fmt.Printf("socket:         %s\n", st.SocketPath)
	fmt.Printf("schema_version: %d\n", st.SchemaVersion)
	return nil
}

func tail(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return "..." + string(b[len(b)-n:])
}
