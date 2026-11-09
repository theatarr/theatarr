// Package routertest gives other packages' tests a real router whose only writers are fake adders that log every
// call, so a test can prove what was (not) written to a provider. Test code only.
package routertest

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// Adder is a fake debrid writer: it accepts every add (or refuses it as not cached, SetUncached) and logs it.
type Adder struct {
	key, kind string
	mu        sync.Mutex
	adds      []string
	selects   int
	uncached  bool
}

// SetUncached makes every later add refused as not cached (on) or accepted (off). A refused add is still logged.
func (f *Adder) SetUncached(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uncached = on
}

func (f *Adder) Key() string        { return f.key }
func (f *Adder) Kind() string       { return f.kind }
func (f *Adder) SelectsFiles() bool { return false }

// AddMagnet logs the add and accepts it, or refuses it as not cached (SetUncached).
func (f *Adder) AddMagnet(_ context.Context, m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adds = append(f.adds, m.Hash())
	if f.uncached {
		return nil, &adder.Error{Provider: f.key, Code: adder.CodeNotCached, Status: 400}
	}
	return &adder.Added{ID: f.key + "-" + m.Hash()[:6], Hash: m.Hash()}, nil
}

// SelectFiles logs the call.
func (f *Adder) SelectFiles(context.Context, string, []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selects++
	return adder.ErrNoSelection
}

// Adds are the hashes added, in order.
func (f *Adder) Adds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.adds...)
}

// Fake is a router over fake adders, one per provider of the policy.
type Fake struct {
	*router.Router
	Adders map[string]*Adder
}

// Writes counts every call to every fake adder: adds and file selections.
func (f *Fake) Writes() int {
	n := 0
	for _, a := range f.Adders {
		a.mu.Lock()
		n += len(a.adds) + a.selects
		a.mu.Unlock()
	}
	return n
}

// New builds the router for st and pol with a fake adder per provider; q, when set, takes its route jobs (register
// them on w with RegisterJobs).
func New(st *store.Store, pol *policy.Policy, q *jobs.Queue, w *jobs.Worker, now func() time.Time) (*Fake, error) {
	f := &Fake{Adders: map[string]*Adder{}}
	adders := map[string]adder.Adder{}
	for k, pv := range pol.Providers {
		f.Adders[k] = &Adder{key: k, kind: pv.Kind}
		adders[k] = f.Adders[k]
	}
	r, err := router.New(router.Config{Store: st, Policy: pol, Adders: adders, Providers: map[string]provider.Provider{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: now})
	if err != nil {
		return nil, err
	}
	if q != nil && w != nil {
		r.RegisterJobs(w, q)
	}
	f.Router = r
	return f, nil
}
