package daemon

// Subscriptions (PARITY_ROADMAP C3, v1's turn-delivery engine on v2's
// eventer): when a SUBSCRIBEE this daemon OWNS finishes a turn (busy→idle,
// observed by the eventer), its latest reply is delivered into every
// subscriber thread. Delivery is owner-side (reading the reply needs the
// local transcript — D0); a subscriber on ANOTHER machine is reached through
// the routed send below. The ported v1 Tracker guards every edge: dedup on
// the monotone reply count (a redundant idle or daemon restart never
// re-delivers — counts persist in the store) and a circuit breaker for
// --allow-cycle loops. Cycles are refused at subscribe time by walking the
// edge graph.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/subscribe"
)

const (
	subMaxPerWindow = 6
	subWindow       = time.Minute
)

// seedSubTracker restores per-edge last-delivered counts at daemon start.
func (d *Daemon) seedSubTracker() {
	d.subTracker = subscribe.NewTracker(subMaxPerWindow, subWindow)
	subs, err := d.store.AllSubscriptions()
	if err != nil {
		log.Printf("subscriptions: seed failed: %v", err)
		return
	}
	for _, s := range subs {
		d.subTracker.Seed(s.Subscriber, s.Subscribee, s.LastCount)
	}
}

// deliverSubscriptions runs on a busy→idle edge of a thread THIS daemon owns.
func (d *Daemon) deliverSubscriptions(snap api.ThreadSnapshot) {
	if snap.Machine != d.cfg.Machine {
		return // owner-side only: the reply lives on the owner's disk
	}
	subs, err := d.store.SubscribersOf(snap.ID)
	if err != nil || len(subs) == 0 {
		return
	}
	homes := agents.ResolveHomes(d.cfg.CodexHome)
	reply, count, err := agents.LastReply(agents.Kind(snap.AgentKind), snap.AgentSessionID, snap.Cwd, homes)
	if err != nil || reply == "" {
		if err != nil {
			log.Printf("subscriptions: %s last-reply: %v", snap.ID, err)
		}
		return
	}
	text := subscribe.FormatDelivery(snap.ID, snap.Name, reply)
	for _, sub := range subs {
		switch d.subTracker.ShouldDeliver(sub.Subscriber, snap.ID, count, time.Now()) {
		case subscribe.Deliver:
			d.store.SetSubscriptionCount(sub.Subscriber, snap.ID, count) //nolint:errcheck
			subscriber := sub.Subscriber
			go d.deliverTo(subscriber, snap.ID, text)
		case subscribe.RateLimit:
			log.Printf("subscriptions: %s→%s rate-limited (possible loop; created with --allow-cycle)", snap.ID, sub.Subscriber)
		}
	}
}

// deliverTo sends the delivery text into the subscriber thread: a live pane
// gets a pane send; a headless·idle subscriber gets a headless turn; a
// headless·busy one is skipped LOUDLY (this turn's dedup already recorded —
// documented behavior, not a retry queue). A subscriber on another machine is
// reached via the routed CLI (the same path every cross-machine op uses).
func (d *Daemon) deliverTo(subscriberID, subscribeeID, text string) {
	th, err := d.store.GetThread(subscriberID)
	if err == nil {
		// Local subscriber: pane send if live, else headless turn.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if derr := d.sendIntoThread(ctx, th, text); derr != nil {
			log.Printf("subscriptions: delivery %s→%s: %v", subscribeeID, subscriberID, derr)
		}
		return
	}
	// Not local: the subscriber lives on a peer — find its owner in the mesh
	// cache and route a send (pane first, headless turn as the idle fallback;
	// both failing is loud in the log).
	machine := d.peerMachineOf(subscriberID)
	if machine == "" {
		log.Printf("subscriptions: delivery %s→%s: subscriber not found anywhere on the mesh", subscribeeID, subscriberID)
		return
	}
	bin, berr := os.Executable()
	if berr != nil {
		log.Printf("subscriptions: routed delivery: %v", berr)
		return
	}
	if out, rerr := exec.Command(bin, "thread", "send", "--id", subscriberID, "--text", text, "--machine", machine).CombinedOutput(); rerr != nil {
		if out2, rerr2 := exec.Command(bin, "thread", "send-headless", "--id", subscriberID, "--text", text, "--machine", machine).CombinedOutput(); rerr2 != nil {
			log.Printf("subscriptions: routed delivery %s→%s on %s failed both ways: send: %v %s; send-headless: %v %s",
				subscribeeID, subscriberID, machine, rerr, out, rerr2, out2)
		}
	}
}

// peerMachineOf finds which peer machine owns a thread (from the mesh cache).
func (d *Daemon) peerMachineOf(id string) string {
	rows, err := d.store.LoadPeerSnapshots()
	if err != nil {
		return ""
	}
	for _, r := range rows {
		var threads []api.ThreadSnapshot
		if json.Unmarshal([]byte(r.Payload), &threads) != nil {
			continue
		}
		for _, th := range threads {
			if th.ID == id {
				return r.Machine
			}
		}
	}
	return ""
}

// sendIntoThread delivers text to a local thread by its runtime state.
func (d *Daemon) sendIntoThread(ctx context.Context, th api.Thread, text string) error {
	head, busy, err := d.resolveState(th)
	if err != nil {
		return err
	}
	switch {
	case head == api.Headful:
		loc, found, ferr := d.tmux.FindPaneByThreadID(th.ID)
		if ferr != nil || !found {
			return fmt.Errorf("subscriber pane vanished: %v", ferr)
		}
		return d.tmux.SendText(loc.Pane, text, true)
	case busy == api.BusyIdle:
		// A headless turn through our OWN send-headless endpoint — the exact
		// same gates (live-pane 409, overlap 409, codex discovery) apply.
		return client.New(d.cfg.SocketPath()).ThreadSendHeadless(ctx, th.ID, text)
	default:
		return fmt.Errorf("subscriber %s is headless·busy — delivery skipped (dedup recorded)", th.ID)
	}
}

// --- API ---

func (d *Daemon) routesSubscriptions(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/subscriptions", d.handleSubscribe)
	mux.HandleFunc("POST /v1/subscriptions/remove", d.handleUnsubscribe)
	mux.HandleFunc("GET /v1/subscriptions", d.handleSubscriptionsList)
}

func (d *Daemon) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	var req api.SubscribeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Subscriber == "" || req.Subscribee == "" {
		writeError(w, http.StatusBadRequest, "subscribe: subscriber and subscribee are required")
		return
	}
	if req.Subscriber == req.Subscribee {
		writeError(w, http.StatusBadRequest, "subscribe: a thread cannot subscribe to itself")
		return
	}
	// The SUBSCRIBEE must be local (delivery is owner-side); the subscriber may
	// live anywhere (delivery routes).
	if _, err := d.store.GetThread(req.Subscribee); err != nil {
		writeError(w, http.StatusNotFound, "subscribe: subscribee "+req.Subscribee+" is not on this machine (subscribe on its owner)")
		return
	}
	// Cycle guard: would subscriber's turns (transitively) deliver back into
	// subscribee? Walk edges from subscribee-as-subscriber upward.
	if !req.AllowCycle {
		if cyc, err := d.subWouldCycle(req.Subscriber, req.Subscribee); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		} else if cyc {
			writeError(w, http.StatusBadRequest, "subscribe: would create a delivery cycle (pass --allow-cycle to permit; the circuit breaker caps runaway loops)")
			return
		}
	}
	if err := d.store.AddSubscription(req.Subscriber, req.Subscribee); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "subscriber": req.Subscriber, "subscribee": req.Subscribee})
}

// subWouldCycle reports whether adding subscriber←subscribee closes a loop:
// i.e. subscribee already (transitively) receives subscriber's turns.
func (d *Daemon) subWouldCycle(subscriber, subscribee string) (bool, error) {
	seen := map[string]bool{}
	frontier := []string{subscribee}
	for len(frontier) > 0 {
		cur := frontier[0]
		frontier = frontier[1:]
		if cur == subscriber {
			return true, nil
		}
		if seen[cur] {
			continue
		}
		seen[cur] = true
		subs, err := d.store.SubscriptionsOf(cur)
		if err != nil {
			return false, err
		}
		for _, s := range subs {
			if s.Subscriber == cur {
				frontier = append(frontier, s.Subscribee)
			}
		}
	}
	return false, nil
}

func (d *Daemon) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var req api.SubscribeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := d.store.RemoveSubscription(req.Subscriber, req.Subscribee); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "removed": true})
}

func (d *Daemon) handleSubscriptionsList(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	rows, err := d.store.SubscriptionsOf(id)
	if id == "" {
		rows, err = d.store.AllSubscriptions()
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := api.SubscriptionsResponse{Schema: api.SchemaVersion, Subscriptions: []api.SubscriptionInfo{}}
	for _, s := range rows {
		out.Subscriptions = append(out.Subscriptions, api.SubscriptionInfo{Subscriber: s.Subscriber, Subscribee: s.Subscribee})
	}
	writeJSON(w, http.StatusOK, out)
}
