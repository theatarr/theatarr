package settings

import (
	"encoding/json"
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
