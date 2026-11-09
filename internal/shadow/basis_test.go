package shadow

import (
	"testing"
	"time"
)

const (
	hWonkaRemux = "abababababababababababababababababababab"
	hWonkaWeb   = "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
	wonkaRemux  = "Wonka.2023.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR"
	wonkaWeb    = "Wonka.2023.2160p.AMZN.WEB-DL.DDP5.1.H.265-FLUX"
)

// Movies, RSS grabs: the arr's RSS grab of Argylle is decided before our RSS read sees the same release a few
// minutes later; the read links the pick and the row moves to same_rss_pick, with our later search's remux kept as
// search_found_better. A Wonka RSS grab of a WEB-DL when our RSS read had picked a remux before is we_grab_another.
// Reclassify puts back a row stored under the old rules, and changes nothing the second time.
func TestRSSGrabs(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.prow.set(argylle.imdb, item{title: argWeb, hash: hArgWeb, size: 20e9, pub: t0.Add(-72 * time.Hour)})
	e.dmm.set(argylle.imdb, item{title: argRemux, hash: hArgRemux, size: 60e9, pub: t0.Add(-96 * time.Hour)})

	// Our first RSS read: a Wonka remux, a pick for movie-remux.
	e.prow.setRSS(item{title: wonkaRemux, hash: hWonkaRemux, size: 60e9, pub: t0})
	if rs, err := e.r.RSS(e.ctx); err != nil || rs.Picks != 1 {
		t.Fatalf("rss %+v %v", rs, err)
	}

	// The arr grabs both from its RSS.
	e.advance(10 * time.Minute)
	e.zurg.add(grabEvent(2, argylle, t0.Add(5*time.Minute), argWeb, hArgWeb, "WEBDL-2160p", 18, 1700, "Rss"))
	e.zurg.add(grabEvent(3, wonka, t0.Add(8*time.Minute), wonkaWeb, hWonkaWeb, "WEBDL-2160p", 18, 1700, "Rss"))
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	row := func(m movieDef) DiffRow {
		t.Helper()
		rows, _, err := ListDiffs(e.ctx, e.st.Reader(), DiffFilter{TitleID: one[int64](t, e.st,
			`SELECT title_id FROM shadow_grab WHERE instance = 'radarr_zurg' AND release_title LIKE ?`, m.title[:4]+"%")})
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s: %v %v", m.title, rows, err)
		}
		return rows[0]
	}
	if d := row(argylle); d.Bucket != BucketAgree || d.Reason != ReasonRSSAccepted || d.Basis != BasisRSS ||
		d.SearchFoundBetter == nil || d.SearchFoundBetter.Release != argRemux || d.RSSPickID != nil {
		t.Errorf("argylle before our read: %+v", d)
	}
	if d := row(wonka); d.Bucket != BucketAnother || d.Reason != ReasonBetterQuality || d.OurRelease != wonkaRemux ||
		d.RSSPickID == nil || d.SearchFoundBetter != nil {
		t.Errorf("wonka: %+v", d)
	}

	// Our second read sees the arr's Argylle release, 6 minutes after the grab.
	e.advance(time.Minute)
	e.prow.setRSS(item{title: argWeb, hash: hArgWeb, size: 20e9, pub: t0})
	if rs, err := e.r.RSS(e.ctx); err != nil || rs.Picks != 2 { // movie-remux and movie-web
		t.Fatalf("rss %+v %v", rs, err)
	}
	d := row(argylle)
	if d.Bucket != BucketAgree || d.Reason != ReasonSameRSSPick || d.SearchFoundBetter == nil ||
		d.SearchFoundBetter.Release != argRemux || d.SearchFoundBetter.Score != 1950 || d.RSSPickID == nil || d.OurRelease != argWeb {
		t.Errorf("argylle after our read: %+v", d)
	}
	if st := one[string](t, e.st, `SELECT status FROM shadow_rss_pick WHERE id = ?`, *d.RSSPickID); st != "linked" {
		t.Errorf("pick %s", st)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_rss_pick WHERE version_key = 'movie-web' AND status = 'open'`); n != 1 {
		t.Errorf("the movie-web pick (no grab of that version) should stay open: %d", n)
	}
	det, err := LoadDiff(e.ctx, e.st.Reader(), d.ID)
	if err != nil || det.Pick == nil || det.Pick.Release != argWeb || det.Grab.Pick == nil || det.Grab.Pick.Release != argRemux {
		t.Errorf("detail %+v %v", det, err)
	}

	s, err := LoadSummary(e.ctx, e.st.Reader(), e.clock())
	if err != nil || s.SearchFoundBetter != 1 || s.Grabs != 2 || s.Buckets[BucketAgree] != 1 {
		t.Errorf("summary %+v %v", s, err)
	}
	for _, rc := range s.Reasons {
		if rc.Reason == ReasonSameRSSPick && (rc.Count != 1 || rc.SearchFoundBetter != 1) {
			t.Errorf("reason %+v", rc)
		}
	}
	if _, n, err := ListDiffs(e.ctx, e.st.Reader(), DiffFilter{SearchFoundBetter: true}); err != nil || n != 1 {
		t.Errorf("search_found_better filter %d %v", n, err)
	}

	// A row stored under the old rules: search vs the arr's pick.
	if _, err := e.st.Writer().Exec(`UPDATE shadow_diff SET bucket = 'we_grab_another', reason = 'better_quality',
		search_better = NULL WHERE id = ?`, d.ID); err != nil {
		t.Fatal(err)
	}
	sum, err := e.r.Reclassify(e.ctx)
	if err != nil || sum.Grabs != 2 || sum.Changed != 1 || sum.Moves["we_grab_another/better_quality -> agree/same_rss_pick"] != 1 {
		t.Fatalf("reclassify %+v %v", sum, err)
	}
	if d := row(argylle); d.Reason != ReasonSameRSSPick || d.SearchFoundBetter == nil {
		t.Errorf("after reclassify: %+v", d)
	}
	if sum, err := e.r.Reclassify(e.ctx); err != nil || sum.Changed != 0 || sum.Grabs != 2 {
		t.Errorf("again %+v %v", sum, err)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM job`); n != 0 {
		t.Errorf("reclassify enqueued %d jobs", n)
	}
	if n := len(e.net.requests("prowlarr.test/1/api?cat=2000&imdbid=15009428")); n != 1 {
		t.Errorf("searches %d: reclassify must not search", n)
	}
}

// Series: a Sonarr RSS grab of the pack our RSS read picked is same_rss_pick; without a pick of ours it is
// rss_accepted; a hand grab is manual_grab_accepted or manual_grab_rejected, whatever our search found.
func TestSeriesGrabBySource(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, source   string
		release        string
		rss            bool
		bucket, reason string
	}{
		{"RSS, our pick", "Rss", showPack, true, BucketAgree, ReasonSameRSSPick},
		{"RSS, no pick", "Rss", showPack, false, BucketAgree, ReasonRSSAccepted},
		{"hand grab", "InteractiveSearch", showPack, false, BucketAgree, ReasonManualAccepted},
		{"hand grab rejected", "InteractiveSearch", "The.Show.S01.2160p.HDTV.x265-GRP", false, BucketWouldnt, ReasonManualRejected},
		{"search", "Search", showPack, false, BucketAgree, ReasonSameRelease},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, false)
			newShow(e.tv)
			if _, err := e.r.Import(e.ctx); err != nil {
				t.Fatal(err)
			}
			e.prow.shows["1001:1"] = []item{{title: showPack, hash: hShowPack, size: 16e9, pub: t0.Add(-48 * time.Hour), tv: true}}
			if tc.rss {
				e.prow.setRSS(item{title: showPack, hash: hShowPack, size: 16e9, pub: t0, tv: true})
				if rs, err := e.r.RSS(e.ctx); err != nil || rs.Picks != 1 {
					t.Fatalf("rss %+v %v", rs, err)
				}
			}
			e.advance(10 * time.Minute)
			at := t0.Add(5 * time.Minute)
			e.tv.add(showGrab(2, 52, at, tc.release, hShowPack, tc.source),
				showGrab(3, 53, at.Add(time.Second), tc.release, hShowPack, tc.source))
			if _, err := e.r.Import(e.ctx); err != nil {
				t.Fatal(err)
			}
			rows, _, err := ListDiffs(e.ctx, e.st.Reader(), DiffFilter{Version: "tv"})
			if err != nil || len(rows) != 1 {
				t.Fatalf("diffs %v %v", rows, err)
			}
			d := rows[0]
			if d.Bucket != tc.bucket || d.Reason != tc.reason || d.Basis != BasisFor(tc.source) || (d.RSSPickID != nil) != tc.rss {
				t.Errorf("%s/%s basis %s pick %v: %s", d.Bucket, d.Reason, d.Basis, d.RSSPickID, d.Detail)
			}
			if k := one[string](t, e.st, `SELECT kind FROM shadow_diff WHERE id = ?`, d.ID); k != "series" {
				t.Errorf("kind %s", k)
			}
			// A hand grab is listed, but outside every agreement ratio.
			s, err := LoadSummary(e.ctx, e.st.Reader(), e.clock())
			if err != nil {
				t.Fatal(err)
			}
			hand := BasisFor(tc.source) == BasisManual
			var series *KindSummary
			for i := range s.Kinds {
				if s.Kinds[i].Kind == "series" {
					series = &s.Kinds[i]
				}
			}
			switch {
			case hand && (s.Grabs != 0 || s.Agreement != nil || series == nil || series.Grabs != 0 || len(s.Versions) != 0 ||
				s.HandGrabs.Grabs != 1 || s.Buckets[tc.bucket] != 1):
				t.Errorf("hand grab summary %+v kinds %+v", s, s.Kinds)
			case hand && tc.reason == ReasonManualAccepted && (s.HandGrabs.Accepted != 1 || s.HandGrabs.Rejected != 0):
				t.Errorf("hand grabs %+v", s.HandGrabs)
			case hand && tc.reason == ReasonManualRejected && (s.HandGrabs.Accepted != 0 || s.HandGrabs.Rejected != 1):
				t.Errorf("hand grabs %+v", s.HandGrabs)
			case !hand && (s.Grabs != 1 || s.Agreement == nil || s.HandGrabs != HandGrabs{}):
				t.Errorf("summary %+v", s)
			}
		})
	}
}
