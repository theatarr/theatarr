package ceiling

import (
	"context"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
)

// ProfileVersion identifies the quality profile needed for a version's ceiling explanation.
type ProfileVersion struct {
	ID        int64
	Key, Kind string
}

// Profiles is Profile for a page of versions; each distinct profile is decoded once per flavour, with caches scoped
// to this call.
func Profiles(_ context.Context, _ DB, pol *policy.Policy, vs []ProfileVersion) (map[int64]*profile.Profile, error) {
	out := map[int64]*profile.Profile{}
	type profileKey struct {
		name string
		fl   format.Flavour
	}
	cache := map[profileKey]*profile.Profile{}
	for _, v := range vs {
		if pol == nil || pol.Versions[v.Key] == nil {
			continue
		}
		key := profileKey{pol.Versions[v.Key].Profile, flavour(v.Kind)}
		p, ok := cache[key]
		if !ok {
			var err error
			if p, err = policyProfile(pol, v.Key, key.fl); err != nil {
				return nil, err
			}
			cache[key] = p
		}
		if p != nil {
			out[v.ID] = p
		}
	}
	return out, nil
}
