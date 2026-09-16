package main

// `sesh schedule <message|spawn|list|show|runs|pause|resume|remove|edit|run-now>`
// — scheduled work (_dev/SCHEDULING.md). A schedule is a record on the machine
// that EXECUTES it: `message` auto-routes to its target thread's owner; `spawn`
// lands on the local daemon unless --machine says otherwise (the cwd is there).
// The daemon validates everything loudly at creation and prints back the zone
// and first fire, so what was typed is what will happen.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

func runSchedule(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return printGroupHelp("schedule")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "message":
		return scheduleMessage(cfg, rest)
	case "spawn":
		return scheduleSpawn(cfg, rest)
	case "list":
		return scheduleList(cfg, rest)
	case "show":
		return scheduleShow(cfg, rest)
	case "runs":
		return scheduleRuns(cfg, rest)
	case "pause":
		return scheduleEnable(cfg, rest, false)
	case "resume":
		return scheduleEnable(cfg, rest, true)
	case "remove":
		return scheduleRemove(cfg, rest)
	case "edit":
		return scheduleEdit(cfg, rest)
	case "run-now":
		return scheduleRunNow(cfg, rest)
	default:
		return fmt.Errorf("schedule: unknown subcommand %q (want message|spawn|list|show|runs|pause|resume|remove|edit|run-now)", sub)
	}
}

// clockFlags are the three mutually exclusive WHEN forms plus the bounds.
type clockFlags struct {
	cron, every, at, tz, catchup, notBefore, notAfter *string
	maxFires                                          *int
}

func addClockFlags(fs *flag.FlagSet) clockFlags {
	return clockFlags{
		cron:      fs.String("cron", "", "5-field cron expression, e.g. '0 9 * * 1-5' (minute hour day-of-month month day-of-week)"),
		every:     fs.String("every", "", "fixed interval, e.g. 20m or 6h (minimum 10s), counted from creation or --not-before"),
		at:        fs.String("at", "", "one instant, 'YYYY-MM-DD HH:MM' or RFC3339 — fires once then disables itself"),
		tz:        fs.String("tz", "", "IANA zone the clock runs in (default: the OWNING machine's zone; recorded on the schedule)"),
		catchup:   fs.String("catchup", "", "what to do with occurrences missed while the daemon was down: skip (default) or once"),
		notBefore: fs.String("not-before", "", "do not fire before this instant ('YYYY-MM-DD HH:MM' or RFC3339)"),
		notAfter:  fs.String("not-after", "", "stop (disable) after this instant ('YYYY-MM-DD HH:MM' or RFC3339)"),
		maxFires:  fs.Int("max-fires", 0, "disable after this many DELIVERED runs (skips do not count); 0 = unlimited"),
	}
}

// spec renders the clock form into the daemon's spec text; exactly one form.
func (c clockFlags) spec() (string, error) {
	n := 0
	spec := ""
	if *c.cron != "" {
		n++
		spec = "cron " + *c.cron
	}
	if *c.every != "" {
		n++
		spec = "every " + *c.every
	}
	if *c.at != "" {
		n++
		spec = "at " + *c.at
	}
	if n != 1 {
		return "", errors.New("exactly one of --cron, --every or --at is required")
	}
	return spec, nil
}

// instantUnix parses a --not-before/--not-after value in --tz (else local).
func instantUnix(s, tz string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	loc := time.Local
	if tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			return 0, fmt.Errorf("--tz %q: %w", tz, err)
		}
		loc = l
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, fmt.Errorf("instant %q: want 'YYYY-MM-DD HH:MM', 'YYYY-MM-DD' or RFC3339", s)
}

func (c clockFlags) apply(req *api.CreateScheduleRequest) error {
	spec, err := c.spec()
	if err != nil {
		return err
	}
	req.Spec, req.TZ, req.Catchup, req.MaxFires = spec, *c.tz, *c.catchup, *c.maxFires
	if req.NotBeforeUnix, err = instantUnix(*c.notBefore, *c.tz); err != nil {
		return fmt.Errorf("--not-before: %w", err)
	}
	if req.NotAfterUnix, err = instantUnix(*c.notAfter, *c.tz); err != nil {
		return fmt.Errorf("--not-after: %w", err)
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// scheduleMessage creates a message schedule on the target's owner.
func scheduleMessage(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("schedule message", flag.ContinueOnError)
	id := fs.String("id", "", "target thread id or unique prefix (required)")
	text := fs.String("text", "", "the message to deliver (may carry @blob() tokens)")
	name := fs.String("name", "", "schedule name, unique per machine (default: message-<id8>)")
	ck := addClockFlags(fs)
	whenHeadless := fs.String("when-headless", "", "when the target has no live pane: turn (a headless turn; default), revive (resume it into a pane first), skip")
	whenBusy := fs.String("when-busy", "", "when the target is mid-turn: skip (default — the heartbeat rule) or send (paste anyway)")
	ifCond := fs.String("if", "", "comma-separated guard words that must ALL hold: idle,busy,headful,headless,attached,detached,flagged,not-flagged,archived,not-archived,blocked,not-blocked")
	idleFor := fs.Duration("idle-for", -1, "with --if idle: require this much quiet since the last activity (default 60s; 0 = none)")
	ignoreHold := fs.Bool("ignore-hold", false, "fire into a thread that is on hold (skipped by default)")
	allowArchived := fs.Bool("allow-archived", false, "fire into an archived thread (skipped by default)")
	respectTyping := fs.Duration("respect-typing", -1, "the pane typing guard for this schedule's pastes (default: [send] respect_typing; 0 = off); a typing viewer SKIPS the run")
	asJSON := fs.Bool("json", false, "emit the created schedule as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*text) == "" {
		return errors.New("schedule message: --text is required")
	}
	if *id == "" {
		return errors.New("schedule message: --id is required")
	}
	c := daemonClient(cfg)
	rid, err := resolveMeshThreadID(c, cfg, *id)
	if err != nil {
		return err
	}
	// A message schedule lives on its target's OWNER. Route there when the
	// thread is a peer's (the request must carry the full id, resolved above).
	if snap, found, ferr := meshThread(c, rid); ferr == nil && found && snap.Machine != cfg.Machine && cfg.RemoteAddr == "" {
		routed := append([]string{"schedule", "message"}, rewriteIDArg(args, rid)...)
		handled, rerr := routeMachine(cfg, snap.Machine, routed)
		if rerr != nil {
			return rerr
		}
		if handled {
			return nil
		}
		cfg = config.Load()
		c = daemonClient(cfg)
	}
	req := api.CreateScheduleRequest{Name: *name, Action: api.ScheduleActionMessage, ThreadID: rid, Text: *text}
	if err := ck.apply(&req); err != nil {
		return fmt.Errorf("schedule message: %w", err)
	}
	req.Rules = api.ScheduleRules{WhenHeadless: *whenHeadless, WhenBusy: *whenBusy, If: splitList(*ifCond), IgnoreHold: *ignoreHold, AllowArchived: *allowArchived}
	if *idleFor >= 0 {
		s := int64(*idleFor / time.Second)
		req.Rules.IdleForS = &s
	}
	if *respectTyping >= 0 {
		ms := int(*respectTyping / time.Millisecond)
		req.Rules.RespectTypingMs = &ms
	}
	resp, err := c.ScheduleCreate(context.Background(), req)
	if err != nil {
		return err
	}
	return printCreated(resp.Schedule, *asJSON)
}

// rewriteIDArg replaces the --id value with the resolved full id so the routed
// command resolves on the owner without a second mesh lookup.
func rewriteIDArg(args []string, full string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--id" || args[i] == "-id":
			out = append(out, "--id", full)
			i++
		case strings.HasPrefix(args[i], "--id=") || strings.HasPrefix(args[i], "-id="):
			out = append(out, "--id="+full)
		default:
			out = append(out, args[i])
		}
	}
	return out
}

// scheduleSpawn creates a spawn schedule on the local (or --machine) daemon.
func scheduleSpawn(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("schedule spawn", flag.ContinueOnError)
	agent := fs.String("agent", "", "agent kind: pi|claude|codex (default: [defaults] agent)")
	cwd := fs.String("cwd", "", "working directory for each run (absolute or ~-relative; required)")
	prompt := fs.String("prompt", "", "the prompt each run is given (may carry @blob() tokens)")
	promptFile := fs.String("prompt-file", "", "read the prompt from this file instead of --prompt")
	name := fs.String("name", "", "schedule name, unique per machine (default: spawn-<id8>)")
	ck := addClockFlags(fs)
	headed := fs.Bool("headed", false, "spawn each run into a real pane (default: headless)")
	intoSession := fs.String("into-session", "", "headed runs: place the pane as a new window of this tmux session")
	parent := fs.String("parent", "", "parent thread id for every run (a virtual group keeps runs in one node)")
	model := fs.String("model", "", "pin the agent model for every run")
	yolo := fs.Bool("yolo", false, "bypass the agent's permissions for every run (overrides [spawn])")
	sandbox := fs.Bool("sandbox", false, "restrict every run (codex read-only; claude default-deny)")
	nameTemplate := fs.String("name-template", "", "run thread name template with {schedule} {date} {time} {unix} (default '{schedule}-{date}-{time}')")
	ifPrevious := fs.String("if-previous", "", "when the previous run is still going (live pane or turn in flight): skip (default), spawn-anyway, stop-previous")
	onTurnEnd := fs.String("on-turn-end", "", "when a run's first turn ends: keep (default), stop, archive, stop+archive, delete")
	maxRuntime := fs.Duration("max-runtime", 0, "stop a run still going after this long (0 = none)")
	flagOnEnd := fs.Bool("flag-on-end", false, "let a run's thread auto-flag at its turn end (off by default: an unattended recurring turn would flag itself every time)")
	asJSON := fs.Bool("json", false, "emit the created schedule as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cwd == "" {
		return errors.New("schedule spawn: --cwd is required")
	}
	if *prompt != "" && *promptFile != "" {
		return errors.New("schedule spawn: --prompt and --prompt-file are mutually exclusive")
	}
	if *promptFile != "" {
		b, err := os.ReadFile(*promptFile)
		if err != nil {
			return fmt.Errorf("schedule spawn: --prompt-file: %w", err)
		}
		*prompt = string(b)
	}
	if strings.TrimSpace(*prompt) == "" {
		return errors.New("schedule spawn: --prompt (or --prompt-file) is required")
	}
	if *yolo && *sandbox {
		return errors.New("schedule spawn: --yolo and --sandbox are mutually exclusive")
	}
	mode := ""
	if *yolo {
		mode = "yolo"
	}
	if *sandbox {
		mode = "sandbox"
	}
	if !strings.HasPrefix(*cwd, "~") {
		abs, err := absCwd(*cwd)
		if err != nil {
			return fmt.Errorf("schedule spawn: --cwd: %w", err)
		}
		*cwd = abs
	}
	headless := !*headed
	req := api.CreateScheduleRequest{
		Name: *name, Action: api.ScheduleActionSpawn, Agent: *agent, Cwd: *cwd, Prompt: *prompt, Model: *model,
		SpawnMode: mode, Headless: &headless, IntoSession: *intoSession, ParentID: *parent, NameTemplate: *nameTemplate,
		Rules: api.ScheduleRules{IfPrevious: *ifPrevious, OnTurnEnd: *onTurnEnd, MaxRuntimeS: int64(*maxRuntime / time.Second), FlagOnEnd: *flagOnEnd},
	}
	if err := ck.apply(&req); err != nil {
		return fmt.Errorf("schedule spawn: %w", err)
	}
	c := daemonClient(cfg)
	if *parent != "" {
		pid, err := resolveIDPrefix(c, *parent)
		if err != nil {
			return fmt.Errorf("schedule spawn: --parent: %w", err)
		}
		req.ParentID = pid
	}
	resp, err := c.ScheduleCreate(context.Background(), req)
	if err != nil {
		return err
	}
	return printCreated(resp.Schedule, *asJSON)
}

func printCreated(sc api.Schedule, asJSON bool) error {
	if asJSON {
		return emitJSON(sc)
	}
	fmt.Printf("scheduled %s %q (%s) on %s: %s in %s — next fire %s\n", sc.Action, sc.Name, sc.ID[:8], sc.Machine, sc.Spec, sc.TZ, fireAt(sc))
	if sc.Action == api.ScheduleActionSpawn {
		fmt.Printf("  each run: %s in %s, %s, mode %s, on turn end: %s\n", sc.Agent, sc.Cwd, map[bool]string{true: "headless", false: "headed"}[sc.Headless], sc.SpawnMode, orDefault(sc.Rules.OnTurnEnd, "keep"))
	}
	return nil
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// fireAt renders a next-fire instant in the schedule's zone with a relative hint.
func fireAt(sc api.Schedule) string {
	if sc.NextFireUnix == 0 {
		return "never"
	}
	loc, err := time.LoadLocation(sc.TZ)
	if err != nil {
		loc = time.Local
	}
	t := time.Unix(sc.NextFireUnix, 0).In(loc)
	return fmt.Sprintf("%s (%s)", t.Format("Mon 2006-01-02 15:04 MST"), relative(t))
}

func relative(t time.Time) string {
	d := time.Until(t).Round(time.Second)
	switch {
	case d < 0:
		return fmt.Sprintf("%s ago", (-d).Round(time.Second))
	case d < time.Hour:
		return "in " + d.Round(time.Second).String()
	case d < 48*time.Hour:
		return "in " + d.Round(time.Minute).String()
	default:
		return fmt.Sprintf("in %dd", int(d.Hours()/24))
	}
}

func scheduleList(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("schedule list", flag.ContinueOnError)
	all := fs.Bool("all-machines", false, "include every reachable peer's schedules (a fan-out)")
	thread := fs.String("thread", "", "only schedules targeting (or last spawned) this thread id")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := daemonClient(cfg)
	resp, err := c.SchedulesList(context.Background(), *thread, *all)
	if err != nil {
		return err
	}
	for _, m := range resp.Unreachable {
		fmt.Fprintf(os.Stderr, "warning: peer %s unreachable\n", m)
	}
	if *asJSON {
		return emitJSON(resp)
	}
	if len(resp.Schedules) == 0 {
		fmt.Println("no schedules")
		return nil
	}
	sort.SliceStable(resp.Schedules, func(i, j int) bool {
		a, b := resp.Schedules[i], resp.Schedules[j]
		if a.Machine != b.Machine {
			return a.Machine < b.Machine
		}
		return a.CreatedAtUnix < b.CreatedAtUnix
	})
	fmt.Printf("%-8s  %-7s  %-22s  %-7s  %-20s  %-28s  %s\n", "ID", "MACHINE", "NAME", "ACTION", "SPEC", "NEXT", "LAST")
	for _, sc := range resp.Schedules {
		next := fireAt(sc)
		if !sc.Enabled {
			next = "paused"
			if sc.DisabledReason != "" && sc.DisabledReason != "paused" {
				next = "DISABLED: " + sc.DisabledReason
			}
		}
		last := sc.LastOutcome
		if last == "" {
			last = "-"
		}
		if sc.Missed > 0 {
			last += fmt.Sprintf(" (missed %d)", sc.Missed)
		}
		fmt.Printf("%-8s  %-7s  %-22s  %-7s  %-20s  %-28s  %s\n", sc.ID[:8], sc.Machine, trunc(sc.Name, 22), sc.Action, trunc(sc.Spec, 20), trunc(next, 28), last)
	}
	return nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func scheduleShow(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("schedule show", flag.ContinueOnError)
	id := fs.String("id", "", "schedule id, unique prefix, or name (required)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("schedule show: --id is required")
	}
	resp, err := daemonClient(cfg).ScheduleGet(context.Background(), *id)
	if err != nil {
		return err
	}
	sc := resp.Schedule
	if *asJSON {
		return emitJSON(sc)
	}
	rules, _ := json.Marshal(sc.Rules)
	fmt.Printf("id:           %s\nname:         %s\nmachine:      %s\naction:       %s\nenabled:      %v", sc.ID, sc.Name, sc.Machine, sc.Action, sc.Enabled)
	if sc.DisabledReason != "" {
		fmt.Printf(" (%s)", sc.DisabledReason)
	}
	fmt.Printf("\nspec:         %s\ntz:           %s\ncatchup:      %s\n", sc.Spec, sc.TZ, sc.Catchup)
	if sc.NotBeforeUnix > 0 {
		fmt.Printf("not before:   %s\n", time.Unix(sc.NotBeforeUnix, 0).Format(time.RFC3339))
	}
	if sc.NotAfterUnix > 0 {
		fmt.Printf("not after:    %s\n", time.Unix(sc.NotAfterUnix, 0).Format(time.RFC3339))
	}
	if sc.MaxFires > 0 {
		fmt.Printf("max fires:    %d\n", sc.MaxFires)
	}
	switch sc.Action {
	case api.ScheduleActionMessage:
		fmt.Printf("thread:       %s\ntext:         %s\n", sc.ThreadID, sc.Text)
	case api.ScheduleActionSpawn:
		fmt.Printf("agent:        %s\ncwd:          %s\nhead:         %s\nmode:         %s\n", sc.Agent, sc.Cwd, map[bool]string{true: "headless", false: "headed"}[sc.Headless], sc.SpawnMode)
		if sc.Model != "" {
			fmt.Printf("model:        %s\n", sc.Model)
		}
		if sc.ParentID != "" {
			fmt.Printf("parent:       %s\n", sc.ParentID)
		}
		if sc.IntoSession != "" {
			fmt.Printf("into session: %s\n", sc.IntoSession)
		}
		fmt.Printf("name tpl:     %s\nprompt:       %s\n", orDefault(sc.NameTemplate, "{schedule}-{date}-{time}"), sc.Prompt)
		if sc.LastRunThreadID != "" {
			fmt.Printf("last run:     %s\n", sc.LastRunThreadID)
		}
	}
	fmt.Printf("rules:        %s\nnext fire:    %s\n", rules, fireAt(sc))
	if sc.LastFireUnix > 0 {
		fmt.Printf("last fire:    %s\n", time.Unix(sc.LastFireUnix, 0).Format(time.RFC3339))
	}
	fmt.Printf("last outcome: %s\nfired:        %d   skipped: %d   fail streak: %d   missed: %d\n", orDefault(sc.LastOutcome, "-"), sc.FireCount, sc.SkipCount, sc.FailStreak, sc.Missed)
	return nil
}

func scheduleRuns(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("schedule runs", flag.ContinueOnError)
	id := fs.String("id", "", "schedule id, unique prefix, or name (required)")
	limit := fs.Int("limit", 20, "newest N runs (0 = all kept)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("schedule runs: --id is required")
	}
	resp, err := daemonClient(cfg).ScheduleRuns(context.Background(), *id, *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp)
	}
	if len(resp.Runs) == 0 {
		fmt.Println("no runs yet")
		return nil
	}
	for _, r := range resp.Runs {
		line := fmt.Sprintf("%s  %s", time.Unix(r.FiredAtUnix, 0).Format("2006-01-02 15:04:05"), r.Outcome)
		if r.ThreadID != "" {
			line += "  thread " + r.ThreadID[:8]
		}
		if r.Detail != "" {
			line += "  " + r.Detail
		}
		if r.EndedAtUnix > 0 {
			line += "  (ended " + time.Unix(r.EndedAtUnix, 0).Format("15:04:05") + ")"
		}
		fmt.Println(line)
	}
	return nil
}

func scheduleEnable(cfg config.Config, args []string, enabled bool) error {
	verb := map[bool]string{true: "resume", false: "pause"}[enabled]
	fs := flag.NewFlagSet("schedule "+verb, flag.ContinueOnError)
	id := fs.String("id", "", "schedule id, unique prefix, or name (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("schedule %s: --id is required", verb)
	}
	resp, err := daemonClient(cfg).ScheduleUpdate(context.Background(), api.UpdateScheduleRequest{ID: *id, Enabled: &enabled})
	if err != nil {
		return err
	}
	if enabled {
		fmt.Printf("resumed %s (%s) — next fire %s\n", resp.Schedule.Name, resp.Schedule.ID[:8], fireAt(resp.Schedule))
	} else {
		fmt.Printf("paused %s (%s)\n", resp.Schedule.Name, resp.Schedule.ID[:8])
	}
	return nil
}

func scheduleRemove(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("schedule remove", flag.ContinueOnError)
	id := fs.String("id", "", "schedule id, unique prefix, or name (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("schedule remove: --id is required")
	}
	if err := daemonClient(cfg).ScheduleRemove(context.Background(), *id); err != nil {
		return err
	}
	fmt.Println("removed", *id)
	return nil
}

func scheduleEdit(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("schedule edit", flag.ContinueOnError)
	id := fs.String("id", "", "schedule id, unique prefix, or name (required)")
	name := fs.String("name", "", "rename")
	ck := addClockFlags(fs)
	text := fs.String("text", "", "message schedules: replace the text")
	prompt := fs.String("prompt", "", "spawn schedules: replace the prompt")
	asJSON := fs.Bool("json", false, "emit the updated schedule as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("schedule edit: --id is required")
	}
	req := api.UpdateScheduleRequest{ID: *id}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["name"] {
		req.Name = name
	}
	if set["cron"] || set["every"] || set["at"] {
		spec, err := ck.spec()
		if err != nil {
			return fmt.Errorf("schedule edit: %w", err)
		}
		req.Spec = &spec
	}
	if set["tz"] {
		req.TZ = ck.tz
	}
	if set["catchup"] {
		req.Catchup = ck.catchup
	}
	if set["not-after"] {
		v, err := instantUnix(*ck.notAfter, *ck.tz)
		if err != nil {
			return fmt.Errorf("schedule edit: --not-after: %w", err)
		}
		req.NotAfterUnix = &v
	}
	if set["max-fires"] {
		req.MaxFires = ck.maxFires
	}
	if set["text"] {
		req.Text = text
	}
	if set["prompt"] {
		req.Prompt = prompt
	}
	if set["not-before"] {
		return errors.New("schedule edit: --not-before cannot be edited (remove and recreate)")
	}
	if len(set) <= 1 {
		return errors.New("schedule edit: nothing to change")
	}
	resp, err := daemonClient(cfg).ScheduleUpdate(context.Background(), req)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp.Schedule)
	}
	fmt.Printf("updated %s (%s): %s in %s — next fire %s\n", resp.Schedule.Name, resp.Schedule.ID[:8], resp.Schedule.Spec, resp.Schedule.TZ, fireAt(resp.Schedule))
	return nil
}

func scheduleRunNow(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("schedule run-now", flag.ContinueOnError)
	id := fs.String("id", "", "schedule id, unique prefix, or name (required)")
	force := fs.Bool("force", false, "bypass the guards (busy/hold/typing/…): the action is performed regardless")
	asJSON := fs.Bool("json", false, "emit the outcome as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("schedule run-now: --id is required")
	}
	resp, err := daemonClient(cfg).ScheduleRunNow(context.Background(), *id, *force)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp)
	}
	line := resp.Outcome
	if resp.Detail != "" {
		line += ": " + resp.Detail
	}
	if resp.ThreadID != "" {
		line += "  thread " + resp.ThreadID
	}
	fmt.Println(line)
	if resp.Outcome == api.OutcomeFailed {
		return fmt.Errorf("schedule run-now: the run failed")
	}
	return nil
}
