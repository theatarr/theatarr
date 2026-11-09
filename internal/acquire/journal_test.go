package acquire

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/ceiling"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/router"
)

// A search records what it decided between for the grab journal (journal_decision), linked by the route source.
func TestSearchRecordsJournalDecision(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	search := e.a.search
	e.a.search = func(ctx context.Context, title decide.Title, v decide.Version, existing *decide.ExistingFile, ss *decide.SeriesSearch) (*router.Searched, error) {
		out, err := search(ctx, title, v, existing, ss)
		if err != nil {
			return nil, err
		}
		d := out.Ranked[0]
		d.MatchType = decide.MatchTitle
		d.Quality = "WEBDL-2160p"
		d.Release = title.Name + ".2160p.WEB-DL"
		out.Releases = map[*decide.Decision]indexer.Release{d: {Indexer: "pub"}}
		out.Coverage = []ceiling.Coverage{{Indexer: "pub", Kind: "torznab", Season: -1, Complete: true}}
		return out, nil
	}
	if _, _, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"movie"}, TitleIDs: []int64{1}}); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM journal_decision WHERE source = ? AND run_id = 1 AND version_key = 'movie'
		AND results = 1`, Source(1)); n != 1 {
		t.Fatalf("journal_decision rows %d", n)
	}
	if n := e.count("SELECT COUNT(*) FROM ceiling_search WHERE title_id=1 AND version_key='movie' AND season=-1"); n != 1 {
		t.Fatalf("ceiling searches %d", n)
	}
	if n := e.count("SELECT COUNT(*) FROM ceiling_search_indexer WHERE source_kind='torznab' AND complete=1"); n != 1 {
		t.Fatalf("indexer coverage %d", n)
	}
	if n := e.count("SELECT COUNT(*) FROM source_sighting WHERE title_id=1 AND source_kind='torznab'"); n == 0 {
		t.Fatal("search candidates not recorded")
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE source = ?`, Source(1)); n != 1 {
		t.Fatalf("downloads %d", n)
	}
}

func TestInteractiveEmptySearchCeilingCoverage(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false, nil)
	e.a.search = func(context.Context, decide.Title, decide.Version, *decide.ExistingFile, *decide.SeriesSearch) (*router.Searched, error) {
		return &router.Searched{Coverage: []ceiling.Coverage{{Indexer: "pub", Kind: "torznab", Season: -1, Complete: true}}}, nil
	}
	if _, err := e.a.Interactive(e.ctx, 1, "movie", nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := e.count("SELECT COUNT(*) FROM ceiling_search_indexer WHERE complete=1"); n != 1 {
		t.Fatalf("empty search coverage %d", n)
	}
	if n := e.count("SELECT COUNT(*) FROM download"); n != 0 {
		t.Fatalf("interactive evidence routed: %d", n)
	}
}
