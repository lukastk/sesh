package main

// The typing guard's CLI surface, shared by `thread send` and `ticket
// send-prompt` (_dev/SCHEDULING.md §6.5.1). The daemon owns the policy; these
// flags only override it per call and render its decision.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
)

// typingFlags is the per-call override set. Unset = the daemon's [send] config.
type typingFlags struct {
	respect  *time.Duration // --respect-typing; -1 = unset
	deadline *time.Duration // --typing-deadline; 0 = unset
	onTyping *string        // --on-typing; "" = defer (or wait under --wait)
}

// addTypingFlags registers the three guard flags on a flag set.
func addTypingFlags(fs *flag.FlagSet) typingFlags {
	return typingFlags{
		respect:  fs.Duration("respect-typing", -1, "hold the paste until the pane has seen no viewer input for this long (0 = paste now; default: the daemon's [send] respect_typing)"),
		deadline: fs.Duration("typing-deadline", 0, "how long a held delivery waits before it fails loudly and flags the thread (default: [send] respect_typing_deadline)"),
		onTyping: fs.String("on-typing", "", "what a held delivery does: defer (the daemon queues it — default), wait (block here until quiet), skip (refuse loudly, queue nothing)"),
	}
}

// apply stamps the overrides onto a request's guard fields (the two request
// types share them). wait=true makes the default policy "wait".
func (t typingFlags) apply(respectMs, deadlineMs **int, onTyping *string, wait bool) error {
	if *t.respect >= 0 {
		ms := int(*t.respect / time.Millisecond)
		*respectMs = &ms
	}
	if *t.deadline < 0 {
		return fmt.Errorf("--typing-deadline must not be negative")
	}
	if *t.deadline > 0 {
		ms := int(*t.deadline / time.Millisecond)
		*deadlineMs = &ms
	}
	switch *t.onTyping {
	case "", api.OnTypingDefer, api.OnTypingWait, api.OnTypingSkip:
	default:
		return fmt.Errorf("--on-typing %q: want defer, wait or skip", *t.onTyping)
	}
	*onTyping = *t.onTyping
	if wait && *onTyping == "" {
		*onTyping = api.OnTypingWait
	}
	return nil
}

// reportTyping prints a non-sent decision. Returns true when the text landed.
func reportTyping(what string, resp api.ThreadSendResponse) bool {
	switch {
	case resp.Sent != "":
		return true
	case resp.Deferred:
		fmt.Fprintf(os.Stderr, "%s deferred: the pane is in use (viewer input %ds ago) — the daemon delivers it once the pane has been quiet, by %s, else it FLAGS the thread with the undelivered message\n",
			what, resp.InputAgoSec, time.Unix(resp.DeadlineUnix, 0).Format("15:04:05"))
	case resp.Typing:
		fmt.Fprintf(os.Stderr, "%s not sent: the pane is still in use (viewer input %ds ago)\n", what, resp.InputAgoSec)
	}
	return false
}

// sendUntilQuiet drives "wait"-mode sends until the text lands or the deadline
// passes (loud). The daemon caps each call, so this loops like waitLoop.
func sendUntilQuiet(c *client.Client, req api.ThreadSendRequest, timeout time.Duration) (api.ThreadSendResponse, error) {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return api.ThreadSendResponse{}, fmt.Errorf("thread send: the pane stayed in use for %s (a viewer kept typing); not sent", timeout)
		}
		req.TypingWaitMs = int(min(remaining, 8*time.Second) / time.Millisecond)
		resp, err := c.ThreadSendWith(context.Background(), req)
		if err != nil {
			return resp, err
		}
		if resp.Sent != "" {
			return resp, nil
		}
		if !resp.Typing {
			return resp, fmt.Errorf("thread send: unexpected daemon decision (neither sent nor typing): %+v", resp)
		}
	}
}
