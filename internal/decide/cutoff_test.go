package decide

import (
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// QualityCutoffNotMet is the quality half of the cutoff only (the arrs' qualityCutoffNotMet and Cutoff Unmet list);
// FileCutoffNotMet adds the format score. With upgrades off the cutoff is the first allowed quality.
func TestQualityCutoffNotMet(t *testing.T) {
	t.Parallel()
	no := false
	pol := &policy.Policy{
		Profiles: map[string]*policy.Profile{
			"up": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}, {Name: "WEBDL-1080p"}}, Cutoff: "WEBDL-2160p",
				CutoffScore: 100},
			"noup": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}, {Name: "WEBDL-1080p"}}, Cutoff: "WEBDL-2160p",
				Upgrades: &no},
		},
		Versions: map[string]*policy.VersionPolicy{
			"m":    {Kind: policy.KindMovie, Profile: "up"},
			"s":    {Kind: policy.KindSeries, Profile: "up"},
			"mnou": {Kind: policy.KindMovie, Profile: "noup"},
		},
	}
	e, err := New(pol, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		version, quality string
		quality2, full   bool
	}{
		{"m", "WEBDL-2160p", false, true}, // at the cutoff quality, score 0 of 100
		{"m", "WEBDL-1080p", true, true},
		{"s", "WEBDL-2160p", false, true},
		{"s", "WEBDL-1080p", true, true},
		{"mnou", "WEBDL-1080p", false, false}, // upgrades off: the first allowed quality (1080p, worst first)
	} {
		f := &ExistingFile{Quality: tc.quality, Revision: 1}
		got, err := e.QualityCutoffNotMet(Version{Key: tc.version}, f)
		if err != nil || got != tc.quality2 {
			t.Errorf("%s %s: quality cutoff not met = %v, %v; want %v", tc.version, tc.quality, got, err, tc.quality2)
		}
		full, err := e.FileCutoffNotMet(&Title{}, Version{Key: tc.version}, f)
		if err != nil || full != tc.full {
			t.Errorf("%s %s: full cutoff not met = %v, %v; want %v", tc.version, tc.quality, full, err, tc.full)
		}
	}
	if _, err := e.QualityCutoffNotMet(Version{Key: "m"}, &ExistingFile{Quality: "Nonsense"}); err == nil {
		t.Error("an unknown quality should be an error")
	}
}
