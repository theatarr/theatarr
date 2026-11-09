// Package logring keeps the most recent log records in memory for GET /api/theatarr/v1/logs (Admin › Advanced › Logs).
//
// [Ring.Handler] wraps the service's slog handler: every record still goes to the wrapped handler unchanged, and a
// copy (time, level, message, component, attributes as strings) goes into a fixed-size ring. A restart empties it.
// Attributes whose key names a secret (password, token, secret, key, authorization, cookie) are stored as
// "[redacted]", so the first-start admin password, logged once at warn level, never reaches the API.
package logring

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Entry is one log record.
type Entry struct {
	Seq       int64             `json:"seq"`
	Time      time.Time         `json:"time"`
	Level     string            `json:"level"`
	Component string            `json:"component,omitempty"`
	Message   string            `json:"msg"`
	Attrs     map[string]string `json:"attrs,omitempty"`
}

// Ring holds the last N entries.
type Ring struct {
	mu      sync.Mutex
	buf     []Entry
	next    int
	full    bool
	seq     int64
	bootID  string
	dropped map[logKind]int64
	// onError hears every error-level entry (the error inbox, internal/errinbox), after the secret-named
	// attributes are redacted.
	onError atomic.Pointer[func(context.Context, Entry)]
}

type logKind struct{ level, component string }

// New returns a ring of size n (at least 1).
func New(n int) *Ring {
	if n < 1 {
		n = 1
	}
	return &Ring{buf: make([]Entry, n), bootID: rand.Text(), dropped: map[logKind]int64{}}
}

func (r *Ring) add(e Entry) {
	r.mu.Lock()
	if r.full {
		old := r.buf[r.next]
		r.dropped[logKind{old.Level, old.Component}]++
	}
	r.seq++
	e.Seq = r.seq
	r.buf[r.next] = e
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

// Filter selects entries. Zero values select everything.
type Filter struct {
	// Level is the minimum level.
	Level slog.Level
	// Component matches exactly when set.
	Component string
	// Since keeps entries after this time.
	Since time.Time
	// After keeps entries with a larger sequence number (for polling).
	After int64
	// Seen is the matching-entry count from the previous page, for the same filters.
	Seen *int64
	// Limit keeps the newest Limit entries; 0 keeps all.
	Limit int
}

// snapshot copies entries and the high-water mark under one lock: concurrent writes belong to the next poll.
func (r *Ring) snapshot() ([]Entry, int64, map[logKind]int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []Entry
	if r.full {
		all = append(all, r.buf[r.next:]...)
	}
	all = append(all, r.buf[:r.next]...)
	dropped := make(map[logKind]int64, len(r.dropped))
	for kind, n := range r.dropped {
		dropped[kind] = n
	}
	return all, r.seq, dropped
}

func matching(all []Entry, f Filter) []Entry {
	out := make([]Entry, 0, len(all))
	for _, e := range all {
		if e.Seq <= f.After || (!f.Since.IsZero() && !e.Time.After(f.Since)) {
			continue
		}
		if !admitted(e.Level, e.Component, f) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func admitted(level, component string, f Filter) bool {
	if f.Component != "" && component != f.Component {
		return false
	}
	var l slog.Level
	return l.UnmarshalText([]byte(level)) != nil || l >= f.Level
}

// Recent returns the matching entries, oldest first, retaining the newest Limit entries.
func (r *Ring) Recent(f Filter) []Entry {
	all, _, _ := r.snapshot()
	out := matching(all, f)
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[len(out)-f.Limit:]
	}
	return out
}

// Page is a cursor page. After is the next poll's cursor, including scanned entries excluded by the filters.
// More asks the caller to drain another page immediately. Skipped counts matching entries overwritten since its cursor;
// those lines may still be in the persistent log files. BootID identifies this ring's lifetime, even when empty.
type Page struct {
	Items   []Entry `json:"items"`
	BootID  string  `json:"boot_id"`
	After   int64   `json:"after"`
	More    bool    `json:"more"`
	Skipped int64   `json:"skipped"`
	Seen    int64   `json:"seen"`
}

// Poll returns the oldest matching entries after the cursor, so bursts larger than Limit can be drained.
// An old boot id resets the cursor before filtering; wall-clock changes cannot strand a polling client.
func (r *Ring) Poll(f Filter, bootID string) Page {
	if bootID != "" && bootID != r.bootID {
		f.After, f.Since, f.Seen = 0, time.Time{}, nil
	}
	all, high, dropped := r.snapshot()
	p := Page{BootID: r.bootID, After: high}
	// Count only evicted records admitted by this viewer's level/component. The count cursor avoids
	// retaining an unbounded history of overwritten log metadata.
	var lost int64
	for kind, n := range dropped {
		if admitted(kind.level, kind.component, f) {
			lost += n
		}
	}
	if f.After > 0 && f.Seen != nil && f.Since.IsZero() {
		p.Skipped = max(0, lost-*f.Seen)
	}
	p.Items = matching(all, f)
	if f.Limit > 0 && len(p.Items) > f.Limit {
		p.Items = p.Items[:f.Limit]
		p.After = p.Items[len(p.Items)-1].Seq
		p.More = true
	}
	p.Seen = lost
	for _, e := range matching(all, Filter{Level: f.Level, Component: f.Component}) {
		if e.Seq <= p.After {
			p.Seen++
		}
	}
	return p
}

// Components lists the components seen in the ring, sorted.
func (r *Ring) Components() []string {
	seen := map[string]bool{}
	for _, e := range r.Recent(Filter{Level: slog.LevelDebug - 10}) {
		if e.Component != "" {
			seen[e.Component] = true
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// OnError sets the function that hears every error-level record as its ring entry, with the record's context (the
// error inbox, internal/errinbox). It runs in the logging goroutine, so it must not block or log at error level.
func (r *Ring) OnError(fn func(ctx context.Context, e Entry)) {
	if fn == nil {
		r.onError.Store(nil)
		return
	}
	r.onError.Store(&fn)
}

// Handler wraps next so every record it handles is also kept in the ring. Records below next's level are not kept.
func (r *Ring) Handler(next slog.Handler) slog.Handler {
	return &handler{ring: r, next: next}
}

type handler struct {
	ring   *Ring
	next   slog.Handler
	attrs  []slog.Attr // from WithAttrs, already prefixed with the group
	groups []string
	// component is the top-level "component" attribute from WithAttrs.
	component string
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *handler) Handle(ctx context.Context, rec slog.Record) error {
	e := Entry{Time: rec.Time, Level: rec.Level.String(), Message: rec.Message, Component: h.component}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	put := func(key string, v slog.Value) {
		if e.Attrs == nil {
			e.Attrs = map[string]string{}
		}
		if SecretKey(key) {
			e.Attrs[key] = "[redacted]"
			return
		}
		s := v.Resolve().String()
		if len(s) > 2000 {
			s = s[:2000] + "…"
		}
		e.Attrs[key] = s
	}
	for _, a := range h.attrs {
		flatten("", a, put)
	}
	prefix := strings.Join(h.groups, ".")
	rec.Attrs(func(a slog.Attr) bool {
		if prefix == "" && a.Key == "component" {
			e.Component = a.Value.String()
			return true
		}
		flatten(prefix, a, put)
		return true
	})
	h.ring.add(e)
	if rec.Level >= slog.LevelError {
		if fn := h.ring.onError.Load(); fn != nil {
			(*fn)(ctx, e)
		}
	}
	return h.next.Handle(ctx, rec)
}

func flatten(prefix string, a slog.Attr, put func(string, slog.Value)) {
	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		for _, g := range v.Group() {
			flatten(key, g, put)
		}
		return
	}
	if a.Key == "" {
		return
	}
	put(key, v)
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	n := *h
	n.next = h.next.WithAttrs(as)
	prefix := strings.Join(h.groups, ".")
	n.attrs = append([]slog.Attr(nil), h.attrs...)
	for _, a := range as {
		if prefix == "" && a.Key == "component" {
			n.component = a.Value.String()
			continue
		}
		if prefix != "" {
			a = slog.Attr{Key: prefix + "." + a.Key, Value: a.Value}
		}
		n.attrs = append(n.attrs, a)
	}
	return &n
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	n := *h
	n.next = h.next.WithGroup(name)
	n.groups = append(append([]string(nil), h.groups...), name)
	return &n
}

var secretWords = []string{"password", "passwd", "token", "secret", "authorization", "cookie", "apikey", "api_key"}

// SecretKey reports whether an attribute named k holds a secret; its value is stored as "[redacted]".
func SecretKey(k string) bool {
	k = strings.ToLower(k)
	if k == "key" || strings.HasSuffix(k, ".key") || strings.HasSuffix(k, "_key") {
		return true
	}
	for _, w := range secretWords {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

// String is for debugging.
func (e Entry) String() string {
	return fmt.Sprintf("%s %s %s %s", e.Time.Format(time.RFC3339), e.Level, e.Component, e.Message)
}
