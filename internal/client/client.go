// Package client is the HTTP+JSON client for a sesh daemon's unix socket. The
// CLI and TUI use it; it is the only sanctioned way to reach a daemon. (An
// Obsidian plugin would speak the same HTTP+JSON over a TCP-exposed daemon.)
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/api"
)

// Client talks to one daemon over its HTTP+JSON surface — a LOCAL unix socket
// (New) or a REMOTE TCP address with a bearer token (NewRemote). Every method is
// transport-agnostic: it works identically over either, which is what gives the
// network API full parity with the local one.
type Client struct {
	http  *http.Client
	base  string // replaces the "http://unix" placeholder in request URLs
	token string // bearer token for the remote (TCP) transport; empty for unix
}

// New builds a client for the local daemon on socketPath (unix socket, no token).
func New(socketPath string) *Client {
	return &Client{
		base: "http://unix",
		http: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

// NewRemote builds a client for a daemon's TCP API at addr ("host:port"),
// authenticating with token. Same methods, same surface — just over the network.
func NewRemote(addr, token string) *Client {
	return &Client{
		base:  "http://" + addr,
		token: token,
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

// req builds a request with the base host substituted and the bearer token
// attached (for the remote transport).
func (c *Client) req(ctx context.Context, method, u string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.Replace(u, "http://unix", c.base, 1), body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

// Health returns nil if the daemon answers GET /v1/health.
func (c *Client) Health(ctx context.Context) error {
	req, err := c.req(ctx, http.MethodGet, "http://unix/v1/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("client: health returned %d", resp.StatusCode)
	}
	return nil
}

// Status fetches GET /v1/status.
func (c *Client) Status(ctx context.Context) (api.StatusResponse, error) {
	var out api.StatusResponse
	req, err := c.req(ctx, http.MethodGet, "http://unix/v1/status", nil)
	if err != nil {
		return out, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("client: status returned %d", resp.StatusCode)
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}

// Shutdown requests POST /v1/shutdown.
func (c *Client) Shutdown(ctx context.Context) error {
	req, err := c.req(ctx, http.MethodPost, "http://unix/v1/shutdown", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("client: shutdown returned %d", resp.StatusCode)
	}
	return nil
}

// HooksList fetches GET /v1/hooks.
func (c *Client) HooksList(ctx context.Context) (api.HooksListResponse, error) {
	var out api.HooksListResponse
	return out, c.getJSON(ctx, "http://unix/v1/hooks", &out)
}

// HooksMute posts POST /v1/hooks/mute.
func (c *Client) HooksMute(ctx context.Context, name string, muted bool) error {
	return c.postJSON(ctx, "http://unix/v1/hooks/mute", api.HookMuteRequest{Name: name, Muted: muted}, nil)
}

// HooksTest posts POST /v1/hooks/test (synchronous run).
func (c *Client) HooksTest(ctx context.Context, name, threadID string) (api.HookTestResponse, error) {
	var out api.HookTestResponse
	return out, c.postJSON(ctx, "http://unix/v1/hooks/test", api.HookTestRequest{Name: name, ThreadID: threadID}, &out)
}

// Subscribe posts POST /v1/subscriptions (on the SUBSCRIBEE's owner daemon).
func (c *Client) Subscribe(ctx context.Context, subscriber, subscribee string, allowCycle bool) error {
	return c.postJSON(ctx, "http://unix/v1/subscriptions", api.SubscribeRequest{Subscriber: subscriber, Subscribee: subscribee, AllowCycle: allowCycle}, nil)
}

// Unsubscribe posts POST /v1/subscriptions/remove.
func (c *Client) Unsubscribe(ctx context.Context, subscriber, subscribee string) error {
	return c.postJSON(ctx, "http://unix/v1/subscriptions/remove", api.SubscribeRequest{Subscriber: subscriber, Subscribee: subscribee}, nil)
}

// Subscriptions fetches GET /v1/subscriptions[?id=].
func (c *Client) Subscriptions(ctx context.Context, id string) (api.SubscriptionsResponse, error) {
	var out api.SubscriptionsResponse
	url := "http://unix/v1/subscriptions"
	if id != "" {
		url += "?id=" + id
	}
	return out, c.getJSON(ctx, url, &out)
}

// Doctor fetches GET /v1/doctor (daemon-side checks).
func (c *Client) Doctor(ctx context.Context) (api.DoctorResponse, error) {
	var out api.DoctorResponse
	return out, c.getJSON(ctx, "http://unix/v1/doctor", &out)
}
