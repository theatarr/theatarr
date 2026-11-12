package router

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func h(c string) string { return strings.Repeat(c, 40) }

// fakeAdder records every call. It is the only writer the router gets in these tests, so its call log is the proof
// of what was (not) written.
type fakeAdder struct {
	key, kind string
	mu        sync.Mutex
	adds      []string // hashes
	selects   [][]string
	deletes   []string // ids
	observe   func(context.Context)
	onDelete  func()                          // called after a delete is recorded
	onSelect  func(ctx context.Context) error // called before a selection is recorded; its error is returned
	add       func(m adder.Magnet, o adder.AddOptions) (*adder.Added, error)
}

func (f *fakeAdder) Key() string        { return f.key }
func (f *fakeAdder) Kind() string       { return f.kind }
func (f *fakeAdder) SelectsFiles() bool { return f.kind == provider.KindRealDebrid }

func (f *fakeAdder) AddMagnet(ctx context.Context, m adder.Magnet, o adder.AddOptions) (*adder.Added, error) {
	f.mu.Lock()
	f.adds = append(f.adds, m.Hash())
	f.mu.Unlock()
	if f.observe != nil {
		f.observe(ctx)
	}
	if f.add != nil {
		return f.add(m, o)
	}
	return &adder.Added{ID: f.key + "-" + m.Hash()[:4], Hash: m.Hash()}, nil
}

func (f *fakeAdder) SelectFiles(ctx context.Context, id string, ids []string) error {
	if !f.SelectsFiles() {
		return adder.ErrNoSelection
	}
	if f.onSelect != nil {
		if err := f.onSelect(ctx); err != nil {
			return err
		}
	}
	f.mu.Lock()
	f.selects = append(f.selects, append([]string{id}, ids...))
	f.mu.Unlock()
	return nil
}

// DeleteOwnUncachedAdd makes the fake a Deleter, so a delete the router sends is counted as a write too.
func (f *fakeAdder) DeleteOwnUncachedAdd(_ context.Context, a *adder.Added, _ adder.OwnAdd) error {
	f.mu.Lock()
	f.deletes = append(f.deletes, a.ID)
	f.mu.Unlock()
	if f.onDelete != nil {
		f.onDelete()
	}
	return nil
}

func (f *fakeAdder) writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.adds) + len(f.selects) + len(f.deletes)
}

func refuse(code adder.Code) func(adder.Magnet, adder.AddOptions) (*adder.Added, error) {
	return func(adder.Magnet, adder.AddOptions) (*adder.Added, error) {
		return nil, &adder.Error{Provider: "x", Code: code, Status: 400}
	}
}

// fakeProvider is a read-only provider with torrents by id.
type fakeProvider struct {
	key, kind  string
	mu         sync.Mutex
	torrents   map[string]*provider.Torrent
	cache      map[string]bool // nil: no cache check
	checked    []provider.CacheQuery
	list       []provider.Torrent // what Torrents returns (the account's list)
	listErr    error
	listReads  int
	beforeList func()
	onList     func(ctx context.Context)            // called before each Torrents read, outside p.mu
	read       func(ctx context.Context, id string) // called before each Torrent read, outside p.mu
	readErr    map[string]error                     // a Torrent read of these ids fails with the error
}

func (p *fakeProvider) Key() string  { return p.key }
func (p *fakeProvider) Kind() string { return p.kind }
func (p *fakeProvider) Account(context.Context) (*provider.Account, error) {
	return &provider.Account{}, nil
}
func (p *fakeProvider) Torrents(ctx context.Context) ([]provider.Torrent, error) {
	if p.beforeList != nil {
		p.beforeList()
	}
	if p.onList != nil {
		p.onList(ctx)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listReads++
	return append([]provider.Torrent(nil), p.list...), p.listErr
}
func (p *fakeProvider) Torrent(ctx context.Context, id string) (*provider.Torrent, error) {
	if p.read != nil {
		p.read(ctx, id)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.readErr[id]; err != nil {
		return nil, err
	}
	t, ok := p.torrents[id]
	if !ok {
		return nil, provider.ErrNotFound
	}
	c := *t
	return &c, nil
}
func (p *fakeProvider) Cache() provider.CacheSupport {
	return provider.CacheSupport{Supported: p.cache != nil}
}
func (p *fakeProvider) CheckCached(_ context.Context, q provider.CacheQuery) (map[string]bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checked = append(p.checked, q)
	out := map[string]bool{}
	for _, h := range q.Hashes {
		out[h] = p.cache[h]
	}
	return out, nil
}

func (p *fakeProvider) set(id string, t provider.Torrent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.torrents == nil {
		p.torrents = map[string]*provider.Torrent{}
	}
	t.ID = id
	p.torrents[id] = &t
}

func testPolicy(enabled bool, mode policy.Mode) *policy.Policy {
	return &policy.Policy{
		Indexers: map[string]*policy.Indexer{
			"pub":  {Kind: "torznab", URL: "http://pub.test/api", Privacy: policy.PrivacyPublic},
			"priv": {Kind: "torznab", URL: "http://priv.test/api", Privacy: policy.PrivacyPrivate},
		},
		Providers: map[string]*policy.Provider{
			"rd": {Kind: provider.KindRealDebrid, Order: 1, URL: "http://rd.test/rest/1.0", APIKeyEnv: "THEATARR_RD_KEY",
				// min_gap off: these tests add to RD back to back on a frozen clock; RD's 20s default has its own
				// tests (gap_test.go).
				Pacing: policy.Pacing{AddsPerMinute: 3, Cooldown: policy.Duration(10 * time.Minute),
					BackoffMax: policy.Duration(2 * time.Hour), MinGap: new(policy.Duration)}},
			"ad": {Kind: provider.KindAllDebrid, Order: 2, URL: "http://ad.test/v4", APIKeyEnv: "THEATARR_AD_KEY",
				Pacing: policy.Pacing{AddsPerMinute: 10, BackoffMax: policy.Duration(2 * time.Hour)}},
			"tb": {Kind: provider.KindTorBox, Order: 3, URL: "http://tb.test/v1", APIKeyEnv: "THEATARR_TB_KEY",
				Pacing: policy.Pacing{AddsPerMinute: 10, UncachedPerHr: 2, BackoffMax: policy.Duration(2 * time.Hour)}},
		},
		Versions: map[string]*policy.VersionPolicy{
			"movie": {Mode: mode, Kind: policy.KindMovie, Storage: policy.StorageDebrid, Indexers: []string{"pub"},
				Providers: []string{"rd", "ad", "tb"}},
			"series": {Mode: mode, Kind: policy.KindSeries, Storage: policy.StorageDebrid, Indexers: []string{"pub"},
				Providers: []string{"rd", "ad", "tb"}},
			"seed": {Mode: mode, Kind: policy.KindMovie, Storage: policy.StorageSeedbox, Indexers: []string{"priv"}},
		},
		Router: &policy.Router{Enabled: enabled},
	}
}

type env struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	pol   *policy.Policy
	r     *Router
	ad    map[string]*fakeAdder
	pr    map[string]*fakeProvider
	clock time.Time
}

func newEnv(t *testing.T, enabled bool, mode policy.Mode) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Movie', 0, 0)`,
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (2, 'series', 'Show', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (10, 1, 'movie', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (20, 2, 'series', 0, 0)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{t: t, ctx: ctx, st: st, pol: testPolicy(enabled, mode), ad: map[string]*fakeAdder{},
		pr: map[string]*fakeProvider{}, clock: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	adders := map[string]adder.Adder{}
	provs := map[string]provider.Provider{}
	for k, pv := range e.pol.Providers {
		e.ad[k] = &fakeAdder{key: k, kind: pv.Kind}
		e.pr[k] = &fakeProvider{key: k, kind: pv.Kind}
		adders[k], provs[k] = e.ad[k], e.pr[k]
	}
	e.r, err = New(Config{Store: st, Policy: e.pol, Adders: adders, Providers: provs,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.clock }})
	if err != nil {
		t.Fatal(err)
	}
	e.r.sleep = func(context.Context, time.Duration) error { return nil }
	return e
}

func (e *env) writes() int {
	n := 0
	for _, a := range e.ad {
		n += a.writes()
	}
	return n
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.Reader().QueryRowContext(e.ctx, q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().ExecContext(e.ctx, q, args...); err != nil {
		e.t.Fatal(err)
	}
}

func cand(hash, title string) Candidate {
	return Candidate{Release: indexer.Release{Indexer: "pub", Kind: "torznab", Title: title, InfoHash: hash,
		Privacy: policy.PrivacyPublic, Size: 10 << 30}, Quality: "WEBDL-2160p", Score: 100}
}

func movieReq(cs ...Candidate) Request {
	return Request{TitleID: 1, VersionID: 10, Version: "movie", Candidates: cs, Source: "test"}
}

// grabReq is movieReq as a person's grab (manualSource): for tests that route several unrelated releases of the one
// test movie in a row, which the queue check (queue.go) would otherwise hold back as in flight.
func grabReq(cs ...Candidate) Request {
	req := movieReq(cs...)
	req.Source = "ui:grab:test"
	return req
}

func steps(res *Result) string {
	var b strings.Builder
	for _, s := range res.Steps {
		fmt.Fprintf(&b, "%s/%s/%s/%s; ", s.Hash[:min(4, len(s.Hash))], s.Provider, s.Outcome, s.Code)
	}
	return b.String()
}

func slogTo(w io.Writer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }
