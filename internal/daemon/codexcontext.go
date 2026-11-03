package daemon

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/lukastk/sesh/internal/agents"
)

// codexHookTrust caches a successful codex context-hook trust check for one
// codex home. A check costs two `codex app-server` starts, so it runs on the
// first codex launch, again whenever the recorded trust disappears from the
// config, and at most every codexHookRecheck otherwise (a codex upgrade can change
// how it hashes a hook, which would quietly turn sesh's hooks off).
type codexHookTrust struct {
	mu      sync.Mutex
	home    string
	args    string
	checked time.Time
	// note is the most recent failure, for doctor.
	note string
}

const codexHookRecheck = time.Hour

// contextHookScript materializes the hook script and returns its path.
func (d *Daemon) contextHookScript() (string, error) {
	return agents.WriteContextHookScript(d.cfg.Home)
}

// launchArgs are the extra arguments for launching an agent of kind k: the
// context hooks (claude --settings, codex -c, nothing for pi), then the
// configured [spawn.<agent>] args. For codex it first makes sure codex trusts
// the hooks — a launch whose hooks codex would silently skip is refused.
func (d *Daemon) launchArgs(kind agents.Kind) ([]string, error) {
	extra := d.spawn.ArgsFor(string(kind))
	if agents.Family(kind) == agents.Claude {
		for _, a := range extra {
			if a == "--settings" || strings.HasPrefix(a, "--settings=") {
				return nil, fmt.Errorf("[spawn.%s] args pass --settings, which sesh needs for the thread-context hooks — move those settings into claude's own settings files", kind)
			}
		}
	}
	hook, err := d.contextHookArgs(kind)
	if err != nil {
		return nil, err
	}
	return append(hook, extra...), nil
}

// contextHookArgs is launchArgs without the configured args (headless turns
// place them themselves).
func (d *Daemon) contextHookArgs(kind agents.Kind) ([]string, error) {
	script, err := d.contextHookScript()
	if err != nil {
		return nil, err
	}
	args := agents.ContextHookArgs(kind, script)
	if agents.Family(kind) == agents.Codex {
		codexHome, err := agents.CodexHome(d.cfg.CodexHome)
		if err != nil {
			return nil, err
		}
		if err := d.ensureCodexContextHooks(codexHome, args); err != nil {
			return nil, err
		}
	}
	return args, nil
}

func (d *Daemon) ensureCodexContextHooks(codexHome string, args []string) error {
	t := &d.codexHooks
	t.mu.Lock()
	defer t.mu.Unlock()
	key := strings.Join(args, "\x00")
	if t.home == codexHome && t.args == key && time.Since(t.checked) < codexHookRecheck && agents.CodexContextHooksRecorded(codexHome) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), agents.CodexProbeTimeout)
	defer cancel()
	if err := agents.EnsureCodexContextHooksTrusted(ctx, codexHome, args); err != nil {
		t.note = err.Error()
		log.Printf("codex: context hooks: %v", err)
		return fmt.Errorf("codex: sesh's thread-context hooks are not trusted by codex: %w", err)
	}
	t.home, t.args, t.checked, t.note = codexHome, key, time.Now(), ""
	return nil
}

// doctorContextHooks reports the thread-context hook plumbing: the hook script
// sesh hands every claude/codex it launches, and whether codex last accepted the
// hooks' trust (a failure refuses codex launches, so it must be visible).
func (d *Daemon) doctorContextHooks(add func(name, status, detail string)) {
	script, err := d.contextHookScript()
	if err != nil {
		add("context hooks", "fail", err.Error())
		return
	}
	add("context hooks", "ok", "claude/codex launches carry "+script)
	t := &d.codexHooks
	t.mu.Lock()
	note, checked, home := t.note, t.checked, t.home
	t.mu.Unlock()
	switch {
	case note != "":
		add("codex context hooks", "fail", note)
	case !checked.IsZero():
		add("codex context hooks", "ok", "trusted by codex in "+home+" (checked "+checked.Format(time.RFC3339)+")")
	}
}
