package main

// `sesh backup` / `sesh restore` / `sesh copy` (PARITY_ROADMAP D4 + D2, v1's
// verbs): idempotent sha256 transcript backups into one portable SQLite file,
// reconstruction --to-dir (every agent) or --native (claude — deterministic
// path; pi/codex are reported Unsupported, never silently skipped), and copy =
// backup+restore composed (cross-machine via the staged-file primitive).

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/backup"
	"github.com/lukastk/sesh/internal/config"
)

func runBackup(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	to := fs.String("to", "", "backup SQLite file to write/update (required)")
	id := fs.String("id", "", "back up only this thread (default: every local thread, archived included)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *to == "" {
		return errors.New("backup: --to <file> is required")
	}
	c := daemonClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var threads []api.Thread
	if *id != "" {
		rid, err := resolveThreadID(cfg, *id)
		if err != nil {
			return err
		}
		resp, err := c.ThreadList(ctx, true, false)
		if err != nil {
			return err
		}
		for _, th := range resp.Threads {
			if th.ID == rid {
				threads = []api.Thread{th}
			}
		}
	} else {
		resp, err := c.ThreadList(ctx, true, false)
		if err != nil {
			return err
		}
		threads = resp.Threads
	}
	db, err := backup.Open(*to)
	if err != nil {
		return err
	}
	defer db.Close()
	res, err := backup.Backup(db, threads, agents.ResolveHomes(cfg.CodexHome), time.Now().UnixNano())
	if err != nil {
		return err
	}
	fmt.Printf("backed up: %d written, %d unchanged, %d without a transcript\n", res.Written, res.Skipped, res.Missing)
	return nil
}

func runRestore(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	from := fs.String("from", "", "backup SQLite file (required)")
	id := fs.String("id", "", "restore only this thread id/prefix (default: everything)")
	toDir := fs.String("to-dir", "", "write <thread-id>.jsonl files into this dir")
	native := fs.Bool("native", false, "reconstruct at the agent's real location (claude only)")
	rewriteCwd := fs.String("rewrite-cwd", "", "rewrite the cwd for the native path (cross-cwd recovery)")
	force := fs.Bool("force", false, "overwrite existing files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" {
		return errors.New("restore: --from <file> is required")
	}
	if (*toDir == "") == !*native {
		return errors.New("restore: exactly one of --to-dir or --native is required")
	}
	db, err := backup.OpenExisting(*from)
	if err != nil {
		return err
	}
	defer db.Close()
	rid, err := backup.ResolveID(db, *id)
	if err != nil {
		return err
	}
	res, err := backup.Restore(db, rid, backup.RestoreTarget{
		Native: *native, Dir: *toDir,
		Homes: agents.ResolveHomes(cfg.CodexHome), RewriteCwd: *rewriteCwd, Force: *force,
	})
	if err != nil {
		return err
	}
	fmt.Printf("restored: %d written, %d skipped (exist; --force overwrites)\n", res.Written, res.Skipped)
	if len(res.Unsupported) > 0 {
		fmt.Printf("NOT natively restorable (nondeterministic path — use --to-dir): %s\n", strings.Join(res.Unsupported, ", "))
	}
	return nil
}

func runCopy(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("copy", flag.ContinueOnError)
	toDir := fs.String("to-dir", "", "local: copy the transcript into this dir")
	toMachine := fs.String("to", "", "remote: ship to this peer and restore natively (claude only)")
	ref := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		ref, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if ref == "" && fs.NArg() == 1 {
		ref = fs.Arg(0)
	}
	if (*toDir == "") == (*toMachine == "") {
		return errors.New("copy: exactly one of --to-dir <dir> or --to <machine> is required")
	}
	rid, err := resolveThreadID(cfg, ref)
	if err != nil {
		return err
	}
	c := daemonClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := c.ThreadList(ctx, true, false)
	if err != nil {
		return err
	}
	var th *api.Thread
	for i := range resp.Threads {
		if resp.Threads[i].ID == rid {
			th = &resp.Threads[i]
		}
	}
	if th == nil {
		return fmt.Errorf("copy: thread %s is not local (copy runs on the owner; use --machine)", rid)
	}
	// Cross-machine restore is NATIVE on the peer — only deterministic paths
	// (claude) can land; refuse loudly rather than shipping a backup that
	// restores zero files and reporting a false success (v1's exact guard).
	if *toMachine != "" && !backup.NativeRestorable(th.AgentKind) {
		return fmt.Errorf("cannot copy %s thread %s cross-machine: its native transcript path is nondeterministic; copy claude threads, or use --to-dir locally", th.AgentKind, rid)
	}

	tmp := filepath.Join(os.TempDir(), "sesh-copy-"+rid+".sqlite")
	defer os.Remove(tmp)
	db, err := backup.Open(tmp)
	if err != nil {
		return err
	}
	homes := agents.ResolveHomes(cfg.CodexHome)
	if res, err := backup.Backup(db, []api.Thread{*th}, homes, time.Now().UnixNano()); err != nil {
		db.Close()
		return err
	} else if res.Written+res.Skipped == 0 {
		db.Close()
		return fmt.Errorf("copy: thread %s has no transcript on disk", rid)
	}
	db.Close()

	if *toDir != "" {
		db2, err := backup.OpenExisting(tmp)
		if err != nil {
			return err
		}
		defer db2.Close()
		res, err := backup.Restore(db2, rid, backup.RestoreTarget{Dir: *toDir, Force: true})
		if err != nil {
			return err
		}
		fmt.Printf("copied %s transcript -> %s (%d file)\n", rid, *toDir, res.Written)
		return nil
	}

	// Cross-machine: stage the backup file onto the peer (the file-shipping
	// primitive), then run the native restore THERE via the routed CLI.
	staged, err := stageFileTo(cfg, *toMachine, tmp)
	if err != nil {
		return err
	}
	out, err := runRouted(cfg, *toMachine, "restore", "--from", staged, "--id", rid, "--native", "--force")
	if err != nil {
		return fmt.Errorf("copy: remote restore on %s: %w: %s", *toMachine, err, out)
	}
	fmt.Printf("copied %s transcript -> %s (native)\n", rid, *toMachine)
	return nil
}

// stageFileTo ships a local file onto a peer via the staged-file primitive
// (the same path mt-send-clipboard uses) and returns the REMOTE staged path.
func stageFileTo(cfg config.Config, machine, localPath string) (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	out, err := execCapture(bin, "tmux", "stage-file", "--to", machine, localPath)
	if err != nil {
		return "", fmt.Errorf("stage-file to %s: %w: %s", machine, err, out)
	}
	staged := strings.TrimSpace(out)
	if staged == "" {
		return "", fmt.Errorf("stage-file to %s returned no path", machine)
	}
	return staged, nil
}

// runRouted re-execs this binary with --machine appended (the global router).
func runRouted(cfg config.Config, machine string, args ...string) (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	return execCapture(bin, append(args, "--machine", machine)...)
}
