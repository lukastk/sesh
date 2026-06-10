package main

// `sesh meta <set|get|unset|list>` (PARITY_ROADMAP D6): arbitrary per-thread
// KV. Feeds the TUI's meta.<key> predicates and scripts. The thread is
// inferred per F1; getting a missing key is LOUD (a typo must not read as
// empty).

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/lukastk/sesh/internal/config"
)

func runMeta(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sesh meta <set|get|unset|list>")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("meta "+sub, flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	var positional []string
	for len(rest) > 0 && rest[0][0] != '-' {
		positional, rest = append(positional, rest[0]), rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	positional = append(positional, fs.Args()...)
	rid, err := resolveThreadID(cfg, *id)
	if err != nil {
		return err
	}
	c := daemonClient(cfg)
	ctx := context.Background()
	switch sub {
	case "set":
		if len(positional) != 2 {
			return errors.New("usage: sesh meta set <key> <value> [--id]")
		}
		if positional[1] == "" {
			return errors.New("meta set: an empty value is `meta unset`")
		}
		if _, err := c.ThreadMeta(ctx, rid, positional[0], positional[1]); err != nil {
			return err
		}
		fmt.Printf("%s = %s\n", positional[0], positional[1])
		return nil
	case "unset":
		if len(positional) != 1 {
			return errors.New("usage: sesh meta unset <key> [--id]")
		}
		if _, err := c.ThreadMeta(ctx, rid, positional[0], ""); err != nil {
			return err
		}
		fmt.Printf("%s unset\n", positional[0])
		return nil
	case "get":
		if len(positional) != 1 {
			return errors.New("usage: sesh meta get <key> [--id]")
		}
		th, ok := lookupThread(c, rid)
		if !ok {
			return fmt.Errorf("thread %s vanished", rid)
		}
		v, present := th.Meta[positional[0]]
		if !present {
			return fmt.Errorf("meta: no key %q on thread %s", positional[0], rid)
		}
		fmt.Println(v)
		return nil
	case "list":
		th, ok := lookupThread(c, rid)
		if !ok {
			return fmt.Errorf("thread %s vanished", rid)
		}
		for k, v := range th.Meta {
			fmt.Printf("%s=%s\n", k, v)
		}
		return nil
	default:
		return fmt.Errorf("unknown meta subcommand %q", sub)
	}
}
