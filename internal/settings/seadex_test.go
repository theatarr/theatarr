package settings

import (
	"context"
	"testing"
)

func TestSeaDexPreferencePersistsWithoutEnablingGrabbing(t *testing.T) {
	t.Parallel()
	s, _, p, initial := versionFixture(t)
	m := versionCoordinator(t, s, p, initial)
	req := saveRequest(p, initial)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.PreferSeaDex = true })
	saved, err := m.SaveAndApply(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	v := saved.Policy.Versions["movie-web"]
	if !v.PreferSeaDex || v.Grab || v.Mode != initial.Policy.Versions["movie-web"].Mode {
		t.Fatalf("preference/gates after save: %+v", v)
	}
	b, err := versionConfigJSON(*v, nil)
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := versionConfigFromJSON(b)
	if err != nil || !back.PreferSeaDex {
		t.Fatalf("stored preference: %+v %v", back, err)
	}
}
