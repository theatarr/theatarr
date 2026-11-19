package web

import (
	"context"
	"database/sql"
	"net/http"
	"slices"
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
	// Kind is the library's media kind (movie or series) from the settings; absent for a library no longer there.
	Kind string `json:"kind,omitempty"`
	// The file counts, for callers who see files (absent otherwise, and when zero).
	WithFile int `json:"with_file,omitempty"` // ... that have a file for it
	// Lacks is what the without= filter returns for the library: the grid's titles of the library's media kind
	// (with or without a copy there) minus those with a file there.
	Lacks     int `json:"lacks,omitempty"`
	Wanted    int `json:"wanted,omitempty"`
	Filled    int `json:"filled,omitempty"`
	CutoffMet int `json:"cutoff_met,omitempty"`
}

// TitleFacets is the facet counts of one grid.
type TitleFacets struct {
	Total       int `json:"total"`
	Monitored   int `json:"monitored"`
	Unmonitored int `json:"unmonitored"`
	// Availability counts titles per status word (titleAvailabilitySQL); without see files, per watchability.
	Availability map[string]int `json:"availability"`
	// SeriesStatus counts series per Series status (continuing, ended); absent for movies.
	SeriesStatus map[string]int `json:"series_status,omitempty"`
	// Upgrades counts titles per quality filter (found); see files only.
	Upgrades  map[string]int `json:"upgrades,omitempty"`
	Versions  []VersionFacet `json:"versions"`
	Qualities []FacetCount   `json:"qualities"`
	Genres    []FacetCount   `json:"genres"`
	// Certifications are US ratings (title_meta).
	Certifications []FacetCount `json:"certifications"`
	// Tags counts titles per tag label on a copy in the library; for callers who see files, absent otherwise.
	Tags []FacetCount `json:"tags,omitempty"`
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
	out := TitleFacets{Availability: map[string]int{}, Versions: []VersionFacet{}, Qualities: []FacetCount{}, Genres: []FacetCount{}, Certifications: []FacetCount{}}
	var ymin, ymax sql.NullInt64
	var movies int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(t.monitored), 0), MIN(t.year), MAX(t.year),
		COALESCE(SUM(t.kind = 'movie'), 0)`+titleFrom+where, args...).Scan(&out.Total, &out.Monitored, &ymin, &ymax,
		&movies); err != nil {
		return out, err
	}
	// The grid's titles by media kind, for each library's Lacks count.
	byKind := map[string]int{"movie": movies, "series": out.Total - movies}
	out.Unmonitored = out.Total - out.Monitored
	out.YearMin, out.YearMax = nullInt(ymin), nullInt(ymax)

	// Versions and availability, from every version of the grid's titles.
	states := map[int64][]versionState{}
	kinds := map[int64]string{}
	keys := map[string]*VersionFacet{}
	// The versions' availability state only for watchability (the household path); those who see files get the
	// title rule below.
	avail := `''`
	if !seeFiles {
		avail = availStateSQL(newAirClock(w.now()))
	}
	err := scanAll(ctx, db, `SELECT t.id, t.kind, v.policy_key, v.state, v.monitored, `+hasFileSQL+`, `+currentFileSQL+`, `+
		avail+titleFrom+` JOIN version v ON v.title_id = t.id`+andWhere(where, liveVersion),
		args, func(r *sql.Rows) error {
			var tid int64
			var kind, key, state, avail string
			var has, current, mon bool
			if err := r.Scan(&tid, &kind, &key, &state, &mon, &has, &current, &avail); err != nil {
				return err
			}
			states[tid] = append(states[tid], versionState{avail, current, mon})
			kinds[tid] = kind
			vf := keys[key]
			if vf == nil {
				vf = &VersionFacet{Key: key, Label: w.versionLabel(key), Kind: w.libraryKind(key)}
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
	if seeFiles {
		// The status words and the quality filter, by the conditions the list filters by (library.go): each title's
		// word in one pass; a held title is Upgrade found when in the found set.
		found, err := w.upgradeFoundTitles(ctx, db)
		if err != nil {
			return out, err
		}
		for _, a := range titleAvailabilities {
			out.Availability[a] = 0
		}
		out.Upgrades = map[string]int{"found": 0}
		if err := scanAll(ctx, db, `SELECT t.id, `+titleAvailabilitySQL(newAirClock(w.now()))+titleFrom+where, args,
			func(r *sql.Rows) error {
				var id int64
				var a string
				if err := r.Scan(&id, &a); err != nil {
					return err
				}
				out.Availability[a]++
				if (a == "available" || a == "partial") && found[id] {
					out.Upgrades["found"]++
				}
				return nil
			}); err != nil {
			return out, err
		}
	} else {
		// Watchability (perms.go plainAvailability).
		out.Availability = map[string]int{"missing": 0, "partial": 0, "available": 0}
		for tid, s := range states {
			out.Availability[plainAvailability(kinds[tid], s)]++
		}
		out.Availability["none"] = out.Total - len(states)
	}
	for _, k := range sortedKeys(keys) {
		vf := *keys[k]
		vf.Lacks = out.Total - vf.WithFile
		if vf.Kind != "" {
			vf.Lacks = byKind[vf.Kind] - vf.WithFile
		}
		if !seeFiles {
			// Per-library file counts are file detail (docs/design/users.md): the facet keeps its titles only.
			vf.WithFile, vf.Lacks, vf.Wanted, vf.Filled, vf.CutoffMet = 0, 0, 0, 0, 0
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
	qualities := `SELECT t.id AS tid, COALESCE(l.quality, '') AS q` + titleFrom + ` JOIN link l ON l.title_id = t.id` +
		andWhere(where, "l.state = 'active' AND "+liveLink)
	if seeFiles {
		if out.Qualities, err = counts(`SELECT q, COUNT(DISTINCT tid) FROM (`+qualities+`) GROUP BY q`, args); err != nil {
			return out, err
		}
	} else if out.Qualities, err = w.resolutionFacets(ctx, db, `SELECT DISTINCT q, tid FROM (`+qualities+`)`, args); err != nil {
		return out, err
	}
	if out.Genres, err = counts(`SELECT g.value, COUNT(*)`+titleFrom+`, json_each(m.genres) g`+where+` GROUP BY g.value`,
		args); err != nil {
		return out, err
	}
	if f.Kind != "movie" {
		// Series per stored status (lower case), summed into the Series status values.
		out.SeriesStatus = map[string]int{"continuing": 0, "upcoming": 0, "ended": 0}
		if err := scanAll(ctx, db, `SELECT `+seriesStatusSQL+`, COUNT(*)`+titleFrom+andWhere(where, "t.kind = 'series'")+` GROUP BY 1`,
			args, func(r *sql.Rows) error {
				var st string
				var n int
				if err := r.Scan(&st, &n); err != nil {
					return err
				}
				for v, sts := range seriesStatuses {
					if slices.Contains(sts, st) {
						out.SeriesStatus[v] += n
					}
				}
				return nil
			}); err != nil {
			return out, err
		}
	}
	if out.Certifications, err = counts(`SELECT COALESCE(m.certification, ''), COUNT(*)`+titleFrom+where+
		` GROUP BY 1`, args); err != nil {
		return out, err
	}
	if seeFiles {
		if out.Tags, err = counts(`SELECT g.label, COUNT(DISTINCT t.id)`+titleFrom+` JOIN version v ON v.title_id = t.id
			JOIN arrview_title_tag tt ON tt.title_id = t.id AND tt.version = v.policy_key JOIN arrview_tag g ON g.id = tt.tag_id`+
			andWhere(where, liveVersion)+` GROUP BY g.id, g.label`, args); err != nil {
			return out, err
		}
		// As the rows' pills and the provider filter: providers holding a current file of a version in the library
		// now (providerHolds; a link's torrent is its hash, heldJoin), counted by cache code.
		titles := map[string]map[int64]bool{}
		err := scanAll(ctx, db, `SELECT DISTINCT cp.provider AS p, t.id AS tid`+titleFrom+` JOIN link l ON l.title_id = t.id
				JOIN `+heldJoin+
			andWhere(where, "l.state = 'active' AND "+liveLink+" AND "+providerHolds), args, func(r *sql.Rows) error {
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
