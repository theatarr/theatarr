package web

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// GET titles/facets: counts for the grid's filter chips, over the titles of one grid (kind and anime). The counts
// ignore the grid's other filters, so a chip's number says how many titles it would show on its own.

// FacetCount is one value and how many titles have it.
type FacetCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// VersionFacet counts the titles of one version key.
type VersionFacet struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Titles int    `json:"titles"` // titles with this version
	// The file counts, for callers who see files (absent otherwise, and when zero).
	WithFile  int `json:"with_file,omitempty"` // ... that have a file for it
	Wanted    int `json:"wanted,omitempty"`
	Filled    int `json:"filled,omitempty"`
	CutoffMet int `json:"cutoff_met,omitempty"`
}

// TitleFacets is the facet counts of one grid.
type TitleFacets struct {
	Total        int            `json:"total"`
	Monitored    int            `json:"monitored"`
	Unmonitored  int            `json:"unmonitored"`
	Availability map[string]int `json:"availability"`
	Versions     []VersionFacet `json:"versions"`
	Qualities    []FacetCount   `json:"qualities"`
	Genres       []FacetCount   `json:"genres"`
	// Certifications are US ratings (title_meta).
	Certifications []FacetCount `json:"certifications"`
	// Providers is for callers who see files; absent otherwise. Values are cache codes (rd, ad, tb), as list rows
	// carry them and the provider filter takes them.
	Providers []FacetCount `json:"providers,omitempty"`
	YearMin   *int64       `json:"year_min,omitempty"`
	YearMax   *int64       `json:"year_max,omitempty"`
}

func (w *Web) apiTitleFacets(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := TitleFilter{Kind: q.Get("kind")}
	if !titleKinds[f.Kind] {
		badRequest(rw, "kind must be movie or series")
		return
	}
	var err error
	if f.Anime, err = boolParam(r, "anime"); err != nil {
		badRequest(rw, err.Error())
		return
	}
	perms := w.permsOf(r)
	out, err := w.titleFacets(r.Context(), f, perms.SeeFiles)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, out)
}

func (w *Web) titleFacets(ctx context.Context, f TitleFilter, seeFiles bool) (TitleFacets, error) {
	db := w.st.Reader()
	where, args := f.where()
	out := TitleFacets{Availability: map[string]int{"missing": 0, "partial": 0, "upgrade": 0, "available": 0, "none": 0},
		Versions: []VersionFacet{}, Qualities: []FacetCount{}, Genres: []FacetCount{}, Certifications: []FacetCount{}}
	var ymin, ymax sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(t.monitored), 0), MIN(t.year), MAX(t.year)`+
		titleFrom+where, args...).Scan(&out.Total, &out.Monitored, &ymin, &ymax); err != nil {
		return out, err
	}
	out.Unmonitored = out.Total - out.Monitored
	out.YearMin, out.YearMax = nullInt(ymin), nullInt(ymax)

	// Versions and availability, from every version of the grid's titles.
	states := map[int64][]versionState{}
	kinds := map[int64]string{}
	keys := map[string]*VersionFacet{}
	err := scanAll(ctx, db, `SELECT t.id, t.kind, v.policy_key, v.state, `+hasFileSQL+titleFrom+`
		JOIN version v ON v.title_id = t.id`+andWhere(where, liveVersion), args, func(r *sql.Rows) error {
		var tid int64
		var kind, key, state string
		var has bool
		if err := r.Scan(&tid, &kind, &key, &state, &has); err != nil {
			return err
		}
		states[tid] = append(states[tid], versionState{state, has})
		kinds[tid] = kind
		vf := keys[key]
		if vf == nil {
			vf = &VersionFacet{Key: key, Label: w.versionLabel(key)}
			keys[key] = vf
		}
		vf.Titles++
		if has {
			vf.WithFile++
		}
		switch state {
		case "wanted":
			vf.Wanted++
		case "filled":
			vf.Filled++
		case "cutoff_met":
			vf.CutoffMet++
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	for tid, s := range states {
		a := availabilityOf(s)
		if !seeFiles {
			a = plainAvailability(kinds[tid], s)
		}
		out.Availability[a]++
	}
	out.Availability["none"] = out.Total - len(states)
	if !seeFiles {
		// Below cutoff is file detail: those titles count as available (perms.go).
		delete(out.Availability, "upgrade")
	}
	for _, k := range sortedKeys(keys) {
		vf := *keys[k]
		if !seeFiles {
			// Per-library file counts are file detail (docs/design/users.md): the facet keeps its titles only.
			vf.WithFile, vf.Wanted, vf.Filled, vf.CutoffMet = 0, 0, 0, 0
		}
		out.Versions = append(out.Versions, vf)
	}

	counts := func(q string, args []any) ([]FacetCount, error) {
		res := []FacetCount{}
		err := scanAll(ctx, db, q, args, func(r *sql.Rows) error {
			var c FacetCount
			if err := r.Scan(&c.Value, &c.Count); err != nil {
				return err
			}
			if c.Value != "" {
				res = append(res, c)
			}
			return nil
		})
		sort.SliceStable(res, func(i, j int) bool {
			if res[i].Count != res[j].Count {
				return res[i].Count > res[j].Count
			}
			return strings.ToLower(res[i].Value) < strings.ToLower(res[j].Value)
		})
		return res, err
	}
	twice := append(append([]any{}, args...), args...)
	qualities := `SELECT t.id AS tid, vf.quality AS q` + titleFrom + ` JOIN version v ON v.title_id = t.id
			JOIN version_file vf ON vf.version_id = v.id LEFT JOIN version_source vs ON vs.version_id = v.id` +
		andWhere(where, liveVersion+" AND "+curFile) + `
		UNION ALL SELECT t.id, COALESCE(l.quality, '')` + titleFrom + ` JOIN link l ON l.title_id = t.id` +
		andWhere(where, "l.state = 'active' AND "+liveLink)
	if seeFiles {
		if out.Qualities, err = counts(`SELECT q, COUNT(DISTINCT tid) FROM (`+qualities+`) GROUP BY q`, twice); err != nil {
			return out, err
		}
	} else if out.Qualities, err = w.resolutionFacets(ctx, db, `SELECT DISTINCT q, tid FROM (`+qualities+`)`, twice); err != nil {
		return out, err
	}
	if out.Genres, err = counts(`SELECT g.value, COUNT(*)`+titleFrom+`, json_each(m.genres) g`+where+` GROUP BY g.value`,
		args); err != nil {
		return out, err
	}
	if out.Certifications, err = counts(`SELECT COALESCE(m.certification, ''), COUNT(*)`+titleFrom+where+
		` GROUP BY 1`, args); err != nil {
		return out, err
	}
	if seeFiles {
		// As the rows' pills and the provider filter: providers holding a current file of a version in the library
		// now (providerHolds; a link's content is linkContentSQL), counted by cache code.
		titles := map[string]map[int64]bool{}
		err := scanAll(ctx, db, `SELECT DISTINCT p, tid FROM (
			SELECT t.id AS tid, cp.provider AS p`+titleFrom+` JOIN version v ON v.title_id = t.id
				JOIN version_file vf ON vf.version_id = v.id LEFT JOIN version_source vs ON vs.version_id = v.id
				JOIN content_provider cp ON cp.content_id = vf.content_id`+
			andWhere(where, liveVersion+" AND "+curFile+" AND "+providerHolds)+`
			UNION ALL SELECT t.id, cp.provider`+titleFrom+` JOIN link l ON l.title_id = t.id
				JOIN content_provider cp ON cp.content_id = `+linkContentSQL+
			andWhere(where, "l.state = 'active' AND "+liveLink+" AND "+providerHolds)+`)`, twice, func(r *sql.Rows) error {
			var p string
			var tid int64
			if err := r.Scan(&p, &tid); err != nil {
				return err
			}
			for _, c := range w.cacheCodes([]string{p}) {
				if titles[c] == nil {
					titles[c] = map[int64]bool{}
				}
				titles[c][tid] = true
			}
			return nil
		})
		if err != nil {
			return out, err
		}
		out.Providers = []FacetCount{}
		for c, ts := range titles {
			out.Providers = append(out.Providers, FacetCount{Value: c, Count: len(ts)})
		}
		sort.Slice(out.Providers, func(i, j int) bool {
			a, b := out.Providers[i], out.Providers[j]
			if a.Count != b.Count {
				return a.Count > b.Count
			}
			return strings.ToLower(a.Value) < strings.ToLower(b.Value)
		})
	}
	return out, nil
}

// andWhere adds cond to a WHERE clause from TitleFilter.where (which may be empty).
func andWhere(where, cond string) string {
	if where == "" {
		return " WHERE " + cond
	}
	return where + " AND " + cond
}

// resolutionFacets buckets the (quality, title) pairs q yields by resolution, for callers without "see files": the
// titles with some current file in 2160p, 1080p and so on, highest first. Qualities with no resolution are left out.
func (w *Web) resolutionFacets(ctx context.Context, db *sql.DB, q string, args []any) ([]FacetCount, error) {
	titles := map[string]map[int64]bool{}
	err := scanAll(ctx, db, q, args, func(r *sql.Rows) error {
		var quality string
		var tid int64
		if err := r.Scan(&quality, &tid); err != nil {
			return err
		}
		if res := Resolution(quality); res != "" {
			if titles[res] == nil {
				titles[res] = map[int64]bool{}
			}
			titles[res][tid] = true
		}
		return nil
	})
	out := []FacetCount{}
	for res, ts := range titles {
		out = append(out, FacetCount{Value: res, Count: len(ts)})
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimSuffix(out[i].Value, "p"))
		b, _ := strconv.Atoi(strings.TrimSuffix(out[j].Value, "p"))
		return a > b
	})
	return out, err
}
