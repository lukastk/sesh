package main

// `sesh thread mcp-arg <set|unset|list>`: the arguments a thread adds to a global
// MCP server entry (api/mcparg.go). `--machine` routes it to a peer's daemon like
// every other thread verb.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"reflect"
	"strings"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/config"
)

func threadMcpArg(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return printGroupHelp("thread mcp-arg")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("thread mcp-arg "+sub, flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	var values, servers multiFlag
	asJSON := fs.Bool("json", false, "emit JSON")
	switch sub {
	case "set":
		fs.Var(&values, "mcp-arg", "<server>=<arg>, repeatable")
	case "unset":
		fs.Var(&servers, "server", "the server to remove, repeatable")
	case "list":
	default:
		return fmt.Errorf("unknown thread mcp-arg subcommand %q", sub)
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("thread mcp-arg %s: unexpected argument %q", sub, fs.Arg(0))
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	c := daemonClient(cfg)
	var th api.Thread
	switch sub {
	case "set":
		set, err := api.ParseMcpArgs(values)
		if err != nil {
			return err
		}
		th, err = c.ThreadMcpArgs(context.Background(), api.SetThreadMcpArgsRequest{ID: rid, Set: set})
		if err != nil {
			return mcpArgRouteError(err)
		}
	case "unset":
		if len(servers) == 0 {
			return errors.New("thread mcp-arg unset: --server is required")
		}
		th, err = c.ThreadMcpArgs(context.Background(), api.SetThreadMcpArgsRequest{ID: rid, Unset: servers})
		if err != nil {
			return mcpArgRouteError(err)
		}
	case "list":
		// Such a daemon has no field to omit, so an empty list from it would read as
		// "no arguments" where the truth is "cannot say".
		listed, err := c.ThreadList(context.Background(), true, false)
		if err != nil {
			return err
		}
		if listed.Schema < mcpArgsSchema {
			return fmt.Errorf("thread mcp-arg list: the thread's daemon predates MCP arguments: it speaks schema %d, they arrived in %d; upgrade it", listed.Schema, mcpArgsSchema)
		}
		var ok bool
		if th, ok = lookupThread(c, rid); !ok {
			return fmt.Errorf("thread %s vanished", rid)
		}
	}
	return printMcpArgs(th, *asJSON)
}

func printMcpArgs(th api.Thread, asJSON bool) error {
	if asJSON {
		servers := th.McpArgs
		if servers == nil {
			servers = map[string][]string{}
		}
		return emitJSON(map[string]any{"schema": api.SchemaVersion, "id": th.ID, "mcp_args": servers})
	}
	for _, server := range api.SortedMcpServers(th.McpArgs) {
		for _, arg := range th.McpArgs[server] {
			fmt.Printf("%s=%s\n", server, arg)
		}
	}
	return nil
}

// mcpArgsSchema is the schema that introduced MCP arguments.
const mcpArgsSchema = 63

// mcpArgFieldError names the cure when a daemon older than MCP arguments refuses a
// new-thread request for carrying them. It refuses before anything spawns.
func mcpArgFieldError(err error) error {
	if strings.Contains(err.Error(), `unknown field "mcp_args"`) {
		return fmt.Errorf("thread new: the daemon predates MCP arguments (schema %d) and refused the request, so no thread was created; upgrade it: %w", mcpArgsSchema, err)
	}
	return err
}

func mcpArgRouteError(err error) error {
	if errors.Is(err, client.ErrRouteMissing) {
		return fmt.Errorf("thread mcp-arg: the thread's daemon predates MCP arguments (schema %d): %w", mcpArgsSchema, err)
	}
	return err
}

// checkMcpArgsApplied fails loudly when the daemon that created a thread dropped its
// arguments. Every daemon refuses a field it does not know (mcpArgFieldError), so this
// guards a daemon that accepted them and then lost them.
func checkMcpArgsApplied(want map[string][]string, th api.Thread) error {
	if len(want) == 0 || reflect.DeepEqual(want, th.McpArgs) {
		return nil
	}
	sent, _ := json.Marshal(want)      //nolint:errcheck // map[string][]string cannot fail
	got, _ := json.Marshal(th.McpArgs) //nolint:errcheck // same
	return fmt.Errorf("thread new: thread %s was created, but its daemon recorded MCP arguments %s where %s were sent. Delete this thread and spawn it again",
		th.ID, got, sent)
}

// mcpArgInfoLines is `thread info`'s text for a thread's MCP arguments.
func mcpArgInfoLines(th api.Thread) string {
	var parts []string
	for _, server := range api.SortedMcpServers(th.McpArgs) {
		parts = append(parts, server+"="+strings.Join(th.McpArgs[server], " "))
	}
	return strings.Join(parts, "; ")
}
