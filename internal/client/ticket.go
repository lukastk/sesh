package client

import (
	"context"
	"net/url"

	"github.com/lukastk/sesh/internal/api"
)

// TicketCreate posts POST /v1/tickets.
func (c *Client) TicketCreate(ctx context.Context, req api.CreateTicketRequest) (api.TicketResponse, error) {
	var out api.TicketResponse
	return out, c.postJSON(ctx, "http://unix/v1/tickets", req, &out)
}

// TicketList fetches GET /v1/tickets, optionally filtered by thread.
func (c *Client) TicketList(ctx context.Context, threadID string) (api.TicketListResponse, error) {
	var out api.TicketListResponse
	u := "http://unix/v1/tickets"
	if threadID != "" {
		u += "?thread=" + url.QueryEscape(threadID)
	}
	return out, c.getJSON(ctx, u, &out)
}

// TicketGet fetches GET /v1/tickets/get?id=.
func (c *Client) TicketGet(ctx context.Context, id string) (api.TicketResponse, error) {
	var out api.TicketResponse
	return out, c.getJSON(ctx, "http://unix/v1/tickets/get?id="+url.QueryEscape(id), &out)
}

// TicketSet posts POST /v1/tickets/set (partial text-field update).
func (c *Client) TicketSet(ctx context.Context, req api.SetTicketRequest) (api.TicketResponse, error) {
	var out api.TicketResponse
	return out, c.postJSON(ctx, "http://unix/v1/tickets/set", req, &out)
}

// TicketDelete posts POST /v1/tickets/delete.
func (c *Client) TicketDelete(ctx context.Context, id string) error {
	return c.postJSON(ctx, "http://unix/v1/tickets/delete", map[string]string{"id": id}, nil)
}

// TicketSetStatus posts POST /v1/tickets/status.
func (c *Client) TicketSetStatus(ctx context.Context, req api.SetStatusRequest) (api.TicketResponse, error) {
	var out api.TicketResponse
	return out, c.postJSON(ctx, "http://unix/v1/tickets/status", req, &out)
}

// TicketImport posts POST /v1/tickets/import — lands a full ticket record on this
// daemon, preserving its id (the receiving half of a cross-machine ticket move).
func (c *Client) TicketImport(ctx context.Context, req api.ImportTicketRequest) (api.TicketResponse, error) {
	var out api.TicketResponse
	return out, c.postJSON(ctx, "http://unix/v1/tickets/import", req, &out)
}

// TicketUnbind posts POST /v1/tickets/unbind — detaches a ticket from its thread.
func (c *Client) TicketUnbind(ctx context.Context, id string) (api.TicketResponse, error) {
	var out api.TicketResponse
	return out, c.postJSON(ctx, "http://unix/v1/tickets/unbind", api.UnbindTicketRequest{ID: id}, &out)
}

// TicketNeedsInput fetches GET /v1/tickets/needs-input?id=.
func (c *Client) TicketNeedsInput(ctx context.Context, id string) (api.TicketNeedsInput, error) {
	var out api.TicketNeedsInput
	return out, c.getJSON(ctx, "http://unix/v1/tickets/needs-input?id="+url.QueryEscape(id), &out)
}

// TicketSendPrompt posts POST /v1/tickets/send-prompt.
func (c *Client) TicketSendPrompt(ctx context.Context, id string) error {
	return c.postJSON(ctx, "http://unix/v1/tickets/send-prompt", map[string]string{"id": id}, nil)
}
