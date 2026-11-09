package library

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

// A pack's file is named, stored and judged as what it is. A "2160p REMUX" pack holding a 1080p WEB-DL
// file links it as [WEBDL-1080p]-FILE, records that quality and group on the link, and leaves the episode below the
// remux cutoff.
func TestPackFileIsWhatItIs(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	pack := "Show.S01.2160p.BluRay.REMUX-PACK"
	e.release(pack, map[string]float64{"Show.S01E01.1080p.WEB-DL-FILE.mkv": 2700,
		"Show.S01E02.2160p.BluRay.REMUX-PACK.mkv": 2700})
	e.videoSize(pack, 1920, 1080)
	e.probes[filepath.Join(e.mount, "__all__", pack, "Show.S01E02.2160p.BluRay.REMUX-PACK.mkv")].Video.Width = 3840
	e.probes[filepath.Join(e.mount, "__all__", pack, "Show.S01E02.2160p.BluRay.REMUX-PACK.mkv")].Video.Height = 2160
	e.seriesDownload(1, 2, 20, hashA, pack, "[201, 202]", nil)
	e.exec(`UPDATE download SET quality = 'Bluray-2160p Remux' WHERE id = 1`)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	got := e.links(plan)
	for _, w := range []string{"S01E01 - Pilot [WEBDL-1080p]-FILE.mkv <- Show.S01E01.1080p.WEB-DL-FILE.mkv",
		"S01E02 - Second Part [Bluray-2160p Remux]-PACK.mkv <- Show.S01E02.2160p.BluRay.REMUX-PACK.mkv"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %s in\n%s", w, got)
		}
	}
	var f decide.ExistingFile
	if err := json.Unmarshal([]byte(e.one(`SELECT file_metadata FROM link l JOIN link_episode le ON le.link_id = l.id
		WHERE le.episode_id = 201`)), &f); err != nil {
		t.Fatal(err)
	}
	if f.Quality != "WEBDL-1080p" || f.ReleaseGroup != "FILE" || f.SceneName != "Show.S01E01.1080p.WEB-DL-FILE" ||
		f.InfoHash != hashA || f.ReleaseType != "seasonPack" {
		t.Errorf("stored file inputs %+v", f)
	}
	if got := e.one(`SELECT group_concat(l.quality, ',') FROM (SELECT l.quality FROM link l JOIN link_episode le ON
		le.link_id = l.id ORDER BY le.episode_id) l`); got != "WEBDL-1080p,Bluray-2160p Remux" {
		t.Errorf("link qualities %s", got)
	}
	if got := e.one(`SELECT group_concat(episode_id || ':' || state, ' ') FROM
		(SELECT * FROM episode_version_state WHERE version_id = 20 ORDER BY episode_id)`); got != "201:filled 202:cutoff_met" {
		t.Errorf("episode states %s (the WEB-DL file is below the remux cutoff)", got)
	}
}

// The upgrade check judges the file, not the pack: a "REMUX" pack's WEB-DL 1080p file does not replace a WEB-DL
// 2160p episode file.
func TestUpgradeCheckJudgesTheFile(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, policy.ModeActive, "")
	held := e.heldLink(1, "WEBDL-2160p", "Show.S01E01.2160p.WEB-DL.H.265-OLD", 201)
	pack := "Show.S01.2160p.BluRay.REMUX-PACK"
	e.release(pack, map[string]float64{"Show.S01E01.1080p.WEB-DL-FILE.mkv": 2700, "Show.S01E02.2160p.BluRay.REMUX-PACK.mkv": 2700})
	e.videoSize(pack, 1920, 1080)
	e.probes[filepath.Join(e.mount, "__all__", pack, "Show.S01E02.2160p.BluRay.REMUX-PACK.mkv")].Video.Width = 3840
	e.probes[filepath.Join(e.mount, "__all__", pack, "Show.S01E02.2160p.BluRay.REMUX-PACK.mkv")].Video.Height = 2160
	e.seriesDownload(1, 2, 20, hashA, pack, "[201, 202]", nil)
	e.exec(`UPDATE download SET quality = 'Bluray-2160p Remux' WHERE id = 1`)
	plan, err := e.lib.Import(e.ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if why := skipped(plan)["Show.S01E01.1080p.WEB-DL-FILE.mkv"]; !strings.Contains(why, "Existing quality: WEBDL-2160p. New Quality WEBDL-1080p.") {
		t.Errorf("E01 %q (held %s)", why, held)
	}
	if eps := linkedEpisodes(plan); len(eps) != 1 || eps[0] != 202 {
		t.Errorf("linked %v", eps)
	}
}

// {Release Hash} is the file's own hash (Sonarr's AggregateReleaseHash).
func TestReleaseHashToken(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["tv"].Naming.Anime = "{Series Title} - S{season:00}E{episode:00} [{Release Hash}]"
	e.exec(`UPDATE title SET series_type = 'anime' WHERE id = 2`)
	e.exec(`INSERT INTO episode_number (episode_id, title_id, scheme, number, source) VALUES
		(201, 2, 'absolute_tvdb', 1, 'tvdb'), (202, 2, 'absolute_tvdb', 2, 'tvdb')`)
	rel := "[Group] Show (BD 1080p)"
	e.release(rel, map[string]float64{"[Group] Show - 01 (BD 1080p) [ABCD1234].mkv": 2700,
		"[Group] Show - 02 (BD 1080p) [EEEE5678].mkv": 2700})
	e.videoSize(rel, 1920, 1080)
	e.seriesDownload(1, 2, 20, hashA, rel, "[201, 202]", nil)
	plan, err := e.lib.Import(e.ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	got := e.links(plan)
	if !strings.Contains(got, "Show - S01E01 [ABCD1234].mkv") || !strings.Contains(got, "Show - S01E02 [EEEE5678].mkv") {
		t.Errorf("links\n%s\nunmatched %v %v", got, plan.Unmatched, plan.Unresolved)
	}
}

// Movies: the picked file's own quality (Radarr's MovieImport aggregation).
func TestMovieFileIsWhatItIs(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	rel := "Amelie.2001.1080p.WEB-DL.DDP5.1.H.264-GRP"
	e.release(rel, map[string]float64{"Amelie.2001.720p.WEB-DL.H.264-FILE.mkv": 7000})
	e.videoSize(rel, 1280, 720)
	e.download(1, "movie-remux", 1, 10, rel, rel, "[]")
	plan, err := e.lib.Import(e.ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 || plan.Links[0].Quality != "WEBDL-720p" || !strings.Contains(plan.Links[0].Path, "[WEBDL-720p]") {
		t.Errorf("plan %+v", plan.Links)
	}
}
