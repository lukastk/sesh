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
	"net/url"
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

// NewRouted builds a client for PEER machine's daemon that goes THROUGH the local
// daemon on socketPath (schema 51, /v1/route/<machine>/...): the local daemon
// forwards each request on the keep-alive connection its mesh sync already holds to
// that peer, so a fresh CLI process no longer pays a cold TCP dial per call. Same
// methods, same surface, no token here (the local daemon authenticates to the peer).
// Only for http peers — the caller decides the transport; this never changes it.
func NewRouted(socketPath, machine string) *Client {
	unix := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{
		base: "http://unix/v1/route/" + url.PathEscape(machine),
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: routeGuard{next: unix, machine: machine},
		},
	}
}

// routeGuard turns the one ambiguous answer into a loud one: a 404 that does NOT
// carry api.RoutedByHeader came from a local daemon with no /v1/route at all (it
// predates schema 51 — the binary was updated but the daemon not restarted). Passed
// through, it would read as "thread/session not found" on the peer. Never a silent
// fallback to dialing the peer directly.
type routeGuard struct {
	next    http.RoundTripper
	machine string
}

func (g routeGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := g.next.RoundTrip(req)
	if err != nil {
		// A routed call depends on the local daemon (it holds the peer connection);
		// say so, rather than surfacing a bare unix-socket dial error.
		return nil, fmt.Errorf("routing to %s goes through the LOCAL sesh daemon, which did not answer: %w", g.machine, err)
	}
	if resp.StatusCode == http.StatusNotFound && resp.Header.Get(api.RoutedByHeader) == "" {
		resp.Body.Close() //nolint:errcheck
		return nil, fmt.Errorf("routing to %s: the LOCAL sesh daemon has no /v1/route (it predates schema %d) — restart it through its service manager (supervisorctl restart sesh-daemon; termux: see AGENTS.local.md)", g.machine, api.SchemaVersion)
	}
	// The route handler's OWN refusal (offline, unknown machine, dial failed…): make it
	// an error carrying the message, so callers whose methods report only a status code
	// (Health, Status, Snapshot) still say WHY. The peer's own answers pass through.
	if resp.Header.Get(api.RouteRefusedHeader) != "" {
		defer resp.Body.Close() //nolint:errcheck
		var e api.ErrorResponse
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != "" {
			return nil, fmt.Errorf("%s (HTTP %d)", e.Error, resp.StatusCode)
		}
		return nil, fmt.Errorf("routing to %s refused by the local daemon (HTTP %d)", g.machine, resp.StatusCode)
	}
	return resp, nil
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
