package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/config"
)

// runInfo implements `sesh info [id|prefix]` (also `sesh thread info`) —
// describe one thread: the record, the two state axes, attachment, the tmux
// locator, and its tickets. With no argument the CURRENT thread is inferred
// (see current.go): the pane or turn process this command runs inside.
func runInfo(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	id := fs.String("id", "", "thread id or unique prefix (default: the current thread)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := guardEmptyIDFlag(fs); err != nil {
		return err
	}
	ref := *id
	refSupplied := *id != ""
	if ref == "" && fs.NArg() == 1 {
		ref, refSupplied = fs.Arg(0), true // positional form: sesh info <id>
	}
	if err := guardEmptyPositionalRef(refSupplied, ref); err != nil {
		return err
	}
	rid, src, err := resolveCurrentThread(cfg, ref)
	if err != nil {
		return err
	}

	c := daemonClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	th, ok := lookupThread(c, rid)
	if !ok {
		return fmt.Errorf("thread %s vanished mid-lookup", rid)
	}
	st, err := c.ThreadStatus(ctx, rid)
	if err != nil {
		return err
	}
	tlist, err := c.TicketList(ctx, rid)
	if err != nil {
		return err
	}
	tickets := tlist.Tickets

	if *asJSON {
		return emitJSON(map[string]any{
			"schema": st.Schema,
			// source/verified are the PROVENANCE of the answer: how sesh decided
			// this is the thread you are asking about. "pane" (the calling pane's
			// live marker) and "explicit" (you named it) are verified; "env" means
			// the answer rests on an inherited $SESH_THREAD_ID with no pane to
			// check it against. A caller about to do something destructive to
			// "itself" should require source == "pane".
			"source":     string(src),
			"verified":   src.verified(),
			"thread":     th,
			"head":       st.Head,
			"busy":       st.Busy,
			"attachment": st.Attachment,
			"clients":    st.Clients,
			"pane":       st.Pane,
			"tickets":    tickets,
		})
	}
	fmt.Printf("id:         %s\n", th.ID)
	fmt.Printf("source:     %s\n", describeSource(src))
	fmt.Printf("name:       %s\n", th.Name)
	fmt.Printf("agent:      %s\n", th.AgentKind)
	fmt.Printf("machine:    %s\n", th.Machine)
	fmt.Printf("cwd:        %s\n", th.Cwd)
	fmt.Printf("head:       %s\n", st.Head)
	fmt.Printf("busy:       %s\n", st.Busy)
	fmt.Printf("attachment: %s (%d clients)\n", st.Attachment, st.Clients)
	fmt.Printf("archived:   %t\n", th.Archived)
	if len(th.Tags) > 0 {
		fmt.Printf("tags:       %s\n", strings.Join(th.Tags, ", "))
	}
	if th.SessionName != "" {
		loc := th.SessionName
		if st.Pane != "" {
			loc += " (pane " + st.Pane + ")"
		}
		fmt.Printf("tmux:       %s\n", loc)
	}
	if th.CreatedAtUnix > 0 {
		fmt.Printf("created:    %s\n", time.Unix(th.CreatedAtUnix, 0).Format("2006-01-02 15:04"))
	}
	if n := len(tickets); n > 0 {
		open := 0
		for _, t := range tickets {
			if t.Status != "done" {
				open++
			}
		}
		fmt.Printf("tickets:    %d (%d open)\n", n, open)
	}
	return nil
}

// describeSource renders the provenance line of the human `sesh info` output.
// An unverified answer says so in the same breath as the id it qualifies —
// the whole point is that it must not read like a fact.
func describeSource(src idSource) string {
	switch src {
	case srcExplicit:
		return "explicit (you named this thread)"
	case srcPane:
		return "pane (verified — the calling pane's @sesh-thread-id marker)"
	case srcEnv:
		return "env (UNVERIFIED — $" + agents.EnvThreadID + ", no tmux pane to confirm it)"
	}
	return string(src)
}
