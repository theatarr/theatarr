// Package errinbox is Theatarr's error inbox (docs/design/errors.md): every unexpected failure while theatarr serve runs
// lands in one durable, deduplicated list (table error_event) that people and tools read through the API,
// the CLI (theatarr errors) and MCP.
//
// Sources:
//   - log: error-level log records, teed from the log ring's handler (logring.Ring.OnError); log output is unchanged.
//   - panic: panics recovered in HTTP handlers, job handlers, serve workers and health checks, with their stack.
//   - http: 5xx answers of Theatarr's own handlers other than 502, 503 and 504 (method, route pattern, status).
//   - job: jobs that failed for good (their last attempt or a permanent error).
//   - health: a health check turning error, resolved again automatically when it recovers.
//
// Expected outcomes stay out: a record logged with a [Quiet] context, context cancellation, 4xx answers and job
// retries. Sightings group by fingerprint (source, component, normalized message, top in-repo frame for a panic);
// a resolved fingerprint that recurs is new again. Every string is redacted before it is stored.
//
// Reporting never blocks and never fails the caller: events go through a bounded queue to one writer goroutine that
// writes them in batches. When the queue is full the event is dropped and counted. A failed write is logged at warn
// level with a quiet context, so it cannot come back into the inbox.
package errinbox

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync/atomic"
	"time"
)

// Sources.
const (
	SourceLog    = "log"
	SourcePanic  = "panic"
	SourceHTTP   = "http"
	SourceJob    = "job"
	SourceHealth = "health"
)

// Statuses.
const (
	StatusNew      = "new"
	StatusSeen     = "seen"
	StatusResolved = "resolved"
	StatusIgnored  = "ignored"
)

// Sources and Statuses list the valid values, for validation.
var (
	Sources  = []string{SourceLog, SourcePanic, SourceHTTP, SourceJob, SourceHealth}
	Statuses = []string{StatusNew, StatusSeen, StatusResolved, StatusIgnored}
)

// Event is one sighting, before redaction and normalization.
type Event struct {
	Source    string
	Component string
	// Level is the log level for log events; "error" otherwise.
	Level string
	// Message is the raw message; its normalized form is the grouping key, the redacted raw text the sample.
	Message string
	// Context is extra detail: log attributes, the request route, job kind and id, title or version.
	Context map[string]string
	// Stack is a panic's stack (debug.Stack()).
	Stack string
	// Key, when set, joins the grouping key: a source that raises one item per thing (the now playing pre-warm, per
	// episode) sets it, so things whose messages normalize alike stay apart.
	Key  string
	Time time.Time
}

type quietKey struct{}

// Quiet marks a context whose error-level log records stay out of the inbox: an expected outcome another source
// already records (a 5xx request line, a VPN egress change the health check reports) or the inbox's own failures.
// Log output is unchanged.
func Quiet(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, quietKey{}, true)
}

// IsQuiet reports whether ctx was marked by Quiet.
func IsQuiet(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	q, _ := ctx.Value(quietKey{}).(bool)
	return q
}

var global atomic.Pointer[Inbox]

// Install makes in the process's inbox, the one Report, Panic and Crash use; nil uninstalls it. Only theatarr serve
// installs one, so the other commands record nothing.
func Install(in *Inbox) { global.Store(in) }

// Installed is the process's inbox, or nil.
func Installed() *Inbox { return global.Load() }

// Report queues ev on the installed inbox; without one it does nothing. It never blocks.
func Report(ev Event) {
	if in := global.Load(); in != nil {
		in.Report(ev)
	}
}

// Panic records a recovered panic value with the stack of the recovering goroutine. Call it from the deferred
// function that called recover(), so the stack still shows where the panic happened.
func Panic(component string, p any, context map[string]string) {
	Report(panicEvent(component, p, context))
}

// Crash records a panic synchronously before the caller lets the process crash (a top-level loop of serve whose
// panic must still end the process): it writes the event itself, waiting at most two seconds.
func Crash(component string, p any) {
	in := global.Load()
	if in == nil {
		return
	}
	ctx, cancel := context.WithTimeout(Quiet(context.Background()), 2*time.Second)
	defer cancel()
	_ = in.write(ctx, []item{{ev: panicEvent(component, p, nil)}})
}

// Go wraps a top-level goroutine of serve: a panic in fn is recorded with Crash and then panics again, so the
// process still crashes (the container restarts it) and the inbox has the stack.
func Go(component string, fn func()) func() {
	return func() {
		defer func() {
			if p := recover(); p != nil {
				Crash(component, p)
				panic(p)
			}
		}()
		fn()
	}
}

func panicEvent(component string, p any, ctx map[string]string) Event {
	return Event{Source: SourcePanic, Component: component, Level: "error", Message: fmt.Sprintf("panic: %v", p),
		Context: ctx, Stack: string(debug.Stack()), Time: time.Now()}
}
