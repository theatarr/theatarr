package router

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

type fakeEnqueuer struct{ reqs []Request }

func (f *fakeEnqueuer) Enqueue(_ context.Context, req Request, _ time.Time) (int64, error) {
	f.reqs = append(f.reqs, req)
	return int64(len(f.reqs)), nil
}

type offerEnv struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	off    *Offerer
	enq    *fakeEnqueuer
	now    time.Time
	asked  []string
	accept func(release, version string) []decide.Rejection
}

func newOfferEnv(t *testing.T) *offerEnv {
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
	f := false
	tr := true
	pol := &policy.Policy{
		Versions: map[string]*policy.VersionPolicy{
			"movie-remux": {Mode: policy.ModeActive, Grab: true, Kind: policy.KindMovie, Anime: &f},
			"movie-web":   {Mode: policy.ModeActive, Grab: true, Kind: policy.KindMovie, Anime: &f},
			"movie-extra": {Mode: policy.ModeShadow, Kind: policy.KindMovie},
			"anime-movie": {Mode: policy.ModeActive, Grab: true, Kind: policy.KindMovie, Anime: &tr},
			"tv":          {Mode: policy.ModeActive, Grab: true, Kind: policy.KindSeries},
		},
		Indexers: map[string]*policy.Indexer{"dmm": {Kind: "dmm", Privacy: policy.PrivacyPublic}},
		// Grabbing on: an active target's accepted offer is routed (with it off it is planned and retried).
		Router: &policy.Router{Enabled: true},
	}
	e := &offerEnv{t: t, ctx: ctx, st: st, enq: &fakeEnqueuer{}, now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	e.accept = func(release, version string) []decide.Rejection {
		if strings.Contains(release, "REMUX") && version == "movie-web" {
			return []decide.Rejection{{Reason: "quality", Message: "Remux-2160p is not wanted in profile"}}
		}
		return nil
	}
	e.off = &Offerer{Store: st, Policy: pol, Router: e.enq, Now: func() time.Time { return e.now },
		Decide: func(_ context.Context, rel decide.Release, t decide.Title, v decide.Version, _ *decide.ExistingFile) (*decide.Decision, error) {
			e.asked = append(e.asked, v.Key)
			if rel.Indexer == "" || rel.InfoHash == "" || t.ID == 0 || v.ID == 0 {
				e.t.Errorf("decide got %+v %+v %+v", rel.Release, t, v)
			}
			rej := e.accept(rel.Title, v.Key)
			return &decide.Decision{Release: rel.Title, Version: v.Key, Accepted: len(rej) == 0, Rejections: rej,
				Quality: "WEBDL-2160p", Score: 1700}, nil
		}}
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES (1, 'movie', 'Movie', 2001, 10, 0, 0)`,
		`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES (2, 'movie', 'Other', 2002, 20, 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (10, 1, 'movie-remux', 0, 0),
			(11, 1, 'movie-web', 0, 0), (12, 1, 'movie-extra', 0, 0), (13, 1, 'anime-movie', 0, 0),
			(20, 2, 'movie-remux', 0, 0)`,
	} {
		e.exec(q)
	}
	return e
}

func (e *offerEnv) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *offerEnv) download(id, title, version int64, key, hash, release, state string) {
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, size_bytes,
		state, created_at, updated_at, imported_at) VALUES (?, ?, ?, ?, ?, ?, 'dmm', 1000, ?, 0, 0, 1)`,
		id, title, version, key, hash, release, state)
}

func (e *offerEnv) outcome(version string, title int64, hash string) string {
	var s string
	_ = e.st.Reader().QueryRow(`SELECT outcome FROM router_offer WHERE version_key = ? AND title_id = ? AND info_hash = ?`,
		version, title, hash).Scan(&s)
	return s
}

func byVersion(rs []OfferResult) map[string]OfferResult {
	m := map[string]OfferResult{}
	for _, r := range rs {
		m[r.Version] = r
	}
	return m
}

func TestOfferAcceptedAndRemembered(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	h := strings.Repeat("a", 40)
	e.download(1, 1, 10, "movie-remux", strings.ToUpper(h), "Movie.2001.2160p.WEB-DL.DDP5.1.Atmos-GRP", "imported")
	res, err := e.off.Offer(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	m := byVersion(res)
	// Same kind and anime flag only: movie-web and movie-extra (no anime flag), not anime-movie or tv.
	if len(res) != 2 || m["movie-web"].Outcome != OfferRouted || m["movie-extra"].Outcome != OfferPlanned {
		t.Fatalf("results %+v", res)
	}
	if len(e.enq.reqs) != 2 {
		t.Fatalf("enqueued %d", len(e.enq.reqs))
	}
	r := e.enq.reqs[0]
	if r.Version != "movie-extra" && r.Version != "movie-web" || r.Source != "offer" || len(r.Candidates) != 1 ||
		r.Candidates[0].Release.InfoHash != h || r.Candidates[0].Release.Privacy.IsPrivate() || r.Candidates[0].Score != 1700 {
		t.Errorf("request %+v", r)
	}
	if e.outcome("movie-web", 1, h) != OfferRouted || e.outcome("movie-extra", 1, h) != OfferPlanned {
		t.Error("outcomes not recorded")
	}
	// Remembered: nothing is decided or queued again.
	asked := len(e.asked)
	if res, err := e.off.Offer(e.ctx, 1, false); err != nil || len(res) != 0 || len(e.asked) != asked || len(e.enq.reqs) != 2 {
		t.Errorf("second offer %+v %v asked %d", res, err, len(e.asked))
	}
}

func TestOfferDeclinedSameAndDryRun(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	h := strings.Repeat("b", 40)
	e.download(1, 1, 10, "movie-remux", h, "Movie.2001.2160p.BluRay.REMUX.TrueHD-GRP", "imported")
	// movie-extra already has this release in flight (its own download of the same hash): same.
	e.download(2, 1, 12, "movie-extra", h, "Movie.2001.2160p.BluRay.REMUX.TrueHD-GRP", "ready")
	// A dry run decides and records nothing.
	res, err := e.off.Offer(e.ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	m := byVersion(res)
	if m["movie-web"].Outcome != OfferDeclined || !strings.Contains(strings.Join(m["movie-web"].Reasons, ""), "not wanted") ||
		m["movie-extra"].Outcome != OfferSame {
		t.Fatalf("dry run %+v", res)
	}
	if e.outcome("movie-web", 1, h) != "" || len(e.enq.reqs) != 0 {
		t.Error("a dry run recorded or queued")
	}
	if _, err := e.off.Offer(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if e.outcome("movie-web", 1, h) != OfferDeclined || e.outcome("movie-extra", 1, h) != OfferSame || len(e.enq.reqs) != 0 {
		t.Errorf("outcomes %q %q, queued %d", e.outcome("movie-web", 1, h), e.outcome("movie-extra", 1, h), len(e.enq.reqs))
	}
	// A download that is not imported is not offered.
	if res, _ := e.off.Offer(e.ctx, 2, false); len(res) != 0 {
		t.Errorf("not imported: %+v", res)
	}
}

func TestOfferPendingRetriesThenGivesUp(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	h := strings.Repeat("c", 40)
	// Title 2 has only movie-remux: the others are missing (missing: the import list adds it later).
	e.download(1, 2, 20, "movie-remux", h, "Other.2002.2160p.WEB-DL-GRP", "imported")
	res, err := e.off.Offer(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if m := byVersion(res); m["movie-web"].Outcome != OfferPending || e.outcome("movie-web", 2, h) != OfferPending {
		t.Fatalf("results %+v", res)
	}
	// The version appears: the retry routes it.
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (21, 2, 'movie-web', 0, 0)`)
	e.now = e.now.Add(OfferRetryEvery)
	res, _ = e.off.Offer(e.ctx, 1, false)
	if m := byVersion(res); m["movie-web"].Outcome != OfferRouted {
		t.Errorf("after the version appeared %+v", res)
	}
	// movie-extra never appears: after six hours it gives up.
	e.now = e.now.Add(OfferRetryFor)
	res, _ = e.off.Offer(e.ctx, 1, false)
	if m := byVersion(res); m["movie-extra"].Outcome != OfferGaveUp || e.outcome("movie-extra", 2, h) != OfferGaveUp {
		t.Errorf("after six hours %+v", res)
	}
}

func TestOfferSweep(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	e.download(1, 1, 10, "movie-remux", strings.Repeat("d", 40), "Movie.2001.2160p.WEB-DL-GRP", "imported")
	e.download(2, 2, 20, "movie-remux", strings.Repeat("e", 40), "Other.2002.2160p.BluRay.REMUX-GRP", "imported")
	e.download(3, 2, 20, "movie-remux", strings.Repeat("f", 40), "Other.2002.1080p.WEB-DL-GRP", "failed")
	sr, err := e.off.Sweep(e.ctx, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Download 1 to movie-web and movie-extra; download 2 to both, missing on title 2.
	if sr.Downloads != 2 || sr.Outcomes[OfferRouted] != 1 || sr.Outcomes[OfferPlanned] != 1 || sr.Outcomes[OfferPending] != 2 {
		t.Errorf("sweep %+v", sr)
	}
	if sr, _ := e.off.Sweep(e.ctx, false, 1); sr.Downloads != 1 {
		t.Errorf("limit: %+v", sr)
	}
}

func TestOfferAndSweepUseRuntimeCapturedAtStart(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	base := e.off.Policy
	current := int64(1)
	var observed []int64
	decision := DecideFunc(func(ctx context.Context, rel decide.Release, _ decide.Title, v decide.Version, _ *decide.ExistingFile) (*decide.Decision, error) {
		observed = append(observed, settings.ContextGeneration(ctx))
		current = 2 // a settings publication during the decision must not change this operation
		return &decide.Decision{Release: rel.Title, Version: v.Key, Accepted: true, Quality: "WEBDL-2160p"}, nil
	})
	e.off.CaptureRuntime = func(ctx context.Context) context.Context { return settings.WithGeneration(ctx, current) }
	e.off.Runtime = func(ctx context.Context) (*policy.Policy, DecideFunc, DecideSeriesFunc, error) {
		gen := settings.ContextGeneration(ctx)
		active := *base
		active.Versions = make(map[string]*policy.VersionPolicy, len(base.Versions))
		for key, value := range base.Versions {
			active.Versions[key] = value
		}
		if gen == 1 {
			delete(active.Versions, "movie-extra")
		}
		return &active, decision, nil, nil
	}
	e.off.Decide = decision
	hash := strings.Repeat("1", 40)
	e.download(1, 1, 10, "movie-remux", hash, "Movie.2001.2160p.WEB-DL-GRP", "imported")
	res, err := e.off.Offer(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Version != "movie-web" || len(observed) != 1 || observed[0] != 1 {
		t.Fatalf("single offer used the wrong runtime: results=%+v generations=%v", res, observed)
	}

	current = 1
	observed = nil
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (21, 2, 'movie-web', 0, 0)`)
	e.download(2, 1, 10, "movie-remux", strings.Repeat("2", 40), "Movie.2001.2160p.WEB-DL-GRP", "imported")
	e.download(3, 2, 20, "movie-remux", strings.Repeat("3", 40), "Other.2002.2160p.WEB-DL-GRP", "imported")
	if _, err := e.off.Sweep(e.ctx, true, 0); err != nil {
		t.Fatal(err)
	}
	if len(observed) == 0 {
		t.Fatal("sweep did not decide any offers")
	}
	for _, generation := range observed {
		if generation != 1 {
			t.Fatalf("sweep changed runtime during the pass: generations %v", observed)
		}
	}
}

func TestOfferSeriesSeasonPack(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	e.off.Policy.Versions["tv-web"] = &policy.VersionPolicy{Mode: policy.ModeActive, Grab: true, Kind: policy.KindSeries}
	e.exec(`INSERT INTO title (id, kind, name, year, tvdb_id, series_type, created_at, updated_at)
		VALUES (3, 'series', 'Show', 2020, 99, 'standard', 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES
		(30, 3, 'tv', 0, 0), (31, 3, 'tv-web', 0, 0)`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, monitored, created_at, updated_at) VALUES
		(301, 3, 1, 1, 'One', 1, 0, 0), (302, 3, 1, 2, 'Two', 1, 0, 0)`)
	h := strings.Repeat("1", 40)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		size_bytes, episodes, state, created_at, updated_at, imported_at) VALUES
		(3, 3, 30, 'tv', ?, 'Show.S01.2160p.WEB-DL-GRP', 'dmm', 'WEBDL-2160p', 1000, '[301,302]',
		'imported', 0, 0, 1)`, h)
	e.off.DecideSeries = func(_ context.Context, _ decide.Release, title decide.Title, version decide.Version,
		search *decide.SeriesSearch) (*decide.Decision, error) {
		if title.ID != 3 || version.Key != "tv-web" || search == nil || search.Season != 1 ||
			(len(search.Episodes) == 2 && search.Kind != profile.SearchSeason) ||
			(len(search.Episodes) == 1 && (search.Kind != profile.SearchSingleEpisode || search.Episode != 1)) {
			t.Errorf("series offer criteria: title=%+v version=%+v search=%+v", title, version, search)
		}
		return &decide.Decision{Accepted: true, Quality: "WEBDL-2160p", Score: 100,
			EpisodeIDs: append([]int64(nil), search.Episodes...)}, nil
	}
	res, err := e.off.Offer(e.ctx, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Version != "tv-web" || res[0].Outcome != OfferRouted || len(e.enq.reqs) != 1 {
		t.Fatalf("series offer %+v, routes %+v", res, e.enq.reqs)
	}
	if got := e.enq.reqs[0].Candidates[0].Episodes; len(got) != 2 || got[0] != 301 || got[1] != 302 {
		t.Fatalf("offered episodes %v", got)
	}
	h = strings.Repeat("2", 40)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		size_bytes, episodes, state, created_at, updated_at, imported_at) VALUES
		(4, 3, 30, 'tv', ?, 'Show.S01E01.2160p.WEB-DL-GRP', 'dmm', 'WEBDL-2160p', 1000, '[301]',
		'imported', 0, 0, 1)`, h)
	res, err = e.off.Offer(e.ctx, 4, false)
	if err != nil || len(res) != 1 || res[0].Outcome != OfferRouted || len(e.enq.reqs) != 2 {
		t.Fatalf("episode offer %+v, routes %+v, error %v", res, e.enq.reqs, err)
	}
	if got := e.enq.reqs[1].Candidates[0].Episodes; len(got) != 1 || got[0] != 301 {
		t.Fatalf("offered episode ids %v", got)
	}
}

// A scene-numbered episode is offered in TVDB numbering and judged in each numbering; the scene criteria (season 2,
// episode 5) accept the scene-named release the TVDB ones reject.
func TestOfferSeriesSceneNumbering(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	e.off.Policy.Versions["tv-web"] = &policy.VersionPolicy{Mode: policy.ModeActive, Grab: true, Kind: policy.KindSeries}
	e.exec(`INSERT INTO title (id, kind, name, year, tvdb_id, series_type, use_scene_numbering, created_at, updated_at)
		VALUES (3, 'series', 'Show', 2020, 99, 'standard', 1, 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES
		(30, 3, 'tv', 0, 0), (31, 3, 'tv-web', 0, 0)`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, monitored, created_at, updated_at) VALUES
		(303, 3, 1, 3, 'Three', 1, 0, 0)`)
	e.exec(`INSERT INTO episode_number (episode_id, title_id, scheme, season, number) VALUES (303, 3, 'scene', 2, 5)`)
	h := strings.Repeat("3", 40)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		size_bytes, episodes, state, created_at, updated_at, imported_at) VALUES
		(5, 3, 30, 'tv', ?, 'Show.S02E05.2160p.WEB-DL-GRP', 'dmm', 'WEBDL-2160p', 1000, '[303]', 'imported', 0, 0, 1)`, h)
	var asked []string
	e.off.DecideSeries = func(_ context.Context, _ decide.Release, _ decide.Title, _ decide.Version,
		search *decide.SeriesSearch) (*decide.Decision, error) {
		asked = append(asked, fmt.Sprintf("%s s%d e%d %v", search.Kind, search.Season, search.Episode, search.Episodes))
		d := &decide.Decision{Accepted: search.Season == 2 && search.Episode == 5, Quality: "WEBDL-2160p",
			EpisodeIDs: []int64{303}}
		if !d.Accepted {
			d.Rejections = []decide.Rejection{{Reason: "WrongSeason", Message: "Wrong season"}}
		}
		return d, nil
	}
	res, err := e.off.Offer(e.ctx, 5, false)
	if err != nil || len(res) != 1 || res[0].Outcome != OfferRouted {
		t.Fatalf("offer %+v, error %v", res, err)
	}
	if want := []string{"episode s1 e3 [303]", "episode s2 e5 [303]"}; !slices.Equal(asked, want) {
		t.Errorf("criteria %q, want %q", asked, want)
	}
}
