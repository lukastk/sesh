package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/config"
)

// pinnedNode is a pinned thread/divider from the merged grid, carrying just the
// manual-order key. The fractional math for pinning/reordering runs here in the
// CLIENT (the daemon is a pure setter), over the whole cross-machine block.
type pinnedNode struct {
	id      string
	machine string
	order   float64
}

// pinnedNodes returns every pinned node across the merged cross-machine grid,
// sorted ascending by (order, machine, id) — the exact order the TUI renders the
// pinned block, so a client-computed midpoint lands where the user sees the gap.
func pinnedNodes(c *client.Client) ([]pinnedNode, error) {
	grid, err := c.ThreadGrid(context.Background(), false, true)
	if err != nil {
		return nil, err
	}
	var out []pinnedNode
	for _, r := range grid.Rows {
		if r.PinOrder != nil {
			out = append(out, pinnedNode{id: r.ID, machine: r.Machine, order: *r.PinOrder})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].order != out[j].order {
			return out[i].order < out[j].order
		}
		if out[i].machine != out[j].machine {
			return out[i].machine < out[j].machine
		}
		return out[i].id < out[j].id
	})
	return out, nil
}

// blockEnd computes the order key just past one end of the pinned block: TOP =
// below the current minimum, BOTTOM = above the current maximum. An empty block
// starts at 0.
func blockEnd(nodes []pinnedNode, top bool) float64 {
	if len(nodes) == 0 {
		return 0
	}
	if top {
		return nodes[0].order - 1
	}
	return nodes[len(nodes)-1].order + 1
}

// findPinnedNode resolves an id/prefix against the pinned block (loud on none/many).
func findPinnedNode(nodes []pinnedNode, ref string) (int, error) {
	if ref == "" {
		return 0, errors.New("a pinned thread/divider id is required")
	}
	match := -1
	for i, n := range nodes {
		if n.id == ref || strings.HasPrefix(n.id, ref) {
			if match != -1 {
				return 0, fmt.Errorf("ambiguous id prefix %q among pinned threads", ref)
			}
			match = i
		}
	}
	if match == -1 {
		return 0, fmt.Errorf("%q is not a pinned thread/divider (position relative to a pinned one)", ref)
	}
	return match, nil
}

// pinPos holds the mutually-exclusive placement flags shared by `thread pin`. The
// default (nothing set) is TOP.
type pinPos struct {
	top, bottom   *bool
	before, after *string
	order         *float64
}

func registerPinPos(fs *flag.FlagSet) pinPos {
	return pinPos{
		top:    fs.Bool("top", false, "place at the TOP of the pinned block (the default)"),
		bottom: fs.Bool("bottom", false, "place at the BOTTOM of the pinned block"),
		before: fs.String("before", "", "place immediately BEFORE this pinned thread/divider (id/prefix)"),
		after:  fs.String("after", "", "place immediately AFTER this pinned thread/divider (id/prefix)"),
		order:  fs.Float64("order", 0, "set an explicit fractional order key (advanced; skips the relative math)"),
	}
}

// resolvePinOrder turns the placement flags into an absolute fractional key,
// fetching the merged block for the relative modes. Exactly one placement may be
// given; none = TOP.
func resolvePinOrder(c *client.Client, fs *flag.FlagSet, p pinPos) (float64, error) {
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	n := 0
	for _, name := range []string{"top", "bottom", "before", "after", "order"} {
		if seen[name] {
			n++
		}
	}
	if n > 1 {
		return 0, errors.New("at most one of --top, --bottom, --before, --after, --order")
	}
	if seen["order"] {
		return *p.order, nil
	}
	nodes, err := pinnedNodes(c)
	if err != nil {
		return 0, err
	}
	switch {
	case seen["bottom"]:
		return blockEnd(nodes, false), nil
	case seen["before"] || seen["after"]:
		ref := *p.before
		isBefore := true
		if seen["after"] {
			ref, isBefore = *p.after, false
		}
		idx, err := findPinnedNode(nodes, ref)
		if err != nil {
			return 0, err
		}
		return neighborMidpoint(nodes, idx, isBefore), nil
	default: // top (explicit or default)
		return blockEnd(nodes, true), nil
	}
}

// neighborMidpoint computes the key that lands the moved node immediately before
// (isBefore) or after the node at idx: the midpoint to the adjacent node on that
// side, or ±1 past idx when it's already at that end.
func neighborMidpoint(nodes []pinnedNode, idx int, isBefore bool) float64 {
	target := nodes[idx].order
	if isBefore {
		if idx == 0 {
			return target - 1
		}
		return (nodes[idx-1].order + target) / 2
	}
	if idx == len(nodes)-1 {
		return target + 1
	}
	return (target + nodes[idx+1].order) / 2
}

// threadPin pins (or repositions) a top-level thread/divider in the manual order.
// The daemon stores the absolute key; the fractional placement math is done here.
func threadPin(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("pin", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	pos := registerPinPos(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	c := daemonClient(cfg)
	order, err := resolvePinOrder(c, fs, pos)
	if err != nil {
		return fmt.Errorf("thread pin: %w", err)
	}
	if err := c.ThreadPin(context.Background(), rid, &order); err != nil {
		return err
	}
	fmt.Printf("pinned %s (order %g)\n", rid, order)
	return nil
}

// threadUnpin removes a thread's manual ordering (it rejoins the auto-sorted block).
// Dividers cannot be un-pinned (delete them) — the daemon refuses that loudly.
func threadUnpin(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("unpin", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	c := daemonClient(cfg)
	if err := c.ThreadPin(context.Background(), rid, nil); err != nil {
		return err
	}
	fmt.Printf("unpinned %s\n", rid)
	return nil
}

// createDivider spawns a DIVIDER at the TOP of the pinned block (reposition later
// with `thread pin`). Called from `thread new --divider`.
func createDivider(cfg config.Config, name string, asJSON bool) error {
	c := daemonClient(cfg)
	nodes, err := pinnedNodes(c)
	if err != nil {
		return err
	}
	order := blockEnd(nodes, true)
	resp, err := c.ThreadNew(context.Background(), api.NewThreadRequest{Divider: true, Name: name, PinOrder: &order})
	if err != nil {
		return err
	}
	if asJSON {
		return emitJSON(resp.Thread)
	}
	fmt.Println(resp.Thread.ID)
	return nil
}
