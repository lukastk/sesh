package main

// `sesh subscribe <subscribee> [--from <subscriber>]` (PARITY_ROADMAP C3,
// v1's forms): the subscribee's completed turns are delivered into the
// subscriber thread (default: the CURRENT thread — an agent runs `sesh
// subscribe <id>` to follow another agent). The edge lives on the
// SUBSCRIBEE's owner daemon (delivery is owner-side); pass --machine to
// reach it, exactly like any routed verb. `unsubscribe` removes the edge;
// `subscriptions [id]` lists edges.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/lukastk/sesh/internal/config"
)

func runSubscribe(cfg config.Config, args []string, remove bool) error {
	verb := "subscribe"
	if remove {
		verb = "unsubscribe"
	}
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	from := fs.String("from", "", "subscriber thread id/prefix (default: the current thread)")
	allowCycle := fs.Bool("allow-cycle", false, "permit a delivery cycle (the circuit breaker caps runaway loops)")
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
	if ref == "" {
		return errors.New(verb + ": a subscribee id is required (sesh " + verb + " <id>)")
	}
	c := daemonClient(cfg)
	subscribee, err := resolveMeshThreadID(c, cfg, ref)
	if err != nil {
		return err
	}
	// --from, not --id: this command has no --id, so a refusal that suggested
	// one would hand the caller a flag that does not parse.
	subscriber, err := resolveMeshThreadIDFor(c, cfg, *from, "--from")
	if err != nil {
		return err
	}
	ctx := context.Background()
	if remove {
		if err := c.Unsubscribe(ctx, subscriber, subscribee); err != nil {
			return err
		}
		fmt.Printf("%s no longer receives %s's turns\n", subscriber[:8], subscribee[:8])
		return nil
	}
	if err := c.Subscribe(ctx, subscriber, subscribee, *allowCycle); err != nil {
		return err
	}
	fmt.Printf("%s now receives %s's turns\n", subscriber[:8], subscribee[:8])
	return nil
}

func runSubscriptions(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("subscriptions", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	ref := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		ref, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := daemonClient(cfg)
	id := ""
	if ref != "" {
		rid, err := resolveMeshThreadID(c, cfg, ref)
		if err != nil {
			return err
		}
		id = rid
	}
	resp, err := c.Subscriptions(context.Background(), id)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp)
	}
	if len(resp.Subscriptions) == 0 {
		fmt.Println("(no subscriptions)")
		return nil
	}
	for _, s := range resp.Subscriptions {
		fmt.Printf("%s <- %s\n", s.Subscriber, s.Subscribee)
	}
	return nil
}
