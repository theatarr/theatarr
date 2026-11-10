package arrview

import (
	"context"
	"encoding/json"
	"github.com/theatarr/theatarr/internal/availability"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/rootprobe"
	"github.com/theatarr/theatarr/internal/rootprobe/probetest"
)

// Every property of the arr resources, camelCased, from the C# classes. A view may leave out what the arr would
// write as null; it must not invent fields.
var (
	// src/Radarr.Api.V3/Movies/MovieResource.cs @ a96bf7c (plus RestResource.Id).
	radarrMovieFields = fields(`id title originalTitle originalLanguage alternateTitles secondaryYear
		secondaryYearSourceId sortTitle sizeOnDisk status overview inCinemas physicalRelease digitalRelease releaseDate
		physicalReleaseNote images website remotePoster year youTubeTrailerId studio path qualityProfileId hasFile
		movieFileId monitored minimumAvailability isAvailable folderName runtime cleanTitle imdbId tmdbId titleSlug
		rootFolderPath folder certification genres keywords tags added addOptions ratings movieFile collection
		popularity lastSearchTime statistics`)
	// src/Radarr.Api.V3/MovieFiles/MovieFileResource.cs.
	radarrMovieFileFields = fields(`id movieId relativePath path size dateAdded sceneName releaseGroup edition languages
		quality customFormats customFormatScore indexerFlags mediaInfo originalFilePath qualityCutoffNotMet`)
	// src/Sonarr.Api.V3/Series/SeriesResource.cs @ cab419a.
	sonarrSeriesFields = fields(`id title alternateTitles sortTitle status ended profileName overview nextAiring
		previousAiring network airTime images originalLanguage remotePoster seasons year path qualityProfileId
		seasonFolder monitored monitorNewItems useSceneNumbering runtime tvdbId tvRageId tvMazeId tmdbId firstAired
		lastAired seriesType cleanTitle imdbId titleSlug rootFolderPath folder certification genres tags added
		addOptions ratings statistics episodesChanged languageProfileId`)
	// src/Sonarr.Api.V3/Episodes/EpisodeResource.cs.
	sonarrEpisodeFields = fields(`id seriesId tvdbId episodeFileId seasonNumber episodeNumber title airDate airDateUtc
		lastSearchTime runtime finaleType overview episodeFile hasFile monitored absoluteEpisodeNumber
		sceneAbsoluteEpisodeNumber sceneEpisodeNumber sceneSeasonNumber unverifiedSceneNumbering endTime grabDate
		series images`)
)

func TestAuth(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	cases := []struct {
		name  string
		path  string
		setup func(r *http.Request)
		want  int
	}{
		{"no key", "/radarr/api/v3/system/status", nil, 401},
		{"wrong header key", "/radarr/api/v3/system/status", func(r *http.Request) { r.Header.Set("X-Api-Key", "nope") }, 401},
		{"header key", "/radarr/api/v3/system/status", func(r *http.Request) { r.Header.Set("X-Api-Key", e.key) }, 200},
		{"query key", "/radarr/api/v3/system/status?apikey=" + e.key, nil, 200},
		{"query key on a sub-path", "/sonarr/api/v3/series?apikey=" + e.key, nil, 200},
		{"wrong query key wins over a good header", "/radarr/api/v3/system/status?apikey=bad",
			func(r *http.Request) { r.Header.Set("X-Api-Key", e.key) }, 401},
		{"bearer", "/radarr/api/v3/tag", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+e.key) }, 200},
		{"view's own key", "/radarr/api/v3/movie?apikey=" + radarrViewKey, nil, 200},
		{"another view's key", "/radarr/api/v3/movie?apikey=" + sonarrViewKey, nil, 401},
		{"per-version view takes auth keys", "/v/movie-web/radarr/api/v3/movie?apikey=" + e.key, nil, 200},
		{"per-version view has no own key", "/v/movie-web/radarr/api/v3/movie?apikey=" + radarrViewKey, nil, 401},
		{"not the API", "/radarr/", func(r *http.Request) { r.Header.Set("X-Api-Key", e.key) }, 404},
		{"unknown endpoint", "/radarr/api/v3/nothing", func(r *http.Request) { r.Header.Set("X-Api-Key", e.key) }, 404},
		{"case-insensitive route (Seerr's /qualityProfile)", "/radarr/api/v3/qualityProfile?apikey=" + e.key, nil, 200},
		{"no movie routes on a Sonarr view", "/sonarr/api/v3/movie?apikey=" + e.key, nil, 404},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", c.path, nil)
		if c.setup != nil {
			c.setup(r)
		}
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, r)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, rec.Code, c.want)
		}
		if c.want == 401 {
			// ApiKeyAuthenticationHandler.HandleChallengeAsync: status only, no body.
			if rec.Body.Len() != 0 {
				t.Errorf("%s: 401 with a body: %q", c.name, rec.Body.String())
			}
		}
		if rec.Code != 404 || c.name == "unknown endpoint" {
			if rec.Header().Get("X-Application-Version") == "" && c.name != "not the API" {
				t.Errorf("%s: no X-Application-Version", c.name)
			}
		}
	}
	// A revoked key stops working.
	_, info, err := e.au.CreateKey(context.Background(), "old", 1)
	if err != nil {
		t.Fatal(err)
	}
	k2, info2, _ := e.au.CreateKey(context.Background(), "gone", 1)
	if err := e.au.RevokeKey(context.Background(), info2.ID, 0); err != nil {
		t.Fatal(err)
	}
	_ = info
	if rec := e.do("GET", "/radarr/api/v3/tag", k2, nil); rec.Code != 401 {
		t.Errorf("revoked key: %d", rec.Code)
	}
}

// migrationFields matches databaseVersion, which carries the newest migration number; the golden files hold a
// placeholder so a new migration doesn't change them.
var migrationFields = regexp.MustCompile(`"databaseVersion"\s*:\s*"[^"]*"`)

func stableStatus(b []byte) []byte {
	return migrationFields.ReplaceAll(b, []byte(`"databaseVersion":"<migration>"`))
}

func TestSystemStatus(t *testing.T) {
	e := newEnv(t)
	rec := e.call("GET", "/radarr/api/v3/system/status", nil, 200, nil)
	if v := rec.Header().Get("X-Application-Version"); v != RadarrVersion {
		t.Errorf("X-Application-Version %q", v)
	}
	e.golden("radarr-system-status", stableStatus(rec.Body.Bytes()))
	// The fields SystemController.GetStatus fills (src/Radarr.Api.V3/System/SystemController.cs @ a96bf7c), less
	// packageVersion, packageAuthor and packageUpdateMechanismMessage, which are null outside packages.
	wantKeys(t, "radarr status", decode(t, rec.Body.Bytes()), fields(`appName instanceName version buildTime isDebug
		isProduction isAdmin isUserInteractive startupPath appData osName osVersion isNetCore isLinux isOsx isWindows
		isDocker isContainerized mode branch databaseType databaseVersion authentication migrationVersion urlBase
		runtimeVersion runtimeName startTime packageUpdateMechanism`))
	rec = e.call("GET", "/sonarr/api/v3/system/status", nil, 200, nil)
	if v := rec.Header().Get("X-Application-Version"); v != SonarrVersion {
		t.Errorf("Sonarr X-Application-Version %q", v)
	}
	e.golden("sonarr-system-status", stableStatus(rec.Body.Bytes()))
	var st struct{ AppName, Version, URLBase string }
	e.call("GET", "/v/anime-tv/sonarr/api/v3/system/status", nil, 200, &st)
	// decypharr tells Radarr from Sonarr by appName (pkg/arr/client.go L149-160); Seerr reads version's major.
	if st.AppName != "Sonarr" || st.Version != SonarrVersion || st.URLBase != "/v/anime-tv/sonarr" {
		t.Errorf("per-version status %+v", st)
	}
}

func TestQualityProfilesStable(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var ps []struct {
		ID   int64
		Name string
	}
	rec := e.call("GET", "/radarr/api/v3/qualityprofile", nil, 200, &ps)
	if len(ps) != 3 {
		t.Fatalf("profiles %+v", ps)
	}
	e.golden("radarr-qualityprofile-names", mustJSON(t, ps))
	obj := decode(t, rec.Body.Bytes()).([]any)[0]
	// QualityProfileResource (src/Radarr.Api.V3/Profiles/Quality/QualityProfileResource.cs).
	wantKeys(t, "profile", obj, fields(`id name upgradeAllowed cutoff items minFormatScore cutoffFormatScore
		minUpgradeFormatScore formatItems language`))
	// A restart keeps the ids.
	e.start()
	var again []struct {
		ID   int64
		Name string
	}
	e.call("GET", "/radarr/api/v3/qualityprofile", nil, 200, &again)
	for i := range ps {
		if ps[i] != again[i] {
			t.Errorf("profile ids changed across a restart: %+v vs %+v", ps, again)
		}
	}
	var one struct{ Name string }
	e.call("GET", "/radarr/api/v3/qualityprofile/"+strconv.FormatInt(ps[0].ID, 10), nil, 200, &one)
	if one.Name != ps[0].Name {
		t.Errorf("profile by id %+v", one)
	}
	e.call("GET", "/radarr/api/v3/qualityprofile/999", nil, 404, nil)
	var single []struct{ Name string }
	e.call("GET", "/v/movie-web/radarr/api/v3/qualityprofile", nil, 200, &single)
	if len(single) != 1 || single[0].Name != "movie-web" {
		t.Errorf("per-version profiles %+v", single)
	}
}

func TestRootFolders(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var rfs []map[string]any
	e.call("GET", "/radarr/api/v3/rootfolder", nil, 200, &rfs)
	if len(rfs) != 3 || rfs[0]["path"] != "/media/movies" || rfs[0]["id"].(float64) != 1 {
		t.Fatalf("root folders %+v", rfs)
	}
	// RootFolderResource: freeSpace is nullable (left out when the folder is not there).
	wantKeys(t, "rootfolder", any(rfs[0]), fields(`id path accessible unmappedFolders`), "freeSpace")
	var ds []map[string]any
	e.call("GET", "/radarr/api/v3/diskspace", nil, 200, &ds)
	for _, d := range ds {
		wantKeys(t, "diskspace", any(d), fields(`path freeSpace totalSpace`), "label")
	}
}

// A root on a share that stops answering is reported inaccessible (rootfolder) and left out (diskspace) after
// rootProbeTimeout; while its stat is stuck, later requests skip it without another call, and other roots answer.
func TestRootFoldersHungShare(t *testing.T) {
	e := newEnv(t)
	dir, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const hung = "/media/movies"
	probetest.Mounts(t, hung) // the hung share; the other roots are on another filesystem
	release := make(chan struct{})
	var hungCalls atomic.Int64
	oldTimeout := rootProbeTimeout
	rootProbeTimeout = 100 * time.Millisecond
	rootStat = func(path string) (os.FileInfo, error) {
		if path == hung {
			hungCalls.Add(1)
			<-release
		}
		return dir, nil
	}
	rootDiskFree = func(string) (int64, int64, bool) { return 10, 20, true }
	t.Cleanup(func() {
		close(release)
		for !rootprobe.Default.Idle() {
			time.Sleep(5 * time.Millisecond)
		}
		rootStat, rootDiskFree, rootProbeTimeout = os.Stat, diskFree, oldTimeout
	})
	for range 3 {
		var rfs []rootFolderResource
		e.call("GET", "/radarr/api/v3/rootfolder", nil, 200, &rfs)
		if len(rfs) != 3 || rfs[0].Path != hung || rfs[0].Accessible || !rfs[1].Accessible || rfs[1].FreeSpace == nil || *rfs[1].FreeSpace != 10 {
			t.Fatalf("root folders %+v", rfs)
		}
		var ds []diskSpaceResource
		e.call("GET", "/radarr/api/v3/diskspace", nil, 200, &ds)
		if len(ds) != 2 || ds[0].Path == hung || ds[1].Path == hung {
			t.Fatalf("disk space %+v", ds)
		}
	}
	if n := hungCalls.Load(); n != 1 {
		t.Fatalf("stat calls on the hung root: %d, want 1", n)
	}
}

func TestTags(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var tags []tagResource
	e.call("GET", "/radarr/api/v3/tag", nil, 200, &tags)
	if len(tags) != 0 {
		t.Fatalf("tags %+v", tags)
	}
	var tg tagResource
	e.call("POST", "/radarr/api/v3/tag", map[string]any{"label": "1-Alice"}, 201, &tg)
	if tg.ID == 0 || tg.Label != "1-alice" {
		t.Fatalf("created %+v", tg)
	}
	// TagService.Add: an existing label, any case, returns the existing tag.
	var again tagResource
	e.call("POST", "/sonarr/api/v3/tag", map[string]any{"label": "1-alice"}, 201, &again)
	if again != tg {
		t.Errorf("second add %+v, want %+v", again, tg)
	}
	e.call("POST", "/radarr/api/v3/tag", map[string]any{"label": "1 - alice"}, 400, nil)
	// Seerr's tag migration renames with PUT tag/{id} (0007_migrate_arr_tags.ts).
	var renamed tagResource
	e.call("PUT", "/radarr/api/v3/tag/"+strconv.FormatInt(tg.ID, 10), map[string]any{"id": tg.ID, "label": "1-bob"},
		202, &renamed)
	if renamed.Label != "1-bob" {
		t.Errorf("renamed %+v", renamed)
	}
	e.call("GET", "/sonarr/api/v3/tag/"+strconv.FormatInt(tg.ID, 10), nil, 200, &again)
	if again.Label != "1-bob" {
		t.Errorf("tag ids are global: %+v", again)
	}
	e.call("PUT", "/radarr/api/v3/tag/99", map[string]any{"id": 99, "label": "x"}, 404, nil)
	rec := e.call("GET", "/radarr/api/v3/tag", nil, 200, nil)
	e.golden("tag-list", rec.Body.Bytes())
}

func TestMovies(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	rec := e.call("GET", "/radarr/api/v3/movie", nil, 200, nil)
	e.golden("radarr-movie-list", rec.Body.Bytes())
	list := decode(t, rec.Body.Bytes()).([]any)
	if len(list) != 3 {
		t.Fatalf("%d movies, want Alpha, Beta, Gamma", len(list))
	}
	for i, m := range list {
		// Seerr's scanner and Homepage's widget read these (scanners/radarr/index.ts L155-185, widgets/radarr/widget.js
		// L10-22).
		wantKeys(t, "movie "+strconv.Itoa(i), m, fields(`id tmdbId titleSlug title hasFile monitored isAvailable`),
			radarrMovieFields...)
	}
	alpha := list[0].(map[string]any)
	if alpha["id"].(float64) != IDBase+1 || alpha["hasFile"] != true || alpha["monitored"] != true {
		t.Errorf("Alpha %v", alpha)
	}
	mf := alpha["movieFile"].(map[string]any)
	wantKeys(t, "movieFile", mf, fields(`id movieId path size quality mediaInfo`), radarrMovieFileFields...)
	// Seerr's 4K split reads movieFile.mediaInfo.resolution (availabilitySync.ts L670-716).
	if mf["mediaInfo"].(map[string]any)["resolution"] != "3840x2160" {
		t.Errorf("mediaInfo %v", mf["mediaInfo"])
	}

	// The same title through a per-version view: same id, only that version's file.
	var web map[string]any
	e.call("GET", "/v/movie-web/radarr/api/v3/movie/"+strconv.Itoa(IDBase+1), nil, 200, &web)
	if web["hasFile"] != false || web["movieFile"] != nil || web["monitored"] != true {
		t.Errorf("Alpha in movie-web: hasFile %v movieFile %v", web["hasFile"], web["movieFile"])
	}
	if st := web["statistics"].(map[string]any); st["movieFileCount"].(float64) != 0 {
		t.Errorf("movie-web statistics %v", st)
	}

	// ?tmdbId= is a list.
	var byTmdb []map[string]any
	e.call("GET", "/radarr/api/v3/movie?tmdbId=102", nil, 200, &byTmdb)
	if len(byTmdb) != 1 || byTmdb[0]["title"] != "Beta" {
		t.Errorf("tmdbId=102: %v", byTmdb)
	}
	e.call("GET", "/radarr/api/v3/movie?tmdbId=104", nil, 200, &byTmdb)
	if len(byTmdb) != 0 {
		t.Errorf("Delta is in no view: %v", byTmdb)
	}

	// Ids: ours, the alias of the first alias_from instance that has it, and 404s.
	for _, c := range []struct {
		path, title string
		code        int
	}{
		{"/radarr/api/v3/movie/" + strconv.Itoa(IDBase+2), "Beta", 200},
		{"/radarr/api/v3/movie/7", "Alpha", 200},                    // radarr_4k_debrid's 7 before radarr_4k_web's 7
		{"/radarr/api/v3/movie/9", "Alpha", 200},                    // radarr_4k_web's 9
		{"/radarr/api/v3/movie/8", "", 404},                         // no alias
		{"/radarr/api/v3/movie/" + strconv.Itoa(IDBase+4), "", 404}, // Delta: in no view
		{"/v/anime-movie/radarr/api/v3/movie/7", "", 404},           // no alias_from on per-version views
		{"/v/anime-movie/radarr/api/v3/movie/" + strconv.Itoa(IDBase+1), "", 404},
	} {
		var m map[string]any
		e.call("GET", c.path, nil, c.code, &m)
		if c.code == 200 && m["title"] != c.title {
			t.Errorf("%s: %v", c.path, m["title"])
		}
	}
	rec = e.call("GET", "/radarr/api/v3/movie/7", nil, 200, nil)
	e.golden("radarr-movie-alpha", rec.Body.Bytes())

	// Gamma: not released, so not available (Movie.IsAvailable); status from the dates (SkyHookProxy).
	var g map[string]any
	e.call("GET", "/radarr/api/v3/movie/"+strconv.Itoa(IDBase+3), nil, 200, &g)
	if g["isAvailable"] != false || g["status"] != "announced" || g["hasFile"] != false {
		t.Errorf("Gamma %v %v %v", g["isAvailable"], g["status"], g["hasFile"])
	}

	var f map[string]any
	e.call("GET", "/radarr/api/v3/moviefile/1", nil, 200, &f)
	if f["movieId"].(float64) != IDBase+1 {
		t.Errorf("moviefile %v", f)
	}
	e.call("GET", "/v/movie-web/radarr/api/v3/moviefile/1", nil, 404, nil)
}

func TestMovieLookup(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var res []map[string]any
	// In the view: id, hasFile and monitored are the view's (Seerr radarr.ts L118-216 decides add, PUT or skip).
	e.call("GET", "/radarr/api/v3/movie/lookup?term=tmdb:101", nil, 200, &res)
	if len(res) != 1 || res[0]["id"].(float64) != IDBase+1 || res[0]["hasFile"] != true {
		t.Fatalf("lookup 101: %v", res)
	}
	// Known title, not in this view: no id, so Seerr adds it.
	res = nil
	e.call("GET", "/v/anime-movie/radarr/api/v3/movie/lookup?term=tmdb:101", nil, 200, &res)
	if len(res) != 1 || res[0]["id"] != nil || res[0]["title"] != "Alpha" || res[0]["tmdbId"].(float64) != 101 {
		t.Fatalf("lookup 101 in anime-movie: %v", res)
	}
	res = nil
	rec := e.call("GET", "/radarr/api/v3/movie/lookup?term=tmdb:555", nil, 200, &res)
	if len(res) != 1 || res[0]["id"] != nil || res[0]["tmdbId"].(float64) != 555 {
		t.Fatalf("unknown TMDb id: %v", res)
	}
	e.golden("radarr-lookup-unknown", rec.Body.Bytes())
	res = nil
	e.call("GET", "/radarr/api/v3/movie/lookup?term=imdb:tt0000101", nil, 200, &res)
	if len(res) != 1 || res[0]["title"] != "Alpha" {
		t.Errorf("imdb lookup: %v", res)
	}
	res = nil
	e.call("GET", "/radarr/api/v3/movie/lookup?term=alp", nil, 200, &res)
	if len(res) != 1 || res[0]["title"] != "Alpha" {
		t.Errorf("text lookup: %v", res)
	}
	var one map[string]any
	e.call("GET", "/radarr/api/v3/movie/lookup/tmdb?tmdbId=102", nil, 200, &one)
	if one["title"] != "Beta" || one["id"].(float64) != IDBase+2 {
		t.Errorf("lookup/tmdb: %v", one)
	}
}

func TestQueueAndFriends(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, c := range []struct{ path, golden string }{
		{"/radarr/api/v3/queue", "radarr-queue"},
		{"/sonarr/api/v3/queue?includeEpisode=true", "sonarr-queue"},
		{"/radarr/api/v3/queue/status", "radarr-queue-status"},
		{"/radarr/api/v3/queue/details", "radarr-queue-details"},
		{"/radarr/api/v3/wanted/missing", "radarr-wanted-missing"},
		{"/sonarr/api/v3/wanted/missing", "sonarr-wanted-missing"},
		{"/radarr/api/v3/history", "radarr-history"},
		{"/radarr/api/v3/health", "radarr-health"},
		{"/radarr/api/v3/config/downloadclient", "radarr-config-downloadclient"},
		{"/sonarr/api/v3/config/downloadclient", "sonarr-config-downloadclient"},
	} {
		rec := e.call("GET", c.path, nil, 200, nil)
		e.golden(c.golden, rec.Body.Bytes())
	}
	// Seerr sends no pageSize (base.ts getQueue): the arrs' default is 10 (PagingResource.cs L33).
	var q pagingResource
	e.call("GET", "/radarr/api/v3/queue", nil, 200, &q)
	if q.PageSize != 10 || q.Page != 1 || q.TotalRecords != 0 {
		t.Errorf("queue paging %+v", q)
	}
	e.call("GET", "/radarr/api/v3/queue?page=2&pageSize=50", nil, 200, &q)
	if q.PageSize != 50 || q.Page != 1 {
		t.Errorf("queue paging %+v", q)
	}
	// Radarr missing: Gamma (monitored, no file) only. Alpha has a remux file; Beta is unmonitored.
	var miss struct {
		TotalRecords int
		Records      []map[string]any
	}
	e.call("GET", "/radarr/api/v3/wanted/missing", nil, 200, &miss)
	if miss.TotalRecords != 1 || miss.Records[0]["title"] != "Gamma" {
		t.Errorf("radarr missing %+v", miss)
	}
	// In movie-web, Alpha is missing too.
	e.call("GET", "/v/movie-web/radarr/api/v3/wanted/missing", nil, 200, &miss)
	if miss.TotalRecords != 1 || miss.Records[0]["title"] != "Alpha" {
		t.Errorf("movie-web missing %+v", miss)
	}
	// Sonarr missing: S01E02 (aired, monitored, no file). Not E03 (future) or the unmonitored special.
	e.call("GET", "/sonarr/api/v3/wanted/missing", nil, 200, &miss)
	if miss.TotalRecords != 1 || miss.Records[0]["id"].(float64) != 102 {
		t.Errorf("sonarr missing %+v", miss)
	}
	var hist struct {
		TotalRecords int
		Records      []map[string]any
	}
	e.call("GET", "/radarr/api/v3/history?eventType=1", nil, 200, &hist)
	if hist.TotalRecords != 1 || hist.Records[0]["eventType"] != "grabbed" {
		t.Errorf("history grabbed %+v", hist)
	}
	wantKeys(t, "history", any(hist.Records[0]), fields(`id movieId sourceTitle languages quality customFormats
		customFormatScore qualityCutoffNotMet date downloadId eventType data`))
	var hm []map[string]any
	e.call("GET", "/radarr/api/v3/history/movie?movieId=7", nil, 200, &hm)
	if len(hm) != 2 {
		t.Errorf("history/movie via alias: %d", len(hm))
	}
	e.call("GET", "/v/movie-web/radarr/api/v3/history", nil, 200, &hist)
	if hist.TotalRecords != 0 {
		t.Errorf("movie-web has no history: %+v", hist)
	}
}

func TestSeries(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	rec := e.call("GET", "/sonarr/api/v3/series", nil, 200, nil)
	e.golden("sonarr-series-list", rec.Body.Bytes())
	list := decode(t, rec.Body.Bytes()).([]any)
	if len(list) != 1 {
		t.Fatalf("series %v", list)
	}
	s := list[0].(map[string]any)
	wantKeys(t, "series", s, fields(`id tvdbId titleSlug title seasons statistics monitored path`), sonarrSeriesFields...)
	seasons := s["seasons"].([]any)
	if len(seasons) != 2 {
		t.Fatalf("seasons %v", seasons)
	}
	s1 := seasons[1].(map[string]any)
	st := s1["statistics"].(map[string]any)
	// Seerr's season availability: episodeFileCount vs totalEpisodeCount (scanners/sonarr/index.ts L165-258).
	if s1["seasonNumber"].(float64) != 1 || s1["monitored"] != true || st["episodeFileCount"].(float64) != 1 ||
		st["totalEpisodeCount"].(float64) != 3 || st["episodeCount"].(float64) != 2 {
		t.Errorf("season 1 %v", s1)
	}
	if sp := seasons[0].(map[string]any); sp["monitored"] != false {
		t.Errorf("specials %v", sp)
	}
	var byTvdb []map[string]any
	e.call("GET", "/sonarr/api/v3/series?tvdbId=201", nil, 200, &byTvdb)
	if len(byTvdb) != 1 {
		t.Errorf("tvdbId=201: %v", byTvdb)
	}
	var one map[string]any
	e.call("GET", "/sonarr/api/v3/series/3", nil, 200, &one) // sonarr_4k_debrid's id
	if one["id"].(float64) != IDBase+10 {
		t.Errorf("alias series %v", one["id"])
	}
	e.call("GET", "/v/anime-tv/sonarr/api/v3/series/"+strconv.Itoa(IDBase+10), nil, 404, nil)

	rec = e.call("GET", "/sonarr/api/v3/episode?seriesId="+strconv.Itoa(IDBase+10), nil, 200, nil)
	e.golden("sonarr-episodes", rec.Body.Bytes())
	eps := decode(t, rec.Body.Bytes()).([]any)
	if len(eps) != 4 {
		t.Fatalf("episodes %d", len(eps))
	}
	for _, x := range eps {
		wantKeys(t, "episode", x, fields(`id seriesId seasonNumber episodeNumber monitored hasFile episodeFileId`),
			sonarrEpisodeFields...)
	}
	var s1eps []map[string]any
	e.call("GET", "/sonarr/api/v3/episode?seriesId=3&seasonNumber=1", nil, 200, &s1eps)
	if len(s1eps) != 3 || s1eps[0]["hasFile"] != true || s1eps[1]["hasFile"] != false {
		t.Errorf("season 1 episodes %v", s1eps)
	}
	var ep map[string]any
	e.call("GET", "/sonarr/api/v3/episode/102", nil, 200, &ep)
	if ep["title"] != "Second" {
		t.Errorf("episode/102 %v", ep)
	}
	var lk []map[string]any
	e.call("GET", "/sonarr/api/v3/series/lookup?term=tvdb:201", nil, 200, &lk)
	if len(lk) != 1 || lk[0]["id"].(float64) != IDBase+10 {
		t.Errorf("series lookup %v", lk)
	}
	lk = nil
	e.call("GET", "/v/anime-tv/sonarr/api/v3/series/lookup?term=tvdb:201", nil, 200, &lk)
	if len(lk) != 1 || lk[0]["id"] != nil || len(lk[0]["seasons"].([]any)) != 2 {
		t.Errorf("series lookup outside the view %v", lk)
	}
	lk = nil
	rec = e.call("GET", "/sonarr/api/v3/series/lookup?term=tvdb:999", nil, 200, &lk)
	e.golden("sonarr-lookup-unknown", rec.Body.Bytes())
}

func TestMovieStatusPort(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	d := func(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		dates  map[string]time.Time
		status string
		avail  bool // minimum availability released
	}{
		{map[string]time.Time{}, "announced", false},
		{map[string]time.Time{"theatrical": d(2026, 10, 1)}, "inCinemas", false},
		{map[string]time.Time{"theatrical": d(2026, 6, 1)}, "released", true}, // > 90 days, no other dates
		{map[string]time.Time{"theatrical": d(2026, 6, 1), "digital": d(2026, 12, 1)}, "inCinemas", false},
		{map[string]time.Time{"theatrical": d(2026, 9, 1), "digital": d(2026, 10, 20)}, "released", true},
		{map[string]time.Time{"physical": d(2027, 1, 1)}, "announced", false},
	}
	for i, c := range cases {
		if got := movieStatus(c.dates, now); got != c.status {
			t.Errorf("case %d: status %s, want %s", i, got, c.status)
		}
		if got := availability.Available("released", c.dates, now, 0); got != c.avail {
			t.Errorf("case %d: available %v, want %v", i, got, c.avail)
		}
	}
	if !availability.Available("announced", nil, now, 0) {
		t.Error("announced is always available")
	}
}

// TestPing: GET and HEAD <view base>/ping answer as the arrs' PingController does (AllowAnonymous, outside the API):
// 200 {"status":"OK"} without a key, the same on every view, and no X-Application-Version, so the answer does not say
// which arr or version it stands for. Nothing else outside /api/v3 is served, and /api/v3/ping is no route.
func TestPing(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	bodies := map[string]bool{}
	for _, base := range []string{"/radarr", "/sonarr", "/v/movie-web/radarr"} {
		for _, method := range []string{"GET", "HEAD"} {
			rec := e.do(method, base+"/ping", "", nil)
			if rec.Code != 200 {
				t.Fatalf("%s %s/ping: %d", method, base, rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
				t.Errorf("%s %s/ping: Content-Type %q", method, base, ct)
			}
			if v := rec.Header().Get("X-Application-Version"); v != "" {
				t.Errorf("%s %s/ping: X-Application-Version %q", method, base, v)
			}
			var got map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("%s %s/ping: %v: %q", method, base, err, rec.Body.String())
			}
			if len(got) != 1 || got["status"] != "OK" {
				t.Errorf("%s %s/ping: %v", method, base, got)
			}
			bodies[rec.Body.String()] = true
		}
	}
	if len(bodies) != 1 {
		t.Errorf("ping bodies differ between views: %v", bodies)
	}
	// A key changes nothing; the path is case-insensitive like the arrs' routes.
	if rec := e.do("GET", "/radarr/Ping", e.key, nil); rec.Code != 200 {
		t.Errorf("GET /radarr/Ping with a key: %d", rec.Code)
	}
	rec := e.do("POST", "/radarr/ping", "", nil)
	if rec.Code != 405 || rec.Body.Len() != 0 || rec.Header().Get("X-Application-Version") != "" {
		t.Errorf("POST /radarr/ping: %d %q", rec.Code, rec.Body.String())
	}
	for path, want := range map[string]int{"/radarr/ping/x": 404, "/radarr/ping/": 404, "/radarr/pingx": 404,
		"/radarr/api/v3/ping": 401, "/sonarr/api/v3/ping": 401} {
		if rec := e.do("GET", path, "", nil); rec.Code != want {
			t.Errorf("GET %s without a key: %d, want %d", path, rec.Code, want)
		}
	}
	e.call("GET", "/radarr/api/v3/ping", nil, 404, nil)
}

// A library's display name is for people only: the arr views keep naming its profile after the key.
func TestLibraryNameKeepsArrProfileNames(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var before, after []struct {
		ID   int64
		Name string
	}
	e.call("GET", "/radarr/api/v3/qualityprofile", nil, 200, &before)
	e.pol.Versions["movie-web"].Name = "Films in 4K"
	e.start()
	e.call("GET", "/radarr/api/v3/qualityprofile", nil, 200, &after)
	if !slices.Equal(before, after) {
		t.Errorf("a library name changed the view's profiles: %+v vs %+v", before, after)
	}
	var single []struct{ Name string }
	e.call("GET", "/v/movie-web/radarr/api/v3/qualityprofile", nil, 200, &single)
	if len(single) != 1 || single[0].Name != "movie-web" {
		t.Errorf("per-version profiles %+v", single)
	}
}
