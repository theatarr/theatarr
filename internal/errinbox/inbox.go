package errinbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/logring"
	"github.com/theatarr/theatarr/internal/store"
)

// Options tune an Inbox. The zero value is fine.
type Options struct {
	// Version is the build version stored with each sighting (buildinfo.String()).
	Version string
	// QueueSize bounds the events waiting for the writer. Default 1024.
	QueueSize int
	// BatchSize is the most events one write takes. Default 100.
	BatchSize int
	// FlushEvery is how long the writer gathers a batch before writing. Default 1s.
	FlushEvery time.Duration
	// MaxRows caps the table: after a write that adds rows, the oldest resolved and ignored rows go first, then the
	// oldest others. Default 5000.
	MaxRows int
	Logger  *slog.Logger
	Now     func() time.Time
}

// Inbox writes events to error_event.
type Inbox struct {
	st    *store.Store
	opts  Options
	log   *slog.Logger
	queue chan item
	known atomic.Pointer[func(string) string]

	drops    atomic.Int64
	failures atomic.Int64
	written  atomic.Int64

	mu          sync.Mutex
	lastWarn    time.Time
	savedDrops  int64
	health      map[string][]string // check source -> fingerprints of its open failures
	healthRead  bool                // the open health rows of earlier runs are in health
	healthState string              // the last snapshot written, for change detection
	healthAt    time.Time
}

// item is one queued write: an event, or an automatic resolution of a fingerprint (a recovered health check), or a
// state value.
type item struct {
	ev           Event
	resolve      string
	resolveNote  string
	stateKey     string
	stateValue   string
	resolveAfter time.Time
}

// New returns an inbox on st. Run starts its writer.
func New(st *store.Store, opts Options) *Inbox {
	if opts.QueueSize <= 0 {
		opts.QueueSize = 1024
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 100
	}
	if opts.FlushEvery <= 0 {
		opts.FlushEvery = time.Second
	}
	if opts.MaxRows <= 0 {
		opts.MaxRows = 5000
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Inbox{st: st, opts: opts, log: opts.Logger.With("component", "errinbox"),
		queue: make(chan item, opts.QueueSize), health: map[string][]string{}}
}

// SetKnownSecrets installs a function that masks the running settings' known credentials (the gateway's redactor);
// it runs before the pattern rules.
func (in *Inbox) SetKnownSecrets(fn func(string) string) {
	if fn == nil {
		in.known.Store(nil)
		return
	}
	in.known.Store(&fn)
}

// Report queues ev. It never blocks: with the queue full the event is dropped and counted.
func (in *Inbox) Report(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = in.opts.Now()
	}
	in.enqueue(item{ev: ev})
}

func (in *Inbox) enqueue(it item) {
	select {
	case in.queue <- it:
	default:
		in.drops.Add(1)
	}
}

// Drops is the number of events dropped since the start because the queue was full.
func (in *Inbox) Drops() int64 { return in.drops.Load() }

// Failures is the number of batch writes that failed since the start.
func (in *Inbox) Failures() int64 { return in.failures.Load() }

// LogTee is the log ring's OnError hook: error-level records become log events, except quiet ones and context
// cancellation.
func (in *Inbox) LogTee(ctx context.Context, e logring.Entry) {
	if IsQuiet(ctx) {
		return
	}
	msg := e.Message
	for _, k := range []string{"err", "error"} {
		v := e.Attrs[k]
		if v == "" {
			continue
		}
		if v == context.Canceled.Error() || strings.HasSuffix(v, ": "+context.Canceled.Error()) {
			return
		}
		msg += ": " + v
		break
	}
	in.Report(Event{Source: SourceLog, Component: e.Component, Level: strings.ToLower(e.Level), Message: msg,
		Context: e.Attrs, Time: e.Time})
}

// Run writes queued events until ctx ends, then writes what is still queued (for at most five seconds).
func (in *Inbox) Run(ctx context.Context) {
	t := time.NewTicker(in.opts.FlushEvery)
	defer t.Stop()
	var batch []item
	flush := func(ctx context.Context) {
		if len(batch) > 0 {
			in.writeOrWarn(ctx, batch)
			batch = batch[:0]
		}
		in.saveDrops(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(Quiet(context.WithoutCancel(ctx)), 5*time.Second)
		drain:
			for {
				select {
				case it := <-in.queue:
					batch = append(batch, it)
					if len(batch) >= in.opts.BatchSize {
						flush(fctx)
					}
				default:
					break drain
				}
			}
			flush(fctx)
			cancel()
			return
		case it := <-in.queue:
			batch = append(batch, it)
			if len(batch) >= in.opts.BatchSize {
				flush(Quiet(ctx))
			}
		case <-t.C:
			flush(Quiet(ctx))
		}
	}
}

// writeOrWarn writes a batch; a failure drops it, counted, with at most one warning a minute. The warning carries a
// quiet context and is below error level, so the inbox never records its own failures.
func (in *Inbox) writeOrWarn(ctx context.Context, batch []item) {
	err := in.write(ctx, batch)
	if err == nil {
		return
	}
	in.failures.Add(1)
	in.mu.Lock()
	warn := in.opts.Now().Sub(in.lastWarn) >= time.Minute
	if warn {
		in.lastWarn = in.opts.Now()
	}
	in.mu.Unlock()
	if warn {
		in.log.WarnContext(Quiet(ctx), "error inbox write failed; events dropped", "events", len(batch), "err", err)
	}
}

// row is a batch's events folded per fingerprint.
type row struct {
	fp, source, component, level, message, sample, context, stack string
	count                                                         int64
	first, last                                                   int64
}

// prepare redacts, caps and fingerprints an event.
func (in *Inbox) prepare(ev Event) row {
	if ev.Level == "" {
		ev.Level = "error"
	}
	raw := in.Redact(ev.Message)
	norm := Normalize(raw)
	frame := ""
	if ev.Source == SourcePanic {
		frame = TopFrame(ev.Stack)
	} else if ev.Key != "" {
		frame = "key:" + ev.Key
	}
	ctxJSON := "{}"
	if len(ev.Context) > 0 {
		attrs := in.redactAttrs(ev.Context)
		if b, err := json.Marshal(attrs); err == nil {
			ctxJSON = string(b)
		}
		if len(ctxJSON) > maxContext {
			// Drop the longest values until it fits, so the JSON stays valid.
			for len(ctxJSON) > maxContext && len(attrs) > 0 {
				longest := ""
				for k, v := range attrs {
					if longest == "" || len(v) > len(attrs[longest]) {
						longest = k
					}
				}
				if len(attrs[longest]) > 64 {
					attrs[longest] = capString(attrs[longest], len(attrs[longest])/2)
				} else {
					delete(attrs, longest)
				}
				b, _ := json.Marshal(attrs)
				ctxJSON = string(b)
			}
		}
	}
	stack := ev.Stack
	if stack != "" {
		stack = capString(in.Redact(stack), maxStack)
	}
	at := store.Millis(ev.Time)
	return row{fp: Fingerprint(ev.Source, ev.Component, norm, frame), source: ev.Source, component: ev.Component,
		level: ev.Level, message: norm, sample: capString(raw, maxMessage), context: ctxJSON, stack: stack,
		count: 1, first: at, last: at}
}

// write stores a batch in one transaction: events folded per fingerprint and upserted, then resolutions and state.
func (in *Inbox) write(ctx context.Context, batch []item) error {
	ctx = Quiet(ctx)
	rows := map[string]*row{}
	var order []string
	var rest []item
	for _, it := range batch {
		if it.resolve != "" || it.stateKey != "" {
			rest = append(rest, it)
			continue
		}
		r := in.prepare(it.ev)
		if have := rows[r.fp]; have != nil {
			have.count++
			have.first, have.last = min(have.first, r.first), max(have.last, r.last)
			if r.last >= have.last {
				have.sample, have.context = r.sample, r.context
				if r.stack != "" {
					have.stack = r.stack
				}
			}
			continue
		}
		rows[r.fp] = &r
		order = append(order, r.fp)
	}
	now := store.Millis(in.opts.Now())
	version := in.opts.Version
	err := in.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, fp := range order {
			r := rows[fp]
			_, err := tx.ExecContext(ctx, `INSERT INTO error_event (fingerprint, source, component, level, message,
				sample, context, stack, count, first_seen, last_seen, first_version, last_version, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (fingerprint) DO UPDATE SET count = count + excluded.count,
					last_seen = max(last_seen, excluded.last_seen), sample = excluded.sample, context = excluded.context,
					stack = CASE WHEN excluded.stack != '' THEN excluded.stack ELSE stack END,
					level = excluded.level, last_version = excluded.last_version, updated_at = excluded.updated_at,
					status = CASE WHEN status = 'resolved' THEN 'new' ELSE status END,
					resolved_at = CASE WHEN status = 'resolved' THEN NULL ELSE resolved_at END,
					regressions = regressions + (status = 'resolved')`,
				r.fp, r.source, r.component, r.level, r.message, r.sample, r.context, r.stack, r.count, r.first, r.last,
				version, version, now)
			if err != nil {
				return err
			}
		}
		for _, it := range rest {
			switch {
			case it.resolve != "":
				if _, err := tx.ExecContext(ctx, `UPDATE error_event SET status = 'resolved', resolved_at = ?,
					note = CASE WHEN note = '' THEN ? ELSE note || char(10) || ? END, updated_at = ?
					WHERE fingerprint = ? AND status IN ('new', 'seen') AND last_seen <= ?`,
					now, it.resolveNote, it.resolveNote, now, it.resolve, store.Millis(it.resolveAfter)); err != nil {
					return err
				}
			case it.stateKey != "":
				if err := setState(ctx, tx, it.stateKey, it.stateValue, now); err != nil {
					return err
				}
			}
		}
		if len(order) > 0 {
			return capRows(ctx, tx, in.opts.MaxRows)
		}
		return nil
	})
	if err == nil {
		in.written.Add(int64(len(order)))
	}
	return err
}

// capRows deletes rows above max: resolved and ignored ones first, oldest first, then the oldest others.
func capRows(ctx context.Context, tx *sql.Tx, maxRows int) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM error_event WHERE id IN (SELECT id FROM error_event
		ORDER BY status IN ('resolved', 'ignored') DESC, last_seen, id
		LIMIT max(0, (SELECT COUNT(*) FROM error_event) - ?))`, maxRows)
	return err
}

func setState(ctx context.Context, tx *sql.Tx, key, value string, now int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO error_inbox_state (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, now)
	return err
}

// saveDrops persists the dropped-event total when it grew, so the CLI and the debug bundle see it.
func (in *Inbox) saveDrops(ctx context.Context) {
	n := in.drops.Load()
	in.mu.Lock()
	saved := in.savedDrops
	in.mu.Unlock()
	if n == saved {
		return
	}
	err := in.st.WriteTx(Quiet(ctx), func(tx *sql.Tx) error {
		var prev int64
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM error_inbox_state WHERE key = 'drops'`).Scan(&raw); err == nil {
			_ = json.Unmarshal([]byte(raw), &prev)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		b, _ := json.Marshal(prev + n - saved)
		return setState(ctx, tx, "drops", string(b), store.Millis(in.opts.Now()))
	})
	if err == nil {
		in.mu.Lock()
		in.savedDrops = n
		in.mu.Unlock()
	}
}

// Flush writes what is queued now, synchronously, alongside the writer goroutine (tests, and before an exit).
func (in *Inbox) Flush(ctx context.Context) error {
	var batch []item
	for len(batch) < cap(in.queue) {
		select {
		case it := <-in.queue:
			batch = append(batch, it)
			continue
		default:
		}
		break
	}
	if len(batch) == 0 {
		return nil
	}
	return in.write(ctx, batch)
}
