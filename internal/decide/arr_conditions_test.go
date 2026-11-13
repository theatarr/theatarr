package decide

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/library/naming"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestMovieProperSearchAndSeeders(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	r := rel("Dune.Part.Two.2024.PROPER.2160p.MA.WEB-DL.DDP5.1-FLUX", "public", 25*gb)
	f := LinkedFile(policy.KindMovie, rel("Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX", "public", 25*gb), "WEBDL-2160p")
	f.Added = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	d, err := e.Decide(context.Background(), r, dune, Version{Key: "movie-remux"}, f)
	if err != nil || reasons(d) != "ProperForOldFile" {
		t.Fatalf("RSS: %+v %v", d, err)
	}
	ctx := WithSearch(context.Background())
	d, err = e.Decide(ctx, r, dune, Version{Key: "movie-remux"}, f)
	if err != nil || !d.Accepted {
		t.Fatalf("search: %+v %v", d, err)
	}
	r.Seeders = 0
	d, err = e.Decide(ctx, r, dune, Version{Key: "movie-remux"}, f)
	if err != nil || reasons(d) != "MinimumSeeders" {
		t.Fatalf("search seeders: %+v %v", d, err)
	}
	e.Policy.Indexers["public"].MinSeeders = new(0)
	d, err = e.Decide(ctx, r, dune, Version{Key: "movie-remux"}, f)
	if err != nil || !d.Accepted {
		t.Fatalf("disabled seeders: %+v %v", d, err)
	}
}

func TestSeriesTitleHoldAndSeeders(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	title := show()
	v := tvVersion(e, "tv")
	episode := ep(&title, 101)
	episode.Title = "TBA"
	episode.AirDateUTC = new(seriesClock)
	r := rel("The.Show.S01E01.2160p.WEB-DL-GRP", "public", 8*gb)
	d, err := e.Decide(context.Background(), r, title, v, nil)
	if err != nil || !d.Accepted {
		t.Fatalf("TBA is accepted at grab time: %+v %v", d, err)
	}
	e.Policy.Versions["tv"].Naming.File = "{Series Title} - S{season:00}E{episode:00}"
	d, err = e.Decide(context.Background(), r, title, v, nil)
	if err != nil || !d.Accepted {
		t.Fatalf("title unused: %+v %v", d, err)
	}
	r.Seeders = 0
	d, err = e.Decide(context.Background(), r, title, v, nil)
	if err != nil || reasons(d) != "MinimumSeeders" {
		t.Fatalf("RSS seeders: %+v %v", d, err)
	}
	r.Seeders = -1
	d, err = e.Decide(context.Background(), r, title, v, nil)
	if err != nil || !d.Accepted {
		t.Fatalf("unknown seeders: %+v %v", d, err)
	}
}

func TestRequiresEpisodeTitle(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	for _, tc := range []struct {
		name, kind, standard, daily, anime string
		absolute                           int
		rename, want                       bool
	}{
		{"standard", "standard", "{Episode Title}", "", "", 0, true, true},
		{"clean title", "standard", "{Episode.CleanTitle}", "", "", 0, true, true},
		{"escaped", "standard", "{{Episode Title}}", "", "", 0, true, false},
		{"rename off", "standard", "{Episode Title}", "", "", 0, false, false},
		{"daily uses daily", "daily", "{Episode Title}", "{Air-Date}", "", 0, true, false},
		{"daily title", "daily", "", "{Episode Title}", "", 0, true, true},
		{"anime absolute", "anime", "{Episode Title}", "", "{absolute:00}", 1, true, false},
		{"anime without absolute", "anime", "{Episode Title}", "", "{absolute:00}", 0, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := naming.Config{Rename: tc.rename, StandardEpisodeFormat: tc.standard, DailyEpisodeFormat: tc.daily, AnimeEpisodeFormat: tc.anime}
			got := e.requiresEpisodeTitle(&Title{SeriesType: tc.kind}, Version{Naming: &cfg}, []*Episode{{Absolute: tc.absolute}})
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadArrNamingAndConfig(t *testing.T) {
	t.Parallel()
	st := linkedDB(t)
	ctx := context.Background()
	e := seriesEngine(t)
	e.Policy.Versions["tv"].Naming = policy.Naming{FromArr: "sonarr"}
	if _, err := st.Writer().Exec(`INSERT INTO library_naming (instance, naming, qualities, fetched_at) VALUES ('sonarr', ?, '{}', 1)`, `{"renameEpisodes":true,"standardEpisodeFormat":"{Episode Title}"}`); err != nil {
		t.Fatal(err)
	}
	v, err := LoadVersion(ctx, st.Reader(), e.Policy, 1, "tv")
	if err != nil || v.Naming == nil || !e.requiresEpisodeTitle(&Title{}, v, nil) {
		t.Fatalf("stored naming: %+v %v", v, err)
	}
	c, err := arrConfig(&ArrSnapshot{MediaManagement: []byte(`{"episodeTitleRequired":"Never"}`), DownloadClientConfig: []byte(`{"enableCompletedDownloadHandling":false}`)})
	if err != nil || c.EpisodeTitleRequired != "Never" || !c.CompletedDownloadHandlingDisabled {
		t.Fatalf("config: %+v %v", c, err)
	}
}

func TestImportedGrabHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := linkedDB(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.Writer().ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO title (id,kind,name,created_at,updated_at) VALUES (1,'movie','Movie',0,0)`)
	exec(`INSERT INTO version (id,title_id,policy_key,created_at,updated_at) VALUES (1,1,'movie',0,0),(2,1,'follower',0,0)`)
	exec(`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,quality,state,created_at,updated_at,added_at,imported_at)
 VALUES (1,1,1,'movie','hash','Movie.2024.1080p.WEB-DL-GRP','pub','WEBDL-1080p','imported',1,2,1,2)`)
	raw, _ := json.Marshal(&ExistingFile{Quality: "HDTV-720p", Revision: 1, Added: time.UnixMilli(3).UTC()})
	exec(`INSERT INTO link (id,title_id,download_id,path,target,quality,file_metadata,created_at,updated_at) VALUES (1,1,1,'/movie.mkv','/mount/movie.mkv','HDTV-720p',?,4,4)`, string(raw))
	exec(`INSERT INTO link_version VALUES (1,1),(1,2)`)
	f, err := LoadExisting(ctx, st.Reader(), 1)
	if err != nil || f.LastGrab == nil || !f.LastGrab.Imported || f.LastGrab.DownloadID != "HASH" || f.LastGrab.GrabbedQuality.Quality.Name != "WEBDL-1080p" || f.LastGrab.ImportedQuality.Quality.Name != "HDTV-720p" || !f.Added.Equal(time.UnixMilli(3)) {
		t.Fatalf("history and original added time: %+v %v", f, err)
	}
	follower, err := LoadExisting(ctx, st.Reader(), 2)
	if err != nil || follower.LastGrab != nil {
		t.Fatalf("follower inherited source history: %+v %v", follower, err)
	}
	exec(`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,quality,state,created_at,updated_at,added_at)
 VALUES (2,1,1,'movie','new','Movie.2024.1080p.WEB-DL-NEW','pub','WEBDL-1080p','added',5,5,5)`)
	f, err = LoadExisting(ctx, st.Reader(), 1)
	if err != nil || f.LastGrab.DownloadID != "NEW" || f.LastGrab.Imported {
		t.Fatalf("latest unimported grab: %+v %v", f, err)
	}
	exec(`UPDATE download SET quality = '' WHERE id = 2`)
	f, err = LoadExisting(ctx, st.Reader(), 1)
	if err != nil || f.LastGrab.DownloadID != "NEW" || f.LastGrab.Imported {
		t.Fatalf("latest unknown-quality grab: %+v %v", f, err)
	}
	exec(`DELETE FROM download WHERE id = 2`)
	exec(`UPDATE link SET file_metadata = '{}' WHERE id = 1`)
	f, err = LoadExisting(ctx, st.Reader(), 1)
	if err != nil || !f.LastGrab.Imported || f.LastGrab.ImportedQuality.Quality.Name != "HDTV-720p" {
		t.Fatalf("legacy imported quality: %+v %v", f, err)
	}
}
