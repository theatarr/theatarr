package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/store"
)

// The event stream, GET /api/theatarr/v1/events (Server-Sent Events). Events:
//
//	jobs     {"counts": {...}, "changed": [Job...]}   the queue counts and the jobs that changed since the last event
//	gateway  GatewayState                              egress paths, budgets and back-offs, when any of them changed
//	request  RequestEvent                              a request changed (requests.go); "mine" in a requester's copy
//	users    {}                                        the user list changed (a new Plex account waits)
//
// Each event names who may get it (event.For): jobs and gateway go to admins only, request events to the requesters
// and to managers, users events to those with the users permission; download events (activity_events.go) carry no file
// detail and go to those who see files and to the requesters of the title; title events go to everyone signed in.
//
// The first message of each kind is a snapshot sent on connect. Nothing in jobs or the gateway publishes changes, so
// a poller looks for them every PollInterval, and only while someone listens. A ": ping" comment every 25 s keeps
// proxies from closing an idle stream.

type event struct {
	name string
	data []byte
	// need is the permission a listener needs to get the event (0: anyone signed in).
	need auth.Perm
	// toUsers limits the event to the accounts in users, and to listeners holding orPerm (when set).
	toUsers bool
	users   []int64
	orPerm  auth.Perm
	// mine, when set, is the payload for listeners in users (it marks the event as theirs); others get data.
	mine []byte
}

// payload is the data a listener gets.
func (e event) payload(id *auth.Identity) []byte {
	if e.mine != nil && id != nil && slices.Contains(e.users, id.UserID) {
		return e.mine
	}
	return e.data
}

// For reports whether a listener with this identity and these permissions gets e.
func (e event) For(id *auth.Identity, p Perms) bool {
	if !p.Has(e.need) {
		return false
	}
	if !e.toUsers {
		return true
	}
	if e.orPerm != 0 && p.Has(e.orPerm) {
		return true
	}
	return id != nil && slices.Contains(e.users, id.UserID)
}

type hub struct {
	mu   sync.Mutex
	subs map[chan event]struct{}
	done chan struct{}
	once sync.Once
}

func newHub() *hub { return &hub{subs: map[chan event]struct{}{}, done: make(chan struct{})} }

func (h *hub) subscribe() chan event {
	ch := make(chan event, 16)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan event) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// publish sends e to every subscriber. A subscriber whose buffer is full misses it; the next snapshot catches it up.
func (h *hub) publish(e event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func (h *hub) close() { h.once.Do(func() { close(h.done) }) }

func (w *Web) runEvents(ctx context.Context) {
	defer w.hub.close()
	t := time.NewTicker(w.poll)
	defer t.Stop()
	var jobCursor int64 = -1
	var lastCounts, lastGatewayComparable []byte
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if w.hub.count() == 0 {
			jobCursor, lastCounts, lastGatewayComparable = -1, nil, nil
			continue
		}
		if jobCursor < 0 {
			// Take a baseline and send nothing: the snapshot on connect already covers the present.
			counts, err1 := w.jobCounts(ctx)
			gs, err2 := w.gatewayState(ctx)
			if err1 != nil || err2 != nil {
				continue
			}
			jobCursor = store.Millis(w.now())
			lastCounts, _ = json.Marshal(counts)
			lastGatewayComparable = stripAges(gatewayJSON(gs))
			w.polls.Add(1)
			continue
		}
		changed, next, err := w.jobsChangedSince(ctx, jobCursor)
		if err != nil {
			w.log.Warn("events: jobs", "err", err)
			continue
		}
		jobCursor = next
		jc, err := w.jobCounts(ctx)
		if err != nil {
			w.log.Warn("events: job counts", "err", err)
			continue
		}
		counts, _ := json.Marshal(jc)
		if len(changed) > 0 || !bytes.Equal(counts, lastCounts) {
			lastCounts = counts
			if changed == nil {
				changed = []Job{}
			}
			data, _ := json.Marshal(map[string]any{"counts": jc, "changed": changed})
			w.hub.publish(event{name: "jobs", data: data, need: auth.PermAdmin})
		}
		gs, err := w.gatewayState(ctx)
		if err != nil {
			w.log.Warn("events: gateway", "err", err)
			continue
		}
		data := gatewayJSON(gs)
		comparable := stripAges(data)
		if !bytes.Equal(comparable, lastGatewayComparable) {
			lastGatewayComparable = comparable
			w.hub.publish(event{name: "gateway", data: data, need: auth.PermAdmin})
		}
		w.polls.Add(1)
	}
}

func gatewayJSON(gs GatewayState) []byte {
	b, _ := json.Marshal(gs)
	return b
}

// stripAges removes the fields that change on every tick (check ages), so they alone send no event.
func stripAges(b []byte) []byte {
	if b == nil {
		return nil
	}
	var gs GatewayState
	if json.Unmarshal(b, &gs) != nil {
		return b
	}
	if paths, ok := gs.Egress["paths"].([]any); ok {
		for _, p := range paths {
			if m, ok := p.(map[string]any); ok {
				delete(m, "age_s")
			}
		}
	}
	out, _ := json.Marshal(gs)
	return out
}

// eventPing is how often the event stream pings and rechecks its credential; eventWriteTimeout bounds each write.
var (
	eventPing         = 25 * time.Second
	eventWriteTimeout = 30 * time.Second
)

func (w *Web) apiEvents(rw http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(rw)
	h := rw.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	rw.WriteHeader(http.StatusOK)
	ch := w.hub.subscribe()
	defer w.hub.unsubscribe(ch)
	viewer := auth.FromContext(r.Context())
	perms := w.perms(viewer)

	// Every write has a deadline, so a client that stops reading cannot hold the handler (and its hub slot) forever.
	write := func(format string, args ...any) error {
		if err := rc.SetWriteDeadline(time.Now().Add(eventWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		if _, err := fmt.Fprintf(rw, format, args...); err != nil {
			return err
		}
		return rc.Flush()
	}
	send := func(e event) error { return write("event: %s\ndata: %s\n\n", e.name, e.data) }
	if write("retry: 5000\n\n") != nil {
		return
	}
	ctx := r.Context()
	// The jobs and gateway snapshots are the admin panel's; other accounts get neither.
	if perms.Admin {
		if counts, err := w.jobCounts(ctx); err == nil {
			data, _ := json.Marshal(map[string]any{"counts": counts, "changed": []Job{}})
			if send(event{name: "jobs", data: data}) != nil {
				return
			}
		}
		if gs, err := w.gatewayState(ctx); err == nil {
			if send(event{name: "gateway", data: gatewayJSON(gs)}) != nil {
				return
			}
		}
	}
	ping := time.NewTicker(eventPing)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.hub.done:
			return
		case e := <-ch:
			if !e.For(viewer, perms) {
				continue
			}
			e.data = e.payload(viewer)
			if send(e) != nil {
				return
			}
		case <-ping.C:
			// The stream follows its credential: a revoked key, an ended session or a disabled account ends it (the
			// client reconnects and is refused), and changed permissions apply from the next event.
			id, err := w.auth.Authenticate(r)
			if err != nil || id == nil {
				return
			}
			viewer, perms = id, w.perms(id)
			if write(": ping\n\n") != nil {
				return
			}
		}
	}
}

func retryAfter(t *auth.Throttled, now time.Time) string {
	s := int(t.Until.Sub(now).Seconds()) + 1
	return strconv.Itoa(max(s, 1))
}
