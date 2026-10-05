package daemon

import (
	"encoding/json"
	"errors"
	"github.com/lukastk/sesh/internal/api"
	"strings"
	"testing"
)

func TestRoutedSubscriptionNeverRetriesAmbiguousSend(t *testing.T) {
	for _, tc := range []struct {
		head api.Head
		busy api.Busy
		verb string
	}{
		{api.Headful, api.BusyIdle, "send"}, {api.Headless, api.BusyIdle, "send-headless"},
	} {
		var calls []string
		run := func(args ...string) ([]byte, error) {
			calls = append(calls, strings.Join(args, " "))
			if len(calls) == 1 {
				return json.Marshal(api.ThreadStatusResponse{Head: tc.head, Busy: tc.busy})
			}
			return nil, errors.New("ACK lost after write")
		}
		if err := routedSubscription(run, "thread-id", "peer", "text"); err == nil {
			t.Fatal("failure hidden")
		}
		if len(calls) != 2 || !strings.HasPrefix(calls[1], "thread "+tc.verb+" ") {
			t.Fatalf("unexpected delivery/retry: %v", calls)
		}
	}
}
