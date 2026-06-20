package client

import (
	"context"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
)

// Peers fetches GET /v1/peers — the local peer registry.
func (c *Client) Peers(ctx context.Context) (api.PeersListResponse, error) {
	var out api.PeersListResponse
	return out, c.getJSON(ctx, "http://unix/v1/peers", &out)
}

// PeerAdd posts POST /v1/peers — insert or replace a peer; returns the new registry.
func (c *Client) PeerAdd(ctx context.Context, p peers.Peer) (api.PeersListResponse, error) {
	var out api.PeersListResponse
	return out, c.postJSON(ctx, "http://unix/v1/peers", api.AddPeerRequest{Peer: p}, &out)
}

// PeerRemove posts POST /v1/peers/remove — drop a peer by machine; returns the new registry.
func (c *Client) PeerRemove(ctx context.Context, machine string) (api.PeersListResponse, error) {
	var out api.PeersListResponse
	return out, c.postJSON(ctx, "http://unix/v1/peers/remove", api.RemovePeerRequest{Machine: machine}, &out)
}
