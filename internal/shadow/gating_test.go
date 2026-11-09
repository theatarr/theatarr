package shadow

import (
	"fmt"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/store"
)

func (e *env) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.st.Writer().ExecContext(e.ctx, q, args...); err != nil {
		t.Fatal(err)
	}
}

func TestRSSReconsidersUnavailableMovie(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE title_release_date SET date = '2099-01-01' WHERE title_id IN (SELECT id FROM title WHERE tmdb_id = ?)`, alien.tmdb)
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0})
	for range 2 {
		if sum, err := e.r.RSS(e.ctx); err != nil || sum.Picks != 0 || sum.New != 1 {
			t.Fatalf("unavailable RSS: %+v %v", sum, err)
		}
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_rss_seen`); n != 0 {
		t.Fatalf("unavailable item remembered: %d", n)
	}
	e.exec(t, `UPDATE title_release_date SET date = '2024-01-01' WHERE title_id IN (SELECT id FROM title WHERE tmdb_id = ?)`, alien.tmdb)
	if sum, err := e.r.RSS(e.ctx); err != nil || sum.Picks != 2 {
		t.Fatalf("available RSS: %+v %v", sum, err)
	}
	if sum, err := e.r.RSS(e.ctx); err != nil || sum.New != 0 {
		t.Fatalf("picked RSS: %+v %v", sum, err)
	}
}

func TestRSSReconsidersMovieWhenRuntimeArrives(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE title SET runtime_minutes=NULL WHERE tmdb_id=?`, alien.tmdb)
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0})
	// Ensure this fixture actually requires a runtime-based size check.
	for _, fl := range []string{"movie-remux", "movie-web"} {
		v := e.pol.Versions[fl]
		v.PreferSeaDex = true // the runtime hold is a prefer-on rule
		p := e.eng.Profile(v.Profile, format.Radarr)
		q, _ := profile.QualityByName(format.Radarr, "WEBDL-2160p")
		minimum := 1.0
		p.Engine.Definitions.Set(profile.Definition{Quality: q, MinSize: &minimum})
	}
	for range 2 {
		if sum, err := e.r.RSS(e.ctx); err != nil || sum.Picks != 0 || sum.New != 1 {
			t.Fatalf("unknown runtime RSS: %+v %v", sum, err)
		}
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_rss_seen`); n != 0 {
		t.Fatal("unknown runtime was remembered as a permanent rejection")
	}
	e.exec(t, `UPDATE title SET runtime_minutes=119 WHERE tmdb_id=?`, alien.tmdb)
	if _, err := e.r.refresh(e.ctx); err != nil {
		t.Fatal(err)
	}
	if sum, err := e.r.RSS(e.ctx); err != nil || sum.Picks != 2 {
		t.Fatalf("known runtime RSS: %+v %v", sum, err)
	}
}

func TestRSSReconsidersUnknownTitle(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0})
	if sum, err := e.r.RSS(e.ctx); err != nil || sum.Matched != 0 {
		t.Fatalf("unknown RSS: %+v %v", sum, err)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_rss_seen`); n != 0 {
		t.Fatal("unknown item remembered")
	}
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	if sum, err := e.r.RSS(e.ctx); err != nil || sum.Picks != 2 {
		t.Fatalf("added title RSS: %+v %v", sum, err)
	}
}

func TestRSSCutoffMetSeriesAndEpisodeUpgradeGuard(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	newShow(e.tv)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE version SET state = 'cutoff_met' WHERE policy_key = 'tv'`)
	e.exec(t, `UPDATE version_file SET quality = 'WEBDL-2160p', scene_name = 'The.Show.S01E01.2160p.WEB-DL-GRP' WHERE version_id IN (SELECT id FROM version WHERE policy_key = 'tv')`)
	e.exec(t, `UPDATE episode_version_state SET state = 'cutoff_met' WHERE episode_id IN (SELECT id FROM episode WHERE number = 1)`)
	e.prow.setRSS(item{title: "The.Show.S01E01.2160p.WEB-DL-GRP", hash: hShowPack, size: 8e9, pub: t0, tv: true},
		item{title: "The.Show.S01E03.2160p.WEB-DL-GRP", hash: hShowE03, size: 8e9, pub: t0, tv: true})
	if sum, err := e.r.RSS(e.ctx); err != nil || sum.Decided != 2 || sum.Picks != 1 {
		t.Fatalf("cutoff RSS: %+v %v", sum, err)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_rss_pick WHERE info_hash = ?`, hShowPack); n != 0 {
		t.Fatal("existing episode accepted without an upgrade")
	}
}

func TestRSSTemporarySeasonPackReconsidered(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	newShow(e.tv)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE episode SET air_date_utc_at = ? WHERE number = 3`, store.Millis(t0.Add(48*time.Hour)))
	e.prow.setRSS(item{title: showPack, hash: hShowPack, size: 16e9, pub: t0, tv: true})
	if sum, err := e.r.RSS(e.ctx); err != nil || sum.Picks != 0 {
		t.Fatalf("unaired pack: %+v %v", sum, err)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_rss_seen`); n != 0 {
		t.Fatal("unaired pack remembered")
	}
	e.exec(t, `UPDATE episode SET air_date_utc_at = ? WHERE number = 3`, store.Millis(t0.Add(-time.Hour)))
	if sum, err := e.r.RSS(e.ctx); err != nil || sum.Picks != 1 {
		t.Fatalf("aired pack: %+v %v", sum, err)
	}
}

func TestPermanentRSSReasons(t *testing.T) {
	t.Parallel()
	if permanentRSS(&decide.Decision{Rejections: []decide.Rejection{
		{Reason: "CustomFormatMinimumScore"}, {Reason: "SeaDexQualityPending", Temporary: true},
	}}) {
		t.Fatal("remembered a format score computed before quality metadata was available")
	}
	for _, reason := range []string{"UnknownMovie", "UnknownEpisode", "FullSeasonNotAired", "Availability", "DiskCutoffMet", "SeaDexQualityPending"} {
		if permanentRSS(&decide.Decision{Rejections: []decide.Rejection{{Reason: reason}}}) {
			t.Errorf("remembered changing reason %s", reason)
		}
	}
	for _, reason := range []string{"QualityNotWanted", "CustomFormatMinimumScore", "Blocklisted"} {
		if !permanentRSS(&decide.Decision{Rejections: []decide.Rejection{{Reason: reason}}}) {
			t.Errorf("did not remember %s", reason)
		}
	}
}

func TestRSSDecisionCap(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	var items []item
	for i := range 1150 {
		items = append(items, item{title: "Unknown.2026.2160p.WEB-DL-GRP", hash: fmt.Sprintf("%040x", i+1), size: 8e9, pub: t0})
	}
	// No cursor yet: one page, as the arrs read without a last release.
	e.prow.setRSS(items[1000:]...)
	if sum, err := e.r.RSS(e.ctx); err != nil || sum.Items != 100 {
		t.Fatalf("first RSS: %+v %v", sum, err)
	}
	// 1,000 new items ahead of the cursor: ten pages, then the catch-up stops short of it.
	e.prow.setRSS(items...)
	if sum, err := e.r.RSS(e.ctx); err != nil || sum.Items != 1000 || sum.New != 1000 || sum.Decided != 0 {
		t.Fatalf("bounded RSS: %+v %v", sum, err)
	}
}
