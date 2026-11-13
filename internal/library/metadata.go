package library

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// linkedMetadata freezes the original release inputs before any library naming. Downloads routed before the
// release inputs were retained still have their title and quality; parsing those is the same fallback as naming.
// The title is the one the decision read (nil without a decision engine).
// A plan of a new import (PlanNew) has no download row: its inputs are the release title and quality alone.
// The result is kept on d for the rest of the import.
func (l *Library) linkedMetadata(ctx context.Context, d *importDownload) (*decide.ExistingFile, *decide.Title, error) {
	if d.linked != nil {
		return d.linked.file, d.linked.title, nil
	}
	f, t, err := l.loadLinkedMetadata(ctx, d)
	if err != nil {
		return nil, nil, err
	}
	d.linked = &linkedInputs{file: f, title: t}
	return f, t, nil
}

type linkedInputs struct {
	file  *decide.ExistingFile
	title *decide.Title
}

func (l *Library) loadLinkedMetadata(ctx context.Context, d *importDownload) (*decide.ExistingFile, *decide.Title, error) {
	// The release inputs are the download's own (release_data); the release table is not written.
	var formats, releaseData sql.NullString
	var size sql.NullInt64
	if d.ID != 0 {
		err := l.st.Reader().QueryRowContext(ctx, `SELECT formats, size_bytes, release_data FROM download WHERE id = ?`,
			d.ID).Scan(&formats, &size, &releaseData)
		if err != nil {
			return nil, nil, err
		}
	}
	var rel indexer.Release
	if releaseData.Valid {
		if err := json.Unmarshal([]byte(releaseData.String), &rel); err != nil {
			return nil, nil, err
		}
	}
	rel.Title, rel.InfoHash, rel.Indexer, rel.Size = d.ReleaseTitle, d.InfoHash, d.Indexer, size.Int64
	v := &d.Version
	if v.Policy.PreferSeaDex {
		// Incoming evidence must never inherit a grab-time rank. Held files use ResolveSeaDexFiles.
		rel = decide.RefreshSeaDexRelease(ctx, rel)
	}
	f := decide.LinkedFile(policy.Kind(v.Title.Kind), decide.Release{Release: rel}, v.Quality)
	f.Score = &v.Score
	if formats.Valid {
		_ = json.Unmarshal([]byte(formats.String), &f.Formats)
	}
	var title *decide.Title
	if l.engine != nil {
		t, err := decide.LoadTitle(ctx, l.st.Reader(), v.Title.ID)
		if err != nil {
			return nil, nil, err
		}
		if t.Kind == policy.KindSeries {
			if v.Policy.PreferSeaDex {
				err = decide.LoadSeriesVersion(ctx, l.st.Reader(), &t, v.VersionID)
				// Scoring imported metadata has always used episodes without existing files.
				for i := range t.Episodes {
					t.Episodes[i].File = nil
					t.Episodes[i].FileID = 0
				}
			} else {
				t.Episodes, err = decide.LoadEpisodes(ctx, l.st.Reader(), t.ID, 0)
			}
			if err != nil {
				return nil, nil, err
			}
		}
		decision, err := l.engine.Decide(ctx, decide.Release{Release: rel}, t,
			decide.Version{ID: v.VersionID, Key: v.Key, Policy: v.Policy}, nil)
		if err != nil {
			return nil, nil, err
		}
		if decision != nil && len(decision.Languages) > 0 {
			f.Languages = decision.Languages
		}
		if decision != nil && !formats.Valid {
			f.Formats = decision.Formats
		}
		title = &t
	}
	return f, title, nil
}
