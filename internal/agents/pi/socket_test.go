package pi

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
)

// Unit protocol peer only; the matrix separately uses real Pi in a real pane.
func peer(t *testing.T, reply func(map[string]any) string) *Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rpc.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			var req map[string]any
			if json.Unmarshal(scanner.Bytes(), &req) != nil {
				return
			}
			line := reply(req)
			if line != "" {
				conn.Write([]byte(line + "\n"))
			}
		}
	}()
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func TestMessageAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		ok         bool
	}{
		{"valid", `{"ok":true,"delivered":"/literal"}`, true},
		{"empty", `{}`, false}, {"negative", `{"ok":false}`, false},
		{"wrong text", `{"ok":true,"delivered":"other"}`, false},
		{"error", `{"error":"refused"}`, false}, {"malformed", `garbage`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := peer(t, func(req map[string]any) string {
				if req["message"] != "/literal" {
					t.Error(req)
				}
				return tc.line
			})
			if err := c.Message("/literal"); (err == nil) != tc.ok {
				t.Fatalf("%v", err)
			}
		})
	}
}
func TestSocketDeadline(t *testing.T) {
	c := peer(t, func(map[string]any) string { return "" })
	start := time.Now()
	err := c.Message("text")
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("connected unresponsive socket not bounded: %v %s", err, time.Since(start))
	}
}
func TestCommandsCorrelationAndVersion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol int
		wrongID  bool
		ok       bool
	}{
		{"v2", 2, false, true}, {"old", 0, false, false}, {"uncorrelated", 2, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := peer(t, func(req map[string]any) string {
				id := req["id"]
				if tc.wrongID {
					id = "other"
				}
				b, _ := json.Marshal(map[string]any{"ok": true, "id": id, "protocol": tc.protocol})
				return string(b)
			})
			if err := c.RequireCommands(); (err == nil) != tc.ok {
				t.Fatalf("%v", err)
			}
		})
	}
}
func TestCheckPaneRefusesStaleSessionSocket(t *testing.T) {
	c := peer(t, func(map[string]any) string {
		return `{"ok":true,"tmux":{"inTmux":true,"paneId":"%2","socketPath":"/tmp/right"}}`
	})
	if err := c.CheckPane("%1", "/tmp/right"); err == nil {
		t.Fatal("wrong pane accepted")
	}
	if err := c.CheckPane("%2", "/tmp/wrong"); err == nil {
		t.Fatal("wrong server accepted")
	}
	if err := c.CheckPane("%2", "/tmp/right"); err != nil {
		t.Fatal(err)
	}
}
func TestOperationValidation(t *testing.T) {
	for _, op := range []api.PiOperation{{}, {Status: "success"}, {Status: "failed"}, {Status: "completed", Error: "contradiction"}} {
		if _, err := validateOperation(op); err == nil {
			t.Fatal(op)
		}
	}
	c := peer(t, func(req map[string]any) string {
		b, _ := json.Marshal(map[string]any{"ok": true, "id": req["id"], "operation": api.PiOperation{Status: "failed", Error: "Nothing to compact"}})
		return string(b)
	})
	op, err := c.Command("id", "/compact")
	if err != nil || !strings.Contains(op.Error, "Nothing to compact") {
		t.Fatal(op, err)
	}
}
