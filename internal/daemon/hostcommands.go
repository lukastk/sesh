package daemon

// Host commands (schema 74; manifest in internal/config/hostcommands.go, wire in
// internal/api/hostcommands.go). The daemon runs a manifest-declared command on
// its own host when a client names it, substituting client-supplied arg values
// into the manifest's argv — never a shell, never a client-supplied program.
// Runs are asynchronous (a start returns at once; a long-poll reads the result)
// because every hop to this daemon has a 15 s HTTP timeout. They live in daemon
// memory: finished runs are kept for hostRunRetention, and a daemon restart
// loses them (a later read is an unknown id, loudly), killing any still running.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

const (
	hostRunOutputCap  = 1 << 20 // per stream
	hostRunRetention  = 15 * time.Minute
	hostRunMaxRunning = 8
	hostRunMaxKept    = 256
	hostRunWaitCap    = 8 * time.Second // under the 15 s client timeout
)

// hostRun is one run; its api view and fingerprint are guarded by d.hostMu.
type hostRun struct {
	view        api.HostCommandRun
	fingerprint string
	done        chan struct{}
	finishedAt  time.Time
}

func (d *Daemon) routesHostCommands(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/host-commands", d.handleHostCommands)
	mux.HandleFunc("POST /v1/host-commands/run", d.handleHostCommandStart)
	mux.HandleFunc("GET /v1/host-commands/run", d.handleHostCommandRun)
}

func (d *Daemon) handleHostCommands(w http.ResponseWriter, r *http.Request) {
	providers, err := config.LoadHostCommands(d.cfg.Home)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := api.HostCommandsResponse{Schema: api.SchemaVersion, Machine: d.cfg.Machine, Providers: []api.HostCommandProvider{}}
	for _, p := range providers {
		ap := api.HostCommandProvider{Name: p.Name, Description: p.Description, Commands: []api.HostCommand{}}
		for _, c := range p.Commands {
			ac := api.HostCommand{Name: c.Name, Description: c.Description, Argv: c.Argv, TimeoutMs: c.Timeout.Milliseconds()}
			for _, a := range c.Args {
				ac.Args = append(ac.Args, api.HostCommandArg{Name: a.Name, Label: a.Label, Description: a.Description, Required: a.Required, AllowDash: a.AllowDash})
			}
			ap.Commands = append(ap.Commands, ac)
		}
		resp.Providers = append(resp.Providers, ap)
	}
	writeJSON(w, http.StatusOK, resp)
}

// hostCommandArgv resolves a request against the manifest into the argv to run.
func hostCommandArgv(providers []config.HostCommandProvider, req api.HostCommandRunRequest) (config.HostCommandSpec, []string, int, error) {
	var spec *config.HostCommandSpec
	for i := range providers {
		if providers[i].Name != req.Provider {
			continue
		}
		for j := range providers[i].Commands {
			if providers[i].Commands[j].Name == req.Command {
				spec = &providers[i].Commands[j]
			}
		}
		if spec == nil {
			return config.HostCommandSpec{}, nil, http.StatusNotFound, fmt.Errorf("host command: provider %q has no command %q", req.Provider, req.Command)
		}
	}
	if spec == nil {
		return config.HostCommandSpec{}, nil, http.StatusNotFound, fmt.Errorf("host command: no provider %q on this machine", req.Provider)
	}
	declared := map[string]config.HostCommandArgSpec{}
	for _, a := range spec.Args {
		declared[a.Name] = a
	}
	for name, value := range req.Args {
		a, ok := declared[name]
		if !ok {
			return *spec, nil, http.StatusBadRequest, fmt.Errorf("host command %s/%s: unknown arg %q", req.Provider, req.Command, name)
		}
		if strings.ContainsRune(value, 0) {
			return *spec, nil, http.StatusBadRequest, fmt.Errorf("host command %s/%s: arg %q contains a NUL byte", req.Provider, req.Command, name)
		}
		if strings.HasPrefix(value, "-") && !a.AllowDash {
			return *spec, nil, http.StatusBadRequest, fmt.Errorf("host command %s/%s: arg %q starts with \"-\", which the manifest does not allow (it would read as an option)", req.Provider, req.Command, name)
		}
	}
	for _, a := range spec.Args {
		if _, ok := req.Args[a.Name]; a.Required && !ok {
			return *spec, nil, http.StatusBadRequest, fmt.Errorf("host command %s/%s: required arg %q is missing", req.Provider, req.Command, a.Name)
		}
	}
	argv := make([]string, len(spec.Argv))
	for i, el := range spec.Argv {
		argv[i] = config.HostCommandPlaceholder.ReplaceAllStringFunc(el, func(ph string) string {
			return req.Args[ph[1:len(ph)-1]] // declared (validated at load); "" when an optional arg is absent
		})
	}
	return *spec, argv, 0, nil
}

// hostRunFingerprint identifies what a request asks for, so a replayed
// request_id carrying a different request is refused rather than answered.
func hostRunFingerprint(req api.HostCommandRunRequest) string {
	keys := make([]string, 0, len(req.Args))
	for k := range req.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	fmt.Fprintf(h, "%q\x00%q", req.Provider, req.Command)
	for _, k := range keys {
		fmt.Fprintf(h, "\x00%q=%q", k, req.Args[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (d *Daemon) handleHostCommandStart(w http.ResponseWriter, r *http.Request) {
	var req api.HostCommandRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := uuid.Parse(req.RequestID); err != nil {
		writeError(w, http.StatusBadRequest, "host command: request_id must be a caller-minted UUID")
		return
	}
	providers, err := config.LoadHostCommands(d.cfg.Home)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	spec, argv, status, err := hostCommandArgv(providers, req)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	fingerprint := hostRunFingerprint(req)

	d.hostMu.Lock()
	d.pruneHostRunsLocked(time.Now())
	if id, ok := d.hostByRequest[req.RequestID]; ok {
		run := d.hostRuns[id]
		if run.fingerprint != fingerprint {
			d.hostMu.Unlock()
			writeError(w, http.StatusConflict, "host command: request_id "+req.RequestID+" was already used for a different request")
			return
		}
		view := run.view
		d.hostMu.Unlock()
		view.Replayed = true
		writeJSON(w, http.StatusOK, view)
		return
	}
	running := 0
	for _, run := range d.hostRuns {
		if run.view.State == api.HostCommandRunning {
			running++
		}
	}
	if running >= hostRunMaxRunning {
		d.hostMu.Unlock()
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("host command: %d commands are already running on this machine; try again when one finishes", running))
		return
	}
	if d.hostRuns == nil {
		d.hostRuns = map[string]*hostRun{}
		d.hostByRequest = map[string]string{}
	}
	run := &hostRun{
		view: api.HostCommandRun{
			Schema: api.SchemaVersion, RunID: uuid.NewString(), RequestID: req.RequestID, Machine: d.cfg.Machine,
			Provider: req.Provider, Command: req.Command, State: api.HostCommandRunning, StartedAtUnix: time.Now().Unix(),
		},
		fingerprint: fingerprint,
		done:        make(chan struct{}),
	}
	d.hostRuns[run.view.RunID] = run
	d.hostByRequest[req.RequestID] = run.view.RunID
	view := run.view
	d.hostMu.Unlock()

	log.Printf("host-commands: %s/%s started (run %s)", req.Provider, req.Command, view.RunID)
	go d.executeHostRun(run, spec, argv)
	writeJSON(w, http.StatusOK, view)
}

func (d *Daemon) handleHostCommandRun(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	waitMs := 0
	if raw := r.URL.Query().Get("wait_ms"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "host command: wait_ms must be a non-negative integer")
			return
		}
		waitMs = n
	}
	d.hostMu.Lock()
	d.pruneHostRunsLocked(time.Now())
	run := d.hostRuns[id]
	d.hostMu.Unlock()
	if run == nil {
		writeError(w, http.StatusNotFound, "host command: no run "+strconv.Quote(id)+" on this machine (finished runs are kept "+hostRunRetention.String()+"; a daemon restart loses them)")
		return
	}
	if wait := min(time.Duration(waitMs)*time.Millisecond, hostRunWaitCap); wait > 0 {
		select {
		case <-run.done:
		case <-r.Context().Done():
		case <-time.After(wait):
		}
	}
	d.hostMu.Lock()
	view := run.view
	d.hostMu.Unlock()
	writeJSON(w, http.StatusOK, view)
}

// pruneHostRunsLocked drops finished runs past retention, and the oldest
// finished ones beyond hostRunMaxKept. Caller holds d.hostMu.
func (d *Daemon) pruneHostRunsLocked(now time.Time) {
	var finished []*hostRun
	for id, run := range d.hostRuns {
		if run.view.State != api.HostCommandDone {
			continue
		}
		if now.Sub(run.finishedAt) > hostRunRetention {
			delete(d.hostRuns, id)
			delete(d.hostByRequest, run.view.RequestID)
			continue
		}
		finished = append(finished, run)
	}
	if len(finished) <= hostRunMaxKept {
		return
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].finishedAt.Before(finished[j].finishedAt) })
	for _, run := range finished[:len(finished)-hostRunMaxKept] {
		delete(d.hostRuns, run.view.RunID)
		delete(d.hostByRequest, run.view.RequestID)
	}
}

// hostOutputBuffer keeps the first max bytes written and records whether more came.
type hostOutputBuffer struct {
	buf       []byte
	max       int
	truncated bool
}

func (b *hostOutputBuffer) Write(p []byte) (int, error) {
	room := b.max - len(b.buf)
	if len(p) > room {
		b.truncated = true
	}
	if room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// hostCommandsContext is cancelled at daemon shutdown, killing running commands.
func (d *Daemon) hostCommandsContext() context.Context {
	if d.hostCtx == nil {
		return context.Background() // a bare test daemon
	}
	return d.hostCtx
}

func (d *Daemon) executeHostRun(run *hostRun, spec config.HostCommandSpec, argv []string) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(d.hostCommandsContext(), spec.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		cmd.Dir = home
	}
	// Its own process group, so a timeout or shutdown kills the children a
	// command spawned too, and WaitDelay stops an orphan holding the pipes open
	// from wedging the run.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	stdout := &hostOutputBuffer{max: hostRunOutputCap}
	stderr := &hostOutputBuffer{max: hostRunOutputCap}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()

	exitCode, failure := 0, ""
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		exitCode, failure = -1, fmt.Sprintf("killed at its %s timeout", spec.Timeout)
	case ctx.Err() != nil:
		exitCode, failure = -1, "killed: the daemon is shutting down"
	case errors.As(err, &exitErr):
		exitCode = exitErr.ExitCode()
	case err != nil:
		exitCode, failure = -1, "could not run: "+err.Error()
	}
	d.hostMu.Lock()
	run.view.State = api.HostCommandDone
	run.view.DurationMs = time.Since(start).Milliseconds()
	run.view.ExitCode = exitCode
	run.view.Error = failure
	run.view.Stdout, run.view.StdoutTruncated = string(stdout.buf), stdout.truncated
	run.view.Stderr, run.view.StderrTruncated = string(stderr.buf), stderr.truncated
	run.finishedAt = time.Now()
	view := run.view
	d.hostMu.Unlock()
	close(run.done)
	if failure != "" {
		log.Printf("host-commands: %s/%s run %s FAILED: %s", view.Provider, view.Command, view.RunID, failure)
	} else {
		log.Printf("host-commands: %s/%s run %s exited %d after %dms", view.Provider, view.Command, view.RunID, exitCode, view.DurationMs)
	}
}

// stopHostCommands kills every running command at shutdown.
func (d *Daemon) stopHostCommands() {
	if d.hostCancel != nil {
		d.hostCancel()
	}
}
