package arrview

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
)

type manualDecider struct {
	Decider
	e      *env
	engine *decide.Engine
}

func (d manualDecider) Decide(ctx context.Context, rel decide.Release, tid int64, key string) (*decide.Decision, error) {
	t, err := decide.LoadTitle(ctx, d.e.st.Reader(), tid)
	if err != nil {
		return nil, err
	}
	v, err := decide.LoadVersion(ctx, d.e.st.Reader(), d.e.pol, tid, key)
	if err != nil {
		return nil, err
	}
	if t.Kind == policy.KindSeries {
		if err := decide.LoadSeriesVersion(ctx, d.e.st.Reader(), &t, v.ID); err != nil {
			return nil, err
		}
	}
	file, err := decide.LoadExisting(ctx, d.e.st.Reader(), v.ID)
	if err != nil {
		return nil, err
	}
	return d.engine.Decide(ctx, rel, t, v, file)
}

func TestManualImportClientOverrides(t *testing.T) {
	t.Parallel()
	for _, series := range []bool{false, true} {
		for _, existing := range []bool{false, true} {
			t.Run(map[bool]string{true: "series", false: "movie"}[series]+map[bool]string{true: "/download", false: "/new"}[existing], func(t *testing.T) {
				e := newDevEnv(t)
				for _, v := range e.pol.Versions {
					v.Profile = "manual"
				}
				e.pol.Profiles = map[string]*policy.Profile{"manual": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}}
				engine, err := decide.New(e.pol, decide.Options{})
				if err != nil {
					t.Fatal(err)
				}
				version, flavour, folder, filename, tid, vid := "movie-web", Radarr, "Alpha.2020.1080p.WEB-DL-OLD", "Alpha.2020.1080p.WEB-DL-OLD.mkv", int64(1), int64(2)
				fl := format.Radarr
				if series {
					version, flavour, folder, filename, tid, vid, fl = "tv-2160p", Sonarr, "The.Show.S01.1080p.WEB-DL-OLD", "The.Show.S01E01.1080p.WEB-DL-OLD.mkv", 10, 10, format.Sonarr
				}
				formats, err := format.ParseArrFormats(fl, []byte(`[{"id":900,"name":"Client choices","includeCustomFormatWhenRenaming":true,"specifications":[{"name":"group","implementation":"ReleaseGroupSpecification","fields":{"value":"^CHOSEN$"}},{"name":"French","implementation":"LanguageSpecification","fields":{"value":2,"exceptLanguage":false}},{"name":"flag","implementation":"IndexerFlagSpecification","fields":{"value":4}}]}]`))
				if err != nil {
					t.Fatal(err)
				}
				pr := engine.Profile("manual", fl)
				pr.Formats = formats
				pr.Profile.FormatItems = []format.FormatItem{{Format: 900, Score: 321}}
				e.svc.decider = manualDecider{Decider: e.svc.decider, e: e.env, engine: engine}
				dir := t.TempDir()
				mount, root := filepath.Join(dir, "mount"), filepath.Join(dir, "library")
				source := filepath.Join(mount, "__all__", folder, filename)
				if err := os.MkdirAll(filepath.Dir(source), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(root, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(source, []byte("source untouched"), 0644); err != nil {
					t.Fatal(err)
				}
				before, _ := os.Stat(source)
				vp := e.pol.Versions[version]
				vp.Library = root
				vp.Naming = policy.Naming{Folder: "{Movie Title} ({Release Year})", File: "{Movie Title} {[Quality Full]}{-Release Group}", Season: "Season {season:00}"}
				if series {
					vp.Naming.Folder = "{Series Title}"
					vp.Naming.File = "{Series Title} - S{season:00}E{episode:00} {[Quality Full]}{-Release Group}"
				}
				e.pol.Library = &policy.Library{Enabled: true, Mount: policy.LibraryMount{Path: mount}, Versions: map[string]*policy.LibraryVersion{version: {Roots: []string{root}}}}
				e.svc.library = library.New(library.Options{Store: e.st, Policy: e.pol, Engine: engine, Probe: func(string, int64) (*probe.Result, error) { return nil, probe.ErrUnknownFormat }, Now: func() time.Time { return e.now }})
				base := "/v/" + version + "/" + flavour + "/api/v3"
				item := map[string]any{"path": source, "movieId": IDBase + tid, "seriesId": IDBase + tid}
				if series {
					item["episodeIds"] = []int64{102}
				}
				var preview []manualImportResource
				e.call("POST", base+"/manualimport", []any{item}, 200, &preview)
				if len(preview) != 1 || len(preview[0].Rejections) == 0 || preview[0].Quality.Quality.Name != "WEBDL-1080p" {
					t.Fatalf("parsed preview %+v", preview)
				}
				item["quality"] = map[string]any{"quality": map[string]any{"id": qualityFor(flavour, "WEBDL-2160p").ID}, "revision": map[string]any{"version": 3, "real": 1, "isRepack": true}}
				item["languages"] = []map[string]any{{"id": 2, "name": "French"}}
				item["releaseGroup"], item["indexerFlags"] = "CHOSEN", 4
				e.call("POST", base+"/manualimport", []any{item}, 200, &preview)
				if preview[0].Quality.Quality.Name != "WEBDL-2160p" || preview[0].Quality.Revision.Version != 3 || preview[0].Quality.Revision.Real != 1 || !preview[0].Quality.Revision.IsRepack || preview[0].ReleaseGroup != "CHOSEN" || len(preview[0].Languages) != 1 || preview[0].Languages[0].Name != "French" || preview[0].CustomFormatScore != 321 || preview[0].IndexerFlags != 4 {
					t.Fatalf("override preview %+v", preview[0])
				}
				for _, r := range preview[0].Rejections {
					if strings.Contains(r.Reason, "not wanted") {
						t.Fatalf("quality override ignored: %+v", preview[0])
					}
				}
				if series && (len(preview[0].Episodes) != 1 || preview[0].Episodes[0].ID != 102) {
					t.Fatalf("episodes %+v", preview[0])
				}
				if existing {
					e.exec(`INSERT INTO download(id,title_id,version_id,version_key,info_hash,release_title,indexer,name,state,created_at,updated_at) VALUES(31,?,?,?,?,?,'import',?,'ready',0,0)`, tid, vid, version, strings.Repeat("a", 40), folder, folder)
					item["downloadId"] = strings.Repeat("a", 40)
				}
				var command map[string]any
				e.call("POST", base+"/command", map[string]any{"name": "ManualImport", "importMode": "move", "files": []any{item}}, 201, &command)
				if command["status"] != "completed" || !strings.Contains(command["message"].(string), "Imported 1 file(s)") {
					t.Fatalf("command %+v", command)
				}
				if existing {
					e.call("GET", base+"/manualimport?downloadId="+strings.Repeat("a", 40), nil, 200, &preview)
					if len(preview) != 1 || preview[0].Quality.Quality.Name != "WEBDL-2160p" || preview[0].Quality.Revision.Version != 3 || preview[0].Quality.Revision.Real != 1 || !preview[0].Quality.Revision.IsRepack || preview[0].ReleaseGroup != "CHOSEN" || len(preview[0].Languages) != 1 || preview[0].Languages[0].Name != "French" || preview[0].CustomFormatScore != 321 || preview[0].IndexerFlags != 4 {
						t.Fatalf("download preview %+v", preview)
					}
					if series && (len(preview[0].Episodes) != 1 || preview[0].Episodes[0].ID != 102) {
						t.Fatalf("download episodes %+v", preview[0])
					}
				}
				var path, raw string
				if err := e.st.Reader().QueryRow(`SELECT path,file_metadata FROM link WHERE download_id IS NOT NULL`).Scan(&path, &raw); err != nil {
					t.Fatal(err)
				}
				var saved decide.ExistingFile
				if err := json.Unmarshal([]byte(raw), &saved); err != nil {
					t.Fatal(err)
				}
				if saved.Quality != "WEBDL-2160p" || saved.Revision != 3 || saved.Real != 1 || !saved.IsRepack || saved.ReleaseGroup != "CHOSEN" || strings.Join(saved.Languages, ",") != "French" || saved.IndexerFlags != 4 || saved.Score == nil || *saved.Score != 321 {
					t.Fatalf("saved %+v", saved)
				}
				if !strings.Contains(path, "WEBDL-2160p") || !strings.Contains(path, "CHOSEN") {
					t.Fatalf("name %s", path)
				}
				if got, err := os.Readlink(path); err != nil || got != source {
					t.Fatalf("symlink %q: %v", got, err)
				}
				after, err := os.Stat(source)
				if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
					t.Fatalf("source changed: %v", err)
				}
				if b, err := os.ReadFile(source); err != nil || string(b) != "source untouched" {
					t.Fatalf("source %q: %v", b, err)
				}
				if got := e.str(`SELECT detail FROM library_action WHERE kind='link'`); !strings.Contains(got, "client importMode move") || !strings.Contains(got, "linked instead") {
					t.Fatalf("journal %q", got)
				}
				var files []map[string]any
				endpoint := "/moviefile?movieId="
				if series {
					endpoint = "/episodefile?seriesId="
				}
				e.call("GET", base+endpoint+itoa(IDBase+tid), nil, 200, &files)
				if len(files) != 1 || files[0]["releaseGroup"] != "CHOSEN" || files[0]["quality"].(map[string]any)["revision"].(map[string]any)["isRepack"] != true || files[0]["indexerFlags"] != float64(4) || files[0]["quality"].(map[string]any)["revision"].(map[string]any)["version"] != float64(3) || files[0]["languages"].([]any)[0].(map[string]any)["name"] != "French" {
					t.Fatalf("arr files %+v", files)
				}
			})
		}
	}
}

func TestManualNativeUsesVersionHandlers(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	r := httptest.NewRequest("GET", "/api/theatarr/v1/manualimport/options?version=tv-2160p", nil)
	w := httptest.NewRecorder()
	e.svc.ServeManualImport(w, r)
	var body struct {
		Flavour   string
		Titles    []struct{ Episodes []any }
		Qualities []any
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || body.Flavour != "sonarr" || len(body.Titles) == 0 || len(body.Titles[0].Episodes) == 0 || len(body.Qualities) == 0 {
		t.Fatalf("options %d: %s", w.Code, w.Body.String())
	}
}

func TestManualOverrideFallbackAndValidation(t *testing.T) {
	t.Parallel()
	for _, flavour := range []string{Radarr, Sonarr} {
		t.Run(flavour, func(t *testing.T) {
			v := &View{Flavour: flavour}
			for _, item := range []map[string]any{{}, {"quality": map[string]any{"quality": map[string]any{"id": 0}}, "languages": []map[string]any{{"id": 0}}, "releaseGroup": " "}} {
				o, err := manualOverrides(v, item, []int64{102})
				if err != nil || o.Quality != nil || o.Languages != nil || o.ReleaseGroup != nil || len(o.Episodes) != 1 || o.Episodes[0] != 102 {
					t.Fatalf("fallback %+v: %v", o, err)
				}
			}
			for _, item := range []map[string]any{{"quality": map[string]any{"quality": map[string]any{"id": 999}}}, {"quality": "bad"}, {"languages": []map[string]any{{"id": 999}}}, {"languages": "bad"}} {
				if _, err := manualOverrides(v, item, nil); err == nil {
					t.Fatalf("invalid override accepted: %+v", item)
				}
			}
		})
	}
}
