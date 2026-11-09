package library

import (
	"fmt"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// A held link remains a SeaDex reference after its title loses other mappings. The catalog lists the
// link's own saved entry for the title, but that is not coverage evidence, so the saved entry stays the file's scope.
func TestUnmappedHeldLinkUsesSavedEntry(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, member string
		saved, want  int
	}{
		{"listed again", fmt.Sprintf(`{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true}}`, hashA), 1, 3},
		{"confirmed retirement", fmt.Sprintf(`{"info_hash":"%s","preference":0,"missing_since":1}`, hashA), 3, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := importUpEnv(t, policy.ModeActive, "")
			e.pol.Versions["tv"].PreferSeaDex = true
			body := fmt.Sprintf(`{"version":2,"anilist":11,"torrents":[%s]}`, tc.member)
			e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,?,0,253402214400000)`, body)
			e.heldLink(1, "WEBDL-2160p", "Show.S01E01.2160p.WEB-DL-GRP", 201)
			meta := fmt.Sprintf(`{"Revision":1,"InfoHash":"%s","SeaDexPreference":%d,"SeaDex":1,"SeaDexEntry":11}`, hashA, tc.saved)
			e.exec(`UPDATE link SET file_metadata=? WHERE id=1`, meta)
			ctx, err := decide.WithStoredSeaDex(e.ctx, e.st.Reader())
			if err != nil {
				t.Fatal(err)
			}
			ctx = decide.WithSeaDexPolicy(ctx, e.pol, e.pol.Versions["tv"])
			eps, err := decide.LoadEpisodes(ctx, e.st.Reader(), 2, 20)
			if err != nil {
				t.Fatal(err)
			}
			for _, ep := range eps {
				if ep.ID == 201 {
					if ep.File == nil {
						t.Fatal("missing file")
					}
					if ep.File.SeaDexPreference != tc.want || ep.File.SeaDexEntry != 11 {
						t.Fatalf("loaded rank=%d entry=%d, want rank=%d entry=11", ep.File.SeaDexPreference, ep.File.SeaDexEntry, tc.want)
					}
					return
				}
			}
			t.Fatal("missing episode")
		})
	}
}

// A confirmed retirement of the saved entry's torrent no longer blocks an allowed quality upgrade.
func TestUnmappedRetirementAllowsQualityUpgrade(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, policy.ModeActive, "")
	e.pol.Versions["tv"].PreferSeaDex = true
	body := fmt.Sprintf(`{"version":2,"anilist":11,"torrents":[{"info_hash":"%s","preference":0,"missing_since":1}]}`, hashA)
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,?,0,253402214400000)`, body)
	e.heldLink(1, "WEBDL-2160p", "Show.S01E01.2160p.WEB-DL-GRP", 201)
	meta := fmt.Sprintf(`{"Revision":1,"InfoHash":"%s","SeaDexPreference":3,"SeaDex":2,"SeaDexEntry":11}`, hashA)
	e.exec(`UPDATE link SET file_metadata=? WHERE id=1`, meta)
	ctx, err := decide.WithStoredSeaDex(e.ctx, e.st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	ctx = decide.WithSeaDexPolicy(ctx, e.pol, e.pol.Versions["tv"])
	title, err := decide.LoadTitle(ctx, e.st.Reader(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := decide.LoadSeriesVersion(ctx, e.st.Reader(), &title, 20); err != nil {
		t.Fatal(err)
	}
	rel := indexer.Release{Title: "Show.S01E01.2160p.BluRay.REMUX.HEVC-GRP", InfoHash: strings.Repeat("b", 40), Size: 2700000000, Indexer: "public"}
	v := decide.Version{ID: 20, Key: "tv", Policy: e.pol.Versions["tv"]}
	d, err := e.lib.engine.DecideSearch(ctx, decide.Release{Release: rel}, title, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("accepted=%v, rejections=%+v", d.Accepted, d.Rejections)
	if !d.Accepted {
		t.Fatal("retired held standing blocked an allowed quality upgrade")
	}
}
