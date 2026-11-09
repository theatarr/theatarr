package decide

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// A legacy membership (stored before upstream facts were kept) is not a current member under a prefer-on
// version: it gives a held file no protection, an incoming release no preference, and is no curated alternative. A
// prefer-off version's file keeps the standing it was stored with.
func TestLegacyMembershipGivesNoStanding(t *testing.T) {
	t.Parallel()
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	st := linkedDB(t)
	seadexExec(t, st, `INSERT INTO title(id,kind,name,tmdb_id,created_at,updated_at) VALUES(1,'movie','X',1,0,0)`)
	withFacts := factsSnapshot(indexer.SeaDexSnapshot{AniList: 1, Torrents: []indexer.SeaDexStanding{{InfoHash: b, Preference: 1}}})
	seadexSnapshotRow(t, st, indexer.SeaDexSnapshot{AniList: 1, Targets: []indexer.AniListTarget{{TMDb: 1}},
		Torrents: append([]indexer.SeaDexStanding{{InfoHash: a, IsBest: true, Preference: 3}}, withFacts.Torrents...)})
	pinned, err := WithStoredSeaDex(context.Background(), st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	on := WithSeaDexTitle(WithSeaDexPolicy(pinned, nil, &policy.VersionPolicy{PreferSeaDex: true}), 1)
	held := &ExistingFile{InfoHash: a, SeaDexPreference: 3, SeaDex: format.SeaDexBest}
	if err := ResolveSeaDexFiles(on, st.Reader(), []*ExistingFile{held}); err != nil {
		t.Fatal(err)
	}
	if held.SeaDexPreference != 0 || held.SeaDex != format.SeaDexNone {
		t.Fatalf("legacy membership protected the held file: %+v", held)
	}
	for _, alt := range held.SeaDexAlternatives {
		if alt.InfoHash == a {
			t.Fatalf("legacy membership offered as a curated alternative: %+v", held.SeaDexAlternatives)
		}
	}
	r := RefreshSeaDexRelease(on, indexer.Release{InfoHash: a, Flags: []string{indexer.FlagSeaDex, indexer.FlagSeaDexBest, indexer.FlagSeaDexPreferred}})
	if indexer.SeaDexPreference(&r) != 0 || len(r.Flags) != 0 {
		t.Fatalf("legacy membership gave an incoming release preference: %+v", r)
	}
	// The member with facts keeps its standing.
	if r := RefreshSeaDexRelease(on, indexer.Release{InfoHash: b}); indexer.SeaDexPreference(&r) == 0 {
		t.Fatalf("member with facts lost its standing: %+v", r)
	}
	off := WithSeaDexTitle(WithSeaDexPolicy(pinned, nil, &policy.VersionPolicy{}), 1)
	kept := &ExistingFile{InfoHash: a, SeaDexPreference: 3, SeaDex: format.SeaDexBest}
	if err := ResolveSeaDexFiles(off, st.Reader(), []*ExistingFile{kept}); err != nil || kept.SeaDexPreference != 3 {
		t.Fatalf("prefer-off file changed: %+v %v", kept, err)
	}
}
