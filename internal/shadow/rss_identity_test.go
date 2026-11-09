package shadow

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestRSSYearlessMovieNeedsUniqueIdentity(t *testing.T) {
	t.Parallel()
	for _, release := range []struct {
		name, imdb string
		decisions  int
	}{
		{"Alien.Romulus.German.DL.2160p.WEB-DL-FLUX", "", 0},
		{alienWebRel, "", 2},
		{"Alien.Romulus.German.DL.2160p.WEB-DL-FLUX", alien.imdb, 2},
	} {
		t.Run(fmt.Sprintf("%s/%s", release.name, release.imdb), func(t *testing.T) {
			e := newEnv(t, false)
			if _, err := e.r.Import(e.ctx); err != nil {
				t.Fatal(err)
			}
			for _, sql := range []string{
				`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES (999,'movie','Alien: Romulus',1980,999,0,0)`,
				`INSERT INTO title_release_date (title_id,source,type,date) VALUES (999,'fixture','digital','1980-01-01')`,
				`INSERT INTO version (title_id,policy_key,state,created_at,updated_at) VALUES (999,'movie-web','wanted',0,0)`,
			} {
				if _, err := e.st.Writer().Exec(sql); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := e.r.refresh(e.ctx); err != nil {
				t.Fatal(err)
			}
			e.prow.setRSS(item{title: release.name, imdb: release.imdb, hash: hAlienWeb, size: 20e9, pub: t0})
			sum, err := e.r.RSS(e.ctx)
			if err != nil || sum.Decided != release.decisions {
				t.Fatalf("rss %+v %v, want %d decisions", sum, err, release.decisions)
			}
		})
	}
}

func TestRSSAnimeEpisodesCannotBecomeMovies(t *testing.T) {
	t.Parallel()
	for _, title := range []string{
		"[SubsPlease] Alien Romulus - 05 (2160p) [ABCD1234].mkv",
		"[SubsPlease] Alien Romulus (2024) - 05 (2160p) [ABCD1234].mkv",
		"Alien.Romulus.2024.S01E05.2160p.WEB-DL-FLUX",
		"Alien.Romulus.2024.S01.2160p.WEB-DL-FLUX",
		"[Group] Alien Romulus (2024) [2160p WEB-DL][Batch]",
	} {
		t.Run(title, func(t *testing.T) {
			e := newEnv(t, false)
			if _, err := e.r.Import(e.ctx); err != nil {
				t.Fatal(err)
			}
			ts, err := e.r.titles(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			cands, err := e.r.candidates(e.ctx, ts)
			if err != nil {
				t.Fatal(err)
			}
			byClean := map[string][]int64{"alienromulus": {}}
			for id, tt := range ts {
				if strings.Contains(tt.Name, "Romulus") {
					byClean["alienromulus"] = append(byClean["alienromulus"], id)
				}
			}
			sum := &RSSSummary{remember: true}
			rel := indexer.Release{Title: title, InfoHash: hAlienWeb, Size: 20e9, Indexer: "prowlarr", Privacy: policy.PrivacyPublic, Categories: []int{5070}, IMDb: alien.imdb}
			if err := e.r.decideItem(e.ctx, rel, cands, byClean, decide.NewLibrary(ts), sum); err != nil {
				t.Fatal(err)
			}
			if sum.Decided != 0 || sum.Picks != 0 {
				t.Fatalf("anime episode reached movie decision: %+v", sum)
			}
		})
	}
}

func TestRSSArrFileAtQualityCutoffRemainsACandidate(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`UPDATE version SET state = 'cutoff_met' WHERE policy_key = 'movie-web'`); err != nil {
		t.Fatal(err)
	}
	ts, err := e.r.titles(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	cands, err := e.r.candidates(e.ctx, ts)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, cs := range cands {
		for _, c := range cs {
			if c.version.Key == "movie-web" {
				n++
			}
		}
	}
	if n == 0 {
		t.Fatal("quality-only arr cutoff excludes every unadopted movie from score upgrades")
	}
}

func TestRSSAnimeMovieRemainsEligible(t *testing.T) {
	t.Parallel()
	for _, title := range []string{"Akira.1988.1080p.BluRay-GRP", "Your.Name.2016.1080p.BluRay-GRP", "Evangelion.1.11.2007.1080p.BluRay-GRP", "Spirited.Away.2001.1080p.BluRay-GRP"} {
		if animeEpisodeItem(context.Background(), indexer.Release{Title: title, Categories: []int{5070}}) {
			t.Errorf("anime movie classified as episode: %s", title)
		}
	}
	e := newEnv(t, false)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	ts, err := e.r.titles(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	cands, err := e.r.candidates(e.ctx, ts)
	if err != nil {
		t.Fatal(err)
	}
	sum := &RSSSummary{remember: true}
	rel := indexer.Release{Title: alienWebRel, IMDb: alien.imdb, Indexer: "prowlarr", Categories: []int{5070}, InfoHash: hAlienWeb, Size: 20e9, Privacy: policy.PrivacyPublic}
	if err := e.r.decideItem(e.ctx, rel, cands, nil, decide.NewLibrary(ts), sum); err != nil {
		t.Fatal(err)
	}
	if sum.Decided != 2 {
		t.Fatalf("anime movie lost its movie decisions: %+v", sum)
	}
}
