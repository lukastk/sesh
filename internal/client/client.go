// Package client is the HTTP+JSON client for a sesh daemon's unix socket. The
// CLI and TUI use it; it is the only sanctioned way to reach a daemon. (An
// Obsidian plugin would speak the same HTTP+JSON over a TCP-exposed daemon.)
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/lukastk/sesh/internal/api"
)

// Client talks to one daemon over its unix domain socket.
type Client struct {
	http   *http.Client
	socket string
}

// New builds a client for the daemon listening on socketPath.
func New(socketPath string) *Client {
	return &Client{
		socket: socketPath,
		http: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

// Health returns nil if the daemon answers GET /v1/health.
func (c *Client) Health(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/health", nil)
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
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/status", nil)
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
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/shutdown", nil)
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
