package indexer

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
)

// An entry without a relation list (no "trs" field, or null) gives no evidence that its expanded
// records are its own: none becomes a release (search or RSS), none is stored, and a note says why.
func TestMissingRelationsYieldNoReleases(t *testing.T) {
	for _, relation := range []string{"", `"trs":null,`, `"trs":["unknown"],`} {
		for _, q := range []Query{
			{Type: QueryMovie, AniList: 47, Title: "Wanted Film", Year: 2020},
			{Type: QueryRSS},
		} {
			t.Run(fmt.Sprintf("relation=%q/%s", relation, q.Type), func(t *testing.T) {
				n := newNet(t)
				n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":47,`+relation+`"expand":{"trs":[{"id":"unknown","infoHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","tracker":"Nyaa","isBest":true,"files":[{"name":"Other Film BD 1080p.mkv","length":5000000000}]}]}}]}`)
				}))
				h := newHarness(t, n, testPolicy(), false)
				h.svc.SetMetadata(Metadata{Targets: fakeTargets, Names: fakeNames{}})
				res, err := h.svc.Search(context.Background(), "seadex", q)
				if err != nil {
					t.Fatal(err)
				}
				if relation == `"trs":["unknown"],` {
					// Control: the same record listed by its entry is offered.
					if len(res.Releases) != 1 {
						t.Fatalf("listed record not offered: %+v", res)
					}
					return
				}
				if len(res.Releases) != 0 {
					t.Fatalf("unverified expansion offered as %#v", res.Releases[0])
				}
				if len(res.SeaDex) != 0 {
					t.Fatalf("standing taken from an entry without relations: %+v", res.SeaDex)
				}
				if !strings.Contains(strings.Join(res.Notes, "; "), "without a relation list") {
					t.Fatalf("no note: %v", res.Notes)
				}
			})
		}
	}
}

// Relations that are not a set of unique ids matching the expansion both ways never confirm an
// omission, so two such answers 13 hours apart do not retire a previously confirmed torrent.
func TestNonMatchingRelationsDoNotRetire(t *testing.T) {
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	own := `{"id":"own","infoHash":"` + a + `","tracker":"Nyaa","files":[{"name":"Film BD 1080p.mkv","length":5000000000}]}`
	for name, entry := range map[string]string{
		"duplicate_relation": `"trs":["own","own"],"expand":{"trs":[` + own + `,{"id":"unrelated","infoHash":"` + b + `","tracker":"Nyaa","files":[{"name":"Other Film BD 1080p.mkv","length":5000000000}]}]}`,
		"duplicate_both":     `"trs":["own","own"],"expand":{"trs":[` + own + `,` + own + `]}`,
	} {
		t.Run(name, func(t *testing.T) {
			n := newNet(t)
			n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":1,`+entry+`}]}`)
			}))
			h := newHarness(t, n, testPolicy(), false, func(_ *gateway.Options, o *Options) { o.CacheTTL = -1 })
			st := openStore(t)
			if _, err := st.Writer().Exec(`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Film',0,0); INSERT INTO title_external_id VALUES(1,'anilist','1')`); err != nil {
				t.Fatal(err)
			}
			ev := NewEvidence(EvidenceConfig{Store: st, Policy: testPolicy(), Now: h.clock})
			initial := SeaDexSnapshot{AniList: 1, Torrents: []SeaDexStanding{{RecordID: "own", InfoHash: a, Facts: &sdTorrent{}}, {RecordID: "old", InfoHash: c, Facts: &sdTorrent{}}}}
			if err := ev.recordSeaDex(context.Background(), []SeaDexSnapshot{initial}); err != nil {
				t.Fatal(err)
			}
			h.svc.SetEvidence(ev)
			h.advance(time.Minute)
			for range 2 {
				if _, err := h.svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1, Title: "Film"}); err != nil {
					t.Fatal(err)
				}
				h.advance(13 * time.Hour)
			}
			for _, v := range storedStanding(t, st.Reader(), "1").Torrents {
				if v.InfoHash == c {
					if v.Preference == 0 || v.MissingSince != 0 {
						t.Fatalf("non-matching relations advanced the old torrent's retirement: %+v", v)
					}
					return
				}
			}
			t.Fatal("old torrent disappeared")
		})
	}
}
