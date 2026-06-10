package daemon

// The hook runner (v1's internal/hooks ported): matches observed events
// against the configured [[hooks]] and execs the matching, non-muted ones.
// Commands run through $SHELL -c — the deploy-env parity doctrine: a hook
// sees exactly the environment a pane command would (zshenv PATH, keys).
// A failing hook is LOUD in the daemon log but never fatal and never blocks
// other hooks (each runs in its own goroutine under a timeout).

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lukastk/sesh/internal/config"
)

const hookTimeout = 15 * time.Second

type hookRunner struct {
	hooks   []config.Hook
	timeout time.Duration

	mu    sync.RWMutex
	muted map[string]bool

	// runCmd is overridable in tests.
	runCmd func(ctx context.Context, h config.Hook, env map[string]string) (string, error)
}

func newHookRunner(hooks []config.Hook, muted map[string]bool) *hookRunner {
	m := map[string]bool{}
	for k, v := range muted {
		if v {
			m[k] = true
		}
	}
	return &hookRunner{hooks: hooks, timeout: hookTimeout, muted: m, runCmd: hookShellRun}
}

// hookMatches reports whether hook h selects event e.
func hookMatches(h config.Hook, e Event) bool {
	if h.Event != e.Type {
		return false
	}
	if h.From != "" && h.From != e.From {
		return false
	}
	if h.To != "" && h.To != e.To {
		return false
	}
	if h.Agent != "" && h.Agent != e.Snap.AgentKind {
		return false
	}
	if h.Machine != "" && h.Machine != e.Snap.Machine {
		return false
	}
	if h.Tag != "" {
		has := false
		for _, t := range e.Snap.Tags {
			if t == h.Tag {
				has = true
			}
		}
		if !has {
			return false
		}
	}
	return true
}

// handle dispatches an event to every matching, non-muted hook (async).
func (r *hookRunner) handle(e Event) {
	r.mu.RLock()
	var run []config.Hook
	for _, h := range r.hooks {
		if !r.muted[h.Name] && hookMatches(h, e) {
			run = append(run, h)
		}
	}
	r.mu.RUnlock()
	for _, h := range run {
		hook, env := h, e.Env()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
			defer cancel()
			out, err := r.runCmd(ctx, hook, env)
			if err != nil {
				// Loud (no-silent-failures) but never fatal: one bad hook can't
				// take the daemon down or block the others.
				log.Printf("hook %q failed: %v%s", hook.Name, err, hookTrailing(out))
			}
		}()
	}
}

// test runs the named hook SYNCHRONOUSLY with the given event and returns its
// combined output — `sesh hooks test`, so the whole chain is verifiable end to
// end. ok=false when no such hook is configured.
func (r *hookRunner) test(name string, e Event) (output string, err error, ok bool) {
	for _, h := range r.hooks {
		if h.Name == name {
			ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
			defer cancel()
			out, err := r.runCmd(ctx, h, e.Env())
			return out, err, true
		}
	}
	return "", nil, false
}

func (r *hookRunner) setMuted(name string, muted bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if muted {
		r.muted[name] = true
	} else {
		delete(r.muted, name)
	}
}

func (r *hookRunner) isMuted(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.muted[name]
}

// hookShellRun executes a hook command through $SHELL -c with the event env
// appended — exactly how panes and headless turns get their environment.
func hookShellRun(ctx context.Context, h config.Hook, env map[string]string) (string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.CommandContext(ctx, shell, "-c", h.Command)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func hookTrailing(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	return "; output: " + out
}
