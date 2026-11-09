package arrview

import (
	"fmt"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
)

// Flavours.
const (
	Radarr = "radarr"
	Sonarr = "sonarr"
)

// View is one arr-shaped API: a set of versions, a flavour and a base path (docs/design/arrview.md).
type View struct {
	// Key names the view: the policy key of a configured view, or "v/<version>" for a per-version view.
	Key     string
	Flavour string
	// Base is the path the API hangs under: /radarr serves /radarr/api/v3/...
	Base string
	// Versions are the version policy keys served, in rank order: the first with a file gives movieFile.
	Versions []string
	// Profiles are the "quality profiles" the view lists: named version sets. IDs come from the database.
	Profiles []*Profile
	// AliasFrom lists the arr instances whose ids this view answers (docs/migration.md), in precedence order.
	AliasFrom []string
	// keyEnv names the environment variable holding the view's own key (configured views only).
	keyEnv string
}

// RulesDefault is the profile meaning "let Theatarr's rules decide": the one a configured view without profiles gets, and
// the name views.yaml gives it. Only it (or no profile) lets an add fall back to the view's default versions when none
// of its own fits the title (chooseVersions).
const RulesDefault = "Rules default"

// Profile is a named set of versions a view offers as a quality profile.
type Profile struct {
	ID       int64
	Name     string
	Versions []string
}

func (v *View) kind() policy.Kind {
	if v.Flavour == Sonarr {
		return policy.KindSeries
	}
	return policy.KindMovie
}

func (v *View) profileByID(id int64) *Profile {
	for _, p := range v.Profiles {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// BuildViews makes the views a policy defines: every configured view, plus /v/<version>/radarr or /sonarr for every
// version (docs/design/arrview.md: per-version views exist without configuration). A configured view without profiles gets one,
// "Rules default", with all its versions; a per-version view has one profile named after its version.
func BuildViews(p *policy.Policy) ([]*View, error) {
	var out []*View
	bases := map[string]string{}
	add := func(v *View) error {
		lb := strings.ToLower(v.Base)
		if other, ok := bases[lb]; ok {
			return fmt.Errorf("arrview: views %s and %s share the path %s", other, v.Key, v.Base)
		}
		bases[lb] = v.Key
		out = append(out, v)
		return nil
	}
	for _, k := range sortedKeys(p.Views) {
		pv := p.Views[k]
		v := &View{Key: k, Flavour: pv.Flavour, Base: pv.Path, Versions: append([]string(nil), pv.Versions...),
			AliasFrom: append([]string(nil), pv.AliasFrom...), keyEnv: pv.APIKeyEnv}
		for _, name := range sortedKeys(pv.Profiles) {
			v.Profiles = append(v.Profiles, &Profile{Name: name, Versions: append([]string(nil), pv.Profiles[name]...)})
		}
		if len(v.Profiles) == 0 {
			v.Profiles = []*Profile{{Name: RulesDefault, Versions: append([]string(nil), v.Versions...)}}
		}
		if err := add(v); err != nil {
			return nil, err
		}
	}
	for _, k := range sortedKeys(p.Versions) {
		fl := Radarr
		if p.Versions[k].Kind == policy.KindSeries {
			fl = Sonarr
		}
		v := &View{Key: "v/" + k, Flavour: fl, Base: "/v/" + k + "/" + fl, Versions: []string{k},
			Profiles: []*Profile{{Name: k, Versions: []string{k}}}}
		if err := add(v); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
