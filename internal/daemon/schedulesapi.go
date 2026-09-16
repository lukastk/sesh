package daemon

// The /v1/schedules endpoint family (_dev/SCHEDULING.md §8.2). Validation is
// loud and total at creation: an unknown guard word, a spec that never fires,
// a message target that is not this machine's thread, a spawn cwd that is not
// absolute — none of it gets stored to fail later, unattended. The zone is
// resolved here (the OWNER's local zone when the request names none) and
// recorded, so what the record fires at is what the response prints back.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
	"github.com/lukastk/sesh/internal/schedule"
	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesSchedules(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/schedules", d.handleScheduleCreate)
	mux.HandleFunc("GET /v1/schedules", d.handleScheduleList)
	mux.HandleFunc("GET /v1/schedules/get", d.handleScheduleGet)
	mux.HandleFunc("POST /v1/schedules/update", d.handleScheduleUpdate)
	mux.HandleFunc("POST /v1/schedules/remove", d.handleScheduleRemove)
	mux.HandleFunc("POST /v1/schedules/run-now", d.handleScheduleRunNow)
	mux.HandleFunc("GET /v1/schedules/runs", d.handleScheduleRuns)
}

// localZoneName is the IANA name of this machine's zone: $TZ, else the
// /etc/localtime link target, else "Local" (which time.LoadLocation resolves
// to the system zone — correct on the one machine that ever runs the record,
// just less informative in a listing).
func localZoneName() string {
	if tz := os.Getenv("TZ"); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			name := target[i+len("zoneinfo/"):]
			if _, err := time.LoadLocation(name); err == nil {
				return name
			}
		}
	}
	return "Local"
}

// validGuard reports whether a --if word is in the closed vocabulary.
func validGuard(word string) bool {
	for _, w := range validGuardWords {
		if w == word {
			return true
		}
	}
	return false
}

// validateRules checks the rules for an action; loud on any unknown value.
func validateRules(action string, r api.ScheduleRules) error {
	switch r.WhenHeadless {
	case "", "turn", "revive", "skip":
	default:
		return fmt.Errorf("when_headless %q: want turn, revive or skip", r.WhenHeadless)
	}
	switch r.WhenBusy {
	case "", "skip", "send":
	default:
		return fmt.Errorf("when_busy %q: want skip or send", r.WhenBusy)
	}
	for _, w := range r.If {
		if !validGuard(w) {
			return fmt.Errorf("--if %q: not a guard word (want one of %s)", w, strings.Join(validGuardWords, ", "))
		}
	}
	if r.IdleForS != nil && *r.IdleForS < 0 {
		return errors.New("idle_for must not be negative")
	}
	if r.RespectTypingMs != nil && *r.RespectTypingMs < 0 {
		return errors.New("respect_typing must not be negative")
	}
	switch r.IfPrevious {
	case "", "skip", "spawn-anyway", "stop-previous":
	default:
		return fmt.Errorf("if_previous %q: want skip, spawn-anyway or stop-previous", r.IfPrevious)
	}
	switch r.OnTurnEnd {
	case "", "keep", "stop", "archive", "stop+archive", "delete":
	default:
		return fmt.Errorf("on_turn_end %q: want keep, stop, archive, stop+archive or delete", r.OnTurnEnd)
	}
	if r.MaxRuntimeS < 0 {
		return errors.New("max_runtime must not be negative")
	}
	if action == api.ScheduleActionMessage && (r.IfPrevious != "" || r.OnTurnEnd != "" || r.MaxRuntimeS != 0 || r.FlagOnEnd) {
		return errors.New("if_previous/on_turn_end/max_runtime/flag_on_end are spawn rules; a message schedule takes none of them")
	}
	if action == api.ScheduleActionSpawn && (r.WhenHeadless != "" || r.WhenBusy != "" || len(r.If) > 0 || r.IdleForS != nil || r.IgnoreHold || r.AllowArchived) {
		return errors.New("when_headless/when_busy/if/idle_for/ignore_hold/allow_archived are message rules; a spawn schedule takes none of them")
	}
	return nil
}

// buildSchedule validates a create request into a record with its first
// next_fire computed at `now`. selfID is the record being EDITED (excluded
// from the name-collision check), "" on create.
func (d *Daemon) buildSchedule(req api.CreateScheduleRequest, now time.Time, selfID string) (api.Schedule, error) {
	sc := api.Schedule{
		ID: uuid.NewString(), Machine: d.cfg.Machine, Name: strings.TrimSpace(req.Name), Action: req.Action, Enabled: true,
		Spec: strings.TrimSpace(req.Spec), TZ: req.TZ, Catchup: req.Catchup,
		NotBeforeUnix: req.NotBeforeUnix, NotAfterUnix: req.NotAfterUnix, MaxFires: req.MaxFires,
		Rules: req.Rules, CreatedAtUnix: now.Unix(),
	}
	switch sc.Action {
	case api.ScheduleActionMessage, api.ScheduleActionSpawn:
	default:
		return sc, fmt.Errorf("schedule: action %q: want message or spawn", sc.Action)
	}
	if sc.Name == "" {
		sc.Name = sc.Action + "-" + sc.ID[:8]
	}
	if strings.ContainsAny(sc.Name, "\t\n") {
		return sc, errors.New("schedule: name must be one line")
	}
	existing, err := d.store.ListSchedules()
	if err != nil {
		return sc, err
	}
	for _, e := range existing {
		if e.ID != selfID && e.Name == sc.Name {
			return sc, fmt.Errorf("schedule: a schedule named %q already exists on this machine (%s)", sc.Name, e.ID[:8])
		}
	}
	switch sc.Catchup {
	case "":
		sc.Catchup = api.CatchupSkip
	case api.CatchupSkip, api.CatchupOnce:
	default:
		return sc, fmt.Errorf("schedule: catchup %q: want skip or once", sc.Catchup)
	}
	if sc.TZ == "" {
		sc.TZ = localZoneName()
	}
	loc, err := time.LoadLocation(sc.TZ)
	if err != nil {
		return sc, fmt.Errorf("schedule: tz %q: %w", sc.TZ, err)
	}
	if sc.MaxFires < 0 || sc.NotBeforeUnix < 0 || sc.NotAfterUnix < 0 {
		return sc, errors.New("schedule: max_fires/not_before/not_after must not be negative")
	}
	if sc.NotAfterUnix > 0 && sc.NotAfterUnix <= now.Unix() {
		return sc, errors.New("schedule: not_after is already in the past")
	}
	if err := validateRules(sc.Action, sc.Rules); err != nil {
		return sc, fmt.Errorf("schedule: %w", err)
	}
	anchor := now
	if sc.NotBeforeUnix > 0 {
		anchor = time.Unix(sc.NotBeforeUnix, 0)
	}
	spec, err := schedule.Parse(sc.Spec, loc, anchor.In(loc))
	if err != nil {
		return sc, err
	}
	sc.Spec = spec.Raw
	from := now.In(loc)
	if sc.NotBeforeUnix > 0 && anchor.After(now) {
		from = anchor.In(loc).Add(-time.Second)
	}
	next := spec.Next(from)
	if next.IsZero() {
		return sc, fmt.Errorf("schedule: %q never fires (no occurrence after %s)", sc.Spec, from.Format(time.RFC3339))
	}
	if sc.NotAfterUnix > 0 && next.Unix() > sc.NotAfterUnix {
		return sc, fmt.Errorf("schedule: the first occurrence (%s) is after not_after", next.Format(time.RFC3339))
	}
	sc.NextFireUnix = next.Unix()

	switch sc.Action {
	case api.ScheduleActionMessage:
		sc.ThreadID, sc.Text = req.ThreadID, req.Text
		if sc.ThreadID == "" || strings.TrimSpace(sc.Text) == "" {
			return sc, errors.New("schedule message: thread_id and text are required")
		}
		th, err := d.store.GetThread(sc.ThreadID)
		if err != nil {
			if errors.Is(err, store.ErrThreadNotFound) {
				return sc, fmt.Errorf("schedule message: thread %s is not on this machine — a message schedule lives on its target's owner (create it with --machine <owner>)", sc.ThreadID)
			}
			return sc, err
		}
		if api.NonAgentKind(th.AgentKind) {
			return sc, fmt.Errorf("schedule message: %s is a %s node, not an agent thread", th.ID[:8], th.AgentKind)
		}
		if _, err := d.expandPrompt(sc.Text); err != nil {
			return sc, fmt.Errorf("schedule message: %w", err)
		}
	case api.ScheduleActionSpawn:
		sc.Agent, sc.Cwd, sc.Prompt, sc.Model = req.Agent, expandHomeCwd(req.Cwd), req.Prompt, req.Model
		sc.IntoSession, sc.ParentID, sc.NameTemplate = req.IntoSession, req.ParentID, req.NameTemplate
		sc.Headless = req.Headless == nil || *req.Headless
		if sc.Agent == "" {
			sc.Agent = d.defaults.Agent
		}
		if sc.Agent == "" {
			return sc, errors.New("schedule spawn: agent is required (or set [defaults] agent)")
		}
		kind, err := agents.ParseKind(sc.Agent)
		if err != nil {
			return sc, fmt.Errorf("schedule spawn: %w", err)
		}
		if !filepath.IsAbs(sc.Cwd) {
			return sc, fmt.Errorf("schedule spawn: cwd %q must be absolute (or ~-relative)", req.Cwd)
		}
		if strings.TrimSpace(sc.Prompt) == "" {
			return sc, errors.New("schedule spawn: prompt is required")
		}
		if _, err := d.expandPrompt(sc.Prompt); err != nil {
			return sc, fmt.Errorf("schedule spawn: %w", err)
		}
		mode, err := d.resolveSpawnMode(kind, req.SpawnMode)
		if err != nil {
			return sc, fmt.Errorf("schedule spawn: %w", err)
		}
		if mode == "" {
			mode = "default" // the agent's own permission defaults — recorded as such, never blank
		}
		sc.SpawnMode = mode
		if sc.ParentID != "" {
			if _, err := d.store.GetThread(sc.ParentID); err != nil {
				return sc, fmt.Errorf("schedule spawn: parent %s: %w", sc.ParentID, err)
			}
		}
		if sc.IntoSession != "" && sc.Headless {
			return sc, errors.New("schedule spawn: into_session needs a headed spawn (headless = false)")
		}
	}
	return sc, nil
}

func (d *Daemon) handleScheduleCreate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateScheduleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sc, err := d.buildSchedule(req, time.Now(), "")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := d.store.InsertSchedule(sc); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ScheduleResponse{Schema: api.SchemaVersion, Schedule: sc})
}

// resolveScheduleRef resolves a full id or a unique prefix against this
// machine's schedules; loud on unknown or ambiguous.
func (d *Daemon) resolveScheduleRef(ref string) (api.Schedule, error) {
	if ref == "" {
		return api.Schedule{}, errors.New("schedule: id is required")
	}
	if sc, err := d.store.GetSchedule(ref); err == nil {
		return sc, nil
	}
	all, err := d.store.ListSchedules()
	if err != nil {
		return api.Schedule{}, err
	}
	var hits []api.Schedule
	for _, sc := range all {
		if strings.HasPrefix(sc.ID, ref) || sc.Name == ref {
			hits = append(hits, sc)
		}
	}
	switch len(hits) {
	case 0:
		return api.Schedule{}, fmt.Errorf("%w: %s", store.ErrScheduleNotFound, ref)
	case 1:
		return hits[0], nil
	default:
		names := make([]string, len(hits))
		for i, h := range hits {
			names[i] = h.ID[:8] + " " + h.Name
		}
		return api.Schedule{}, fmt.Errorf("schedule: %q is ambiguous: %s", ref, strings.Join(names, "; "))
	}
}

func (d *Daemon) scheduleErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrScheduleNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

func (d *Daemon) handleScheduleList(w http.ResponseWriter, r *http.Request) {
	all, err := d.store.ListSchedules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	thread := r.URL.Query().Get("thread")
	out := api.SchedulesResponse{Schema: api.SchemaVersion, Schedules: []api.Schedule{}}
	for _, sc := range all {
		sc.Machine = d.cfg.Machine
		if thread == "" || sc.ThreadID == thread || sc.LastRunThreadID == thread {
			out.Schedules = append(out.Schedules, sc)
		}
	}
	if r.URL.Query().Get("all-machines") == "1" {
		peerRows, unreachable := d.fanOutSchedules(thread)
		out.Schedules = append(out.Schedules, peerRows...)
		out.Unreachable = unreachable
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *Daemon) handleScheduleGet(w http.ResponseWriter, r *http.Request) {
	sc, err := d.resolveScheduleRef(r.URL.Query().Get("id"))
	if err != nil {
		d.scheduleErr(w, err)
		return
	}
	sc.Machine = d.cfg.Machine
	writeJSON(w, http.StatusOK, api.ScheduleResponse{Schema: api.SchemaVersion, Schedule: sc})
}

func (d *Daemon) handleScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateScheduleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sc, err := d.resolveScheduleRef(req.ID)
	if err != nil {
		d.scheduleErr(w, err)
		return
	}
	now := time.Now()
	clockChanged := false
	if req.Name != nil {
		sc.Name = strings.TrimSpace(*req.Name)
	}
	if req.Spec != nil {
		sc.Spec, clockChanged = strings.TrimSpace(*req.Spec), true
	}
	if req.TZ != nil {
		sc.TZ, clockChanged = *req.TZ, true
	}
	if req.Catchup != nil {
		sc.Catchup = *req.Catchup
	}
	if req.NotAfterUnix != nil {
		sc.NotAfterUnix = *req.NotAfterUnix
	}
	if req.MaxFires != nil {
		sc.MaxFires = *req.MaxFires
	}
	if req.Text != nil {
		sc.Text = *req.Text
	}
	if req.Prompt != nil {
		sc.Prompt = *req.Prompt
	}
	if req.Rules != nil {
		sc.Rules = *req.Rules
	}
	if req.Enabled != nil {
		if *req.Enabled && !sc.Enabled {
			clockChanged = true // a resume never fires its backlog
		}
		sc.Enabled, sc.DisabledReason = *req.Enabled, ""
		if !*req.Enabled {
			sc.DisabledReason = "paused"
		}
	}
	// Re-validate the whole record through the create path (same rules, same
	// loudness), keeping identity and state.
	check, err := d.buildSchedule(api.CreateScheduleRequest{
		Name: sc.Name, Action: sc.Action, Spec: sc.Spec, TZ: sc.TZ, Catchup: sc.Catchup,
		NotBeforeUnix: sc.NotBeforeUnix, NotAfterUnix: sc.NotAfterUnix, MaxFires: sc.MaxFires,
		ThreadID: sc.ThreadID, Text: sc.Text, Agent: sc.Agent, Cwd: sc.Cwd, Prompt: sc.Prompt, Model: sc.Model,
		SpawnMode: sc.SpawnMode, Headless: &sc.Headless, IntoSession: sc.IntoSession, ParentID: sc.ParentID, NameTemplate: sc.NameTemplate,
		Rules: sc.Rules,
	}, now, sc.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sc.Spec, sc.TZ, sc.Catchup, sc.SpawnMode = check.Spec, check.TZ, check.Catchup, check.SpawnMode
	if clockChanged {
		sc.NextFireUnix = check.NextFireUnix
	}
	if err := d.store.UpdateSchedule(sc); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sc.Machine = d.cfg.Machine
	writeJSON(w, http.StatusOK, api.ScheduleResponse{Schema: api.SchemaVersion, Schedule: sc})
}

func (d *Daemon) handleScheduleRemove(w http.ResponseWriter, r *http.Request) {
	var req api.ScheduleIDRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sc, err := d.resolveScheduleRef(req.ID)
	if err != nil {
		d.scheduleErr(w, err)
		return
	}
	if err := d.store.DeleteSchedule(sc.ID); err != nil {
		d.scheduleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "removed": sc.ID})
}

// handleScheduleRunNow fires a schedule immediately (--force bypasses the
// guards) and answers with the outcome — the way to test a schedule without
// waiting for its clock. It does not touch next_fire.
func (d *Daemon) handleScheduleRunNow(w http.ResponseWriter, r *http.Request) {
	var req api.ScheduleIDRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sc, err := d.resolveScheduleRef(req.ID)
	if err != nil {
		d.scheduleErr(w, err)
		return
	}
	if d.sched == nil {
		writeError(w, http.StatusServiceUnavailable, "schedule run-now: no scheduler in this daemon")
		return
	}
	d.sched.mu.Lock()
	busy := d.sched.inFlight[sc.ID]
	if !busy {
		d.sched.inFlight[sc.ID] = true
	}
	d.sched.mu.Unlock()
	if busy {
		writeError(w, http.StatusConflict, "schedule run-now: a run of "+sc.Name+" is already in progress")
		return
	}
	defer func() {
		d.sched.mu.Lock()
		delete(d.sched.inFlight, sc.ID)
		d.sched.mu.Unlock()
	}()
	outcome, detail, threadID := d.sched.runOnce(sc, time.Now(), req.Force)
	writeJSON(w, http.StatusOK, api.ScheduleRunNowResponse{Schema: api.SchemaVersion, ID: sc.ID, Outcome: outcome, Detail: detail, ThreadID: threadID})
}

func (d *Daemon) handleScheduleRuns(w http.ResponseWriter, r *http.Request) {
	sc, err := d.resolveScheduleRef(r.URL.Query().Get("id"))
	if err != nil {
		d.scheduleErr(w, err)
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "schedule runs: limit must be a non-negative integer")
			return
		}
		limit = n
	}
	runs, err := d.store.ListScheduleRuns(sc.ID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ScheduleRunsResponse{Schema: api.SchemaVersion, Runs: runs})
}

// fanOutSchedules asks every reachable peer for its schedules (the tickets
// fan-out pattern: http via the TCP API, else a real ssh hop; a peer the
// liveness cache knows is down is reported unreachable without a dial).
func (d *Daemon) fanOutSchedules(thread string) ([]api.Schedule, []string) {
	d.noteMeshDemand()
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		return nil, nil
	}
	down := d.knownOfflinePeers()
	var merged []api.Schedule
	var unreachable []string
	for _, p := range reg.List() {
		if down[p.Machine] {
			unreachable = append(unreachable, p.Machine)
			continue
		}
		rows, err := fetchPeerSchedules(p, thread)
		if err != nil {
			unreachable = append(unreachable, p.Machine)
			continue
		}
		merged = append(merged, rows...)
	}
	return merged, unreachable
}

func fetchPeerSchedules(p peers.Peer, thread string) ([]api.Schedule, error) {
	if p.Transport() == "http" {
		c, err := peerRemoteClient(p)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), meshFetchTimeout)
		defer cancel()
		resp, err := c.SchedulesList(ctx, thread, false)
		if err != nil {
			return nil, err
		}
		return resp.Schedules, nil
	}
	args := []string{"env", "SESH_HOME=" + shQuote(p.Home), "SESH_MACHINE=" + shQuote(p.Machine), shQuote(p.Binary), "schedule", "list", "--json"}
	if thread != "" {
		args = append(args, "--thread", shQuote(thread))
	}
	sshArgs := append(peers.SSHMultiplexArgs(), p.SSHArgs()...)
	sshArgs = append(sshArgs, p.SSH, strings.Join(args, " "))
	cmd := exec.Command("ssh", sshArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var out api.SchedulesResponse
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, err
	}
	return out.Schedules, nil
}
