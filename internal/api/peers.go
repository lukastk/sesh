package api

import "github.com/lukastk/sesh/internal/peers"

// PeersListResponse is GET /v1/peers — the local peer registry (how to reach each
// OTHER machine's daemon). It is the SAME registry the mesh fan-out uses; the tokens
// it carries are unredacted because the API is already token-gated (or a local-trust
// unix socket) and the client keeps them in its trusted transport layer, never the UI.
type PeersListResponse struct {
	Schema int          `json:"schema"`
	Peers  []peers.Peer `json:"peers"`
}

// AddPeerRequest is the body of POST /v1/peers — insert or replace a peer (keyed by
// machine). The embedded peers.Peer is the full record (machine + ssh + home + the
// optional http-transport fields); the request flattens to the peer's own JSON shape.
type AddPeerRequest struct {
	peers.Peer
}

// RemovePeerRequest is the body of POST /v1/peers/remove — drop a peer by machine
// name (removing an unknown peer is a LOUD 404, never a silent success).
type RemovePeerRequest struct {
	Machine string `json:"machine"`
}
