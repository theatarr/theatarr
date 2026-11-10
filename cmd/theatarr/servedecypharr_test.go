package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/library/decypharrtest"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// filesProvider answers Torrent from a map by id.
type filesProvider struct {
	provider.Provider
	torrents map[string]*provider.Torrent
	reads    []string
}

func (p *filesProvider) Torrent(_ context.Context, id string) (*provider.Torrent, error) {
	p.reads = append(p.reads, id)
	if t := p.torrents[id]; t != nil {
		return t, nil
	}
	return nil, provider.ErrNotFound
}

// TestServeDecypharr: with library.mount.decypharr set, theatarr serve's library reads decypharr through the gateway
// with the credentials its env names hold, the torrent manager shares that client, and the mount check says when
// decypharr loads or refuses the credentials. Without the block there is no client.
func TestServeDecypharr(t *testing.T) {
	ctx := context.Background()
	hash := decypharrtest.SharedFirst
	t.Setenv("TEST_DECYPHARR_TOKEN", "made-up-token")
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	mount := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mount, "__all__", decypharrtest.SharedName), 0o755); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// serve builds one library per case: the client keeps its loading answer for a while.
	serve := func(edit func(*decypharrtest.Server)) (*serveLibrary, *policy.Policy, *gateway.Gateway) {
		srv := decypharrtest.New(t)
		srv.Set(func(s *decypharrtest.Server) {
			s.Providers, s.Torrents = decypharrtest.Fixture()
			s.Token = "made-up-token"
			if edit != nil {
				edit(s)
			}
		})
		pol := &policy.Policy{Library: &policy.Library{Enabled: true, Mount: policy.LibraryMount{Path: mount,
			Decypharr: &policy.LibraryDecypharr{URL: srv.URL(), APITokenEnv: "TEST_DECYPHARR_TOKEN"}}}}
		gw, err := gateway.New(ctx, pol, gateway.Options{})
		if err != nil {
			t.Fatal(err)
		}
		lb, err := newServeLibrary(pol, st, jobs.NewQueue(st), gw, log, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return lb, pol, gw
	}
	mountCheck := func(lb *serveLibrary, pol *policy.Policy) health.Result {
		hc := newServeHealth(serveHealthConfig{pol: pol, st: st, q: jobs.NewQueue(st), lib: lb.lib})
		for _, r := range hc.Results(ctx) {
			if r.Source == "MountCheck" {
				return r
			}
		}
		t.Fatal("no MountCheck")
		return health.Result{}
	}

	lb, pol, gw := serve(nil)
	d := lb.lib.Decypharr()
	if d == nil {
		t.Fatal("no decypharr client with the block set")
	}
	ix, err := d.Index(ctx)
	if names, _ := ix.Names(hash); err != nil || len(names) != 1 || names[0] != decypharrtest.SharedName {
		t.Fatalf("index through the gateway: %q, %v", names, err)
	}
	if r := mountCheck(lb, pol); r.Type != health.OK {
		t.Errorf("mount check with decypharr answering: %+v", r)
	}
	svc := &serveTorrents{st: st, q: jobs.NewQueue(st), log: log, pol: pol, gw: gw,
		ps: &providerSync{syncer: &provider.Syncer{}}, lib: lb}
	m, err := svc.Manager(ctx)
	if err != nil || m.Decypharr != d {
		t.Errorf("the torrent manager's decypharr client: %p, want the library's %p (%v)", m.Decypharr, d, err)
	}

	lb, pol, _ = serve(func(s *decypharrtest.Server) { s.Loading = true })
	if r := mountCheck(lb, pol); r.Type != health.Error || !strings.Contains(r.Message, "decypharr is still loading") {
		t.Errorf("mount check while decypharr loads: %+v", r)
	}
	lb, pol, _ = serve(func(s *decypharrtest.Server) { s.Token = "another-token" })
	if r := mountCheck(lb, pol); r.Type != health.Error || !strings.Contains(r.Message, "decypharr refuses the credentials") {
		t.Errorf("mount check with refused credentials: %+v", r)
	}
	lb, pol, _ = serve(func(s *decypharrtest.Server) { s.Fail = 500 })
	if r := mountCheck(lb, pol); r.Type != health.Error || !strings.Contains(r.Message, "decypharr does not answer") {
		t.Errorf("mount check with decypharr failing: %+v", r)
	}

	// Without the block: no client, and the mount check is the mount's alone.
	plain := &policy.Policy{Library: &policy.Library{Enabled: true, Mount: policy.LibraryMount{Path: mount}}}
	lb, err = newServeLibrary(plain, st, jobs.NewQueue(st), nil, log, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lb.lib.Decypharr() != nil {
		t.Error("a decypharr client without the block")
	}
	if r := mountCheck(lb, plain); r.Type != health.OK {
		t.Errorf("mount check without decypharr: %+v", r)
	}
}

// TestProviderTorrentFiles: the library's file list of a direct add is the provider's selected files of the torrent
// the download recorded; when that id is gone or holds another hash now, of the id the registry records for the
// hash. Anything else is no list.
func TestProviderTorrentFiles(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	hash, other := decypharrtest.SharedFirst, decypharrtest.SharedSecond
	files := []provider.File{{Path: "/Folder/movie.mkv", Bytes: 5, Selected: true}, {Path: "/Folder/Sample/movie.mkv", Bytes: 1}}
	p := &filesProvider{torrents: map[string]*provider.Torrent{
		"A": {ID: "A", Hash: strings.ToUpper(hash), Files: files},
		"B": {ID: "B", Hash: other, Files: files},
		"C": {ID: "C", Hash: hash, Files: files[1:]},
		"N": {ID: "N", Hash: hash, Files: files},
	}}
	read := providerTorrentFiles(st, map[string]provider.Provider{"rd": p})
	want := func(name, id string, ok bool) {
		t.Helper()
		got, found := read(ctx, "rd", id, hash)
		if found != ok || ok && (len(got) != 1 || got[0] != library.TorrentFile{Name: "/Folder/movie.mkv", Size: 5}) {
			t.Errorf("%s: %+v, found %v", name, got, found)
		}
	}
	want("the recorded id", "A", true)
	want("another hash under the id, no registry row", "B", false)
	want("no selected file", "C", false)
	want("no id", "", false)
	if _, found := read(ctx, "tb", "A", hash); found {
		t.Error("a list from a provider that is not set up")
	}
	// The registry knows the hash's current id on the provider.
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO content (id, info_hash, name, first_seen_at, last_seen_at)
		VALUES (1, ?, 'x', 0, 0)`, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO content_provider (content_id, provider, torrent_id, seen_at)
		VALUES (1, 'rd', 'N', 0)`); err != nil {
		t.Fatal(err)
	}
	p.reads = nil
	want("the id is gone: the registry's", "gone", true)
	want("another hash under the id: the registry's", "B", true)
	if got := strings.Join(p.reads, ","); got != "gone,N,B,N" {
		t.Errorf("provider reads %s", got)
	}
}
