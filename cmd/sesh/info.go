package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

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
	ref := *id
	if ref == "" && fs.NArg() == 1 {
		ref = fs.Arg(0) // positional form: sesh info <id>
	}
	rid, err := resolveThreadID(cfg, ref)
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
			"schema":     st.Schema,
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
