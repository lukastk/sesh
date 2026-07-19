package daemon

// Daemon-side diagnostics (PARITY_ROADMAP E2). These run in the DAEMON's
// process, so they see the environment that actually launches agents and
// tmux — which differs from the caller's interactive shell (the deploy-env
// failure class: the supervised daemon's PATH lacked the mise shims, so
// headless turns couldn't find pi/codex while the user's shell could). The
// poster-child check runs `$SHELL -c 'command -v <agent>'` exactly as a
// headless turn resolves its binary.

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/api"
)

func (d *Daemon) routesDoctor(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/doctor", d.handleDoctor)
}

func (d *Daemon) handleDoctor(w http.ResponseWriter, r *http.Request) {
	var checks []api.DoctorCheck
	add := func(name, status, detail string) {
		checks = append(checks, api.DoctorCheck{Name: name, Status: status, Detail: detail})
	}

	// SHELL: headless turns run through $SHELL -c, so it must be set.
	shell := os.Getenv("SHELL")
	if shell == "" {
		add("daemon SHELL", "warn", "unset — headless turns fall back to /bin/sh (zshenv PATH/keys won't load)")
		shell = "/bin/sh"
	} else {
		add("daemon SHELL", "ok", shell)
	}

	// Agents on the DAEMON's shell PATH — resolved the SAME way a headless turn
	// resolves its binary ($SHELL -c 'command -v'). This is the check that
	// would have caught the missing-mise-shims deploy bug.
	for _, agent := range []string{"claude", "codex", "pi", "tmux", "ssh"} {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		out, err := exec.CommandContext(ctx, shell, "-lc", "command -v "+agent).Output()
		cancel()
		path := strings.TrimSpace(string(out))
		if err != nil || path == "" {
			st := "warn"
			if agent == "tmux" || agent == "ssh" {
				st = "fail" // core deps; a missing agent is only a warn (maybe unused)
			}
			add("agent:"+agent, st, "not on the daemon's $SHELL PATH (headless turns / tmux would fail)")
		} else {
			add("agent:"+agent, "ok", path)
		}
	}

	// Config files parse (a broken one would already have refused the daemon,
	// but report which policies are active).
	add("config", "ok", configSummary(d))

	// The work tmux server: reachable, and started with the expected conf.
	if _, err := d.tmux.AttachedSessions(); err != nil {
		add("work tmux", "warn", "server not reachable: "+err.Error())
	} else {
		add("work tmux", "ok", "socket "+d.tmux.Socket())
	}

	// API exposure: exposed-without-a-token is refused at startup. The bind itself
	// is background-retried, so "configured" is NOT "listening" — an unresolvable
	// bind host (ideapad's NM-clobbered resolver, ticket aeaca0d0) once spun the
	// retry loop for 12 days while this check said "ok" and the whole mesh showed
	// the machine offline. Report the REAL bind state, loudly.
	if d.cfg.APIAddr != "" {
		if d.apiBound.Load() {
			add("api", "ok", "listening on "+d.cfg.APIAddr)
		} else {
			msg := "configured on " + d.cfg.APIAddr + " but NOT BOUND — cross-machine http access to this daemon is down (bind retries every " + apiBindRetry.String() + ")"
			if e, _ := d.apiBindErr.Load().(string); e != "" {
				msg += "; last error: " + e
			}
			add("api", "fail", msg)
		}
	}

	// Peers: the mesh sync's last-known reachability (no fresh hop — the live
	// mesh state already reflects it, and a hop here would block the request).
	if rows, err := d.store.LoadPeerSnapshots(); err == nil {
		for _, row := range rows {
			if row.Reachable {
				add("peer:"+row.Machine, "ok", "synced "+time.Unix(row.SyncedAtUnix, 0).Format("15:04:05"))
			} else {
				add("peer:"+row.Machine, "warn", "unreachable (last sync "+time.Unix(row.SyncedAtUnix, 0).Format("15:04:05")+")")
			}
		}
	}

	writeJSON(w, http.StatusOK, api.DoctorResponse{Schema: api.SchemaVersion, Checks: checks})
}

func configSummary(d *Daemon) string {
	parts := []string{}
	if d.naming != nil {
		parts = append(parts, "session-name")
	}
	if d.spawn.ModeFor("") != "" {
		parts = append(parts, "spawn:"+d.spawn.ModeFor(""))
	}
	if len(d.hooks.hooks) > 0 {
		parts = append(parts, "hooks")
	}
	if len(parts) == 0 {
		return "defaults (no policy config)"
	}
	return strings.Join(parts, ", ")
}
