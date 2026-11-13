package ceiling

import (
	"context"
	"reflect"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

func TestProfiles(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	pol := testPolicy()
	pol.Profiles["imported"] = &policy.Profile{FromArr: "arr/Shared"}
	pol.Versions["imported"] = &policy.VersionPolicy{Profile: "imported"}
	pol.Versions["no-profile"] = &policy.VersionPolicy{Profile: "absent"}
	pol.Profiles["tv"] = &policy.Profile{Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}
	pol.Versions["tv"] = &policy.VersionPolicy{Profile: "tv"}
	vs := []ProfileVersion{{1, "imported", "movie"}, {2, "remux", "movie"}, {3, "no-profile", "movie"},
		{4, "absent", "movie"}, {5, "tv", "series"}, {6, "tv", "movie"}}
	// Reuse a policy profile across many versions.
	for i := int64(7); i < 610; i++ {
		vs = append(vs, ProfileVersion{i, "remux", "movie"})
	}
	got, err := Profiles(ctx, st.Reader(), pol, vs)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		want, err := Profile(ctx, st.Reader(), pol, v.ID, v.Key, v.Kind)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got[v.ID], want) {
			t.Fatalf("version %d: batch %+v, single %+v", v.ID, got[v.ID], want)
		}
	}
	// A profile taken from an arr has no qualities of its own: no profile, as for a missing one.
	if got[1] != nil || got[3] != nil || got[4] != nil || got[2] == nil || got[5] == nil {
		t.Fatalf("profiles %+v", got)
	}
	if got[2] != got[609] || got[5] == got[6] {
		t.Fatal("a profile was decoded more than once, or shared across flavours")
	}
	if got, err := Profiles(ctx, st.Reader(), nil, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty batch: %v, %v", got, err)
	}
}
