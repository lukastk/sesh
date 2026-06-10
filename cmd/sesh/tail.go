package main

// `sesh tail [id] [-n N]` + `sesh transcript [id]` (PARITY_ROADMAP D1, v1's
// exact forms): the last N transcript lines (default 20) / the whole dump.
// Pure ergonomics over D0's owner-side read; the current thread is inferred
// when no id is given; remote threads route with --machine.

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lukastk/sesh/internal/config"
)

func runTail(cfg config.Config, args []string, whole bool) error {
	verb := "tail"
	if whole {
		verb = "transcript"
	}
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	n := fs.Int("n", 20, "number of lines (tail)")
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
	rid, err := resolveThreadID(cfg, ref)
	if err != nil {
		return err
	}
	tail := *n
	if whole {
		tail = -1
	}
	resp, err := daemonClient(cfg).ThreadTranscript(context.Background(), rid, tail)
	if err != nil {
		return err
	}
	for _, l := range resp.Lines {
		fmt.Println(l)
	}
	return nil
}
