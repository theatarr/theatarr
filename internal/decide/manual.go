package decide

import (
	"fmt"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
)

// ManualOverrides are client choices reapplied after import aggregation, as the arrs' ManualImportService does.
// Nil fields keep the parsed value. Unknown quality/language and a blank group are resolved by the caller.
type ManualOverrides struct {
	Quality      *ManualQuality `json:"quality,omitempty"`
	Languages    []string       `json:"languages,omitempty"`
	ReleaseGroup *string        `json:"release_group,omitempty"`
	IndexerFlags *int           `json:"indexer_flags,omitempty"`
	Episodes     []int64        `json:"episodes,omitempty"`
	ImportMode   string         `json:"import_mode,omitempty"`
}

type ManualQuality struct {
	Name     string           `json:"name"`
	Revision profile.Revision `json:"revision"`
}

func (m *ManualOverrides) Apply(f *FileFacts) {
	if m == nil {
		return
	}
	if m.Quality != nil {
		f.Quality, f.Revision, f.Real = m.Quality.Name, max(m.Quality.Revision.Version, 1), m.Quality.Revision.Real
		f.IsRepack = m.Quality.Revision.IsRepack
	}
	if m.Languages != nil {
		f.Languages = append([]string{}, m.Languages...)
	}
	if m.ReleaseGroup != nil {
		f.ReleaseGroup = *m.ReleaseGroup
	}
}

func (m *ManualOverrides) quality(fl format.Flavour, qm profile.QualityModel) profile.QualityModel {
	if m != nil && m.Quality != nil {
		if q, ok := profile.QualityByName(fl, m.Quality.Name); ok {
			qm.Quality, qm.Revision = q, m.Quality.Revision
			qm.Revision.Version = max(qm.Revision.Version, 1)
		}
	}
	return qm
}

func (m *ManualOverrides) input(in *format.Input, qm profile.QualityModel) {
	if m == nil {
		return
	}
	in.Source, in.Resolution, in.Modifier = qm.Quality.Source, qm.Quality.Resolution, qm.Quality.Modifier
	if m.ReleaseGroup != nil {
		in.ReleaseGroup = m.ReleaseGroup
	}
	if m.Languages != nil {
		in.Languages, in.LanguagesAssumed = languageIDs(in.Flavour, m.Languages), false
	}
	if m.IndexerFlags != nil {
		in.IndexerFlags = *m.IndexerFlags
	}
}

// ScoreFile calculates formats from the file inputs that imports name and retain, including manual choices.
func (e *Engine) ScoreFile(t *Title, v Version, x *ExistingFile) error {
	fl := flavourOf(v.Policy.Kind)
	p := e.Profile(v.Policy.Profile, fl)
	if p == nil {
		return fmt.Errorf("decide: profile %q was not built", v.Policy.Profile)
	}
	return ScoreFileWith(p, t, v, x)
}

// ScoreFileWith is ScoreFile under the compiled profile p (the router's, which has the profiles but not the engine):
// x.Formats and x.Score are set as the decision's existing-file comparison sees them.
func ScoreFileWith(p *Profile, t *Title, v Version, x *ExistingFile) error {
	fl := flavourOf(v.Policy.Kind)
	var f *profile.File
	var err error
	if fl == format.Sonarr {
		f, err = seriesFile(x, t, languageID(fl, t.OriginalLanguage), p)
	} else {
		f, err = existingFile(fl, x, t, languageID(fl, t.OriginalLanguage), p)
	}
	if err != nil {
		return err
	}
	x.Formats = formatScores(p.Profile, f.Formats)
	score := p.Profile.FormatScore(f.Formats)
	x.Score = &score
	return nil
}
