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

// TicketSetStatus posts POST /v1/tickets/status.
func (c *Client) TicketSetStatus(ctx context.Context, req api.SetStatusRequest) (api.TicketResponse, error) {
	var out api.TicketResponse
	return out, c.postJSON(ctx, "http://unix/v1/tickets/status", req, &out)
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
