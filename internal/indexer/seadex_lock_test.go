package indexer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

type lockSearcher struct {
	kind  string
	fetch func(context.Context, Query) (*Result, error)
}

func (s lockSearcher) Key() string                                          { return s.kind }
func (s lockSearcher) Kind() string                                         { return s.kind }
func (s lockSearcher) Supports(Query) bool                                  { return true }
func (s lockSearcher) Search(ctx context.Context, q Query) (*Result, error) { return s.fetch(ctx, q) }
func lockService(st *store.Store, kind string, fetch func(context.Context, Query) (*Result, error)) *Service {
	return &Service{searchers: map[string]Searcher{kind: lockSearcher{kind, fetch}}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), ttl: time.Minute, timeout: time.Second * 5, now: time.Now, cache: map[string]cacheEntry{}, inflight: map[string]*flight{}, ev: NewEvidence(EvidenceConfig{Store: st, Policy: testPolicy()})}
}
func lockAnswer(hash string) *Result {
	return &Result{Requests: 1, SeaDex: []SeaDexSnapshot{{AniList: 1, Torrents: []SeaDexStanding{{InfoHash: strings.Repeat(hash, 40), Facts: &sdTorrent{}}}}}}
}
func lockMappedStore(t *testing.T) *store.Store {
	t.Helper()
	st := openStore(t)
	if _, err := st.Writer().Exec(`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Film',0,0); INSERT INTO title_external_id VALUES(1,'anilist','1')`); err != nil {
		t.Fatal(err)
	}
	return st
}
func TestSeaDexLockProcessHelper(t *testing.T) {
	if os.Getenv("THEATARR_SEADEX_LOCK_HELPER") != "1" {
		return
	}
	st, err := store.Open(context.Background(), os.Args[len(os.Args)-1], store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := lockService(st, "seadex", func(context.Context, Query) (*Result, error) {
		fmt.Println("FETCH")
		_, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return nil, err
		}
		return lockAnswer("a"), nil
	})
	if _, err := svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1}); err != nil {
		t.Fatal(err)
	}
}
func lockChild(t *testing.T, path string) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSeaDexLockProcessHelper$", "--", path)
	cmd.Env = []string{"THEATARR_SEADEX_LOCK_HELPER=1"}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = in.Close() })
	ready := make(chan bool, 1)
	go func() {
		s := bufio.NewScanner(out)
		for s.Scan() {
			if s.Text() == "FETCH" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("helper exited before fetch")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not fetch")
	}
	return cmd, in
}
func TestCrossProcessFetchThroughCommit(t *testing.T) {
	st := lockMappedStore(t)
	cmd, in := lockChild(t, st.Path())
	entered := make(chan struct{})
	svc := lockService(st, "seadex", func(context.Context, Query) (*Result, error) { close(entered); return lockAnswer("b"), nil })
	done := make(chan error, 1)
	go func() {
		_, err := svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1})
		done <- err
	}()
	select {
	case <-entered:
		t.Error("new fetch entered before older process committed")
	case <-time.After(100 * time.Millisecond):
	}
	fmt.Fprintln(in, "release")
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := storedStanding(t, st.Reader(), "1")
	for _, tor := range got.Torrents {
		if tor.InfoHash == strings.Repeat("b", 40) && tor.MissingSince == 0 {
			return
		}
	}
	t.Fatalf("newer member marked missing: %+v", got)
}
func TestDeadlineAndProcessDeath(t *testing.T) {
	st := lockMappedStore(t)
	cmd, _ := lockChild(t, st.Path())
	// An alias must lock the same sidecar.
	alias := filepath.Join(t.TempDir(), "alias.db")
	if err := os.Symlink(st.Path(), alias); err != nil {
		t.Fatal(err)
	}
	other, err := store.Open(context.Background(), alias, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	fetched := make(chan struct{}, 2)
	svc := lockService(other, "seadex", func(context.Context, Query) (*Result, error) { fetched <- struct{}{}; return lockAnswer("b"), nil })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = svc.Search(ctx, "seadex", Query{Type: QueryMovie, AniList: 1})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 300*time.Millisecond {
		t.Fatalf("deadline: %v after %v", err, time.Since(start))
	}
	select {
	case <-fetched:
		t.Fatal("fetch ran while another process held lock")
	default:
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	// The last waiter cancelled its flight; a fresh caller can fetch after the holder exits.
	if _, err := svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if len(fetched) != 1 {
		t.Fatalf("shared flight did not fetch exactly once: %d", len(fetched))
	}
}
func TestGenerationsAndIndependentSearches(t *testing.T) {
	st := lockMappedStore(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	first := lockService(st, "seadex", func(context.Context, Query) (*Result, error) {
		once.Do(func() { close(entered) })
		<-release
		return lockAnswer("a"), nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := first.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1})
		done <- err
	}()
	<-entered
	// Policy is prefer-off: ordinary indexers and overlays still complete while the DB lock is held.
	for _, test := range []struct {
		st   *store.Store
		kind string
	}{{st, "torznab"}, {nil, "seadex"}} {
		svc := lockService(test.st, test.kind, func(context.Context, Query) (*Result, error) { return &Result{}, nil })
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := svc.Search(ctx, test.kind, Query{Type: QueryMovie, AniList: 1})
		cancel()
		if err != nil {
			t.Error(err)
		}
	}
	// Network I/O must not retain SQLite's writer.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err := st.Writer().ExecContext(ctx, `UPDATE title SET name='Film2' WHERE id=1`)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	nextEntered := make(chan struct{})
	next := lockService(st, "seadex", func(context.Context, Query) (*Result, error) { close(nextEntered); return lockAnswer("b"), nil })
	nextDone := make(chan error, 1)
	go func() {
		_, err := next.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1})
		nextDone <- err
	}()
	select {
	case <-nextEntered:
		t.Error("second generation fetched concurrently")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-nextDone; err != nil {
		t.Fatal(err)
	}
	// Reusing the older cached answer must not replay its transition against b.
	if res, err := first.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1}); err != nil || !res.Cached {
		t.Fatalf("cache: %+v %v", res, err)
	}
	got := storedStanding(t, st.Reader(), "1")
	for _, tor := range got.Torrents {
		if tor.InfoHash == strings.Repeat("b", 40) && tor.MissingSince != 0 {
			t.Fatalf("cached observation replayed: %+v", got)
		}
	}
}

func TestUnverifiableIdentityDiagnostic(t *testing.T) {
	svc := lockService(nil, "torznab", func(context.Context, Query) (*Result, error) {
		return &Result{Releases: []Release{{InfoHash: strings.Repeat("a", 40), Magnet: NewLink("magnet:?xt=urn:btmh:1220" + strings.Repeat("b", 64))}}}, nil
	})
	res, err := svc.Search(context.Background(), "torznab", Query{Type: QueryMovie, AniList: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Releases) != 0 || !strings.Contains(strings.Join(res.Notes, " "), "unverifiable") {
		t.Fatalf("unverifiable identity not explained: %+v", res)
	}
}
