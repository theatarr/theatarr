package gateway

import (
	"context"
	"database/sql"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

func getWith(ctx context.Context, g *Gateway, t Target, url string) error {
	resp, err := g.Get(ctx, t, url)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// flush writes the request counts made since the last flush.
func flush(t *testing.T, g *Gateway) {
	t.Helper()
	if err := g.FlushRequests(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func storedRequests(t *testing.T, st *store.Store) map[string]int {
	t.Helper()
	rows, err := st.Reader().Query(`SELECT indexer, purpose, SUM(count) FROM gateway_indexer_request GROUP BY indexer, purpose`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var ix, p string
		var n int
		if err := rows.Scan(&ix, &p, &n); err != nil {
			t.Fatal(err)
		}
		out[ix+"/"+p] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func wantRequests(t *testing.T, g *Gateway, key string, today, week map[Purpose]int) {
	t.Helper()
	got := g.State().Requests[key]
	if today == nil {
		today = map[Purpose]int{}
	}
	if !maps.Equal(got.Today, today) || !maps.Equal(got.Week, week) {
		t.Fatalf("%s requests: today %v week %v, want today %v week %v", key, got.Today, got.Week, today, week)
	}
}

// Every indexer request sent is counted per indexer, purpose and UTC day; untagged ones as other, other classes not at
// all. The counts survive a restart, roll over at UTC midnight, and are kept 30 days.
func TestIndexerRequestsByPurpose(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	e.serve("tmdb.test", okHandler("ok"))
	p := testPolicy()
	st := openStore(t, filepath.Join(t.TempDir(), "theatarr.db"))
	g := e.gateway(p, st)
	ctx := context.Background()
	g.CheckEgress(ctx)
	rss := WithPurpose(ctx, PurposeRSS)
	for _, c := range []context.Context{ctx, rss, rss, WithPurpose(ctx, PurposeInteractive)} {
		if err := getWith(c, g, dmm, "http://dmm.test/"); err != nil {
			t.Fatal(err)
		}
	}
	// A transport error was still sent; a metadata request is no indexer's.
	if err := getWith(WithPurpose(ctx, PurposeWanted), g, Target{Class: ClassIndexer, Key: "tracker"}, "http://tracker.test/api"); err == nil {
		t.Fatal("tracker.test answered")
	}
	if err := getWith(rss, g, Target{Class: ClassMetadata, Key: "tmdb"}, "http://tmdb.test/3/x"); err != nil {
		t.Fatal(err)
	}
	day1 := map[Purpose]int{PurposeOther: 1, PurposeRSS: 2, PurposeInteractive: 1}
	wantRequests(t, g, "dmm", day1, day1)
	wantRequests(t, g, "tracker", map[Purpose]int{PurposeWanted: 1}, map[Purpose]int{PurposeWanted: 1})
	if _, ok := g.State().Requests["tmdb"]; ok {
		t.Fatal("a metadata request was counted")
	}

	// Restart: a new gateway on the same database has the counts written.
	flush(t, g)
	g = e.gateway(p, st)
	g.CheckEgress(ctx)
	wantRequests(t, g, "dmm", day1, day1)

	// The next UTC day: today starts again, the week keeps both days.
	e.advance(12 * time.Hour)
	g.CheckEgress(ctx)
	if err := getWith(rss, g, dmm, "http://dmm.test/"); err != nil {
		t.Fatal(err)
	}
	wantRequests(t, g, "dmm", map[Purpose]int{PurposeRSS: 1}, map[Purpose]int{PurposeOther: 1, PurposeRSS: 3, PurposeInteractive: 1})

	// Seven days on, day 1 has left the week but is still kept; 30 days on it is pruned, in memory and stored.
	e.advance(6 * 24 * time.Hour)
	wantRequests(t, g, "dmm", nil, map[Purpose]int{PurposeRSS: 1})
	var rows int
	countRows := func() int {
		t.Helper()
		if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM gateway_indexer_request`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	flush(t, g)
	if n := countRows(); n != 5 {
		t.Fatalf("%d stored rows, want 5", n)
	}
	e.advance(23 * 24 * time.Hour) // 30 days after day 1: it is no longer kept
	g.CheckEgress(ctx)
	if err := getWith(WithPurpose(ctx, PurposeTest), g, dmm, "http://dmm.test/"); err != nil {
		t.Fatal(err)
	}
	flush(t, g)
	var oldest int64
	if err := st.Reader().QueryRow(`SELECT MIN(day_start_at) FROM gateway_indexer_request`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if n := countRows(); n != 2 || store.FromMillis(oldest).Day() != 26 {
		t.Fatalf("after 30 days: %d rows, oldest %v; want day 2's rss and today's test", n, store.FromMillis(oldest))
	}
	g.state.mu.Lock()
	kept := len(g.state.requests)
	g.state.mu.Unlock()
	if kept != 2 {
		t.Fatalf("%d counts in memory, want 2", kept)
	}
	wantRequests(t, g, "dmm", map[Purpose]int{PurposeTest: 1}, map[Purpose]int{PurposeTest: 1})
}

// A source probe counts only tagged requests: the settings test tags its own; the quality sync and the cardigann
// definitions fetch, probes of indexer-class targets that are no indexer, are not counted.
func TestProbeCountsOnlyTaggedRequests(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	p := testPolicy()
	g := e.gateway(p, openStore(t, filepath.Join(t.TempDir(), "theatarr.db")))
	ctx := context.Background()
	g.CheckEgress(ctx)
	pg, err := g.Probe(ctx, p, ProbeOptions{Target: dmm})
	if err != nil {
		t.Fatal(err)
	}
	if err := getWith(ctx, pg, dmm, "http://dmm.test/"); err != nil {
		t.Fatal(err)
	}
	if _, ok := g.State().Requests["dmm"]; ok {
		t.Fatal("an untagged probe request was counted")
	}
	if err := getWith(WithPurpose(ctx, PurposeTest), pg, dmm, "http://dmm.test/"); err != nil {
		t.Fatal(err)
	}
	wantRequests(t, g, "dmm", map[Purpose]int{PurposeTest: 1}, map[Purpose]int{PurposeTest: 1})
}

// purposeSites are the places that tag indexer searches with a purpose (WithPurpose), by file. A new search path
// tags its requests and joins this list; one that does not counts as other.
var purposeSites = map[string][]string{
	"cmd/theatarr/servesettingstest.go": {"PurposeTest"},
	"internal/acquire/devices.go":       {"PurposeInteractive"},
	// run and runFanout: searchPurpose (wanted or upgrade).
	"internal/acquire/acquire.go": {"searchPurpose", "purpose"},
	"internal/shadow/rss.go":      {"PurposeRSS"},
	"internal/shadow/grab.go":     {"PurposeShadow"},
}

var withPurposeRE = regexp.MustCompile(`gateway\.WithPurpose\([^,]+,\s*(?:gateway\.)?(\w+)`)

// TestPurposeCallSites lists where indexer requests get their purpose, so a new search path does not silently count as
// other, and checks every purpose but other has a site.
func TestPurposeCallSites(t *testing.T) {
	root := filepath.Join("..", "..")
	got := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && rel != "." || d.Name() == "testdata" || d.Name() == "vendor" ||
				d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range withPurposeRE.FindAllStringSubmatch(string(b), -1) {
			got[rel] = append(got[rel], m[1])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for f := range got {
		slices.Sort(got[f])
	}
	want := map[string][]string{}
	for f, s := range purposeSites {
		want[f] = slices.Sorted(slices.Values(s))
	}
	if !maps.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Fatalf("WithPurpose sites:\n got %v\nwant %v", got, want)
	}
	// Each named purpose has a site: the constants above, and searchPurpose's two answers in internal/acquire.
	src, err := os.ReadFile(filepath.Join(root, "internal/acquire/acquire.go"))
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, s := range purposeSites {
		for _, p := range s {
			named[p] = true
		}
	}
	for _, p := range []string{"PurposeWanted", "PurposeUpgrade"} {
		if strings.Contains(string(src), "return gateway."+p) {
			named[p] = true
		}
	}
	consts := map[Purpose]string{PurposeInteractive: "PurposeInteractive", PurposeWanted: "PurposeWanted",
		PurposeUpgrade: "PurposeUpgrade", PurposeRSS: "PurposeRSS", PurposeShadow: "PurposeShadow", PurposeTest: "PurposeTest"}
	for _, p := range Purposes {
		if p != PurposeOther && !named[consts[p]] {
			t.Errorf("no call site sets %s", p)
		}
	}
}

// Counting does not hold a response: the counts are written by the flush, not by the request, so a busy database
// writer delays nothing. The flush adds to the stored counts and keeps what it could not write for the next one.
func TestRequestCountNotBlockedByWriter(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	st := openStore(t, filepath.Join(t.TempDir(), "theatarr.db"))
	g := e.gateway(testPolicy(), st)
	ctx := context.Background()
	g.CheckEgress(ctx)
	if err := getWith(ctx, g, dmm, "http://dmm.test/"); err != nil {
		t.Fatal(err)
	}
	flush(t, g)

	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- st.WriteTx(ctx, func(*sql.Tx) error { close(held); <-release; return nil })
	}()
	<-held
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	start := time.Now()
	err := getWith(WithPurpose(rctx, PurposeRSS), g, dmm, "http://dmm.test/")
	if took := time.Since(start); err != nil || took > time.Second {
		close(release)
		t.Fatalf("request with the writer busy: %v after %v", err, took)
	}
	// A flush that cannot write keeps its counts.
	fctx, fcancel := context.WithTimeout(ctx, 50*time.Millisecond)
	if err := g.FlushRequests(fctx); err == nil {
		t.Fatal("flush wrote with the writer busy")
	}
	fcancel()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	flush(t, g)
	if got, want := storedRequests(t, st), map[string]int{"dmm/other": 1, "dmm/rss": 1}; !maps.Equal(got, want) {
		t.Fatalf("stored %v, want %v", got, want)
	}
}

// RunRequestFlush writes the counts when it ends, so they survive a clean restart.
func TestRequestCountsSurviveShutdown(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	st := openStore(t, filepath.Join(t.TempDir(), "theatarr.db"))
	p := testPolicy()
	g := e.gateway(p, st)
	ctx, cancel := context.WithCancel(context.Background())
	g.CheckEgress(ctx)
	stopped := make(chan struct{})
	go func() { g.RunRequestFlush(ctx); close(stopped) }()
	for range 2 {
		if err := getWith(WithPurpose(ctx, PurposeWanted), g, dmm, "http://dmm.test/"); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	<-stopped
	g = e.gateway(p, st)
	want := map[Purpose]int{PurposeWanted: 2}
	wantRequests(t, g, "dmm", want, want)
}

// Each hop sent counts as a request: a followed redirect counts both requests, a redirect refused at its next hop
// counts the one sent, and a NoFollow request its one.
func TestRedirectHopsCounted(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "http://dmm.test/next", http.StatusFound)
		case "/away":
			http.Redirect(w, r, "http://evil.test/x", http.StatusFound)
		default:
			io.WriteString(w, "ok")
		}
	}))
	g := e.gateway(testPolicy(), nil)
	ctx := context.Background()
	g.CheckEgress(ctx)
	if err := getWith(WithPurpose(ctx, PurposeRSS), g, dmm, "http://dmm.test/start"); err != nil {
		t.Fatal(err)
	}
	want := map[Purpose]int{PurposeRSS: 2}
	wantRequests(t, g, "dmm", want, want)
	if err := getWith(WithPurpose(ctx, PurposeTest), g, dmm, "http://dmm.test/away"); err == nil {
		t.Fatal("redirect to an unlisted host followed")
	}
	want[PurposeTest] = 1
	wantRequests(t, g, "dmm", want, want)
	nf := dmm
	nf.NoFollow = true
	if err := getWith(WithPurpose(ctx, PurposeTest), g, nf, "http://dmm.test/start"); err != nil {
		t.Fatal(err)
	}
	want[PurposeTest] = 2
	wantRequests(t, g, "dmm", want, want)
}
