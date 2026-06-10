package pi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

// Wire protocol (v1 exp03 §2): newline-delimited JSON over a per-session
// unix socket, request/response symmetrical. Commands are single-key
// objects; replies carry {ok:true,...} or {error:"..."}.

// State is the reply to {getState:true}.
type State struct {
	OK           bool `json:"ok"`
	Idle         bool `json:"idle"`
	ContextUsage struct {
		Tokens        int     `json:"tokens"`
		ContextWindow int     `json:"contextWindow"`
		Percent       float64 `json:"percent"`
	} `json:"contextUsage"`
	HasAppendedSystemPrompt bool     `json:"hasAppendedSystemPrompt"`
	Cwd                     string   `json:"cwd"`
	Tmux                    TmuxInfo `json:"tmux"`
}

// TmuxInfo is the reply to {getTmuxInfo:true} (and embedded in State).
type TmuxInfo struct {
	InTmux      bool   `json:"inTmux"`
	PaneID      string `json:"paneId"`
	SocketPath  string `json:"socketPath"`
	Session     string `json:"session"`
	Window      string `json:"window"`
	WindowIndex int    `json:"windowIndex"`
	PaneIndex   int    `json:"paneIndex"`
}

// Event is a subscribed stream event. Broadcast ONLY for socket-initiated
// turns (v1 exp03 §2.2) — TUI-typed turns do NOT stream here; use the
// JSONL watch (transcript.go) for those.
type Event struct {
	Event    string `json:"event"`    // text_delta | tool_execution_start | tool_execution_end | agent_end
	Delta    string `json:"delta"`    // for text_delta
	ToolName string `json:"toolName"` // for tool_execution_*
}

// Client is a pi-rpc-socket connection.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
}

// Dial connects to a session socket. Use ProbeLive first if you need to
// distinguish dead/stale.
func Dial(socketPath string) (*Client, error) {
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, r: bufio.NewReader(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// roundTrip sends one request object and decodes one reply line into out.
func (c *Client) roundTrip(req any, out any) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := c.conn.Write(append(b, '\n')); err != nil {
		return err
	}
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(line, out)
}

func (c *Client) GetState() (State, error) {
	var s State
	err := c.roundTrip(map[string]any{"getState": true}, &s)
	return s, err
}

func (c *Client) GetTmuxInfo() (TmuxInfo, error) {
	var resp struct {
		OK   bool     `json:"ok"`
		Tmux TmuxInfo `json:"tmux"`
	}
	err := c.roundTrip(map[string]any{"getTmuxInfo": true}, &resp)
	return resp.Tmux, err
}

// Message delivers text; the turn runs async and events broadcast to
// subscribers. v1 exp05b §D: pi queues mid-stream as "steer"; to
// interrupt, Abort then Message.
func (c *Client) Message(text string) error {
	var resp struct {
		OK        bool   `json:"ok"`
		Delivered string `json:"delivered"`
		Error     string `json:"error"`
	}
	if err := c.roundTrip(map[string]any{"message": text}, &resp); err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("pi message: %s", resp.Error)
	}
	return nil
}

func (c *Client) Abort() error {
	return c.roundTrip(map[string]any{"abort": true}, nil)
}

// Compact triggers compaction. v1 exp03 §5.1: pi-rpc-socket replies with
// an error even though compaction succeeds — so a non-nil error here is
// NOT necessarily a failure. We surface it but callers should treat it as
// best-effort (the bug is upstream).
func (c *Client) Compact() error {
	return c.roundTrip(map[string]any{"compact": true}, nil)
}

func (c *Client) AppendSystemPrompt(text string) error {
	var resp struct{ OK bool `json:"ok"` }
	return c.roundTrip(map[string]any{"appendSystemPrompt": text}, &resp)
}

func (c *Client) ClearSystemPrompt() error {
	var resp struct{ OK bool `json:"ok"` }
	return c.roundTrip(map[string]any{"clearSystemPrompt": true}, &resp)
}

// Subscribe sends {subscribe:true} and returns a channel of streamed
// events. The channel closes when the connection ends. A separate Dial is
// recommended for the subscription so request/reply traffic doesn't
// interleave with the event stream.
func (c *Client) Subscribe() (<-chan Event, error) {
	var resp struct {
		OK         bool `json:"ok"`
		Subscribed bool `json:"subscribed"`
	}
	if err := c.roundTrip(map[string]any{"subscribe": true}, &resp); err != nil {
		return nil, err
	}
	if !resp.Subscribed {
		return nil, fmt.Errorf("subscribe not acknowledged")
	}
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		for {
			line, err := c.r.ReadBytes('\n')
			if err != nil {
				return
			}
			var ev Event
			if json.Unmarshal(line, &ev) == nil && ev.Event != "" {
				ch <- ev
			}
		}
	}()
	return ch, nil
}

// LivenessResult tags why a probe failed (v1 exp03 §4: net.Connect
// returns ENOENT for BOTH missing-file and present-but-no-listener, so
// isSocket() is needed to distinguish for diagnostics).
type LivenessResult string

const (
	Live          LivenessResult = "live"
	NoFile        LivenessResult = "no-file"
	NotSocket     LivenessResult = "not-socket"
	ConnectFailed LivenessResult = "connect-failed"
)

// ProbeLive checks a socket path: stat for missing/wrong-type, then
// connect. Never trust mere file presence — a stale socket file is
// indistinguishable from missing at the connect layer.
func ProbeLive(socketPath string) LivenessResult {
	fi, err := os.Stat(socketPath)
	if err != nil {
		return NoFile
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return NotSocket
	}
	conn, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		return ConnectFailed
	}
	conn.Close()
	return Live
}
