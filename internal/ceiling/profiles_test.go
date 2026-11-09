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
	pol.Profiles["missing"] = &policy.Profile{FromArr: "arr/Missing"}
	pol.Versions["imported"] = &policy.VersionPolicy{Profile: "imported"}
	pol.Versions["missing"] = &policy.VersionPolicy{Profile: "missing"}
	pol.Versions["no-profile"] = &policy.VersionPolicy{Profile: "absent"}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.Writer().ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO arr_instance(name,flavour,policy_key,created_at,updated_at) VALUES('arr','radarr','remux',0,0)`)
	exec(`INSERT INTO arr_quality_profile(instance,arr_id,name,cutoff,quality_items) VALUES
 ('arr',1,'Shared','WEBDL-1080p','[{"quality":{"id":3,"name":"WEBDL-1080p"},"allowed":true}]'),
 ('arr',2,'Empty','','[]')`)
	exec(`INSERT INTO version_source(version_id,instance,arr_id) VALUES(1,'arr',1),(2,'arr',2)`)
	exec(`INSERT INTO arr_title(instance,arr_id,kind,title_id,version_id,monitored,quality_profile_id) VALUES
 ('arr',1,'movie',1,1,1,1),('arr',2,'movie',1,2,1,2)`)
	vs := []ProfileVersion{{1, "missing", "movie"}, {2, "remux", "movie"},
		{3, "imported", "movie"}, {4, "missing", "movie"}, {5, "no-profile", "movie"},
		{6, "absent", "movie"}, {7, "remux", "movie"}}
	// Cross multiple chunks while reusing both an arr and a policy profile.
	for i := int64(8); i < 610; i++ {
		key := "remux"
		if i%2 == 0 {
			key = "imported"
		}
		vs = append(vs, ProfileVersion{i, key, "movie"})
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
	if got[1] != got[3] || got[3] != got[608] || got[7] != got[609] {
		t.Fatal("shared profiles were decoded more than once")
	}
	exec(`UPDATE arr_quality_profile SET quality_items='invalid' WHERE arr_id=1`)
	if _, err := Profiles(ctx, st.Reader(), pol, vs); err == nil {
		t.Fatal("invalid profile JSON was ignored")
	}
	exec(`UPDATE arr_quality_profile SET quality_items='[]' WHERE arr_id=1`)
	got, err = Profiles(ctx, st.Reader(), pol, vs[:1])
	if err != nil || got[1] != nil {
		t.Fatalf("profile cache survived a call: %+v, %v", got[1], err)
	}
	if got, err := Profiles(ctx, st.Reader(), nil, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty batch: %v, %v", got, err)
	}
}
