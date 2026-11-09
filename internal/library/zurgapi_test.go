package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// zurgServer serves the read-only endpoints of zurg's API (and /ready) with made-up torrents. Anything but a GET
// of one of them is a test failure.
type zurgServer struct {
	t        *testing.T
	mu       sync.Mutex
	torrents []ZurgTorrent
	entries  map[string]ZurgEntry // by DAV path
	running  bool
	fail     bool
	// ready is the status of /ready (0: 200).
	ready int
	// listing is the status of /debug/torrents (0: 200 with torrents).
	listing int
	hits    map[string]int
}

func newZurgServer(t *testing.T) *zurgServer {
	return &zurgServer{t: t, entries: map[string]ZurgEntry{}, hits: map[string]int{}}
}

func (s *zurgServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method != http.MethodGet {
		s.t.Errorf("%s %s: zurg's API is read-only", r.Method, r.URL)
		http.Error(w, "no", http.StatusMethodNotAllowed)
		return
	}
	s.hits[r.URL.Path]++
	if s.fail {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/debug/torrents":
		if s.listing != 0 {
			http.Error(w, "no listing", s.listing)
			return
		}
		_ = json.NewEncoder(w).Encode(s.torrents)
	case "/api/library/entry":
		p := r.URL.Query().Get("path")
		if !strings.HasPrefix(p, "/dav/") {
			http.Error(w, "expected a DAV release or file path", http.StatusBadRequest)
			return
		}
		e, ok := s.entries[p]
		if !ok {
			http.Error(w, "404 page not found", http.StatusNotFound)
			return
		}
		// The live answer also carries a signed streaming path; it must never be read.
		_ = json.NewEncoder(w).Encode(map[string]any{"hash": e.Hash, "name": e.Name, "imdb_id": e.IMDbID,
			"signed_path": "/strm/e/made-up-signature", "size": e.Size, "media_info": nil})
	case "/torrents/repair/status":
		_ = json.NewEncoder(w).Encode(map[string]bool{"running": s.running})
	case "/stats":
		_, _ = w.Write([]byte(`{"active_streams":2,"stream_bytes":9,"blocked_reads":[{"path":"/__all__/R/e.mkv",` +
			`"filename":"e.mkv","torrent_hash":"` + hashB + `","status":"refused","reason":"starved","attempts":3}]}`))
	case "/ready":
		if s.ready != 0 {
			w.WriteHeader(s.ready)
		}
	default:
		s.t.Errorf("GET %s: not one of the read-only endpoints", r.URL.Path)
		http.NotFound(w, r)
	}
}

func (s *zurgServer) set(f func(s *zurgServer)) { s.mu.Lock(); f(s); s.mu.Unlock() }
func (s *zurgServer) count(p string) int        { s.mu.Lock(); defer s.mu.Unlock(); return s.hits[p] }

// withZurg starts srv and points e's library at it through a real gateway (target zurgapi:api, own budget).
func (e *env) withZurg(srv *zurgServer) *ZurgAPI {
	e.t.Helper()
	ts := httptest.NewServer(srv)
	e.t.Cleanup(ts.Close)
	p := &policy.Policy{
		Budgets: map[string]*policy.Budget{"zurg-api": {Requests: 6000, Per: policy.Duration(time.Minute)}},
		Library: &policy.Library{Mount: policy.LibraryMount{ZurgAPI: ts.URL, ZurgAPIBudget: "zurg-api"}},
	}
	gw, err := gateway.New(e.ctx, p, gateway.Options{})
	if err != nil {
		e.t.Fatal(err)
	}
	z := &ZurgAPI{URL: ts.URL, GW: gw, AllDir: "__all__", Interval: time.Minute, RepairGrace: 2 * time.Hour,
		Now: func() time.Time { return e.now }}
	e.lib.zurg = z
	return z
}

func davPath(folder, rel string) string { return "/dav/" + filepath.Join("__all__", folder, rel) }

const (
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC = "cccccccccccccccccccccccccccccccccccccccc"
)

// TestZurgAPIClient: the three GETs through the gateway, the listing reused for the interval, entries by DAV path
// (404 is no entry), a failure keeping the API alone for the interval, and the repair grace bounded.
func TestZurgAPIClient(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	srv := newZurgServer(t)
	srv.torrents = []ZurgTorrent{{Name: "Some.Movie.2001.2160p.mkv", Hash: strings.ToUpper(hashA), Size: 5},
		{Name: "Lost.Show.S01", Hash: hashB, Size: 0}, {Name: "Lost.Show.S01", Hash: hashC, Size: 0}}
	srv.entries[davPath("Some.Movie.2001.2160p", "movie.mkv")] = ZurgEntry{Hash: hashA, Name: "movie.mkv", Size: 5}
	z := e.withZurg(srv)

	l, err := z.Torrents(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Folder names as zurg gives them: the name without .mkv, and " {hash6}" for a shared name.
	if l.NoVideo("", "Some.Movie.2001.2160p") || !l.NoVideo("", "Lost.Show.S01") || !l.NoVideo("", "Lost.Show.S01 {bbbbbb}") ||
		!l.NoVideo(hashC, "anything") || l.NoVideo(hashA, "Lost.Show.S01") || l.NoVideo("", "Unknown") ||
		l.NoVideo("dddddddddddddddddddddddddddddddddddddddd", "Lost.Show.S01") || l.NoVideoCount() != 2 {
		t.Errorf("classification of %+v", l.All)
	}
	if _, err := z.Torrents(e.ctx); err != nil || srv.count("/debug/torrents") != 1 {
		t.Errorf("listing not reused within the interval: %d fetches, %v", srv.count("/debug/torrents"), err)
	}
	e.now = e.now.Add(time.Minute)
	if _, err := z.Torrents(e.ctx); err != nil || srv.count("/debug/torrents") != 2 {
		t.Errorf("listing not fetched again after the interval: %d", srv.count("/debug/torrents"))
	}

	ent, err := z.Entry(e.ctx, "Some.Movie.2001.2160p", "movie.mkv")
	if err != nil || ent == nil || ent.Hash != hashA || ent.Size != 5 {
		t.Fatalf("entry %+v %v", ent, err)
	}
	if ent, err := z.Entry(e.ctx, "Not.There", "x.mkv"); err != nil || ent != nil {
		t.Errorf("missing entry: %+v %v", ent, err)
	}

	// Repair: running is first seen now; the grace ends RepairGrace later even while it keeps running.
	if rs := z.Repairing(e.ctx); rs.Running || rs.Grace || rs.Error != "" {
		t.Errorf("idle repair %+v", rs)
	}
	srv.set(func(s *zurgServer) { s.running = true })
	if rs := z.Repairing(e.ctx); rs.Running {
		t.Errorf("repair state not reused within the interval: %+v", rs)
	}
	e.now = e.now.Add(time.Minute)
	start := e.now
	if rs := z.Repairing(e.ctx); !rs.Running || !rs.Grace || !rs.Since.Equal(start) {
		t.Errorf("running repair %+v", rs)
	}
	e.now = start.Add(2*time.Hour - time.Second)
	if rs := z.Repairing(e.ctx); !rs.Grace || !rs.Since.Equal(start) {
		t.Errorf("within the grace %+v", rs)
	}
	e.now = start.Add(2 * time.Hour)
	if rs := z.Repairing(e.ctx); !rs.Running || rs.Grace {
		t.Errorf("past the grace %+v", rs)
	}
	// A new repair after an idle answer starts a new grace.
	srv.set(func(s *zurgServer) { s.running = false })
	e.now = e.now.Add(time.Minute)
	z.Repairing(e.ctx)
	srv.set(func(s *zurgServer) { s.running = true })
	e.now = e.now.Add(time.Minute)
	if rs := z.Repairing(e.ctx); !rs.Grace || !rs.Since.Equal(e.now) {
		t.Errorf("second repair %+v", rs)
	}

	// A failed request leaves the API alone for the interval: no grace, no listing, no request.
	srv.set(func(s *zurgServer) { s.fail = true })
	e.now = e.now.Add(time.Minute)
	if rs := z.Repairing(e.ctx); rs.Grace || rs.Error == "" {
		t.Errorf("failed repair state %+v", rs)
	}
	hits := srv.count("/debug/torrents")
	if _, err := z.Torrents(e.ctx); !errors.Is(err, ErrZurgAPIDown) || srv.count("/debug/torrents") != hits {
		t.Errorf("down: %v, %d requests", err, srv.count("/debug/torrents")-hits)
	}
	if ent, err := z.Entry(e.ctx, "Some.Movie.2001.2160p", "movie.mkv"); ent != nil || !errors.Is(err, ErrZurgAPIDown) {
		t.Errorf("entry while down: %+v %v", ent, err)
	}

	// Only the configured host is the target's.
	other := &ZurgAPI{URL: "http://other-zurg:9999", GW: z.GW, Interval: time.Minute}
	var r *gateway.Refused
	if _, err := other.Torrents(e.ctx); !errors.As(err, &r) || r.Reason != gateway.ReasonHost {
		t.Errorf("another host: %v", err)
	}
}

// TestDanglingReportNoVideo: with the API, a dangling link whose torrent zurg lists with no video is named so, the
// report counts them, and the health check says it. Without an answer the rows stay unclassified.
func TestDanglingReportNoVideo(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false, policy.ModeShadow)
	e.release("Here", map[string]float64{"a.mkv": 1})
	// The torrent lost its video upstream: only an .nfo is left in its folder.
	if err := os.MkdirAll(filepath.Join(e.mount, "__all__", "Lost.Movie"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.mount, "__all__", "Lost.Movie", "info.nfo"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for p, tg := range map[string]string{
		filepath.Join(e.movies, "Good (2001)", "Good.mkv"): "Here/a.mkv",
		filepath.Join(e.movies, "Lost (2001)", "Lost.mkv"): "Lost.Movie/movie.mkv",
		filepath.Join(e.movies, "Gone (2002)", "Gone.mkv"): "Gone.Movie/movie.mkv",
	} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.Symlink(filepath.Join(e.mount, "__all__", tg), p); err != nil {
			t.Fatal(err)
		}
	}
	srv := newZurgServer(t)
	srv.torrents = []ZurgTorrent{{Name: "Here", Hash: hashA, Size: 5}, {Name: "Lost.Movie.mkv", Hash: hashB, Size: 0},
		{Name: "Unlinked.Lost", Hash: hashC, Size: 0}}
	e.withZurg(srv)
	before := tree(t, e.dir)
	sum, err := e.lib.DanglingReport(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Dangling != 2 || sum.Zurg == nil || sum.Zurg.NoVideo != 1 || sum.Zurg.NoVideoTorrents != 2 || sum.Zurg.Error != "" {
		t.Fatalf("summary %+v zurg %+v", sum, sum.Zurg)
	}
	if after := tree(t, e.dir); strings.Join(before, "|") != strings.Join(after, "|") {
		t.Error("the report wrote to the filesystem")
	}
	rows, last, err := Dangling(e.ctx, e.st.Reader())
	if err != nil || len(rows) != 2 || last.Zurg == nil || last.Zurg.NoVideo != 1 || last.Zurg.NoVideoTorrents != 2 {
		t.Fatalf("rows %+v last %+v %v", rows, last, err)
	}
	for _, r := range rows {
		want := ""
		if r.Folder == "Lost.Movie" {
			want = CauseNoVideo
		}
		if r.Cause != want {
			t.Errorf("%s: cause %q, want %q", r.Path, r.Cause, want)
		}
	}
	res := health.Dangling(e.st)(e.ctx)
	if res.Type != health.Notice || !strings.Contains(res.Message, "2 of 3 links") ||
		!strings.Contains(res.Message, "1 of them: "+ReasonNoVideo) {
		t.Errorf("health %+v", res)
	}
	// Nothing was marked: the report only reads.
	if n := e.one(`SELECT COUNT(*) FROM library_action`); n != "0" {
		t.Errorf("actions %s", n)
	}

	// zurg not answering: the report still runs, unclassified, and says why.
	srv.set(func(s *zurgServer) { s.fail = true })
	e.now = e.now.Add(6 * time.Hour)
	if sum, err = e.lib.DanglingReport(e.ctx); err != nil || sum.Dangling != 2 || sum.Zurg.Error == "" || sum.Zurg.NoVideo != 0 {
		t.Fatalf("report without zurg: %+v %v", sum, err)
	}
	rows, _, _ = Dangling(e.ctx, e.st.Reader())
	for _, r := range rows {
		if r.Cause != "" {
			t.Errorf("classified without an answer: %+v", r)
		}
	}
	if res := health.Dangling(e.st)(e.ctx); strings.Contains(res.Message, "no video") {
		t.Errorf("health without zurg %+v", res)
	}

	// Without the API (the default) there is no zurg section at all.
	e.lib.zurg = nil
	if sum, err = e.lib.DanglingReport(e.ctx); err != nil || sum.Zurg != nil {
		t.Fatalf("report without the API: %+v %v", sum, err)
	}
	if _, last, _ = Dangling(e.ctx, e.st.Reader()); last.Zurg != nil {
		t.Errorf("stored zurg section without the API: %+v", last.Zurg)
	}
}

// TestRelinkZurgNoVideoAndRepair: the sweep names a link whose torrent has no video (and marks it dead as before);
// while zurg repairs, dangling links are reported as repairing and stay active, until the grace is over.
func TestRelinkZurgNoVideoAndRepair(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("Other", map[string]float64{"o.mkv": 1})
	e.exec(`UPDATE version SET state = 'filled' WHERE id = 10`)
	lost := filepath.Join(e.movies, "Amelie (2001)", "Amelie.mkv")
	e.theatarrLink(1, lost, filepath.Join(e.mount, "__all__", "Lost.Movie", "movie.mkv"))
	srv := newZurgServer(t)
	srv.torrents = []ZurgTorrent{{Name: "Lost.Movie", Hash: hashB, Size: 0}}
	srv.running = true
	e.withZurg(srv)

	// Repairing: grace, not dead. Verify says the same.
	vs, err := e.lib.Verify(e.ctx, "movie-remux")
	if err != nil || len(vs[0].Dangling) != 1 || vs[0].Dangling[0].Cause != CauseRepairing ||
		!strings.Contains(vs[0].Dangling[0].Reason, ReasonNoVideo) || vs[0].ZurgRepair == nil || !vs[0].ZurgRepair.Grace {
		t.Fatalf("verify while repairing %+v %v", vs[0], err)
	}
	scans, err := e.lib.Relink(e.ctx, "movie-remux", false)
	if err != nil {
		t.Fatal(err)
	}
	if d := scans[0].Dangling; len(d) != 1 || d[0].Cause != CauseRepairing || scans[0].Causes[CauseRepairing] != 1 {
		t.Fatalf("relink while repairing %+v", scans[0])
	}
	if s := e.one(`SELECT state FROM link WHERE id = 1`); s != "active" {
		t.Errorf("link marked %s during a repair", s)
	}
	if n := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'dead'`); n != "0" {
		t.Errorf("dead actions %s during a repair", n)
	}
	if s := e.one(`SELECT state FROM version WHERE id = 10`); s != "filled" {
		t.Errorf("version %s during a repair", s)
	}

	// Past the grace the sweep marks it as it would without the API, naming the cause.
	e.now = e.now.Add(2*time.Hour + time.Minute)
	scans, err = e.lib.Relink(e.ctx, "movie-remux", false)
	if err != nil {
		t.Fatal(err)
	}
	if d := scans[0].Dangling; len(d) != 1 || d[0].Cause != CauseNoVideo || d[0].Reason != ReasonNoVideo {
		t.Fatalf("relink past the grace %+v", scans[0])
	}
	if s := e.one(`SELECT state FROM link WHERE id = 1`); s != "dead" {
		t.Errorf("link %s past the grace", s)
	}
	if d := e.one(`SELECT detail FROM library_action WHERE kind = 'dead'`); !strings.HasPrefix(d, CauseNoVideo) {
		t.Errorf("dead action detail %q", d)
	}
	if tg, _ := os.Readlink(lost); !strings.Contains(tg, "Lost.Movie") {
		t.Errorf("the link itself changed: %q", tg)
	}
}

// TestRelinkZurgUnreachableKeepsBehaviour: zurg not answering gives no grace and no classification: the sweep is
// the mount-only sweep.
func TestRelinkZurgUnreachableKeepsBehaviour(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("Other", map[string]float64{"o.mkv": 1})
	e.theatarrLink(1, filepath.Join(e.movies, "Amelie (2001)", "Amelie.mkv"), filepath.Join(e.mount, "__all__", "Gone", "m.mkv"))
	srv := newZurgServer(t)
	srv.running, srv.fail = true, true
	e.withZurg(srv)
	if _, err := e.lib.Relink(e.ctx, "movie-remux", false); err != nil {
		t.Fatal(err)
	}
	// The miss is confirmed by a later sweep.
	e.now = e.now.Add(deadConfirm)
	scans, err := e.lib.Relink(e.ctx, "movie-remux", false)
	if err != nil {
		t.Fatal(err)
	}
	if d := scans[0].Dangling; len(d) != 1 || d[0].Cause != "unknown" || d[0].Reason != "" || scans[0].ZurgError == "" {
		t.Fatalf("relink without zurg %+v", scans[0])
	}
	if s := e.one(`SELECT state FROM link WHERE id = 1`); s != "dead" {
		t.Errorf("link %s", s)
	}
}

// TestRelinkZurgConfirmsCandidate: a hash-suffixed candidate folder that zurg says holds another torrent's file is
// not this link's; one it confirms, or one it has no answer for, is relinked as before.
func TestRelinkZurgConfirmsCandidate(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		entry   string // hash zurg gives the candidate file; "" no entry
		fail    bool
		relinks bool
	}{
		{"confirmed", hashA, false, true},
		{"another torrent", hashB, false, false},
		{"no entry", "", false, true},
		{"not answering", hashB, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.release("Movie.Release {abcdef}", map[string]float64{"movie.mkv": 7000})
			e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES (1, ?, 'Movie.Release', 'present', 0, 0)`, hashA)
			link := filepath.Join(e.movies, "Movie (2001)", "Movie (2001).mkv")
			os.MkdirAll(filepath.Dir(link), 0o755)
			if err := os.Symlink(filepath.Join(e.mount, "__all__", "Movie.Release", "movie.mkv"), link); err != nil {
				t.Fatal(err)
			}
			srv := newZurgServer(t)
			srv.fail = c.fail
			if c.entry != "" {
				srv.entries[davPath("Movie.Release {abcdef}", "movie.mkv")] = ZurgEntry{Hash: c.entry, Size: 5}
			}
			e.withZurg(srv)
			scans, err := e.lib.Relink(e.ctx, "movie-remux", false)
			if err != nil {
				t.Fatal(err)
			}
			tg, _ := os.Readlink(link)
			moved := strings.Contains(tg, "{abcdef}")
			if moved != c.relinks || (len(scans[0].Relinked) == 1) != c.relinks {
				t.Errorf("relinked %v (target %q), want %v: %+v", moved, tg, c.relinks, scans[0])
			}
		})
	}
}

// TestImportZurgConfirmsRelease: zurg names the torrent behind a release folder. A bare name held by another torrent
// of the same name is passed over for this download's " {hash6}" folder, preferred even without the API. A
// file zurg does not confirm is not linked and holds the import for review, never fails or blocklists it.
func TestImportZurgConfirmsRelease(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) *env {
		e := newEnv(t, true, policy.ModeActive)
		e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
		e.release(movieRelease+" {abcdef}", map[string]float64{"movie.mkv": 7000})
		e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
		return e
	}
	target := func(e *env) string {
		e.t.Helper()
		tg, err := os.Readlink(filepath.Join(e.movies, "Amelie The Movie (2001)",
			"Amelie The Movie (2001) {imdb-tt0211915} - [Remux-2160p][TrueHD Atmos 7.1][HDR10][HEVC]-GRP.mkv"))
		if err != nil {
			e.t.Fatal(err)
		}
		return tg
	}

	t.Run("without the API", func(t *testing.T) {
		e := setup(t)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		// {abcdef} matches hashA's prefix, so it identifies our torrent without an API answer.
		if tg := target(e); !strings.Contains(tg, movieRelease+" {abcdef}/movie.mkv") {
			t.Errorf("mount alone linked %q", tg)
		}
	})

	t.Run("without the API non-matching suffix", func(t *testing.T) {
		e := setup(t)
		if err := os.Rename(filepath.Join(e.mount, "__all__", movieRelease+" {abcdef}"),
			filepath.Join(e.mount, "__all__", movieRelease+" {123456}")); err != nil {
			t.Fatal(err)
		}
		// Even a recorded folder name is not preferred when its suffix belongs to another hash.
		e.exec(`INSERT INTO content (info_hash, name, mount_folder, first_seen_at, last_seen_at)
			VALUES (?, ?, ?, 0, 0)`, hashA, movieRelease, movieRelease+" {123456}")
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		if tg := target(e); tg != filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv") {
			t.Errorf("mount alone linked %q", tg)
		}
	})

	t.Run("without the API uppercase hash", func(t *testing.T) {
		e := setup(t)
		e.exec(`UPDATE download SET info_hash = ? WHERE id = 1`, strings.ToUpper(hashA))
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		if tg := target(e); !strings.Contains(tg, movieRelease+" {abcdef}/movie.mkv") {
			t.Errorf("mount alone linked %q", tg)
		}
	})

	t.Run("source path keeps the chosen folder", func(t *testing.T) {
		e := setup(t)
		e.exec(`UPDATE download SET source_path = ? WHERE id = 1`, filepath.Join(e.mount, "__all__", movieRelease))
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		if tg := target(e); tg != filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv") {
			t.Errorf("path import linked %q", tg)
		}
	})

	t.Run("manual files keep the original folder order", func(t *testing.T) {
		e := setup(t)
		e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, updated_at)
			VALUES (1, 'movie.mkv', '[]', 'manual', 0)`)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		if tg := target(e); tg != filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv") {
			t.Errorf("manual import linked %q", tg)
		}
	})

	t.Run("bare name is another torrent", func(t *testing.T) {
		e := setup(t)
		srv := newZurgServer(t)
		srv.entries[davPath(movieRelease, "")] = ZurgEntry{Hash: hashB}
		srv.entries[davPath(movieRelease+" {abcdef}", "")] = ZurgEntry{Hash: hashA}
		srv.entries[davPath(movieRelease+" {abcdef}", "movie.mkv")] = ZurgEntry{Hash: hashA, Size: 5}
		e.withZurg(srv)
		plan, err := e.lib.Import(e.ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		if tg := target(e); !strings.Contains(tg, movieRelease+" {abcdef}/movie.mkv") {
			t.Errorf("linked %q", tg)
		}
		if len(plan.Files) != 1 || plan.Files[0].Zurg == nil || !plan.Files[0].Zurg.Confirmed || plan.Files[0].Zurg.Hash != hashA {
			t.Errorf("file check %+v", plan.Files)
		}
		if s := e.one(`SELECT state FROM download WHERE id = 1`); s != "imported" {
			t.Errorf("download %s", s)
		}
	})

	t.Run("no entries", func(t *testing.T) {
		e := setup(t)
		e.withZurg(newZurgServer(t))
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		if tg := target(e); !strings.Contains(tg, movieRelease+" {abcdef}/movie.mkv") {
			t.Errorf("without answers the mount's folder stands: %q", tg)
		}
	})

	t.Run("matching suffix is another torrent", func(t *testing.T) {
		e := setup(t)
		srv := newZurgServer(t)
		srv.entries[davPath(movieRelease+" {abcdef}", "")] = ZurgEntry{Hash: hashB}
		srv.entries[davPath(movieRelease, "")] = ZurgEntry{Hash: hashA}
		srv.entries[davPath(movieRelease, "movie.mkv")] = ZurgEntry{Hash: hashA, Size: 5}
		e.withZurg(srv)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		if tg := target(e); tg != filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv") {
			t.Errorf("linked %q despite API confirmation", tg)
		}
	})

	t.Run("size differs", func(t *testing.T) {
		e := setup(t)
		srv := newZurgServer(t)
		srv.entries[davPath(movieRelease+" {abcdef}", "")] = ZurgEntry{Hash: hashA}
		srv.entries[davPath(movieRelease+" {abcdef}", "movie.mkv")] = ZurgEntry{Hash: hashA, Size: 6}
		e.withZurg(srv)
		plan, err := e.lib.Import(e.ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Links) != 0 || len(plan.Rejected) != 1 || !strings.Contains(plan.Rejected[0].Reason, "zurg") {
			t.Fatalf("plan %+v", plan)
		}
		if s := e.one(`SELECT state FROM download WHERE id = 1`); s != "ready" {
			t.Errorf("download %s: held for review, not failed", s)
		}
		if n := e.one(`SELECT COUNT(*) FROM router_blocklist`); n != "0" {
			t.Errorf("blocklisted %s", n)
		}
		if s := e.one(`SELECT state FROM library_import WHERE download_id = 1`); s != "rejected" {
			t.Errorf("library import %s", s)
		}
	})
}

// TestImportZurgNameForHash: the providers may name a torrent otherwise than zurg's folder (a lower-cased video file
// name, a cut or different title), so an automatic import looks the folder up by zurg's name for the hash first, with
// its " {hash6}" form when another torrent shares the name. A listing without the hash, or no listing, leaves the
// import waiting for the mount as before; a path import never reads the listing.
// A torrent marked absent that is added and imported again is present: its claim on the bare folder of its original
// name holds against another download of the same name, which waits for its own folder.
func TestImportOriginalNameReaddedClaim(t *testing.T) {
	t.Parallel()
	const (
		original = "Movie.Name.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-GRP[TGx]"
		release  = "Movie.Name.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-GRP"
	)
	e := newEnv(t, true, policy.ModeActive)
	second := filepath.Join(e.dir, "media", "movies-second")
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	v := *e.pol.Versions["movie-remux"]
	v.Library = second
	e.pol.Versions["movie-second"] = &v
	e.pol.Library.Versions["movie-second"] = &policy.LibraryVersion{Roots: []string{second}}
	e.lib.gate = NewGate(e.pol)
	e.lib.w.gate = e.lib.gate
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (11, 1, 'movie-second', 0, 0)`)
	e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, state, first_seen_at, last_seen_at)
		VALUES (7, ?, ?, ?, 'absent', 0, 0)`, hashB, original, original)
	e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, folder_name, state, seen_at, absent_at)
		VALUES (7, 'rd', 'B', ?, 'absent', 0, 0)`, original)
	e.release(original, map[string]float64{"movie.mkv": 7000})

	// B is added again and imported from its bare folder.
	e.download(1, "movie-remux", 1, 10, release, strings.ToLower(release)+".mkv", "[]")
	e.exec(`UPDATE download SET info_hash = ?, original_name = ? WHERE id = 1`, hashB, original)
	if p, err := e.lib.Import(e.ctx, 1, false); err != nil || p.Waiting {
		t.Fatalf("import B: %+v %v", p, err)
	}
	if s := e.one(`SELECT state || ' ' || mount_folder FROM content WHERE id = 7`); s != "present "+original {
		t.Errorf("content %s", s)
	}

	// A declared the same name; with zurg's API off it waits for its own folder.
	e.download(2, "movie-second", 1, 11, release, strings.ToLower(release)+".mkv", "[]")
	e.exec(`UPDATE download SET original_name = ? WHERE id = 2`, original)
	p, err := e.lib.Import(e.ctx, 2, false)
	if err != nil || !p.Waiting {
		t.Fatalf("import A: %+v %v", p, err)
	}
}

func TestImportZurgNameForHash(t *testing.T) {
	t.Parallel()
	const (
		zname   = "Movie.Name.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-GRP[TGx]"
		release = "Movie.Name.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-GRP"
	)
	provider := strings.ToLower(release) + ".mkv"
	setup := func(t *testing.T, folder string) (*env, *zurgServer, *bytes.Buffer) {
		e := newEnv(t, true, policy.ModeActive)
		var buf bytes.Buffer
		e.lib.log = slog.New(slog.NewTextHandler(&buf, nil))
		if folder != "" {
			e.release(folder, map[string]float64{"movie.mkv": 7000})
		}
		e.download(1, "movie-remux", 1, 10, release, provider, "[]")
		srv := newZurgServer(t)
		srv.torrents = []ZurgTorrent{{Name: zname, Hash: hashA, Size: 7}}
		if folder != "" {
			srv.entries[davPath(folder, "")] = ZurgEntry{Hash: hashA}
			srv.entries[davPath(folder, "movie.mkv")] = ZurgEntry{Hash: hashA, Size: 5}
		}
		return e, srv, &buf
	}
	imported := func(e *env, folder string) {
		e.t.Helper()
		tg, err := os.Readlink(filepath.Join(e.movies, "Amelie The Movie (2001)",
			"Amelie The Movie (2001) {imdb-tt0211915} - [Remux-2160p][TrueHD Atmos 7.1][HDR10][HEVC]-GRP.mkv"))
		if err != nil || tg != filepath.Join(e.mount, "__all__", folder, "movie.mkv") {
			e.t.Fatalf("link %q %v", tg, err)
		}
		if s := e.one(`SELECT state FROM download WHERE id = 1`); s != "imported" {
			e.t.Errorf("download %s", s)
		}
	}
	waiting := func(e *env) {
		e.t.Helper()
		if got := e.one(`SELECT library_import.state || ' ' || download.state || ' ' || last_error FROM library_import
			JOIN download ON download.id = download_id WHERE download_id = 1`); got != "waiting_mount ready release not on the mount yet" {
			e.t.Errorf("download %s", got)
		}
	}

	t.Run("zurg's name finds the folder", func(t *testing.T) {
		e, srv, buf := setup(t, zname)
		e.withZurg(srv)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		imported(e, zname)
		if n := srv.count("/debug/torrents"); n != 1 {
			t.Errorf("%d listings read", n)
		}
		if l := buf.String(); !strings.Contains(l, "release folder found by zurg's name for the hash") ||
			!strings.Contains(l, "download=1") || !strings.Contains(l, provider) {
			t.Errorf("log %q", l)
		}
	})

	t.Run("zurg's name shared by another torrent", func(t *testing.T) {
		e, srv, _ := setup(t, zname+" {abcdef}")
		srv.torrents = append(srv.torrents, ZurgTorrent{Name: zname, Hash: hashB, Size: 7})
		e.withZurg(srv)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		imported(e, zname+" {abcdef}")
	})

	t.Run("exact name logs nothing", func(t *testing.T) {
		e, srv, buf := setup(t, release)
		srv.torrents = []ZurgTorrent{{Name: release, Hash: hashA, Size: 7}}
		e.withZurg(srv)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		imported(e, release)
		if strings.Contains(buf.String(), "zurg's name") {
			t.Errorf("log %q", buf.String())
		}
	})

	t.Run("hash not listed", func(t *testing.T) {
		e, srv, _ := setup(t, zname)
		srv.torrents = []ZurgTorrent{{Name: zname, Hash: hashB, Size: 7}}
		e.withZurg(srv)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		waiting(e)
	})

	t.Run("API fails", func(t *testing.T) {
		e, srv, _ := setup(t, zname)
		srv.fail = true
		e.withZurg(srv)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		waiting(e)
	})

	t.Run("failed listing still confirms the folder", func(t *testing.T) {
		// Only another torrent's folder of the same bare name is on the mount: its entry still rejects it.
		e, srv, _ := setup(t, "")
		e.release(release, map[string]float64{"movie.mkv": 7000})
		srv.entries[davPath(release, "")] = ZurgEntry{Hash: hashB}
		srv.listing = http.StatusInternalServerError
		e.withZurg(srv)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		waiting(e)
		if n := srv.count("/api/library/entry"); n != 1 {
			t.Errorf("%d entries asked", n)
		}
	})

	t.Run("missing listing asked once per interval", func(t *testing.T) {
		e, srv, _ := setup(t, zname)
		srv.listing = http.StatusNotFound
		e.withZurg(srv)
		for range 2 {
			if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
				t.Fatal(err)
			}
		}
		waiting(e)
		if n := srv.count("/debug/torrents"); n != 1 {
			t.Errorf("%d listings within the interval", n)
		}
		e.now = e.now.Add(time.Minute)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		if n := srv.count("/debug/torrents"); n != 2 {
			t.Errorf("%d listings after the interval", n)
		}
	})

	t.Run("API off", func(t *testing.T) {
		e, _, _ := setup(t, zname)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		waiting(e)
	})

	t.Run("API off, the torrent's original name finds the folder", func(t *testing.T) {
		// The provider's status read recorded the name the torrent declared (Real-Debrid's original_filename),
		// the name zurg gives the folder.
		e, _, buf := setup(t, zname)
		e.exec(`UPDATE download SET original_name = ? WHERE id = 1`, zname)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		imported(e, zname)
		if strings.Contains(buf.String(), "zurg's name") {
			t.Errorf("log %q", buf.String())
		}
	})

	t.Run("API off, another torrent's folder of the original name waits", func(t *testing.T) {
		// The registry records the bare folder for another hash: this torrent's folder is "name {hash6}".
		e, _, _ := setup(t, zname)
		e.exec(`UPDATE download SET original_name = ? WHERE id = 1`, zname)
		e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, state, first_seen_at, last_seen_at)
			VALUES (7, ?, ?, ?, 'present', 0, 0)`, hashB, zname, zname)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		waiting(e)
		e.release(zname+" {"+hashA[:6]+"}", map[string]float64{"movie.mkv": 7000})
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		imported(e, zname+" {"+hashA[:6]+"}")
	})

	t.Run("API off, an absent torrent's claim on the original name does not hold", func(t *testing.T) {
		// Provider sync marked the other torrent off every provider: the bare folder is this one's now.
		e, _, _ := setup(t, zname)
		e.exec(`UPDATE download SET original_name = ? WHERE id = 1`, zname)
		e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, state, first_seen_at, last_seen_at)
			VALUES (7, ?, ?, ?, 'absent', 0, 0)`, hashB, zname, zname)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		imported(e, zname)
	})

	t.Run("hash not listed, zurg's entry outweighs another torrent's claim", func(t *testing.T) {
		claimed := func(t *testing.T) (*env, *zurgServer) {
			e, srv, _ := setup(t, zname)
			srv.torrents = nil
			e.exec(`UPDATE download SET original_name = ? WHERE id = 1`, zname)
			e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, state, first_seen_at, last_seen_at)
				VALUES (7, ?, ?, ?, 'present', 0, 0)`, hashB, zname, zname)
			return e, srv
		}
		// The entry names this torrent: imported.
		e, srv := claimed(t)
		e.withZurg(srv)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		imported(e, zname)
		// The entry is unavailable, or names another torrent: the claim stands and the download waits.
		e, srv = claimed(t)
		delete(srv.entries, davPath(zname, ""))
		e.withZurg(srv)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		waiting(e)
		e, srv = claimed(t)
		srv.entries[davPath(zname, "")] = ZurgEntry{Hash: hashB}
		e.withZurg(srv)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		waiting(e)
	})

	t.Run("API off, the original name's folder recorded for this torrent imports", func(t *testing.T) {
		e, _, _ := setup(t, zname)
		e.exec(`UPDATE download SET original_name = ? WHERE id = 1`, zname)
		e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, state, first_seen_at, last_seen_at)
			VALUES (7, ?, ?, ?, 'present', 0, 0)`, hashA, zname, zname)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		imported(e, zname)
	})

	t.Run("original name with zurg's API on logs nothing", func(t *testing.T) {
		e, srv, buf := setup(t, zname)
		e.exec(`UPDATE download SET original_name = ? WHERE id = 1`, zname)
		e.withZurg(srv)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		imported(e, zname)
		if strings.Contains(buf.String(), "zurg's name") {
			t.Errorf("log %q", buf.String())
		}
	})

	t.Run("path import reads no listing", func(t *testing.T) {
		e, srv, _ := setup(t, zname)
		e.exec(`UPDATE download SET source_path = ? WHERE id = 1`, filepath.Join(e.mount, "__all__", zname))
		e.withZurg(srv)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		imported(e, zname)
		if n := srv.count("/debug/torrents"); n != 0 {
			t.Errorf("path import read the listing %d times", n)
		}
	})
}

// TestImportZurgFreshListing: a download that turned ready within ReadyRecheckWindow and is not in the cached listing
// reads the listing again, at most once per freshListFloor; the listing's retry clock holds it back, and outside the
// window the cached listing is used as before.
func TestImportZurgFreshListing(t *testing.T) {
	t.Parallel()
	const (
		zname   = "Movie.Name.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-GRP[TGx]"
		release = "Movie.Name.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-GRP"
	)
	// zurg has the folder but listed the torrent after the first listing was read.
	setup := func(t *testing.T, readyAgo time.Duration) (*env, *zurgServer) {
		e := newEnv(t, true, policy.ModeActive)
		e.release(zname, map[string]float64{"movie.mkv": 7000})
		e.download(1, "movie-remux", 1, 10, release, strings.ToLower(release)+".mkv", "[]")
		e.exec(`UPDATE download SET ready_at = ? WHERE id = 1`, store.Millis(e.now.Add(-readyAgo)))
		srv := newZurgServer(t)
		srv.torrents = []ZurgTorrent{{Name: "Other.Release", Hash: hashB, Size: 7}}
		srv.entries[davPath(zname, "")] = ZurgEntry{Hash: hashA}
		srv.entries[davPath(zname, "movie.mkv")] = ZurgEntry{Hash: hashA, Size: 5}
		e.withZurg(srv)
		return e, srv
	}
	importAt := func(e *env, srv *zurgServer, step time.Duration, reads int) {
		e.t.Helper()
		e.now = e.now.Add(step)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			e.t.Fatal(err)
		}
		if n := srv.count("/debug/torrents"); n != reads {
			e.t.Fatalf("after %v: %d listings read, want %d", step, n, reads)
		}
	}
	state := func(e *env) string {
		return e.one(`SELECT state FROM download WHERE id = 1`)
	}

	t.Run("read again on a miss, once per floor", func(t *testing.T) {
		e, srv := setup(t, 0)
		importAt(e, srv, 0, 1) // the listing just read is fresh enough
		importAt(e, srv, freshListFloor, 2)
		importAt(e, srv, time.Second, 2)
		srv.set(func(s *zurgServer) { s.torrents = append(s.torrents, ZurgTorrent{Name: zname, Hash: hashA, Size: 7}) })
		importAt(e, srv, time.Second, 3)
		if s := state(e); s != "imported" {
			t.Errorf("download %s", s)
		}
	})

	t.Run("outside the window the cached listing stands", func(t *testing.T) {
		e, srv := setup(t, ReadyRecheckWindow)
		importAt(e, srv, 0, 1)
		srv.set(func(s *zurgServer) { s.torrents = append(s.torrents, ZurgTorrent{Name: zname, Hash: hashA, Size: 7}) })
		importAt(e, srv, 10*time.Second, 1)
		if s := state(e); s != "ready" {
			t.Errorf("download %s", s)
		}
	})

	t.Run("a future or missing ready time reads no fresh listing", func(t *testing.T) {
		e, srv := setup(t, -time.Minute)
		importAt(e, srv, 0, 1)
		importAt(e, srv, freshListFloor, 1)
		e.exec(`UPDATE download SET ready_at = NULL WHERE id = 1`)
		importAt(e, srv, freshListFloor, 1)
	})

	t.Run("a failed fresh read keeps the listing's retry clock", func(t *testing.T) {
		e, srv := setup(t, 0)
		importAt(e, srv, 0, 1)
		srv.set(func(s *zurgServer) { s.listing = http.StatusInternalServerError })
		importAt(e, srv, freshListFloor, 2)
		srv.set(func(s *zurgServer) { s.listing = 0 })
		importAt(e, srv, freshListFloor, 2)
		importAt(e, srv, 30*time.Second, 2)
		if s := state(e); s != "ready" {
			t.Errorf("download %s", s)
		}
	})
}

// TestImportZurgForgetsReleaseRoot: with forget_release_root on, an import whose torrent zurg lists but whose folder
// the mount does not show clears the mount's cached listing of the release root, at most once per forgetFloor; not
// when zurg does not list the hash, and not when the setting is off.
func TestImportZurgForgetsReleaseRoot(t *testing.T) {
	t.Parallel()
	const zname = "Movie.Name.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-GRP[TGx]"
	setup := func(t *testing.T, listed string, on bool) (*env, *[]string) {
		e := newEnv(t, true, policy.ModeActive)
		e.download(1, "movie-remux", 1, 10, "Movie.Name.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-GRP",
			"movie.name.2001.mkv", "[]")
		srv := newZurgServer(t)
		srv.torrents = []ZurgTorrent{{Name: zname, Hash: listed, Size: 7}}
		e.withZurg(srv)
		var dirs []string
		if on {
			e.lib.forget = &rootForget{call: func(_ context.Context, dir string) error {
				dirs = append(dirs, dir)
				return nil
			}}
		}
		return e, &dirs
	}
	importAt := func(e *env, step time.Duration) {
		e.t.Helper()
		e.now = e.now.Add(step)
		plan, err := e.lib.Import(e.ctx, 1, false)
		if err != nil || !plan.Waiting {
			e.t.Fatalf("import: %+v %v", plan, err)
		}
	}

	t.Run("listed but not on the mount", func(t *testing.T) {
		e, dirs := setup(t, hashA, true)
		importAt(e, 0)
		if strings.Join(*dirs, ",") != "__all__" {
			t.Fatalf("forgot %q", *dirs)
		}
		importAt(e, time.Second)
		if len(*dirs) != 1 {
			t.Errorf("forgot %d times within the floor", len(*dirs))
		}
		importAt(e, forgetFloor)
		if len(*dirs) != 2 {
			t.Errorf("forgot %d times after the floor", len(*dirs))
		}
	})

	t.Run("hash not listed", func(t *testing.T) {
		e, dirs := setup(t, hashB, true)
		importAt(e, 0)
		if len(*dirs) != 0 {
			t.Errorf("forgot %q", *dirs)
		}
	})

	t.Run("setting off", func(t *testing.T) {
		e, dirs := setup(t, hashA, false)
		importAt(e, 0)
		if len(*dirs) != 0 || e.lib.forget != nil {
			t.Errorf("forgot %q", *dirs)
		}
	})
}

// TestZurgEntryPathEscaping: the entry path goes in the query, escaped; names with spaces, braces and & arrive whole.
func TestZurgEntryPathEscaping(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	srv := newZurgServer(t)
	name := "A & B {abcdef} 100%"
	srv.entries[davPath(name, "Season 1/e 01.mkv")] = ZurgEntry{Hash: hashA, Size: 1}
	z := e.withZurg(srv)
	if ent, err := z.Entry(context.Background(), name, "Season 1/e 01.mkv"); err != nil || ent == nil {
		t.Fatalf("%+v %v", ent, err)
	}
}

// zurg's size is the torrent's, not what it can still serve: a listed torrent with a size whose folder holds only an
// .nfo has lost its video too (live 2026-10-28: 32 such links, every one with a non-zero size).
func TestDanglingReportNoVideoOnMount(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false, policy.ModeShadow)
	e.release("Here", map[string]float64{"a.mkv": 1})
	if err := os.MkdirAll(filepath.Join(e.mount, "__all__", "Lost.Movie"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.mount, "__all__", "Lost.Movie", "info.nfo"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for p, tg := range map[string]string{
		filepath.Join(e.movies, "Good (2001)", "Good.mkv"):   "Here/a.mkv",
		filepath.Join(e.movies, "Lost (2001)", "Lost.mkv"):   "Lost.Movie/movie.mkv",
		filepath.Join(e.movies, "Other (2003)", "Other.mkv"): "Here/b.mkv",
	} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.Symlink(filepath.Join(e.mount, "__all__", tg), p); err != nil {
			t.Fatal(err)
		}
	}
	srv := newZurgServer(t)
	srv.torrents = []ZurgTorrent{{Name: "Here", Hash: hashA, Size: 5}, {Name: "Lost.Movie", Hash: hashB, Size: 6759132069}}
	e.withZurg(srv)
	sum, err := e.lib.DanglingReport(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Dangling != 2 || sum.Zurg == nil || sum.Zurg.NoVideo != 1 || sum.Zurg.NoVideoTorrents != 0 {
		t.Fatalf("summary %+v zurg %+v", sum, sum.Zurg)
	}
	rows, _, err := Dangling(e.ctx, e.st.Reader())
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows %+v %v", rows, err)
	}
	for _, r := range rows {
		// "Here" still has a video (a.mkv): the missing b.mkv is some other cause, not a lost torrent.
		want := ""
		if r.Folder == "Lost.Movie" {
			want = CauseNoVideo
		}
		if r.Cause != want {
			t.Errorf("%s: cause %q, want %q", r.Path, r.Cause, want)
		}
	}
}

// A settings save builds a new library and zurg client; the repair grace, backoff and cached list must carry over
// (only for the same zurg), or every save would hold dangling links as "repairing" for a fresh grace.
func TestZurgAPIInheritsStateAcrossGenerations(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	prev := &ZurgAPI{URL: "http://zurg:9999", AllDir: "__all__"}
	prev.list, prev.listAt, prev.downUntil = &ZurgTorrents{}, since, since.Add(time.Minute)
	prev.running, prev.repairSince, prev.graceWarned = true, since, true
	next := &ZurgAPI{URL: "http://zurg:9999", AllDir: "__all__"}
	next.inherit(prev)
	if next.list != prev.list || !next.listAt.Equal(since) || !next.downUntil.Equal(since.Add(time.Minute)) ||
		!next.running || !next.repairSince.Equal(since) || !next.graceWarned {
		t.Fatalf("state not carried over: %+v", next)
	}
	other := &ZurgAPI{URL: "http://other:9999", AllDir: "__all__"}
	other.inherit(prev)
	if other.list != nil || other.running || !other.repairSince.IsZero() {
		t.Fatal("state carried to a different zurg")
	}
	var none *ZurgAPI
	none.inherit(prev) // no zurg API configured: a no-op
}
