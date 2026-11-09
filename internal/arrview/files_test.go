package arrview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/policy"
)

// seedSeriesFile gives The Show an arr file for S01E01 (version_file 3) and an active link of ours for movie-web's
// Alpha (link 7, file id LinkFileBase+7).
func (e *env) seedFiles() {
	e.t.Helper()
	now := ms(testNow)
	e.exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, relative_path, size_bytes,
		quality, release_group, scene_name, languages, media_info, custom_format_score, added_at) VALUES
		(3, 10, 10, 'sonarr_4k_debrid', 900, '/media/tv/The Show/Season 01/The Show - S01E01.mkv',
		 'Season 01/The Show - S01E01.mkv', 3000000000, 'WEBDL-2160p', 'GRP', 'The.Show.S01E01.2160p.WEB-DL-GRP',
		 '["English"]', '{}', 120, ?)`, now)
	e.exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (3, 101)`)
	e.exec(`INSERT INTO content (id, info_hash, name, first_seen_at, last_seen_at) VALUES (1, ?, 'Alpha.WEB', ?, ?)`,
		strings.Repeat("a", 40), now, now)
	e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (1, 1, 'Alpha.WEB/a.mkv', 9000)`)
	e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, release_title, quality, score, state,
		created_at, updated_at) VALUES (7, '/media/movies-web/Alpha (2020)/Alpha (2020) WEBDL-2160p.mkv',
		'/mnt/debrid/__all__/Alpha.WEB/a.mkv', 1, 1, 'Alpha.2020.2160p.WEB-DL-GRP', 'WEBDL-2160p', 1700, 'active', ?, ?)`,
		now, now)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (7, 2)`)
	e.exec(`UPDATE version SET state = 'filled' WHERE id = 2`)
}

func TestFilesListAndUnlink(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.seedFiles()
	// Radarr: moviefile?movieId= (Ruddarr, LunaSea, upgrade scripts): the arr's remux file and our web link.
	var fs []map[string]any
	e.call("GET", "/radarr/api/v3/moviefile?movieId="+strconv.Itoa(IDBase+1), nil, 200, &fs)
	if len(fs) != 2 || fs[0]["id"].(float64) != 1 || fs[1]["id"].(float64) != LinkFileBase+7 ||
		fs[1]["size"].(float64) != 9000 || fs[1]["quality"].(map[string]any)["quality"].(map[string]any)["name"] != "WEBDL-2160p" {
		t.Fatalf("movie files %v", fs)
	}
	// Ruddarr's MediaFile requires id, size, dateAdded, quality.
	wantKeys(t, "moviefile", fs[1], fields(`id movieId relativePath path size dateAdded languages quality mediaInfo
		qualityCutoffNotMet indexerFlags`), "sceneName", "customFormats", "customFormatScore", "releaseGroup")
	fs = nil
	e.call("GET", "/radarr/api/v3/moviefile?movieFileIds=1", nil, 200, &fs)
	if len(fs) != 1 {
		t.Errorf("by file id %v", fs)
	}
	e.call("GET", "/radarr/api/v3/moviefile", nil, 400, nil)
	var one map[string]any
	e.call("GET", "/radarr/api/v3/moviefile/"+strconv.Itoa(LinkFileBase+7), nil, 200, &one)
	// The movie now shows our link as its file in the web view.
	var m map[string]any
	e.call("GET", "/v/movie-web/radarr/api/v3/movie/"+strconv.Itoa(IDBase+1), nil, 200, &m)
	if m["hasFile"] != true || m["movieFileId"].(float64) != LinkFileBase+7 {
		t.Errorf("movie with link %v %v", m["hasFile"], m["movieFileId"])
	}

	// Sonarr: episodefile?seriesId= (decypharr library.go, upgrade scripts), and the episode's episodeFileId.
	var efs []map[string]any
	e.call("GET", "/sonarr/api/v3/episodefile?seriesId="+strconv.Itoa(IDBase+10), nil, 200, &efs)
	if len(efs) != 1 || efs[0]["id"].(float64) != 3 || efs[0]["seasonNumber"].(float64) != 1 ||
		efs[0]["customFormatScore"].(float64) != 120 || efs[0]["seriesId"].(float64) != IDBase+10 {
		t.Fatalf("episode files %v", efs)
	}
	var eps []map[string]any
	e.call("GET", "/sonarr/api/v3/episode?seriesId="+strconv.Itoa(IDBase+10), nil, 200, &eps)
	for _, x := range eps {
		if x["id"].(float64) == 101 && x["episodeFileId"].(float64) != 3 {
			t.Errorf("episode 101 file %v", x["episodeFileId"])
		}
	}
	e.call("GET", "/sonarr/api/v3/episodefile/3", nil, 200, &one)

	// DELETE while the library gate refuses (library section missing): 409, nothing changes.
	rec := e.do("DELETE", "/v/movie-web/radarr/api/v3/moviefile/"+strconv.Itoa(LinkFileBase+7), e.key, nil)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "dry run") {
		t.Fatalf("gated delete %d %s", rec.Code, rec.Body.String())
	}
	if len(e.lib.unlinks) != 0 || e.str(`SELECT state FROM version WHERE id = 2`) != "filled" {
		t.Error("gated delete changed something")
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at)
		VALUES (8, '/media/movies-web/Alpha (2020)/Alpha (2020) other.mkv',
		'/mnt/debrid/__all__/Alpha.WEB/other.mkv', 1, 'active', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (8, 2)`)
	// Open the gate for movie-web (active), with the links inside its root; deleting one of two files keeps the
	// version filled.
	root := "/media/movies-web"
	e.pol.Library = &policy.Library{Enabled: true, Versions: map[string]*policy.LibraryVersion{
		"movie-web": {Roots: []string{root}}}}
	e.lib.gate = library.NewGate(e.pol)
	e.call("DELETE", "/v/movie-web/radarr/api/v3/moviefile/"+strconv.Itoa(LinkFileBase+7), nil, 200, nil)
	if len(e.lib.unlinks) != 1 || !strings.HasPrefix(e.lib.unlinks[0], "movie-web /media/movies-web/") {
		t.Errorf("unlinks %v", e.lib.unlinks)
	}
	if e.str(`SELECT state FROM version WHERE id = 2`) != "filled" {
		t.Error("version reset while another file remains")
	}
	if ev := e.events.events; len(ev) != 1 || ev[0].Kind != notify.FileDelete || ev[0].Version != "movie-web" {
		t.Errorf("events %+v", ev)
	}
	e.call("DELETE", "/v/movie-web/radarr/api/v3/moviefile/"+strconv.Itoa(LinkFileBase+8), nil, 200, nil)
	if e.str(`SELECT state FROM version WHERE id = 2`) != "wanted" {
		t.Error("version not wanted after its last file was removed")
	}
	// A shadow version's file (movie-remux) stays: the gate refuses it.
	rec = e.do("DELETE", "/radarr/api/v3/moviefile/1", e.key, nil)
	if rec.Code != 409 {
		t.Errorf("shadow delete %d", rec.Code)
	}
	// Bulk: an empty list is a 400.
	e.call("DELETE", "/sonarr/api/v3/episodefile/bulk", map[string]any{"episodeFileIds": []int{}}, 400, nil)
}

func TestManualImportAndImportAPI(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.seedDownloads()
	dir := t.TempDir()
	folder := filepath.Join(dir, "Alpha.2020.2160p.WEB-DL.H265-GRP")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "alpha.2160p.mkv"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// An upgrade script: manualimport?folder=&filterExistingFiles=false; the file maps by its folder's name.
	var items []map[string]any
	e.call("GET", "/v/movie-web/radarr/api/v3/manualimport?folder="+folder+"&filterExistingFiles=false", nil, 200, &items)
	if len(items) != 1 || items[0]["movie"].(map[string]any)["id"].(float64) != IDBase+1 ||
		items[0]["size"].(float64) != 100 || len(items[0]["rejections"].([]any)) != 0 {
		t.Fatalf("folder candidates %v", items)
	}
	wantKeys(t, "manualimport", items[0], fields(`id path folderName name size movie quality languages qualityWeight
		customFormats customFormatScore indexerFlags rejections releaseGroup`))
	// Reprocess (LunaSea's POST manualimport) for an explicit movie.
	var re []map[string]any
	e.call("POST", "/v/movie-web/radarr/api/v3/manualimport", []map[string]any{{"id": 5,
		"path": filepath.Join(folder, "alpha.2160p.mkv"), "movieId": IDBase + 1}}, 200, &re)
	if len(re) != 1 || re[0]["id"].(float64) != 5 || re[0]["customFormatScore"].(float64) != 50 {
		t.Errorf("reprocess %v", re)
	}
	// decypharr: manualimport?downloadId= plans our download with the library (dry run).
	e.lib.plan = func(id int64) *library.Plan {
		return &library.Plan{DownloadID: id, Folder: "/mnt/debrid/__all__/Alpha.WEB",
			Files: []library.PlannedFile{{Path: "/mnt/debrid/__all__/Alpha.WEB/a.mkv", Rel: "a.mkv", Size: 9000}},
			Links: []library.PlannedLink{{File: "a.mkv", Path: "/x/Alpha (2020).mkv"}}}
	}
	items = nil
	e.call("GET", "/radarr/api/v3/manualimport?downloadId="+strings.Repeat("A", 40), nil, 200, &items)
	if len(items) != 1 || items[0]["downloadId"] != strings.Repeat("A", 40) || len(e.lib.imports) != 1 ||
		e.lib.imports[0] != "1 dry" {
		t.Fatalf("download candidates %v %v", items, e.lib.imports)
	}
	// The ManualImport command imports the chosen file of our download (through the library) at its path, and
	// says what it refused: a file off the mount.
	e.exec(`UPDATE download SET state = 'ready' WHERE id = 1`)
	var c map[string]any
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "ManualImport", "importMode": "copy",
		"files": []map[string]any{{"path": "/mnt/debrid/__all__/Alpha.WEB/a.mkv", "downloadId": strings.Repeat("A", 40),
			"movieId": IDBase + 1}, {"path": "/elsewhere/b.mkv", "movieId": IDBase + 1}}}, 201, &c)
	if e.lib.imports[len(e.lib.imports)-1] != "1" || !strings.Contains(c["message"].(string), "Imported 1 file(s); 1 file(s) refused") ||
		!strings.Contains(c["message"].(string), "/elsewhere/b.mkv: not on the debrid mount") {
		t.Errorf("ManualImport %v %v", e.lib.imports, c["message"])
	}
	if got := e.str(`SELECT source_path || ' ' || (SELECT group_concat(rel || ':' || source) FROM download_file WHERE download_id = 1)
		FROM download WHERE id = 1`); got != "/mnt/debrid/__all__/Alpha.WEB/a.mkv a.mkv:manual" {
		t.Errorf("chosen file %q", got)
	}
	// Move requests use the same linking path; Theatarr leaves mount files in place.
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "ManualImport", "importMode": "move",
		"files": []map[string]any{{"path": "/mnt/debrid/__all__/Alpha.WEB/a.mkv", "movieId": IDBase + 1}}}, 201, &c)
	if c["status"] != "completed" || !strings.Contains(c["message"].(string), "Imported 1 file(s)") {
		t.Errorf("move: %v %v", c["status"], c["message"])
	}

	// The import API: the gate refuses (no library section), so it is a dry run and leaves no download behind.
	before := e.one(`SELECT COUNT(*) FROM download`)
	res, err := e.svc.Import(t.Context(), ImportRequest{TitleID: 1, Version: "movie-web",
		InfoHash: strings.Repeat("e", 40), Release: "Alpha.2020.2160p.WEB-DL.H265-GRP"})
	if err != nil || res.Applied || res.Refused == "" || !res.Plan.DryRun {
		t.Fatalf("gated import %+v %v", res, err)
	}
	if e.one(`SELECT COUNT(*) FROM download`) != before {
		t.Error("a dry-run import left a download")
	}
	// Open: the download stays and is imported by the library.
	e.pol.Library = &policy.Library{Enabled: true, Versions: map[string]*policy.LibraryVersion{
		"movie-web": {Roots: []string{t.TempDir()}}}}
	e.lib.gate = library.NewGate(e.pol)
	res, err = e.svc.Import(t.Context(), ImportRequest{TitleID: 1, Version: "movie-web",
		Path: "/mnt/debrid/__all__/Alpha.2020.2160p.WEB-DL.H265-GRP"})
	if err != nil || !res.Applied || res.DownloadID == 0 {
		t.Fatalf("import %+v %v", res, err)
	}
	if e.str(`SELECT name FROM download WHERE id = ?`, res.DownloadID) != "Alpha.2020.2160p.WEB-DL.H265-GRP" {
		t.Error("download name")
	}
	// A path to a file inside the release keeps the release folder (not the file's name) and the path itself.
	res, err = e.svc.Import(t.Context(), ImportRequest{TitleID: 1, Version: "movie-web",
		Path: "/mnt/debrid/__all__/Alpha.Pack/Movie/alpha.2160p.mkv"})
	if err != nil || !res.Applied {
		t.Fatalf("file import %+v %v", res, err)
	}
	if got := e.str(`SELECT name || ' | ' || source_path || ' | ' || release_title FROM download WHERE id = ?`, res.DownloadID); got !=
		"Alpha.Pack | /mnt/debrid/__all__/Alpha.Pack/Movie/alpha.2160p.mkv | alpha.2160p" {
		t.Errorf("file import row %q", got)
	}
	if _, err := e.svc.Import(t.Context(), ImportRequest{TitleID: 1, Version: "movie-web", Path: "/srv/elsewhere/x.mkv"}); err == nil ||
		!errors.Is(err, ErrBadRequest) {
		t.Errorf("a path off the mount: %v", err)
	}
	if _, err := e.svc.Import(t.Context(), ImportRequest{TitleID: 1, Version: "movie-web"}); err == nil {
		t.Error("no hash or path accepted")
	}
	if _, err := e.svc.Import(t.Context(), ImportRequest{TitleID: 2, Version: "movie-remux", InfoHash: strings.Repeat("e", 40),
		Release: "x"}); err == nil {
		t.Error("a version the title does not have")
	}
	d, err := e.svc.Decide(t.Context(), DecideRequest{TitleID: 1, Version: "movie-web",
		Release: "Alpha.2020.2160p.WEB-DL.H265-GRP", Indexer: "prowlarr-public"})
	if err != nil || !d.Accepted || d.Score != 50 {
		t.Errorf("decide %+v %v", d, err)
	}
}

// TestDryRunImportPublishesNoDownload: a dry run of the import API plans without any download row, so nothing the
// import pass could pick up exists while it plans, and a request cancelled meanwhile leaves nothing behind.
func TestDryRunImportPublishesNoDownload(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.seedDownloads()
	before, readyBefore := e.one(`SELECT COUNT(*) FROM download`), e.one(`SELECT COUNT(*) FROM download WHERE state = 'ready'`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := int64(-1)
	e.lib.during = func() {
		ready = e.one(`SELECT COUNT(*) FROM download WHERE state = 'ready'`) - readyBefore
		cancel() // the client goes away while the plan is made
	}
	res, err := e.svc.Import(ctx, ImportRequest{TitleID: 1, Version: "movie-web", InfoHash: strings.Repeat("e", 40),
		Release: "Alpha.2020.2160p.WEB-DL.H265-GRP", DryRun: true})
	if err != nil || res.Applied || res.DownloadID != 0 || !res.Plan.DryRun {
		t.Fatalf("dry run %+v %v", res, err)
	}
	if ready != 0 {
		t.Errorf("%d ready download(s) while the dry run planned", ready)
	}
	if n := e.one(`SELECT COUNT(*) FROM download`); n != before {
		t.Errorf("%d download(s) after a cancelled dry run, want %d", n, before)
	}
	if strings.Join(e.lib.imports, ",") != "new dry" {
		t.Errorf("imports %v", e.lib.imports)
	}
}

// TestManualImportPreviewOnADeadMount: the folder preview reads through the mount guard; a mount that does not
// answer is an error, not an empty folder (and not a request blocked in the kernel).
func TestManualImportPreviewOnADeadMount(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.seedDownloads()
	folder := filepath.Join(t.TempDir(), "Alpha.2020.2160p.WEB-DL.H265-GRP")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	e.lib.mountDown = true
	var out any
	e.call("GET", "/v/movie-web/radarr/api/v3/manualimport?folder="+folder, nil, 500, &out)
	e.call("POST", "/v/movie-web/radarr/api/v3/manualimport", []map[string]any{{"id": 5,
		"path": filepath.Join(folder, "alpha.2160p.mkv"), "movieId": IDBase + 1}}, 500, &out)
}

// TestManualImportSeries: Sonarr's ManualImport imports each chosen file with the client's episodes, whatever the
// file names say: files of our download into it, a file of a release not ours into a new download of the series'
// version; a file without episodes, or with another series' episodes, is refused.
func TestManualImportSeries(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.seedDownloads()
	e.exec(`UPDATE download SET state = 'ready' WHERE id = 3`)
	dl := strings.Repeat("C", 40)
	base := "/mnt/debrid/__all__/"
	var c map[string]any
	e.call("POST", "/sonarr/api/v3/command", map[string]any{"name": "ManualImport", "importMode": "auto",
		"files": []map[string]any{
			{"path": base + "The.Show.S01/e1.mkv", "downloadId": dl, "seriesId": IDBase + 10, "episodeIds": []int{102}},
			{"path": base + "The.Show.S01/e2.mkv", "downloadId": dl, "seriesId": IDBase + 10, "episodeIds": []int{101}},
			{"path": base + "Show.Extras/special.mkv", "seriesId": IDBase + 10, "episodeIds": []int{100}},
			{"path": base + "Show.Extras/none.mkv", "seriesId": IDBase + 10},
			{"path": base + "Show.Extras/other.mkv", "seriesId": IDBase + 10, "episodeIds": []int{999}},
		}}, 201, &c)
	msg, _ := c["message"].(string)
	if !strings.Contains(msg, "2 file(s) refused") || !strings.Contains(msg, "none.mkv: no episodes chosen") ||
		!strings.Contains(msg, "other.mkv: bad request: episodes [999]") {
		t.Errorf("message %q", msg)
	}
	if got := e.str(`SELECT group_concat(rel || '=' || episodes, ' ') FROM (SELECT * FROM download_file WHERE download_id = 3
		AND source = 'manual' ORDER BY rel)`); got != "e1.mkv=[102] e2.mkv=[101]" {
		t.Errorf("our download's files %q", got)
	}
	newID := e.str(`SELECT id FROM download WHERE source = 'api:manualimport'`)
	if got := e.str(`SELECT name || ' ' || version_key || ' ' || state FROM download WHERE id = ?`, newID); got != "Show.Extras tv-2160p ready" {
		t.Errorf("new download %q", got)
	}
	if got := e.str(`SELECT rel || '=' || episodes FROM download_file WHERE download_id = ?`, newID); got != "special.mkv=[100]" {
		t.Errorf("new download's file %q", got)
	}
	if strings.Join(e.lib.imports, ",") != "3,"+newID {
		t.Errorf("imports %v", e.lib.imports)
	}

	// The native import takes chosen files too: in one release folder, with the title's episodes.
	if _, err := e.svc.Import(t.Context(), ImportRequest{TitleID: 10, Version: "tv-2160p", Files: []ImportFile{
		{Path: base + "Pack/a.mkv", Episodes: []int64{101}}, {Path: base + "Other/b.mkv", Episodes: []int64{102}}}}); err == nil ||
		!strings.Contains(err.Error(), "not a file of the release folder Pack") {
		t.Errorf("files across folders: %v", err)
	}
	if _, err := e.svc.Import(t.Context(), ImportRequest{TitleID: 10, Version: "tv-2160p", Files: []ImportFile{
		{Path: base + "Pack/a.mkv", Episodes: []int64{1}}}}); err == nil || !errors.Is(err, ErrBadRequest) {
		t.Errorf("another title's episodes: %v", err)
	}
	res, err := e.svc.Import(t.Context(), ImportRequest{TitleID: 10, Version: "tv-2160p", Files: []ImportFile{
		{Path: base + "Pack/a.mkv", Episodes: []int64{102}}, {Path: base + "Pack/b.mkv", Episodes: []int64{101}}}})
	if err != nil || res.Applied || !res.Plan.DryRun {
		t.Fatalf("gated files import %+v %v", res, err)
	}
	if n := e.str(`SELECT COUNT(*) FROM download_file WHERE download_id NOT IN (SELECT id FROM download)`); n != "0" {
		t.Errorf("a dry run left %s file rows", n)
	}
}
