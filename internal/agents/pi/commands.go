package pi

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/lukastk/sesh/internal/api"
)

// CheckPane verifies the socket belongs to this exact marked pane, not another
// live process with a stale/inherited session id. No write occurs before this check.
func (c *Client) CheckPane(pane, socket string) error {
	ti, err := c.GetTmuxInfo()
	if err != nil {
		return err
	}
	if !ti.InTmux || ti.PaneID != pane || realpath(ti.SocketPath) != realpath(socket) {
		return fmt.Errorf("pi RPC socket belongs to pane %q on %q, not %q on %q; refusing delivery", ti.PaneID, ti.SocketPath, pane, socket)
	}
	return nil
}

type commandReply struct {
	ID        string          `json:"id"`
	OK        bool            `json:"ok"`
	Error     string          `json:"error"`
	Protocol  int             `json:"protocol"`
	Operation api.PiOperation `json:"operation"`
}

func (c *Client) commandRequest(req map[string]any) (commandReply, error) {
	var out commandReply
	if err := c.roundTrip(req, &out); err != nil {
		return out, fmt.Errorf("pi RPC: %w (outcome uncertain; do not automatically retry)", err)
	}
	if out.Error != "" {
		return out, fmt.Errorf("pi RPC: %s", out.Error)
	}
	if !out.OK || out.ID != req["id"] {
		return out, fmt.Errorf("pi RPC: invalid or uncorrelated acknowledgement")
	}
	return out, nil
}
func (c *Client) RequireCommands() error {
	out, err := c.commandRequest(map[string]any{"id": uuid.NewString(), "capabilities": true})
	if err != nil {
		return fmt.Errorf("Pi command support unavailable: update pi-rpc-socket and run /reload in Pi; %w", err)
	}
	if out.Protocol != 2 {
		return fmt.Errorf("unsupported Pi socket command protocol %d (need 2); update pi-rpc-socket and run /reload", out.Protocol)
	}
	return nil
}
func validateOperation(op api.PiOperation) (api.PiOperation, error) {
	switch op.Status {
	case "running", "completed", "submitted":
		if op.Error != "" {
			return op, fmt.Errorf("pi RPC: contradictory operation result")
		}
	case "failed":
		if op.Error == "" {
			return op, fmt.Errorf("pi RPC: failed operation without an error")
		}
	default:
		return op, fmt.Errorf("pi RPC: invalid operation status %q", op.Status)
	}
	return op, nil
}
func (c *Client) Command(id, text string) (api.PiOperation, error) {
	out, err := c.commandRequest(map[string]any{"id": id, "command": text})
	if err != nil {
		return out.Operation, err
	}
	return validateOperation(out.Operation)
}
func (c *Client) Operation(id string) (api.PiOperation, error) {
	out, err := c.commandRequest(map[string]any{"id": uuid.NewString(), "getOperation": id})
	if err != nil {
		return out.Operation, err
	}
	return validateOperation(out.Operation)
}
