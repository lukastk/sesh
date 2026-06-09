package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/config"
)

// runTicket implements `sesh ticket <create|list|set-status|needs-input>`.
func runTicket(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sesh ticket <create|list|set-status|needs-input|send-prompt>")
	}
	cfg := config.Load()

	// Single-writer ownership: tickets live on one canonical owner machine. If an
	// owner is configured and it is not us, route the whole ticket command there
	// (over a real ssh hop) — writes go to the owner, never silently local.
	if cfg.TicketOwner != "" && cfg.TicketOwner != cfg.Machine {
		return routeToMachine(cfg, cfg.TicketOwner, append([]string{"ticket"}, args...))
	}

	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		return ticketCreate(cfg, rest)
	case "list":
		return ticketList(cfg, rest)
	case "set-status":
		return ticketSetStatus(cfg, rest)
	case "needs-input":
		return ticketNeedsInput(cfg, rest)
	case "send-prompt":
		return ticketSendPrompt(cfg, rest)
	default:
		return fmt.Errorf("unknown ticket subcommand %q", sub)
	}
}

func ticketCreate(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	name := fs.String("name", "", "ticket name (required)")
	desc := fs.String("description", "", "description")
	prompt := fs.String("prompt", "", "prompt")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("ticket create: --name is required")
	}
	c := client.New(cfg.SocketPath())
	resp, err := c.TicketCreate(context.Background(), api.CreateTicketRequest{Name: *name, Description: *desc, Prompt: *prompt})
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp.Ticket)
	}
	fmt.Println(resp.Ticket.ID)
	return nil
}

func ticketList(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	thread := fs.String("thread", "", "filter to tickets bound to this thread")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := client.New(cfg.SocketPath())
	resp, err := c.TicketList(context.Background(), *thread)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, tk := range resp.Tickets {
			if err := enc.Encode(tk); err != nil {
				return err
			}
		}
		return nil
	}
	for _, tk := range resp.Tickets {
		fmt.Printf("%s\t%s\t%s\t%s\n", tk.ID, tk.Status, tk.Name, tk.ThreadID)
	}
	return nil
}

func ticketSetStatus(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("set-status", flag.ContinueOnError)
	id := fs.String("id", "", "ticket id (required)")
	status := fs.String("status", "", "triage|ready|active|done|dropped (required)")
	thread := fs.String("thread", "", "thread to bind (required for active)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" || *status == "" {
		return errors.New("ticket set-status: --id and --status are required")
	}
	c := client.New(cfg.SocketPath())
	resp, err := c.TicketSetStatus(context.Background(), api.SetStatusRequest{ID: *id, Status: *status, ThreadID: *thread})
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp.Ticket)
	}
	fmt.Printf("%s -> %s\n", resp.Ticket.ID, resp.Ticket.Status)
	return nil
}

func ticketSendPrompt(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("send-prompt", flag.ContinueOnError)
	id := fs.String("id", "", "ticket id (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("ticket send-prompt: --id is required")
	}
	c := client.New(cfg.SocketPath())
	if err := c.TicketSendPrompt(context.Background(), *id); err != nil {
		return err
	}
	fmt.Println("sent prompt for", *id)
	return nil
}

func ticketNeedsInput(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("needs-input", flag.ContinueOnError)
	id := fs.String("id", "", "ticket id (required)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("ticket needs-input: --id is required")
	}
	c := client.New(cfg.SocketPath())
	resp, err := c.TicketNeedsInput(context.Background(), *id)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp)
	}
	fmt.Printf("needs_input=%t needs_restart=%t (status=%s activity=%s)\n",
		resp.NeedsInput, resp.NeedsRestart, resp.Status, resp.ThreadActivity)
	return nil
}
