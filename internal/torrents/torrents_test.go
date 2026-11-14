package torrents

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// Hashes used across the tests (invented).
const (
	hA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hC = "cccccccccccccccccccccccccccccccccccccccc"
	hD = "dddddddddddddddddddddddddddddddddddddddd"
	hE = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	hF = "ffffffffffffffffffffffffffffffffffffffff"
	hG = "1111111111111111111111111111111111111111"
	hH = "2222222222222222222222222222222222222222"
)

const mountPath = "/mnt/test-debrid"

type fakeProvider struct {
	key, kind string
	list      []provider.Torrent
	listErr   error
	detail    map[string]*provider.Torrent
	detailErr error
	reads     int
	lists     int    // list calls
	onDetail  func() // runs on each detail read
}

func (f *fakeProvider) Key() string  { return f.key }
func (f *fakeProvider) Kind() string { return f.kind }
func (f *fakeProvider) Account(context.Context) (*provider.Account, error) {
	return &provider.Account{}, nil
}

func (f *fakeProvider) Torrents(context.Context) ([]provider.Torrent, error) {
	f.lists++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]provider.Torrent(nil), f.list...), nil
}

func (f *fakeProvider) Torrent(_ context.Context, id string) (*provider.Torrent, error) {
	f.reads++
	if f.onDetail != nil {
		f.onDetail()
	}
	if f.detailErr != nil {
		return nil, f.detailErr
	}
	if t := f.detail[id]; t != nil {
		c := *t
		return &c, nil
	}
	for _, t := range f.list {
		if t.ID == id {
			c := t
			c.Files = []provider.File{}
			if c.OriginalName == "" {
				c.OriginalName = c.Name // a detail read names it (Real-Debrid's original_filename)
			}
			return &c, nil
		}
	}
	return nil, errors.New("no such torrent")
}

func (f *fakeProvider) Cache() provider.CacheSupport { return provider.CacheSupport{} }
func (f *fakeProvider) CheckCached(context.Context, provider.CacheQuery) (map[string]bool, error) {
	return nil, provider.ErrCacheUnsupported
}

type fakeAdder struct {
	key, kind string
	selects   bool
	added     []string
	addErr    map[string]error
	ready     map[string]bool
	selected  map[string][]string
	deleted   int
	deleteErr error
	onAdd     func(id, hash string)
	onDelete  func()
}

func (a *fakeAdder) Key() string        { return a.key }
func (a *fakeAdder) Kind() string       { return a.kind }
func (a *fakeAdder) SelectsFiles() bool { return a.selects }
func (a *fakeAdder) AddMagnet(_ context.Context, m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
	if err := a.addErr[m.Hash()]; err != nil {
		return nil, err
	}
	a.added = append(a.added, m.Hash())
	id := "new-" + m.Hash()[:4]
	if a.onAdd != nil {
		a.onAdd(id, m.Hash())
	}
	return &adder.Added{ID: id, Hash: m.Hash(), Ready: a.ready[m.Hash()]}, nil
}

func (a *fakeAdder) SelectFiles(_ context.Context, id string, ids []string) error {
	if a.selected == nil {
		a.selected = map[string][]string{}
	}
	a.selected[id] = ids
	return nil
}

func (a *fakeAdder) DeleteUserChosen(_ context.Context, c adder.UserChoice) error {
	if a.onDelete != nil {
		a.onDelete()
	}
	if a.deleteErr != nil {
		return a.deleteErr
	}
	a.deleted++
	return nil
}

type env struct {
	t   *testing.T
	st  *store.Store
	m   *Manager
	rd  *fakeProvider
	tb  *fakeProvider
	ard *fakeAdder
	atb *fakeAdder
	now time.Time
	pol *policy.Policy
}

func newEnv(t *testing.T) *env {
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
	pol := &policy.Policy{
		Providers: map[string]*policy.Provider{
			"rd": {Kind: provider.KindRealDebrid, Order: 1},
			"tb": {Kind: provider.KindTorBox, Order: 2},
		},
		Indexers: map[string]*policy.Indexer{
			"pub":  {Privacy: policy.PrivacyPublic},
			"priv": {Privacy: policy.PrivacyPrivate},
		},
		Library: &policy.Library{Mount: policy.LibraryMount{Path: mountPath}},
	}
	e := &env{t: t, st: st, pol: pol, now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
		rd:  &fakeProvider{key: "rd", kind: provider.KindRealDebrid},
		tb:  &fakeProvider{key: "tb", kind: provider.KindTorBox},
		ard: &fakeAdder{key: "rd", kind: provider.KindRealDebrid, selects: true},
		atb: &fakeAdder{key: "tb", kind: provider.KindTorBox}}
	e.m = &Manager{Store: st, Policy: pol,
		Providers: map[string]provider.Provider{"rd": e.rd, "tb": e.tb},
		Adders:    map[string]adder.Adder{"rd": e.ard, "tb": e.atb},
		Dir:       filepath.Join(t.TempDir(), "torrent-backups"),
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:       func() time.Time { return e.now },
		Sleep: func(_ context.Context, d time.Duration) error {
			e.now = e.now.Add(d)
			return nil
		},
		MountPaths: []string{mountPath}}
	e.exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'M', 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'v', 0, 0)`)
	return e
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().ExecContext(context.Background(), q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) content(hash string, files int) int64 {
	e.t.Helper()
	res, err := e.st.Writer().ExecContext(context.Background(), `INSERT INTO content (info_hash, name, first_seen_at,
		last_seen_at) VALUES (?, '', 0, 0)`, hash)
	if err != nil {
		e.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	for i := range files {
		e.exec(`INSERT INTO content_file (content_id, path, size_bytes) VALUES (?, ?, 1)`, id,
			"f"+string(rune('a'+i))+".mkv")
	}
	return id
}

// record writes what each provider's fake lists now into the record (as a successful provider sync does; a provider
// whose list fails keeps its record), so a report shows it.
func (e *env) record() {
	e.t.Helper()
	ctx := context.Background()
	for k, p := range e.m.Providers {
		list, err := p.Torrents(ctx)
		if err != nil {
			continue
		}
		if err := e.st.WriteTx(ctx, func(tx *sql.Tx) error {
			return provider.WriteRecord(ctx, tx, k, list, store.Millis(e.now))
		}); err != nil {
			e.t.Fatal(err)
		}
	}
}

// report is the report after recording the fakes' lists (record).
func (e *env) report(ctx context.Context) (*Report, error) {
	e.record()
	return e.m.Report(ctx)
}

func (e *env) synced(prov string) {
	e.exec(`INSERT INTO provider_sync_run (provider, started_at, finished_at, ok) VALUES (?, 0, 0, 1)`, prov)
}

func tor(id, hash, name string, bytes int64, added time.Time, state provider.State) provider.Torrent {
	return provider.Torrent{ID: id, Hash: hash, Name: name, Bytes: bytes, Added: added, State: state,
		Status: string(state)}
}

func find(r *Report, prov, id string) *Torrent {
	for _, p := range r.Providers {
		for i := range p.Torrents {
			if p.Provider == prov && p.Torrents[i].ID == id {
				return &p.Torrents[i]
			}
		}
	}
	return nil
}

// usageFixture: rd holds A (an Theatarr link by content hash), B (a link by mount folder), C (an arr file), D (a queued
// download), E (a symlink under a scan root), F (in the registry, unreferenced), G (not in the registry); tb was
// never synced.
func usageFixture(t *testing.T) (*env, string) {
	e := newEnv(t)
	old := e.now.Add(-72 * time.Hour)
	e.rd.list = []provider.Torrent{
		tor("1", hA, "Movie.A.2020.1080p.WEB-DL", 100, old, provider.StateDownloaded),
		tor("2", hB, "Movie.B.2021.2160p.WEB-DL", 200, old, provider.StateDownloaded),
		tor("3", hC, "Movie.C.2019.720p.BluRay", 300, old, provider.StateDownloaded),
		tor("4", hD, "Movie.D.2018.1080p.WEB-DL", 400, old, provider.StateDownloaded),
		tor("5", hE, "Movie.E.2017.1080p.WEB-DL", 500, old, provider.StateDownloaded),
		tor("6", hF, "Movie.F.2016.1080p.WEB-DL", 600, old, provider.StateDownloaded),
		tor("7", hG, "Movie.G.2015.1080p.WEB-DL", 700, old, provider.StateDownloaded),
	}
	e.tb.list = []provider.Torrent{tor("90", hF, "Movie.F.2016.1080p.WEB-DL", 600, old, provider.StateDownloaded)}
	e.synced("rd")
	for _, h := range []string{hA, hB, hC, hD, hE} {
		e.content(h, 1)
	}
	cA := e.content(hF, 2)
	_ = cA
	var fileA int64
	if err := e.st.Reader().QueryRow(`SELECT f.id FROM content_file f JOIN content c ON c.id = f.content_id
		WHERE c.info_hash = ?`, hA).Scan(&fileA); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (path, target, content_file_id, title_id, created_at, updated_at)
		VALUES ('/lib/A/A.mkv', '/elsewhere/A.mkv', ?, 1, 0, 0)`, fileA)
	e.exec(`INSERT INTO link (path, target, title_id, created_at, updated_at, state)
		VALUES ('/lib/B/B.mkv', ?, 1, 0, 0, 'dead')`, mountPath+"/__all__/Movie.B.2021.2160p.WEB-DL/B.mkv")
	e.exec(`INSERT INTO link (path, target, title_id, created_at, updated_at, state)
		VALUES ('/lib/old.mkv', ?, 1, 0, 0, 'unlinked')`, mountPath+"/__all__/Movie.F.2016.1080p.WEB-DL/F.mkv")
	e.exec(`INSERT INTO version_file (version_id, title_id, instance, arr_file_id, path, download_id)
		VALUES (1, 1, 'radarr', 7, '/movies/C.mkv', ?)`, strings.ToUpper(hC))
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		provider, provider_torrent_id, created_at, updated_at) VALUES (1, 1, 'v', ?, 'D', 'pub', 'queued', 'rd', '4',
		0, 0)`, hD)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		provider, provider_torrent_id, created_at, updated_at) VALUES (1, 1, 'v', ?, 'F', 'pub', 'imported', 'rd',
		'6', 0, 0)`, hF)
	root := t.TempDir()
	if err := os.Symlink(mountPath+"/__all__/Movie.E.2017.1080p.WEB-DL/E.mkv", filepath.Join(root, "E.mkv")); err != nil {
		t.Fatal(err)
	}
	e.pol.Torrents = &policy.Torrents{ScanRoots: []string{root}}
	return e, root
}

func TestUsage(t *testing.T) {
	t.Parallel()
	e, _ := usageFixture(t)
	r, err := e.report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		u    Usage
		kind string
	}{
		"1": {Used, RefLink}, "2": {Used, RefLink}, "3": {Used, RefArrFile}, "4": {Used, RefDownload},
		"5": {Used, RefScan}, "6": {Unused, ""}, "7": {Unknown, ""},
	}
	for id, w := range want {
		got := find(r, "rd", id)
		if got == nil {
			t.Fatalf("rd %s missing", id)
		}
		if got.Usage != w.u || (w.kind != "" && got.Refs[w.kind] != 1) {
			t.Errorf("rd %s: usage %s refs %v, want %s by %q (reasons %v)", id, got.Usage, got.Refs, w.u, w.kind,
				got.Reasons)
		}
	}
	if got := find(r, "rd", "2"); got.LinkedFiles != 1 || !strings.Contains(got.RefList[0].Detail, "(dead)") {
		t.Errorf("a dead link is a reference, shown as dead: %+v", got)
	}
	for _, id := range []string{"1", "3", "4"} {
		if got := find(r, "rd", id); len(got.Titles) != 1 || got.Titles[0] != (LinkedTitle{ID: 1, Name: "M"}) ||
			got.RefList[0].TitleID != 1 {
			t.Errorf("rd %s: its references name title 1: %+v", id, got)
		}
	}
	if got := find(r, "rd", "5"); len(got.Titles) != 0 {
		t.Errorf("a scanned link names no title: %+v", got.Titles)
	}
	if got := find(r, "rd", "6"); !got.AddedByTheatarr || got.Files != 2 {
		t.Errorf("F was added by the router and has 2 registry files: %+v", got)
	}
	if got := find(r, "rd", "7"); !strings.Contains(strings.Join(got.Reasons, ";"), "not in the registry") {
		t.Errorf("G reasons: %v", got.Reasons)
	}
	if got := find(r, "tb", "90"); got.Usage != Unknown ||
		!strings.Contains(strings.Join(got.Reasons, ";"), "no complete sync of tb") {
		t.Errorf("tb never synced: %+v", got)
	}
	if len(r.Unused) != 1 || r.Unused[0] != (Member{Provider: "rd", ID: "6"}) {
		t.Errorf("unused group: %+v", r.Unused)
	}
}

// The titles of a torrent are those of all its references, also the ones past RefList's cut.
func TestReportTitlesBeforeCut(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.synced("rd")
	e.rd.list = []provider.Torrent{tor("1", hA, "Show.S01.1080p.WEB-DL", 100, e.now, provider.StateDownloaded)}
	c := e.content(hA, 1)
	var file int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM content_file WHERE content_id = ?`, c).Scan(&file); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (2, 'series', 'Another', 0, 0)`)
	for i := range maxRefList + 1 {
		title := 1
		if i == maxRefList {
			title = 2
		}
		e.exec(`INSERT INTO link (path, target, content_file_id, title_id, created_at, updated_at)
			VALUES (?, '/elsewhere/x.mkv', ?, ?, 0, 0)`, "/lib/"+string(rune('a'+i))+".mkv", file, title)
	}
	r, err := e.report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := find(r, "rd", "1")
	if len(got.RefList) != maxRefList || got.Refs[RefLink] != maxRefList+1 {
		t.Fatalf("refs: %d listed, %v", len(got.RefList), got.Refs)
	}
	want := []LinkedTitle{{ID: 2, Name: "Another"}, {ID: 1, Name: "M"}}
	if len(got.Titles) != 2 || got.Titles[0] != want[0] || got.Titles[1] != want[1] {
		t.Errorf("titles: %+v, want %+v", got.Titles, want)
	}
}

func TestUsageUnreadableScanRoot(t *testing.T) {
	t.Parallel()
	e, _ := usageFixture(t)
	e.pol.Torrents.ScanRoots = append(e.pol.Torrents.ScanRoots, filepath.Join(t.TempDir(), "missing"))
	r, err := e.report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := find(r, "rd", "6"); got.Usage != Unknown {
		t.Errorf("an unreadable scan root makes unreferenced torrents unknown, got %s", got.Usage)
	}
	if got := find(r, "rd", "5"); got.Usage != Used {
		t.Errorf("references found still count: %s", got.Usage)
	}
	if len(r.Unused) != 0 {
		t.Errorf("nothing is unused: %+v", r.Unused)
	}
}

func TestUsageScanRootInMount(t *testing.T) {
	t.Parallel()
	e, _ := usageFixture(t)
	e.pol.Torrents.ScanRoots = []string{mountPath + "/__all__"}
	r, err := e.report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := find(r, "rd", "6"); got.Usage != Unknown ||
		!strings.Contains(strings.Join(got.Reasons, ";"), "inside the debrid mount") {
		t.Errorf("a scan root in the mount is refused: %+v", got)
	}
}

func TestReportGroups(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.synced("rd")
	e.synced("tb")
	old := e.now.Add(-72 * time.Hour)
	e.rd.list = []provider.Torrent{
		tor("1", hA, "Show.Name.S01E02.1080p.WEB-DL.x264-GRP", 100, old, provider.StateDownloaded),
		tor("2", hB, "Show.Name.S01E02.2160p.WEB-DL.x265-OTHER", 900, old, provider.StateDownloaded),
		tor("3", hC, "Broken.Movie.2001.1080p.BluRay", 50, old, provider.StateError),
		tor("4", hD, "Slow.Movie.2002.1080p.BluRay", 60, old, provider.StateDownloading),
		tor("5", hE, "New.Movie.2003.1080p.BluRay", 70, e.now.Add(-time.Hour), provider.StateDownloading),
	}
	e.tb.list = []provider.Torrent{tor("10", hA, "Show.Name.S01E02.1080p.WEB-DL.x264-GRP", 100, old,
		provider.StateDownloaded)}
	for _, h := range []string{hA, hB, hC, hD, hE} {
		e.content(h, 1)
	}
	r, err := e.report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.DuplicateHash) != 1 || len(r.DuplicateHash[0].Members) != 2 {
		t.Fatalf("duplicate hash: %+v", r.DuplicateHash)
	}
	if len(r.DuplicateTitle) != 1 || len(r.DuplicateTitle[0].Members) != 3 ||
		!strings.Contains(r.DuplicateTitle[0].Label, "S01E02") {
		t.Fatalf("duplicate title: %+v", r.DuplicateTitle)
	}
	if len(r.Failed) != 1 || r.Failed[0].ID != "3" {
		t.Errorf("failed: %+v", r.Failed)
	}
	if len(r.Stalled) != 1 || r.Stalled[0].ID != "4" {
		t.Errorf("stalled (downloading over a day; the new one is not): %+v", r.Stalled)
	}
	if len(r.Largest) == 0 || r.Largest[0].ID != "2" {
		t.Errorf("largest: %+v", r.Largest)
	}
	if got := find(r, "rd", "1"); got.Quality == "" {
		t.Errorf("quality parsed: %+v", got)
	}
}

// A film whose year the series parser reads as an episode is the film; real series names stay series.
func TestParseNameFilmYear(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, label string }{
		{"The.Dark.Knight.Rises.2012.UHD.BluRay.2160p.TrueHD.Atmos.7.1.DV.HEVC.REMUX-FraMeSToR", "The Dark Knight Rises (2012)"},
		{"Transformers.The.Last.Knight.2017.1080p.BluRay.x264-SPARKS", "Transformers The Last Knight (2017)"},
		{"Gladiator.2000.1080p.BluRay.x264-GRP", "Gladiator (2000)"},
		{"A.Knight.of.the.Seven.Kingdoms.S01.1080p.WEB-DL.DDP5.1.H.264-NTb", "A Knight of the Seven Kingdoms S01"},
		{"Show.2020.S01E02.1080p.WEB-DL-GRP", "Show 2020 S01E02"},
		{"[SubsPlease] One Piece - 1085 (1080p) [ABCD1234].mkv", "One Piece 1085"},
		{"[Sub] Anime (2023) - 05 [ABCD1234]", "Anime (2023) 5"},
		{"The.Daily.Show.2024.03.14.1080p.WEB.h264-GRP", "The Daily Show (2024)"},
	} {
		if _, label, _ := parseName(c.name, 2026); label != c.label {
			t.Errorf("%s: %q, want %q", c.name, label, c.label)
		}
	}
	if key, _, _ := parseName("The.Dark.Knight.Rises.2012.1080p.BluRay", 2026); key != "movie:thedarkknightrises:2012" {
		t.Errorf("duplicate-title key of a film: %q", key)
	}
}

func TestBackup(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	old := e.now.Add(-72 * time.Hour)
	e.rd.list = []provider.Torrent{tor("1", hA, "A", 10, old, provider.StateDownloaded),
		tor("2", hB, "B", 20, old, provider.StateDownloaded)}
	e.rd.detail = map[string]*provider.Torrent{"1": {ID: "1", Hash: hA, Files: []provider.File{
		{ID: "1", Path: "/A/a.mkv", Bytes: 9, Selected: true}, {ID: "2", Path: "/A/a.nfo", Bytes: 1}}}}
	e.tb.list = []provider.Torrent{{ID: "9", Hash: strings.ToUpper(hC), Name: "C", Bytes: 30, Added: old,
		Files: []provider.File{{ID: "0", Path: "C/c.mkv", Bytes: 30, Selected: true}}}}
	b, info, err := e.m.TakeBackup(ctx, BackupOptions{Reason: ReasonManual, By: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Torrents != 3 || info.Bytes != 60 || len(info.Providers) != 2 {
		t.Errorf("info: %+v", info)
	}
	if got := b.Providers[0].Torrents[0]; !got.FilesKnown || len(got.Files) != 1 || got.Files[0].Path != "/A/a.mkv" {
		t.Errorf("rd 1 selected files: %+v", got)
	}
	if got := b.Providers[1].Torrents[0]; got.Hash != hC || len(got.Files) != 1 {
		t.Errorf("tb lists files and hashes are lower-cased: %+v", got)
	}
	st, err := os.Stat(filepath.Join(e.m.Dir, b.ID+".json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("backup file mode: %v %v", st, err)
	}
	if dst, _ := os.Stat(e.m.Dir); dst.Mode().Perm() != 0o700 {
		t.Errorf("backup dir mode %v", dst.Mode().Perm())
	}
	reads := e.rd.reads
	e.now = e.now.Add(time.Hour)
	if _, _, err := e.m.TakeBackup(ctx, BackupOptions{Reason: ReasonScheduled}); err != nil {
		t.Fatal(err)
	}
	if e.rd.reads != reads {
		t.Errorf("a second backup reuses the files the first read: %d reads, then %d", reads, e.rd.reads)
	}
	list, err := e.m.Backups()
	if err != nil || len(list) != 2 || list[0].Reason != ReasonScheduled {
		t.Fatalf("backups newest first: %+v %v", list, err)
	}
	dmm := b.DMM("")
	if len(dmm) != 3 || dmm[0] != (DMMEntry{Filename: "A", Hash: hA}) {
		t.Errorf("dmm export: %+v", dmm)
	}
	if _, err := e.m.ReadBackup("../../etc/passwd"); !errors.Is(err, ErrNoBackup) {
		t.Errorf("a bad id is no backup: %v", err)
	}
}

func TestBackupPrune(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.pol.Torrents = &policy.Torrents{Backup: policy.TorrentBackup{Keep: 2}}
	for range 5 {
		if _, _, err := e.m.TakeBackup(context.Background(), BackupOptions{}); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(3 * 24 * time.Hour)
	}
	list, _ := e.m.Backups()
	// keep 2, plus any from the last 7 days: the backups at +9d and +12d are the newest two; +6d is within a week.
	if len(list) != 3 {
		t.Fatalf("kept %d: %+v", len(list), list)
	}
}

func TestImportBackup(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	dmm := []byte(`[{"filename":"A","hash":"` + strings.ToUpper(hA) + `"},{"filename":"A again","hash":"` + hA +
		`"},{"filename":"B","hash":"` + hB + `"}]`)
	if _, _, err := e.m.ImportBackup(ctx, dmm, "", "admin"); err == nil {
		t.Error("a DMM backup needs a target provider")
	}
	b, info, err := e.m.ImportBackup(ctx, dmm, "rd", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if info.Torrents != 2 || b.Reason != ReasonImport || b.Providers[0].Provider != "rd" ||
		b.Providers[0].Torrents[0].Hash != hA {
		t.Errorf("imported: %+v %+v", info, b.Providers)
	}
	if _, _, err := e.m.ImportBackup(ctx, []byte(`{"nope":1}`), "rd", "admin"); err == nil {
		t.Error("an unknown document is refused")
	}
	if _, _, err := e.m.ImportBackup(ctx, dmm, "xx", "admin"); err == nil {
		t.Error("an unknown provider is refused")
	}
}

func TestRestore(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	e.pol.Providers["rd"].Pacing = policy.Pacing{AddsPerMinute: 1}
	old := e.now.Add(-72 * time.Hour)
	// The backup: A (still present), B (an RSS pick from a private indexer), C (download from an indexer not in the
	// policy), D and E (D with a file selection, E planned from a public indexer), "zz" (not a hash). B to E are
	// re-added: privacy is a label only.
	e.rd.list = []provider.Torrent{tor("1", hA, "A", 1, old, provider.StateDownloaded),
		tor("2", hB, "B", 2, old, provider.StateDownloaded), tor("3", hC, "C", 3, old, provider.StateDownloaded),
		tor("4", hD, "D", 4, old, provider.StateDownloaded), tor("5", hE, "E", 5, old, provider.StateDownloaded),
		tor("6", "zz", "Z", 6, old, provider.StateDownloaded)}
	e.rd.detail = map[string]*provider.Torrent{"4": {ID: "4", Hash: hD, Files: []provider.File{
		{ID: "1", Path: "/D/sample.mkv"}, {ID: "2", Path: "/D/d.mkv", Selected: true}}},
		"5": {ID: "5", Hash: hE, Files: []provider.File{{ID: "1", Path: "/E/e.mkv", Selected: true}}}}
	b, _, err := e.m.TakeBackup(ctx, BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e.rd.list = e.rd.list[:1]
	// After the add, RD lists D's files and waits for a selection, with new file ids.
	e.ard.onAdd = func(id, hash string) {
		if hash == hD {
			e.rd.detail[id] = &provider.Torrent{ID: id, Hash: hD, Status: rdWaitingFiles, Files: []provider.File{
				{ID: "7", Path: "/D/sample.mkv"}, {ID: "8", Path: "/D/d.mkv"}}}
		}
		if hash == hE {
			e.rd.detail[id] = &provider.Torrent{ID: id, Hash: hE, Status: rdWaitingFiles, Files: []provider.File{
				{ID: "3", Path: "/E/e.mkv"}}}
		}
	}
	req := RestoreRequest{BackupID: b.ID}
	plan, err := e.m.PlanRestore(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	acts := map[string]string{}
	for _, it := range plan.Items {
		acts[it.Name] = it.Action
	}
	wantActs := map[string]string{"A": ActPresent, "B": ActReadd, "C": ActReadd, "D": ActReadd, "E": ActReadd,
		"Z": ActInvalid}
	for k, v := range wantActs {
		if acts[k] != v {
			t.Errorf("%s: %s, want %s", k, acts[k], v)
		}
	}
	if _, err := e.m.CheckRestore(ctx, req, "r1-2-wrong"); !errors.Is(err, ErrConfirmation) {
		t.Errorf("a wrong token is refused: %v", err)
	}
	if _, err := e.m.CheckRestore(ctx, req, plan.Token); err != nil {
		t.Fatal(err)
	}
	// Only the confirmed set is added.
	start := e.now
	got, err := e.m.Restore(ctx, req, []string{"rd|" + hD, "rd|" + hE}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(e.ard.added, ",") != hD+","+hE {
		t.Fatalf("added %v", e.ard.added)
	}
	if sel := e.ard.selected["new-dddd"]; len(sel) != 1 || sel[0] != "8" {
		t.Errorf("D's selection maps by path to the new file ids: %v", sel)
	}
	if e.now.Sub(start) < time.Minute-time.Second {
		t.Errorf("adds_per_minute 1 paces the second add: waited %s", e.now.Sub(start))
	}
	for _, it := range got.Items {
		want := OutUncached // the fake is not ready, so uncached
		if it.Name == "B" || it.Name == "C" {
			want = OutSkipped // not in the confirmed set
		}
		if it.Action == ActReadd && it.Outcome != want {
			t.Errorf("%s: outcome %s, want %s", it.Name, it.Outcome, want)
		}
	}
	j, _ := e.m.Journal(ctx, 10)
	if len(j) != 2 || j[0].Action != "restore" || j[0].BackupID != b.ID {
		t.Errorf("journal: %+v", j)
	}
}

func deleteFixture(t *testing.T) *env {
	e, _ := usageFixture(t)
	e.rd.list = append(e.rd.list, tor("8", hH, "Movie.H.2014.1080p.WEB-DL", 800, e.now.Add(-time.Hour),
		provider.StateDownloaded))
	e.content(hH, 1)
	return e
}

func TestDelete(t *testing.T) {
	t.Parallel()
	e := deleteFixture(t)
	ctx := context.Background()
	sel := []DeleteSelection{
		{Provider: "rd", ID: "6", Hash: hF},                  // unused: deleted
		{Provider: "rd", ID: "1", Hash: hA},                  // used: refused
		{Provider: "rd", ID: "8", Hash: hH},                  // added an hour ago: refused
		{Provider: "rd", ID: "7", Hash: hB},                  // hash changed: refused
		{Provider: "rd", ID: "99", Hash: hB},                 // gone: refused
		{Provider: "tb", ID: "90", Hash: hF, Override: true}, // unknown with a wrong override: refused
	}
	// The record holds rd 99 too: a delete lists the provider again, the record is not its authority.
	e.record()
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash, name) VALUES ('rd', '99', ?, 'Gone')`, hB)
	e.rd.lists = 0
	plan, err := e.m.PlanDelete(ctx, sel)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Count != 1 || plan.Refused != 5 || plan.Bytes != 600 || plan.Warning == "" {
		t.Fatalf("plan: count %d refused %d bytes %d", plan.Count, plan.Refused, plan.Bytes)
	}
	if !plan.Items[1].NeedsOverride || !strings.Contains(plan.Items[1].Refusal, "1 link") ||
		plan.Items[1].ExpectedOverride == "" {
		t.Errorf("a used torrent names its references: %+v", plan.Items[1])
	}
	if !plan.Items[2].NeedsRecent {
		t.Errorf("a new torrent needs allow_recent: %+v", plan.Items[2])
	}
	// The wrong token deletes nothing and takes no backup.
	if _, err := e.m.Delete(ctx, sel, "d1-1-600-0000", "admin"); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("wrong token: %v", err)
	}
	if list, _ := e.m.Backups(); e.ard.deleted != 0 || len(list) != 0 {
		t.Fatalf("nothing happens without the confirmation: %d deletes, %d backups", e.ard.deleted, len(list))
	}
	done, err := e.m.Delete(ctx, sel, plan.Token, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if e.ard.deleted != 1 || e.atb.deleted != 0 || done.Deleted != 1 || done.BackupID == "" {
		t.Fatalf("deleted rd %d tb %d: %+v", e.ard.deleted, e.atb.deleted, done)
	}
	b, err := e.m.ReadBackup(done.BackupID)
	if err != nil || b.Reason != ReasonPreDelete || len(b.Providers) != 2 {
		t.Fatalf("pre-delete backup: %+v %v", b, err)
	}
	j, _ := e.m.Journal(ctx, 10)
	var deletes int
	for _, x := range j {
		if x.Action == "delete" {
			deletes++
			if x.Hash != hF || x.BackupID != done.BackupID || x.By != "admin" || x.Outcome != "deleted" {
				t.Errorf("journal: %+v", x)
			}
		}
	}
	if deletes != 1 {
		t.Errorf("one delete journaled: %+v", j)
	}
	if e.rd.lists == 0 || plan.Items[4].Refusal != "no longer on the provider" {
		t.Errorf("the delete listed rd (%d lists) and refused 99 the record still holds: %+v", e.rd.lists, plan.Items[4])
	}
	var left string
	if err := e.st.Reader().QueryRow(`SELECT group_concat(torrent_id, ' ') FROM (SELECT torrent_id FROM provider_torrent
		WHERE provider = 'rd' ORDER BY torrent_id)`).Scan(&left); err != nil || strings.Contains(" "+left+" ", " 6 ") ||
		!strings.Contains(left, "1") {
		t.Errorf("the deleted torrent left the record, the refused ones stay: %q %v", left, err)
	}
}

func TestDeleteOverrides(t *testing.T) {
	t.Parallel()
	e := deleteFixture(t)
	ctx := context.Background()
	first, err := e.m.PlanDelete(ctx, []DeleteSelection{{Provider: "rd", ID: "1", Hash: hA},
		{Provider: "rd", ID: "8", Hash: hH}})
	if err != nil {
		t.Fatal(err)
	}
	sel := []DeleteSelection{
		{Provider: "rd", ID: "1", Hash: hA, Override: true, OverrideToken: first.Items[0].ExpectedOverride},
		{Provider: "rd", ID: "8", Hash: hH, AllowRecent: true},
	}
	plan, err := e.m.PlanDelete(ctx, sel)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Count != 2 {
		t.Fatalf("overrides allow both: %+v", plan.Items)
	}
	// A new reference since the preview invalidates the override.
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (1, 1, 'v', ?, 'A', 'pub', 'downloading', 0, 0)`, hA)
	if _, err := e.m.Delete(ctx, sel, plan.Token, "admin"); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("the set changed: %v", err)
	}
	if e.ard.deleted != 0 {
		t.Fatal("nothing deleted")
	}
}

func TestDeleteBackupFailsClosed(t *testing.T) {
	t.Parallel()
	e := deleteFixture(t)
	ctx := context.Background()
	sel := []DeleteSelection{{Provider: "rd", ID: "6", Hash: hF}}
	plan, err := e.m.PlanDelete(ctx, sel)
	if err != nil || plan.Count != 1 {
		t.Fatal(plan, err)
	}
	// The plan's read of its original name passes (a scan root link is known only by its folder); the backup's fails.
	reads := 0
	e.rd.onDetail = func() {
		if reads++; reads > 1 {
			e.rd.detailErr = errors.New("rd is down")
		}
	}
	if _, err := e.m.Delete(ctx, sel, plan.Token, "admin"); !errors.Is(err, ErrBackupFailed) {
		t.Fatalf("no backup, no delete: %v", err)
	}
	if e.ard.deleted != 0 {
		t.Fatal("deleted without a backup")
	}
	e.rd.onDetail, e.rd.detailErr = nil, nil
	e.m.Dir = "" // no backup directory: fails closed too
	if _, err := e.m.Delete(ctx, sel, plan.Token, "admin"); !errors.Is(err, ErrBackupFailed) || e.ard.deleted != 0 {
		t.Fatalf("no backup dir: %v, %d deleted", err, e.ard.deleted)
	}
}

func TestDeleteStopsProviderOnError(t *testing.T) {
	t.Parallel()
	e := deleteFixture(t)
	ctx := context.Background()
	e.rd.list = append(e.rd.list, tor("11", hB[:39]+"1", "X", 1, e.now.Add(-48*time.Hour), provider.StateDownloaded))
	e.content(hB[:39]+"1", 1)
	e.ard.deleteErr = &adder.Error{Provider: "rd", Code: adder.CodeRateLimited}
	sel := []DeleteSelection{{Provider: "rd", ID: "6", Hash: hF}, {Provider: "rd", ID: "11", Hash: hB[:39] + "1"}}
	plan, _ := e.m.PlanDelete(ctx, sel)
	if plan.Count != 2 {
		t.Fatalf("plan: %+v", plan.Items)
	}
	done, err := e.m.Delete(ctx, sel, plan.Token, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if done.Failed != 1 || done.Items[1].Outcome != OutSkipped {
		t.Errorf("a rate limit stops the provider: %+v", done.Items)
	}
}

func TestBackupDue(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if e.m.BackupDue() {
		t.Error("scheduled backups are off by default")
	}
	e.pol.Torrents = &policy.Torrents{Backup: policy.TorrentBackup{Every: policy.Duration(24 * time.Hour)}}
	if !e.m.BackupDue() {
		t.Error("due with no scheduled backup yet")
	}
	if _, _, err := e.m.TakeBackup(context.Background(), BackupOptions{Reason: ReasonScheduled}); err != nil {
		t.Fatal(err)
	}
	if e.m.BackupDue() {
		t.Error("not due right after one")
	}
	e.now = e.now.Add(25 * time.Hour)
	if !e.m.BackupDue() {
		t.Error("due a day later")
	}
}
