package arrview

import (
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

// newCutoffEnv is the dev environment with a decision engine: every version uses a profile with a quality cutoff
// (WEBDL-2160p) and a score cutoff (100) that no file here reaches. Files (state filled, below the full cutoff):
// Alpha's movie-web link and Beta's file at WEBDL-2160p (the cutoff quality), Delta's at WEBDL-1080p; S01E01 at
// WEBDL-2160p, S01E02 at WEBDL-1080p.
// mode is router.wanted.cutoff_unmet.
func newCutoffEnv(t *testing.T, mode policy.CutoffMode) *devEnv {
	t.Helper()
	e := buildDevEnv(t, true, func(pol *policy.Policy) *decide.Engine {
		pol.Router.Wanted = &policy.Wanted{CutoffUnmet: mode}
		pol.Profiles["cut"] = &policy.Profile{Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"},
			{Name: "WEBDL-1080p"}}, Cutoff: "WEBDL-2160p", CutoffScore: 100}
		for _, v := range pol.Versions {
			v.Profile = "cut"
		}
		eng, err := decide.New(pol, decide.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return eng
	})
	e.seedFiles()
	now := ms(testNow)
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(5, 4, 'movie-web', 1, 'filled', ?, ?)`, now, now)
	e.exec(`INSERT INTO title_release_date (title_id, source, country, type, date) VALUES
		(4, 'radarr_4k_web', '', 'digital', '2022-03-01'), (2, 'radarr_4k_web', '', 'digital', '2021-03-01')`)
	e.exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, relative_path, quality,
		scene_name, languages, added_at) VALUES
		(4, 5, 4, 'radarr_4k_web', 504, '/m/Delta (2022)/Delta.mkv', 'Delta.mkv', 'WEBDL-1080p',
		 'Delta.2022.1080p.WEB-DL-GRP', '[]', ?),
		(5, 10, 10, 'sonarr_4k_debrid', 902, '/s/The Show - S01E02.mkv', 'The Show - S01E02.mkv', 'WEBDL-1080p',
		 'The.Show.S01E02.1080p.WEB-DL-GRP', '[]', ?)`, now, now)
	e.exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (5, 102)`)
	e.exec(`UPDATE episode_version_state SET state = 'filled' WHERE version_id = 10 AND episode_id IN (101, 102)`)
	return e
}

// GET wanted/cutoff lists what the arrs list: a file below the quality cutoff (Radarr MovieCutoffService, Sonarr
// EpisodeCutoffService), not one at the cutoff quality below the score cutoff, though both are filled.
func TestWantedCutoffQualityOnly(t *testing.T) {
	t.Parallel()
	e := newCutoffEnv(t, "")
	var p struct {
		TotalRecords int
		Records      []map[string]any
	}
	titles := func() string {
		var out []string
		for _, r := range p.Records {
			out = append(out, r["title"].(string))
		}
		return strings.Join(out, " ")
	}
	e.call("GET", "/radarr/api/v3/wanted/cutoff", nil, 200, &p)
	if p.TotalRecords != 1 || titles() != "Delta" { // Alpha: WEBDL-2160p, the cutoff quality
		t.Errorf("radarr cutoff: %d %s", p.TotalRecords, titles())
	}
	p.Records = nil
	e.call("GET", "/radarr/api/v3/wanted/cutoff?monitored=false", nil, 200, &p)
	if p.TotalRecords != 0 { // Beta: WEBDL-2160p
		t.Errorf("radarr unmonitored cutoff: %d %s", p.TotalRecords, titles())
	}
	p.Records = nil
	e.call("GET", "/sonarr/api/v3/wanted/cutoff?pageSize=10000&monitored=true", nil, 200, &p)
	if p.TotalRecords != 1 || p.Records[0]["episodeNumber"].(float64) != 2 {
		t.Errorf("sonarr cutoff %v", p)
	}

	// Without the quality judgement (no engine), every filled item is listed, as before.
	e.cutoff = nil
	e.startDev(devDecider{e: e})
	p.Records = nil
	e.call("GET", "/radarr/api/v3/wanted/cutoff", nil, 200, &p)
	if titles() != "Alpha Delta" {
		t.Errorf("no judge: %s", titles())
	}
	p.Records = nil
	e.call("GET", "/sonarr/api/v3/wanted/cutoff?pageSize=10000&monitored=true", nil, 200, &p)
	if p.TotalRecords != 2 {
		t.Errorf("no judge, sonarr: %v", p)
	}
}

// CutOffUnmetMoviesSearch and CutoffUnmetEpisodeSearch search the arrs' list (quality cutoff only), whatever
// router.wanted.cutoff_unmet says: that setting is Theatarr's own loop.
func TestCutoffUnmetCommandsQualityOnly(t *testing.T) {
	t.Parallel()
	for _, mode := range []policy.CutoffMode{policy.CutoffOff, policy.CutoffScore} {
		// Each command searches its own view's version only, never the other kind's (acquire.Submit). A fresh
		// environment each: the first command's download would hold the item back as in flight.
		for _, tc := range []struct{ path, name, want string }{
			{"/v/movie-web/radarr/api/v3/command", "CutOffUnmetMoviesSearch", "movie-web Delta"},
			{"/v/tv-2160p/sonarr/api/v3/command", "CutoffUnmetEpisodeSearch", "tv-2160p The Show s1e2"},
			{"/radarr/api/v3/command", "CutOffUnmetMoviesSearch", "movie-web Delta"},
			{"/sonarr/api/v3/command", "CutoffUnmetEpisodeSearch", "tv-2160p The Show s1e2"},
		} {
			e := newCutoffEnv(t, mode)
			e.call("POST", tc.path, map[string]any{"name": tc.name}, 201, nil)
			e.drain()
			e.mu.Lock()
			got := strings.Join(e.calls, "|")
			e.mu.Unlock()
			if got != tc.want {
				t.Errorf("cutoff_unmet %s, %s %s: searches %s, want %s", mode, tc.path, tc.name, got, tc.want)
			}
		}
	}
}
