package importarr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

const testKey = "k3y-0123456789abcdef"

// fakeRadarr serves the endpoints the importer reads from hand-written data.
type fakeRadarr struct {
	mu        sync.Mutex
	movies    []map[string]any
	files     map[int]map[string]any // by movie id
	history   []map[string]any
	blocklist []map[string]any
	requests  []string
	root      string
	// delays and releaseProfiles are the selection profiles; nil delays serve the arr's default delay profile.
	delays          []map[string]any
	releaseProfiles []map[string]any
	// tags replaces the default tag list when set.
	tags []map[string]any
	// collections is GET collection; nil answers 404, as a Radarr without collections does.
	collections []map[string]any
}

func (f *fakeRadarr) delayProfiles() []map[string]any { return orDefaultDelay(f.delays) }

// orDefaultDelay is d, or the default delay profile every arr has (no delay, prefers usenet) when d is nil.
func orDefaultDelay(d []map[string]any) []map[string]any {
	if d != nil {
		return d
	}
	return []map[string]any{{"id": 1, "enableUsenet": true, "enableTorrent": true, "preferredProtocol": "usenet",
		"usenetDelay": 0, "torrentDelay": 0, "bypassIfHighestQuality": true, "bypassIfAboveCustomFormatScore": false,
		"minimumCustomFormatScore": 0, "order": 2147483647, "tags": []int{}}}
}

func (f *fakeRadarr) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-Api-Key") != testKey {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	ep := strings.TrimPrefix(r.URL.Path, "/api/v3/")
	f.requests = append(f.requests, ep+"?"+r.URL.RawQuery)
	var out any
	switch ep {
	case "system/status":
		out = map[string]any{"appName": "Radarr", "version": "6.4.4.10685"}
	case "rootfolder":
		out = []map[string]any{{"id": 1, "path": f.root}}
	case "customformat", "qualitydefinition":
		out = []any{}
	case "config/mediamanagement", "config/indexer", "config/naming":
		out = map[string]any{}
	case "tag":
		out = []map[string]any{{"id": 1, "label": "best-available"}, {"id": 2, "label": "1-someone"}}
		if f.tags != nil {
			out = f.tags
		}
	case "delayprofile":
		out = f.delayProfiles()
	case "releaseprofile":
		out = f.releaseProfiles
	case "qualityprofile":
		out = []map[string]any{{"id": 7, "name": "Remux + WEB 2160p", "upgradeAllowed": true, "cutoff": 31,
			"minFormatScore": 0, "cutoffFormatScore": 10000,
			"items": []map[string]any{{"quality": map[string]any{"id": 31, "name": "Remux-2160p"}}}}}
	case "movie":
		out = f.movies
	case "collection":
		if f.collections == nil {
			http.Error(w, "not found", 404)
			return
		}
		out = f.collections
	case "moviefile":
		var list []map[string]any
		for _, s := range r.URL.Query()["movieId"] {
			id, _ := strconv.Atoi(s)
			if mf, ok := f.files[id]; ok {
				list = append(list, mf)
			}
		}
		if len(r.URL.Query()["movieId"]) == 0 {
			http.Error(w, "movieId or movieFileIds must be provided", 400)
			return
		}
		out = list
	case "history":
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		size, _ := strconv.Atoi(q.Get("pageSize"))
		recs := append([]map[string]any(nil), f.history...)
		sort.Slice(recs, func(i, j int) bool { return recs[i]["date"].(string) > recs[j]["date"].(string) })
		lo, hi := min((page-1)*size, len(recs)), min(page*size, len(recs))
		out = map[string]any{"page": page, "pageSize": size, "totalRecords": len(recs), "records": recs[lo:hi]}
	case "blocklist":
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		size, _ := strconv.Atoi(q.Get("pageSize"))
		lo, hi := min((page-1)*size, len(f.blocklist)), min(page*size, len(f.blocklist))
		out = map[string]any{"totalRecords": len(f.blocklist), "records": f.blocklist[lo:hi]}
	default:
		http.Error(w, "not found", 404)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func quality(name string, rev int) map[string]any {
	return map[string]any{"quality": map[string]any{"id": 31, "name": name}, "revision": map[string]any{"version": rev, "real": 0}}
}

const (
	hashA = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	hashB = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	hashC = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
)

// newFake builds an instance with four movies:
//   - 1 "Alpha" (tmdb 100): a file from torrent hashA, grabbed and imported, cutoff met;
//   - 2 "Beta" (tmdb 200): no file, one failed torrent grab (hashB);
//   - 3 "Gamma" (no TMDb id, IMDb tt0000003): unmapped;
//   - 4 "Delta" (tmdb 400): a file below cutoff from a manual import with no download id, plus a usenet grab.
func newFake(root string) *fakeRadarr {
	f := &fakeRadarr{root: root, files: map[int]map[string]any{}}
	movie := func(id int, title string, tmdb int, imdb string, hasFile bool) map[string]any {
		return map[string]any{
			"id": id, "title": title, "sortTitle": strings.ToLower(title), "year": 2000 + id, "tmdbId": tmdb,
			"imdbId": imdb, "monitored": id != 2, "hasFile": hasFile, "qualityProfileId": 7,
			"minimumAvailability": "released", "status": "released", "path": root + "/" + title,
			"rootFolderPath": root, "tags": []int{1}, "added": "2024-01-02T03:04:05Z",
			"inCinemas": "2001-05-01T00:00:00Z", "digitalRelease": "2001-09-01T00:00:00Z",
		}
	}
	f.movies = []map[string]any{
		movie(1, "Alpha", 100, "tt0000001", true),
		movie(2, "Beta", 200, "tt0000002", false),
		movie(3, "Gamma", 0, "tt0000003", false),
		movie(4, "Delta", 400, "", true),
	}
	f.movies[0]["collection"] = map[string]any{"tmdbId": 9100}
	f.movies[0]["secondaryYear"] = 2000
	f.movies[0]["originalLanguage"] = map[string]any{"id": 8, "name": "Japanese"}
	f.movies[0]["alternateTitles"] = []map[string]any{
		{"sourceType": "tmdb", "movieMetadataId": 1, "title": "Alpha: The Movie", "id": 1},
		{"sourceType": "tmdb", "movieMetadataId": 1, "title": "Die Alpha", "id": 2},
		{"sourceType": "tmdb", "movieMetadataId": 1, "title": "Die Alpha", "id": 3}, // repeated
	}
	f.files[1] = map[string]any{"id": 11, "movieId": 1, "relativePath": "Alpha (2001) [Remux-2160p]-GRP.mkv",
		"path": root + "/Alpha/Alpha (2001) [Remux-2160p]-GRP.mkv", "size": 50000000000,
		"dateAdded": "2024-02-01T00:00:00Z", "sceneName": "Alpha.2001.2160p.BluRay.REMUX-GRP", "releaseGroup": "GRP",
		"languages": []map[string]any{{"id": 1, "name": "English"}}, "quality": quality("Remux-2160p", 1),
		"customFormatScore": 2500, "indexerFlags": 0, "qualityCutoffNotMet": false,
		"mediaInfo": map[string]any{"videoCodec": "HEVC", "resolution": "3840x2160"}}
	f.files[4] = map[string]any{"id": 14, "movieId": 4, "relativePath": "Delta (2004) [WEBDL-2160p]-WEB.mkv",
		"path": root + "/Delta/Delta (2004) [WEBDL-2160p]-WEB.mkv", "size": 12000000000,
		"dateAdded": "2024-03-01T00:00:00Z", "releaseGroup": "WEB",
		"languages": []map[string]any{{"id": 1, "name": "English"}}, "quality": quality("WEBDL-2160p", 1),
		"customFormatScore": 100, "qualityCutoffNotMet": true}
	f.history = []map[string]any{
		{"id": 1, "movieId": 1, "eventType": "grabbed", "date": "2024-01-31T10:00:00Z", "downloadId": hashA,
			"sourceTitle": "Alpha.2001.2160p.BluRay.REMUX-GRP", "quality": quality("Remux-2160p", 1),
			"customFormatScore": 2500, "languages": []map[string]any{{"id": 1, "name": "English"}},
			"data": map[string]any{"indexer": "Example (Prowlarr)", "protocol": "2", "size": "50000000000",
				"torrentInfoHash": strings.ToLower(hashA), "guid": "https://tracker.example/details/1",
				"downloadUrl": "https://tracker.example/dl/1?passkey=secret", "releaseGroup": "GRP"}},
		{"id": 2, "movieId": 1, "eventType": "downloadFolderImported", "date": "2024-02-01T00:00:00Z",
			"downloadId": hashA, "sourceTitle": "Alpha.2001.2160p.BluRay.REMUX-GRP", "quality": quality("Remux-2160p", 1),
			"data": map[string]any{"fileId": "11", "importedPath": root + "/Alpha/Alpha (2001) [Remux-2160p]-GRP.mkv"}},
		{"id": 3, "movieId": 2, "eventType": "grabbed", "date": "2024-02-02T00:00:00Z", "downloadId": hashB,
			"sourceTitle": "Beta.2002.2160p.WEB-DL-XYZ", "quality": quality("WEBDL-2160p", 1),
			"data": map[string]any{"protocol": "2", "size": "8000000000"}},
		{"id": 4, "movieId": 2, "eventType": "downloadFailed", "date": "2024-02-02T01:00:00Z", "downloadId": hashB,
			"sourceTitle": "Beta.2002.2160p.WEB-DL-XYZ", "quality": quality("WEBDL-2160p", 1),
			"data": map[string]any{"message": "uncached at the provider"}},
		{"id": 5, "movieId": 4, "eventType": "grabbed", "date": "2024-02-20T00:00:00Z",
			"downloadId": "0b5e7a7e-1111-2222-3333-444455556666", "sourceTitle": "Delta.2004.2160p.WEB-DL-NZB",
			"quality": quality("WEBDL-2160p", 1), "data": map[string]any{"protocol": "1", "size": "11000000000"}},
		{"id": 6, "movieId": 4, "eventType": "downloadFolderImported", "date": "2024-03-01T00:00:00Z",
			"sourceTitle": "Delta.2004.2160p.WEB-DL-WEB", "quality": quality("WEBDL-2160p", 1),
			"data": map[string]any{"fileId": "14", "droppedPath": "/downloads/Delta.2004.2160p.WEB-DL-WEB/x.mkv"}},
		{"id": 7, "movieId": 99, "eventType": "grabbed", "date": "2023-01-01T00:00:00Z", "downloadId": hashC,
			"sourceTitle": "Gone.Movie.1999.2160p-OLD", "quality": quality("Remux-2160p", 1),
			"data": map[string]any{"protocol": "2"}},
	}
	return f
}

func openStore(t *testing.T) *store.Store {
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
	return st
}

func runImport(t *testing.T, im *Importer, src Source, srv *httptest.Server) *Summary {
	t.Helper()
	g, err := arr.New(srv.URL, testKey, arr.Options{MinInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	sum, err := im.Run(context.Background(), src, g)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func one[T any](t *testing.T, st *store.Store, q string, args ...any) T {
	t.Helper()
	var v T
	if err := st.Reader().QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func fixedNow() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) }

func TestImportRadarr(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	fake := newFake("/mnt/movies-a")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	im := &Importer{Store: st, Now: fixedNow}
	src := Source{Instance: "radarr_a", Flavour: "radarr", Version: "movie-remux", Library: "/mnt/movies-a"}

	sum := runImport(t, im, src, srv)
	if sum.Titles.Total != 4 || sum.Titles.Imported != 3 || sum.Titles.Unmapped != 1 {
		t.Errorf("titles %+v", sum.Titles)
	}
	if len(sum.Unmapped) != 1 || sum.Unmapped[0].ArrID != 3 || !strings.Contains(sum.Unmapped[0].Reason, "--enrich") {
		t.Errorf("unmapped %+v", sum.Unmapped)
	}
	if sum.Titles.Wanted != 1 || sum.Titles.Filled != 1 || sum.Titles.CutoffMet != 1 || sum.Titles.Monitored != 2 {
		t.Errorf("states %+v", sum.Titles)
	}
	if sum.History.New != 7 || sum.History.TorrentGrabs != 3 || sum.History.UsenetGrabs != 1 {
		t.Errorf("history %+v", sum.History)
	}
	if sum.History.Unmapped["arr title deleted from the arr"] != 1 {
		t.Errorf("history unmapped %v", sum.History.Unmapped)
	}
	if sum.Registry.Hashes != 3 || sum.Registry.ContentVersions != 2 || sum.Registry.Imported != 1 || sum.Registry.Failed != 1 {
		t.Errorf("registry %+v", sum.Registry)
	}
	if f := sum.Files; f.Total != 2 || f.WithImport != 2 || f.WithHash != 1 || f.ManualImport != 1 ||
		f.WithReleaseTitle != 1 || f.ReleaseTitleFromImport != 1 {
		t.Errorf("files %+v", f)
	}
	if len(sum.Warnings) != 0 {
		t.Errorf("warnings %v", sum.Warnings)
	}

	// Database contents.
	if n := one[int](t, st, `SELECT COUNT(*) FROM title`); n != 3 {
		t.Errorf("titles %d", n)
	}
	if s := one[string](t, st, `SELECT v.state FROM version v JOIN title t ON t.id = v.title_id WHERE t.tmdb_id = 400`); s != "filled" {
		t.Errorf("Delta state %s", s)
	}
	if s := one[string](t, st, `SELECT release_title FROM version_file WHERE arr_file_id = 11`); s != "Alpha.2001.2160p.BluRay.REMUX-GRP" {
		t.Errorf("release title %q", s)
	}
	if h := one[string](t, st, `SELECT c.info_hash FROM version_file f JOIN content c ON c.id = f.content_id WHERE f.arr_file_id = 11`); h != strings.ToLower(hashA) {
		t.Errorf("content hash %q", h)
	}
	if n := one[int64](t, st, `SELECT size_bytes FROM content WHERE info_hash = ?`, strings.ToLower(hashA)); n != 50000000000 {
		t.Errorf("content size %d", n)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM content_version WHERE failed_at IS NOT NULL AND imported_at IS NULL`); n != 1 {
		t.Errorf("failed content versions %d", n)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM arr_alias WHERE instance = 'radarr_a'`); n != 5 {
		t.Errorf("aliases %d (3 movies, 2 files)", n)
	}
	if s := one[string](t, st, `SELECT tags FROM arr_title WHERE arr_id = 1`); s != `["best-available"]` {
		t.Errorf("tags %s", s)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM title_release_date WHERE source = 'radarr_a'`); n != 6 {
		t.Errorf("release dates %d", n)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM title_alias WHERE source = 'radarr_a' AND kind = 'alternative'`); n != 2 {
		t.Errorf("alternative titles %d", n)
	}
	if n := one[int](t, st, `SELECT secondary_year FROM title WHERE tmdb_id = 100`); n != 2000 {
		t.Errorf("secondary year %d", n)
	}
	if s := one[string](t, st, `SELECT original_language FROM title WHERE tmdb_id = 100`); s != "Japanese" {
		t.Errorf("original language %q", s)
	}
	if s := one[string](t, st, `SELECT external_id FROM title_external_id WHERE source = 'tmdb_collection'`); s != "9100" {
		t.Errorf("collection %s", s)
	}
	// Nothing secret was stored.
	var leak int
	st.Reader().QueryRow(`SELECT COUNT(*) FROM arr_history WHERE data LIKE '%passkey%' OR data LIKE '%://%' OR data LIKE '%guid%'`).Scan(&leak)
	if leak != 0 {
		t.Errorf("%d history rows kept a URL, GUID or passkey", leak)
	}
	if s := one[string](t, st, `SELECT data FROM arr_history WHERE arr_id = 1`); !strings.Contains(s, `"indexer":"Example (Prowlarr)"`) {
		t.Errorf("history data %s", s)
	}

	// A second run changes nothing and reads one page of history.
	fake.requests = nil
	sum2 := runImport(t, im, src, srv)
	if sum2.Changes.Total() != 0 {
		t.Errorf("second run changed rows: %s", sum2.Changes)
	}
	if sum2.History.New != 0 || sum2.History.Stored != 7 {
		t.Errorf("second run history %+v", sum2.History)
	}
	var histReqs int
	for _, r := range fake.requests {
		if strings.HasPrefix(r, "history?") {
			histReqs++
		}
	}
	if histReqs != 1 {
		t.Errorf("history requests on re-run: %d", histReqs)
	}

	// New upstream: Beta gets a file through a new grab and import, Alpha is unmonitored.
	fake.mu.Lock()
	fake.movies[1]["hasFile"] = true
	fake.movies[0]["monitored"] = false
	fake.files[2] = map[string]any{"id": 12, "movieId": 2, "relativePath": "Beta (2002) [WEBDL-2160p]-XYZ.mkv",
		"path": "/mnt/movies-a/Beta/Beta (2002) [WEBDL-2160p]-XYZ.mkv", "size": 8100000000,
		"dateAdded": "2024-04-01T00:00:00Z", "quality": quality("WEBDL-2160p", 1)}
	fake.history = append(fake.history,
		map[string]any{"id": 8, "movieId": 2, "eventType": "grabbed", "date": "2024-03-31T00:00:00Z",
			"downloadId": "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD", "sourceTitle": "Beta.2002.2160p.WEB-DL-XYZ.v2",
			"quality": quality("WEBDL-2160p", 1), "data": map[string]any{"protocol": "2", "size": "8100000000"}},
		map[string]any{"id": 9, "movieId": 2, "eventType": "downloadFolderImported", "date": "2024-04-01T00:00:00Z",
			"downloadId": "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD", "sourceTitle": "Beta.2002.2160p.WEB-DL-XYZ.v2",
			"quality": quality("WEBDL-2160p", 1), "data": map[string]any{"fileId": "12"}})
	fake.mu.Unlock()
	sum3 := runImport(t, im, src, srv)
	c := sum3.Changes
	if sum3.History.New != 2 || c["arr_history"].Inserted != 2 || c["content"].Inserted != 1 ||
		c["content_version"].Inserted != 1 || c["version_file"].Inserted != 1 {
		t.Errorf("third run: history %+v, changes %s", sum3.History, c)
	}
	if c["version"] == nil || c["version"].Updated != 2 {
		t.Errorf("third run version updates: %s", c)
	}
	if s := one[string](t, st, `SELECT v.state FROM version v JOIN title t ON t.id = v.title_id WHERE t.tmdb_id = 200`); s != "cutoff_met" {
		t.Errorf("Beta state %s", s)
	}
	if m := one[int](t, st, `SELECT monitored FROM title WHERE tmdb_id = 100`); m != 0 {
		t.Errorf("Alpha title still monitored")
	}
	if sum4 := runImport(t, im, src, srv); sum4.Changes.Total() != 0 {
		t.Errorf("fourth run changed rows: %s", sum4.Changes)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM import_run`); n != 4 {
		t.Errorf("import runs %d", n)
	}
}

func TestImportResolveAndStale(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	fake := newFake("/mnt/movies-a")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	im := &Importer{Store: st, Now: fixedNow, ResolveIMDb: func(_ context.Context, imdb string) (int, error) {
		if imdb == "tt0000003" {
			return 300, nil
		}
		return 0, errors.New("unexpected lookup " + imdb)
	}}
	src := Source{Instance: "radarr_a", Flavour: "radarr", Version: "movie-remux", Library: "/mnt/elsewhere"}
	sum := runImport(t, im, src, srv)
	if sum.Titles.Imported != 4 || sum.Titles.Unmapped != 0 {
		t.Errorf("titles %+v", sum.Titles)
	}
	if len(sum.Warnings) != 1 || !strings.Contains(sum.Warnings[0], "not a root folder") {
		t.Errorf("warnings %v", sum.Warnings)
	}
	// Delta leaves the arr.
	fake.mu.Lock()
	fake.movies = fake.movies[:3]
	fake.mu.Unlock()
	sum = runImport(t, im, src, srv)
	if sum.Titles.Removed != 1 || sum.Changes["arr_title"].Deleted != 1 || sum.Changes["version_file"].Deleted != 1 {
		t.Errorf("stale: %+v %s", sum.Titles, sum.Changes)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM arr_alias WHERE kind = 'moviefile'`); n != 1 {
		t.Errorf("file aliases %d", n)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM title WHERE tmdb_id = 400`); n != 1 {
		t.Error("our title went with the arr's")
	}
}

func TestConflicts(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	a := newFake("/mnt/movies-a")
	b := newFake("/mnt/movies-b")
	// Instance b: Alpha's file differs, Delta has no file, Beta has none in either.
	b.files[1]["size"] = 49000000000
	b.files[1]["sceneName"] = "Alpha.2001.2160p.BluRay.REMUX-OTHER"
	b.files[1]["relativePath"] = "Alpha (2001) [Remux-2160p]-OTHER.mkv"
	b.history = b.history[:0]
	delete(b.files, 4)
	b.movies[3]["hasFile"] = false
	srvA, srvB := httptest.NewServer(a), httptest.NewServer(b)
	defer srvA.Close()
	defer srvB.Close()
	im := &Importer{Store: st, Now: fixedNow}
	runImport(t, im, Source{Instance: "radarr_a", Flavour: "radarr", Version: "movie-remux"}, srvA)
	sumB := runImport(t, im, Source{Instance: "radarr_b", Flavour: "radarr", Version: "movie-remux"}, srvB)
	if sumB.Titles.Shared != 3 {
		t.Errorf("shared %d", sumB.Titles.Shared)
	}
	// radarr_a owns the versions: Delta stays filled although radarr_b has no file.
	if s := one[string](t, st, `SELECT v.state FROM version v JOIN title t ON t.id = v.title_id WHERE t.tmdb_id = 400`); s != "filled" {
		t.Errorf("Delta state %s", s)
	}
	cs, err := Conflicts(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]string{}
	for _, c := range cs {
		got[c.TmdbID] = c.Detail
	}
	if len(cs) != 2 || got[100] != "different files" || !strings.HasPrefix(got[400], "a file in one instance") {
		t.Errorf("conflicts %+v", cs)
	}
	vr, err := VersionReport(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if len(vr) != 1 || vr[0].Titles != 3 || vr[0].Files != 3 || fmt.Sprint(vr[0].Instances) != "[radarr_a radarr_b]" {
		t.Errorf("version report %+v", vr)
	}
	// Running b again changes nothing (it never takes over a's versions).
	if s := runImport(t, im, Source{Instance: "radarr_b", Flavour: "radarr", Version: "movie-remux"}, srvB); s.Changes.Total() != 0 {
		t.Errorf("re-run of b changed %s", s.Changes)
	}
}

// TestGetterEndpoints checks that the arr client allows every endpoint the importer reads, refuses others before
// the network, and masks the key in errors.
func TestGetterEndpoints(t *testing.T) {
	t.Parallel()
	for _, ep := range Endpoints {
		if !arr.Allowed(ep) {
			t.Errorf("internal/arr does not allow %q", ep)
		}
	}
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "bad key "+r.Header.Get("X-Api-Key"), http.StatusUnauthorized)
	}))
	defer srv.Close()
	g, err := arr.New(srv.URL, testKey, arr.Options{MinInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := g.GetJSON(context.Background(), "command", nil, &v); !errors.Is(err, arr.ErrNotAllowed) {
		t.Errorf("command endpoint: %v", err)
	}
	if hits != 0 {
		t.Error("refused request reached the server")
	}
	err = g.GetJSON(context.Background(), "movie", nil, &v)
	if err == nil || strings.Contains(err.Error(), testKey) || !strings.Contains(err.Error(), "***") {
		t.Errorf("unmasked: %v", err)
	}
}

func TestToEvent(t *testing.T) {
	t.Parallel()
	r := historyRecord{ID: 1, MovieID: 2, DownloadID: "abc", EventType: json.RawMessage(`1`),
		Data: map[string]any{"protocol": "2", "torrentInfoHash": strings.Repeat("Ab", 20), "nzbInfoUrl": "x",
			"Indexer": "I", "message": "see https://x/?apikey=1"}}
	ev := toEvent(r)
	if ev.EventType != EventGrabbed || ev.Protocol != "torrent" || ev.InfoHash != strings.Repeat("ab", 20) {
		t.Errorf("%+v", ev)
	}
	if _, ok := ev.Data["nzbInfoUrl"]; ok || ev.Data["indexer"] != "I" || ev.Data["message"] != "" {
		t.Errorf("data %v", ev.Data)
	}
	// A usenet grab with a hex-looking id is not a torrent.
	u := toEvent(historyRecord{DownloadID: strings.Repeat("a", 40), EventType: json.RawMessage(`"grabbed"`),
		Data: map[string]any{"protocol": "1"}})
	if u.InfoHash != "" || u.Protocol != "usenet" {
		t.Errorf("usenet %+v", u)
	}
}

func TestImportRefreshesTitleIdentity(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	fake := newFake("/mnt/movies-a")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	im := &Importer{Store: st, Now: fixedNow}
	src := Source{Instance: "radarr_a", Flavour: "radarr", Version: "movie-remux", Library: "/mnt/movies-a"}
	runImport(t, im, src, srv)
	fake.mu.Lock()
	tmdb := int(fake.movies[0]["tmdbId"].(int))
	fake.movies[0]["title"] = "Corrected Title"
	fake.movies[0]["sortTitle"] = "corrected title"
	fake.movies[0]["year"] = 2025
	fake.mu.Unlock()
	runImport(t, im, src, srv)
	if name := one[string](t, st, `SELECT name FROM title WHERE tmdb_id=?`, tmdb); name != "Corrected Title" {
		t.Fatalf("name %s", name)
	}
	if sort := one[string](t, st, `SELECT sort_name FROM title WHERE tmdb_id=?`, tmdb); sort != "corrected title" {
		t.Fatalf("sort %s", sort)
	}
	if year := one[int](t, st, `SELECT year FROM title WHERE tmdb_id=?`, tmdb); year != 2025 {
		t.Fatalf("year %d", year)
	}
}
