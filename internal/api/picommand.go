package api

import "encoding/json"

// PiOperation is owned by the live Pi extension, not persisted by sesh.
// submitted means native resource dispatch was requested, NOT that it completed.
type PiOperation struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type PiCommandRequest struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	RequestID string `json:"request_id"`
}

type PiCommandResponse struct {
	Schema    int         `json:"schema"`
	RequestID string      `json:"request_id"`
	Operation PiOperation `json:"operation"`
}
