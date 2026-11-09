package arrview

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
)

func (s *Service) runtimeProfile(v *View, versions []string) *decide.Profile {
	if s.engine == nil {
		return nil
	}
	for _, key := range versions {
		if version := s.pol.Versions[key]; version != nil {
			if p := s.engine.Profile(version.Profile, format.Flavour(v.Flavour)); p != nil {
				return p
			}
		}
	}
	return nil
}

// runtimeFormats gives the view one format namespace, including profiles with separate format sets or arr ids.
// Equal definitions share an id; profile score tables are remapped without changing the decision engine's ids.
func (s *Service) runtimeFormats(v *View) ([]format.FormatJSON, map[*decide.Profile]map[int]int) {
	out := []format.FormatJSON{}
	ids := map[*decide.Profile]map[int]int{}
	seen := map[string]int{}
	for _, key := range v.Versions {
		p := s.runtimeProfile(v, []string{key})
		if p == nil || ids[p] != nil {
			continue
		}
		ids[p] = map[int]int{}
		for _, f := range p.CustomFormats {
			f.Specifications = append([]format.SpecJSON(nil), f.Specifications...)
			for i := range f.Specifications {
				var fields map[string]json.RawMessage
				if json.Unmarshal(f.Specifications[i].Fields, &fields) == nil && fields != nil {
					names := make([]string, 0, len(fields))
					for name := range fields {
						names = append(names, name)
					}
					sort.Strings(names)
					list := []struct {
						Name  string          `json:"name"`
						Value json.RawMessage `json:"value"`
					}{}
					for _, name := range names {
						list = append(list, struct {
							Name  string          `json:"name"`
							Value json.RawMessage `json:"value"`
						}{name, fields[name]})
					}
					f.Specifications[i].Fields, _ = json.Marshal(list)
				}
			}
			original := f.ID
			f.ID = 0
			raw, _ := json.Marshal(f)
			id := seen[string(raw)]
			if id == 0 {
				id = len(out) + 1
				seen[string(raw)] = id
				f.ID = id
				out = append(out, f)
			}
			ids[p][original] = id
		}
	}
	return out, ids
}

func (s *Service) customFormats(w http.ResponseWriter, r *request) {
	out, _ := s.runtimeFormats(r.v)
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) customFormat(w http.ResponseWriter, r *request) {
	id, _ := r.pathID(1)
	all, _ := s.runtimeFormats(r.v)
	for _, f := range all {
		if int64(f.ID) == id {
			writeJSON(w, http.StatusOK, f)
			return
		}
	}
	notFound(w, "CustomFormat with ID "+strconv.FormatInt(id, 10)+" does not exist")
}
