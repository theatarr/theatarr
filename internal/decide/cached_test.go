package decide

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// cachedEngine is testEngine with providers rd, ad, tb on movie-remux, in that order.
func cachedEngine(t *testing.T) *Engine {
	t.Helper()
	e := testEngine(t)
	e.Policy.Providers["ad"] = &policy.Provider{Kind: "alldebrid", Order: 2}
	e.Policy.Providers["tb"] = &policy.Provider{Kind: "torbox", Order: 3}
	e.Policy.Versions["movie-remux"].Providers = []string{"rd", "ad", "tb"}
	return e
}

type cachedRel struct {
	label, title, indexer string
	on                    []string
}

// rankCached decides the releases for movie-remux and returns their labels in Rank order (accepted ones only).
func rankCached(t *testing.T, e *Engine, rels []cachedRel) string {
	t.Helper()
	label := map[*Decision]string{}
	var ds []*Decision
	for i, c := range rels {
		ix := c.indexer
		if ix == "" {
			ix = "public"
		}
		r := rel(c.title, ix, 20*gb)
		r.InfoHash = fmt.Sprintf("%040x", i+1)
		r.CachedOn = c.on
		d, err := e.Decide(context.Background(), r, dune, Version{Key: "movie-remux"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !d.Accepted {
			t.Fatalf("%s rejected: %v", c.label, d.Rejections)
		}
		label[d] = c.label
		ds = append(ds, d)
	}
	var got []string
	for _, d := range Rank(ds) {
		got = append(got, label[d])
	}
	return strings.Join(got, ",")
}

const webNTb = "Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1-NTb"

// At equal quality and format score the version's first provider decides, then the number of providers: with rd,
// ad, tb, rd+ad+tb > rd > ad+tb > ad > tb > none.
func TestRankCachedOrdered(t *testing.T) {
	t.Parallel()
	e := cachedEngine(t)
	got := rankCached(t, e, []cachedRel{
		{"none", webNTb, "", nil},
		{"tb", webNTb, "", []string{"tb"}},
		{"ad", webNTb, "", []string{"ad"}},
		{"ad+tb", webNTb, "", []string{"ad", "tb"}},
		{"rd", webNTb, "", []string{"rd"}},
		{"rd+ad+tb", webNTb, "", []string{"rd", "ad", "tb"}},
	})
	if want := "rd+ad+tb,rd,ad+tb,ad,tb,none"; got != want {
		t.Errorf("rank %s, want %s", got, want)
	}
}

// With provider_preference: none only the number of providers counts; equals keep their order.
func TestRankCachedNoPreference(t *testing.T) {
	t.Parallel()
	e := cachedEngine(t)
	e.Policy.Versions["movie-remux"].ProviderPreference = policy.ProviderPreferenceNone
	got := rankCached(t, e, []cachedRel{
		{"none", webNTb, "", nil},
		{"tb", webNTb, "", []string{"tb"}},
		{"rd", webNTb, "", []string{"rd"}},
		{"ad+tb", webNTb, "", []string{"ad", "tb"}},
		{"rd+ad", webNTb, "", []string{"rd", "ad"}},
		{"rd+ad+tb", webNTb, "", []string{"rd", "ad", "tb"}},
		{"ad", webNTb, "", []string{"ad"}},
	})
	if want := "rd+ad+tb,ad+tb,rd+ad,tb,rd,ad,none"; got != want {
		t.Errorf("rank %s, want %s", got, want)
	}
}

// Quality and format score still come first: a better release with no evidence beats one cached everywhere.
func TestRankCachedAfterQualityAndScore(t *testing.T) {
	t.Parallel()
	e := cachedEngine(t)
	all := []string{"rd", "ad", "tb"}
	got := rankCached(t, e, []cachedRel{
		{"web", "Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX", "", all},
		{"remux", "Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC-GRP", "", all},
		{"remux-scored", "Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR", "", nil},
	})
	if want := "remux-scored,remux,web"; got != want {
		t.Errorf("rank %s, want %s", got, want)
	}
}

// Only providers the release may go to count: one outside the version, one the indexer does not allow, or one
// routes cannot add to (disabled, or no key) is no evidence.
func TestRankCachedUsableProvidersOnly(t *testing.T) {
	t.Parallel()
	e := cachedEngine(t)
	e.Policy.Versions["movie-remux"].Providers = []string{"rd", "ad"}
	e.Policy.Indexers["multi"].Providers = []string{"rd"}
	got := rankCached(t, e, []cachedRel{
		{"none", webNTb, "", nil},
		{"tb-outside-version", webNTb, "", []string{"tb"}},
		{"ad-not-allowed", webNTb, "multi", []string{"ad"}},
		{"ad", webNTb, "", []string{"ad"}},
	})
	if want := "ad,none,tb-outside-version,ad-not-allowed"; got != want {
		t.Errorf("rank %s, want %s", got, want)
	}
	e.SetUsableProviders(func(key string) bool { return key != "ad" })
	got = rankCached(t, e, []cachedRel{
		{"none", webNTb, "", nil},
		{"ad-no-key", webNTb, "", []string{"ad"}},
		{"rd", webNTb, "", []string{"rd"}},
	})
	if want := "rd,none,ad-no-key"; got != want {
		t.Errorf("rank %s, want %s", got, want)
	}
}

// The cached_on extra a feed carries is evidence on its own, with no CachedOn: it ranks like the same codes there.
func TestRankCachedOnExtraOnly(t *testing.T) {
	t.Parallel()
	e := cachedEngine(t)
	label := map[*Decision]string{}
	var ds []*Decision
	for i, extra := range []string{"", "tb", "rd"} {
		r := rel(webNTb, "public", 20*gb)
		r.InfoHash = fmt.Sprintf("%040x", i+1)
		if extra != "" {
			r.Extras = map[string]string{"cached_on": extra}
		}
		d, err := e.Decide(context.Background(), r, dune, Version{Key: "movie-remux"}, nil)
		if err != nil || !d.Accepted {
			t.Fatalf("%q: %+v %v", extra, d, err)
		}
		label[d] = extra
		ds = append(ds, d)
	}
	var got []string
	for _, d := range Rank(ds) {
		got = append(got, label[d])
	}
	if s := strings.Join(got, ","); s != "rd,tb," {
		t.Errorf("rank %q, want rd,tb, (none last)", s)
	}
}
