package settings

import (
	"context"
	"github.com/theatarr/theatarr/internal/policy"
	"strings"
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

func TestActiveSeaDexPreferenceExplainsReplacementEffect(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	req := saveRequest(p, active)
	editVersion(&req.Records, "movie-web", func(v *Version) { v.Config.PreferSeaDex = true })
	_, err := m.SaveAndApply(context.Background(), req)
	requireConfirmation(t, err, "existing higher-resolution files")
}

func TestDisableSeaDexConfirmationDescribesOrdinaryOrdering(t *testing.T) {
	t.Parallel()
	_, _, active := activeWebFixture(t)
	before := *active.Policy
	before.Versions = map[string]*policy.VersionPolicy{}
	for k, v := range active.Policy.Versions {
		copy := *v
		before.Versions[k] = &copy
	}
	before.Versions["movie-web"].PreferSeaDex = true
	effects, err := versionConfirmationEffects(context.Background(), nil, &before, active.Policy)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range effects {
		if strings.Contains(e.Effect, "disables Prefer SeaDex") {
			found = true
			if strings.Contains(e.Effect, "curated releases may replace") {
				t.Fatal(e.Effect)
			}
		}
	}
	if !found {
		t.Fatalf("missing disable explanation: %+v", effects)
	}
}
