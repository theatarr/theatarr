package profile

// Port of Radarr src/NzbDrone.Core/Qualities/{Quality,Revision,QualityModel,QualityModelComparer}.cs @ a96bf7c and
// the Sonarr files @ cab419a. The quality lists themselves come from the parser ports (radarr.AllQualities,
// sonarr.AllQualities), so ids, names, sources and resolutions are defined once.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
)

// Quality is one of an arr's fixed qualities, flavour-neutral. Source is in the flavour's QualitySource numbering
// and Modifier in Radarr's Modifier numbering (always 0 for Sonarr), as in format.Input. Equality is by ID, as in
// the arrs.
type Quality struct {
	ID         int
	Name       string
	Source     int
	Resolution int
	Modifier   int
}

func (q Quality) String() string { return q.Name }

// qualityJSON is the arr API's QualityResource: sources and modifiers as camelCased enum names.
type qualityJSON struct {
	ID         int             `json:"id"`
	Name       string          `json:"name"`
	Source     json.RawMessage `json:"source"`
	Resolution int             `json:"resolution"`
	Modifier   json.RawMessage `json:"modifier,omitempty"`
}

var (
	radarrQualities = func() []Quality {
		out := make([]Quality, len(radarr.AllQualities))
		for i, q := range radarr.AllQualities {
			out[i] = Quality{q.ID, q.Name, int(q.Source), q.Resolution, int(q.Modifier)}
		}
		return out
	}()
	sonarrQualities = func() []Quality {
		out := make([]Quality, len(sonarr.AllQualities))
		for i, q := range sonarr.AllQualities {
			out[i] = Quality{q.ID, q.Name, int(q.Source), q.Resolution, 0}
		}
		return out
	}()
)

// Qualities is Quality.All for flavour fl, in the arr's order.
func Qualities(fl format.Flavour) []Quality {
	if fl == format.Sonarr {
		return sonarrQualities
	}
	return radarrQualities
}

// QualityByID is Quality.FindById.
func QualityByID(fl format.Flavour, id int) (Quality, bool) {
	for _, q := range Qualities(fl) {
		if q.ID == id {
			return q, true
		}
	}
	return Quality{}, false
}

// QualityByName finds a quality by name, ignoring case (TRaSH and recyclarr compare names that way).
func QualityByName(fl format.Flavour, name string) (Quality, bool) {
	for _, q := range Qualities(fl) {
		if strings.EqualFold(q.Name, name) {
			return q, true
		}
	}
	return Quality{}, false
}

// MustQuality returns the named quality or panics. For tables and tests.
func MustQuality(fl format.Flavour, name string) Quality {
	q, ok := QualityByName(fl, name)
	if !ok {
		panic(fmt.Sprintf("profile: no %s quality %q", fl, name))
	}
	return q
}

func qualityToJSON(fl format.Flavour, q Quality) qualityJSON {
	j := qualityJSON{ID: q.ID, Name: q.Name, Resolution: q.Resolution}
	if fl == format.Sonarr {
		j.Source, _ = json.Marshal(sonarr.QualitySource(q.Source).String())
	} else {
		j.Source, _ = json.Marshal(radarr.QualitySource(q.Source).String())
		j.Modifier, _ = json.Marshal(radarr.Modifier(q.Modifier).String())
	}
	return j
}

// qualityFromJSON resolves an API quality by id and checks that the rest agrees with our table.
func qualityFromJSON(fl format.Flavour, j qualityJSON) (Quality, error) {
	q, ok := QualityByID(fl, j.ID)
	if !ok {
		return Quality{}, fmt.Errorf("unknown %s quality id %d (%q)", fl, j.ID, j.Name)
	}
	want := qualityToJSON(fl, q)
	if j.Name != q.Name || j.Resolution != q.Resolution || !sameEnum(j.Source, want.Source) ||
		(fl == format.Radarr && len(j.Modifier) > 0 && !sameEnum(j.Modifier, want.Modifier)) {
		return Quality{}, fmt.Errorf("%s quality %d: API has %q/%s/%d/%s, our table %q/%s/%d/%s", fl, j.ID,
			j.Name, j.Source, j.Resolution, j.Modifier, q.Name, want.Source, q.Resolution, want.Modifier)
	}
	return q, nil
}

func sameEnum(a, b json.RawMessage) bool {
	var x, y string
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return string(a) == string(b)
	}
	return strings.EqualFold(x, y)
}

// Revision is the arrs' Revision. Equality ignores IsRepack, as in the arrs.
type Revision struct {
	Version  int  `json:"version"`
	Real     int  `json:"real"`
	IsRepack bool `json:"isRepack"`
}

// NewRevision is new Revision(): version 1.
func NewRevision() Revision { return Revision{Version: 1} }

// CompareTo is Revision.CompareTo: Real first, then Version.
func (r Revision) CompareTo(o Revision) int {
	switch {
	case r.Real > o.Real:
		return 1
	case r.Real < o.Real:
		return -1
	case r.Version > o.Version:
		return 1
	case r.Version < o.Version:
		return -1
	}
	return 0
}

// Equal is Revision.Equals: Version and Real.
func (r Revision) Equal(o Revision) bool { return r.Version == o.Version && r.Real == o.Real }

func (r Revision) String() string {
	s := fmt.Sprintf("v%d", r.Version)
	if r.Real > 0 {
		s += fmt.Sprintf(" Real:%d", r.Real)
	}
	return s
}

// QualityModel is a quality plus revision.
type QualityModel struct {
	Quality  Quality
	Revision Revision
	// SeaDexPreference is native curator preference, separate from quality/resolution; zero preserves arr parity.
	SeaDexPreference int `json:"-"`
}

// NewQualityModel is new QualityModel(quality): revision v1.
func NewQualityModel(q Quality) QualityModel {
	return QualityModel{Quality: q, Revision: NewRevision()}
}

// String is QualityModel.ToString: "{Quality} {Revision}".
func (m QualityModel) String() string { return m.Quality.Name + " " + m.Revision.String() }
