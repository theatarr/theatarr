package settings

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

func TestProfileFieldRoundTrip(t *testing.T) {
	t.Parallel()
	up := false
	src := policy.Profile{Qualities: []policy.QualityItem{{Name: "WEB", Group: true, Qualities: []string{"WEBDL-2160p", "WEBRip-2160p"}}, {Name: "Remux-2160p"}},
		Cutoff: "WEB", Upgrades: &up, MinScore: 5, Formats: map[string]int{"A": 10}, Sizes: map[string]policy.SizeLimit{"Remux-2160p": {Min: 187.4, Max: 400}}}
	if got := string(profileField(&src, "qualities")); got != `[{"group":"WEB","qualities":["WEBDL-2160p","WEBRip-2160p"]},"Remux-2160p"]` {
		t.Errorf("qualities as records spell them: %s", got)
	}
	if got := string(profileField(&src, "sizes/Remux-2160p")); got != `{"min":187.4,"preferred":0,"max":400}` {
		t.Errorf("size: %s", got)
	}
	dst := policy.Profile{Qualities: []policy.QualityItem{{Name: "HDTV-720p"}}, Cutoff: "HDTV-720p", QualityOrder: []policy.QualityItem{{Name: "HDTV-720p"}}, Formats: map[string]int{"B": 1}}
	shared := dst.Formats
	for _, f := range []string{"qualities", "cutoff", "upgrades", "min_score", "formats/A", "sizes/Remux-2160p"} {
		if err := setProfileField(&dst, f, profileField(&src, f)); err != nil {
			t.Fatal(f, err)
		}
		if !sameFieldJSON(profileField(&dst, f), profileField(&src, f)) {
			t.Errorf("%s: %s != %s", f, profileField(&dst, f), profileField(&src, f))
		}
	}
	if dst.QualityOrder != nil || dst.Formats["B"] != 1 || len(shared) != 1 {
		t.Errorf("quality_order %v formats %v (shared map written: %v)", dst.QualityOrder, dst.Formats, shared)
	}
	if err := setProfileField(&dst, "formats/A", jsonNull); err != nil || dst.Formats["A"] != 0 || len(dst.Formats) != 1 {
		t.Errorf("null removes the score: %v %v", dst.Formats, err)
	}
}

func TestFollowField(t *testing.T) {
	t.Parallel()
	n := func(v int) json.RawMessage { b, _ := json.Marshal(v); return b }
	for name, c := range map[string]struct {
		ours, up json.RawMessage
		prev     FieldMemory
		known    bool
		take     bool
		kept     bool
	}{
		"equal":                  {ours: n(5), up: n(5)},
		"source unchanged, ours": {ours: n(7), up: n(5), prev: FieldMemory{Upstream: n(5)}, known: true},
		"follows":                {ours: n(5), up: n(9), prev: FieldMemory{Upstream: n(5)}, known: true, take: true},
		"ours kept":              {ours: n(7), up: n(9), prev: FieldMemory{Upstream: n(5)}, known: true, kept: true},
		"first run, not set":     {ours: jsonNull, up: n(9), take: true},
		"first run, differing":   {ours: n(7), up: n(9), take: true},
		"kept, source unchanged": {ours: n(7), up: n(9), prev: FieldMemory{Upstream: n(9)}, known: true},
	} {
		mem, take, kept := followField(c.ours, c.up, c.prev, c.known)
		if take != c.take || kept != c.kept {
			t.Errorf("%s: take %v kept %v", name, take, kept)
		}
		// The memory is always the source's current value: a kept value is not asked about again.
		if !sameFieldJSON(mem.Upstream, c.up) {
			t.Errorf("%s: remembers %s, want %s", name, mem.Upstream, c.up)
		}
	}
}

func qualityItems(names ...string) []policy.QualityItem {
	out := make([]policy.QualityItem, len(names))
	for i, n := range names {
		out[i] = policy.QualityItem{Name: n}
	}
	return out
}

// A score reset_unmatched_scores removed is remembered while the reset still covers it: restored by hand, it is
// yours and stays.
func TestReconcileResetScoreRestoredStays(t *testing.T) {
	t.Parallel()
	in := RecyclarrProfile{Key: "p", Reset: func(k string) bool { return k == "Seadex" }}
	none := func(string) bool { return false }
	cur := policy.Profile{Formats: map[string]int{"Seadex": 500}}
	last, _, err := reconcileProfile(&cur, in, nil, false, none)
	if err != nil || len(cur.Formats) != 0 {
		t.Fatalf("first run did not reset: %v %v", cur.Formats, err)
	}
	if last, _, err = reconcileProfile(&cur, in, last, false, none); err != nil {
		t.Fatal(err)
	}
	cur.Formats = map[string]int{"Seadex": 500}
	_, kept, err := reconcileProfile(&cur, in, last, false, none)
	if err != nil || cur.Formats["Seadex"] != 500 || len(kept) != 0 {
		t.Fatalf("restored score reset again: %v kept %v %v", cur.Formats, kept, err)
	}
}

// Taking a changed quality list keeps a quality_order that is still valid for it.
func TestReconcileQualitiesKeepValidOrder(t *testing.T) {
	t.Parallel()
	order := qualityItems("Remux-2160p", "WEBDL-2160p", "Bluray-1080p", "WEBDL-1080p")
	cur := policy.Profile{Qualities: qualityItems("WEBDL-2160p"), QualityOrder: order, Cutoff: "WEBDL-2160p"}
	in := RecyclarrProfile{Key: "p", Fields: []string{"qualities"}, Config: policy.Profile{Qualities: qualityItems("WEBDL-2160p", "WEBDL-1080p"), Cutoff: "WEBDL-2160p"}}
	last := map[string]FieldMemory{"qualities": {Upstream: profileField(&cur, "qualities")}}
	if _, _, err := reconcileProfile(&cur, in, last, false, func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if len(cur.Qualities) != 2 || !reflect.DeepEqual(cur.QualityOrder, order) {
		t.Fatalf("qualities %v order %v", cur.Qualities, cur.QualityOrder)
	}
	// An order the new list does not fit is dropped.
	in.Config.Qualities = qualityItems("WEBDL-1080p", "WEBDL-2160p")
	last = map[string]FieldMemory{"qualities": {Upstream: profileField(&cur, "qualities")}}
	if _, _, err := reconcileProfile(&cur, in, last, false, func(string) bool { return false }); err != nil || cur.QualityOrder != nil {
		t.Fatalf("invalid order kept: %v %v", cur.QualityOrder, err)
	}
}

// The cutoff is compared as it was before the run: a cutoff the source changes together with the qualities is taken,
// not mistaken for one of yours because taking the qualities moved it.
func TestReconcileCutoffComparedBeforeQualities(t *testing.T) {
	t.Parallel()
	cur := policy.Profile{Qualities: qualityItems("Bluray-2160p", "WEBDL-1080p"), Cutoff: "WEBDL-1080p"}
	last := map[string]FieldMemory{"qualities": {Upstream: profileField(&cur, "qualities")}, "cutoff": {Upstream: profileField(&cur, "cutoff")}}
	in := RecyclarrProfile{Key: "p", Fields: []string{"qualities", "cutoff"}, Config: policy.Profile{Qualities: qualityItems("Bluray-2160p", "WEBDL-2160p"), Cutoff: "WEBDL-2160p"}}
	_, kept, err := reconcileProfile(&cur, in, last, false, func(string) bool { return false })
	if err != nil || cur.Cutoff != "WEBDL-2160p" || len(kept) != 0 {
		t.Fatalf("cutoff %q kept %+v %v", cur.Cutoff, kept, err)
	}
}
