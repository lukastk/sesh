package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

func threadCommand(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("thread command", flag.ContinueOnError)
	id := fs.String("id", "", "thread id")
	text := fs.String("text", "", "explicit slash command")
	timeout := fs.Duration("timeout", 0, "required overall wait timeout")
	requestID := fs.String("request-id", "", "poll an existing operation, without re-sending")
	jsonOut := fs.Bool("json", false, "JSON result")
	if err := fs.Parse(args); err != nil {
		return err
	}
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	if fs.NArg() != 0 || *timeout <= 0 || seen["text"] == seen["request-id"] {
		return fmt.Errorf("thread command requires --timeout and exactly one of --text or --request-id")
	}
	if seen["text"] && !strings.HasPrefix(strings.TrimSpace(*text), "/") {
		return fmt.Errorf("command text must start with /")
	}
	if seen["request-id"] {
		// An explicitly empty recovery handle must NEVER become a new command.
		if strings.TrimSpace(*requestID) == "" {
			return fmt.Errorf("--request-id was passed but is empty; refusing to start a new command")
		}
		if _, err := uuid.Parse(*requestID); err != nil {
			return fmt.Errorf("request-id must be a UUID: %w", err)
		}
	}
	tid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	c := daemonClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var out api.PiCommandResponse
	if *requestID == "" {
		*requestID = uuid.NewString()
		// Announce before sending so even a lost response leaves a handle to inspect.
		fmt.Fprintf(os.Stderr, "Pi command request %s (poll with --id %s --request-id %s --timeout 5m; do not resend on uncertainty)\n", *requestID, tid, *requestID)
		out, err = c.PiCommand(ctx, api.PiCommandRequest{ID: tid, Text: *text, RequestID: *requestID})
	} else {
		out, err = c.PiOperation(ctx, tid, *requestID)
	}
	for {
		if err != nil {
			return fmt.Errorf("Pi command %s: %w; no automatic retry (a transport failure can leave the outcome unknown)", *requestID, err)
		}
		if out.RequestID != *requestID {
			return fmt.Errorf("Pi command: uncorrelated response")
		}
		switch out.Operation.Status {
		case "running":
		case "completed", "submitted", "failed":
			if *jsonOut {
				if err := emitJSON(out); err != nil {
					return err
				}
			} else if out.Operation.Status != "failed" {
				fmt.Printf("%s %s\n", out.Operation.Status, string(out.Operation.Result))
			}
			if out.Operation.Status == "failed" {
				return fmt.Errorf("Pi command failed: %s", out.Operation.Error)
			}
			return nil
		default:
			return fmt.Errorf("Pi command: invalid status %q", out.Operation.Status)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("Pi command %s timed out waiting; it may still be running; poll --request-id, do not resend", *requestID)
		case <-time.After(250 * time.Millisecond):
		}
		out, err = c.PiOperation(ctx, tid, *requestID)
	}
}
