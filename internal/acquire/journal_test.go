package acquire

import (
	"context"
	"testing"

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
	if n := e.count(`SELECT COUNT(*) FROM download WHERE source = ?`, Source(1)); n != 1 {
		t.Fatalf("downloads %d", n)
	}
}
