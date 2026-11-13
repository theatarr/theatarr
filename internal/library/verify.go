package library

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/library/naming"
	"github.com/theatarr/theatarr/internal/store"
)

// NameCheck compares the arr's file names in a version with the names we would give the same files.
type NameCheck struct {
	Files   int        `json:"files"`   // the version's arr files (version_file) under the root
	Shaped  int        `json:"shaped"`  // in the naming format's shape (the arr renamed them)
	Equal   int        `json:"equal"`   // our name equals the arr's
	Differs []NameDiff `json:"differs"` // the rest, at most 200
	Naming  string     `json:"naming"`
	Error   string     `json:"error,omitempty"`
}

// NameDiff is one file whose name we would give differently.
type NameDiff struct {
	Path string `json:"path"`
	Ours string `json:"ours"`
}

// Verify scans a version's roots read-only: counts, dangling links (with the candidate a relink would use), and
// the names that differ from ours. It writes nothing, to the filesystem or the database.
func (l *Library) Verify(ctx context.Context, version string) ([]*LinkScan, error) {
	if l.runtime != nil {
		ctx, active := l.runtime(ctx)
		return active.Verify(ctx, version)
	}
	var out []*LinkScan
	var ix *mountIndex
	mountErr := ""
	var zv *zurgView
	if l.set.MountPath == "" {
		mountErr = "library.mount.path is not set"
	} else if err := l.mountHealthy(ctx); err != nil {
		mountErr = err.Error()
	} else if ix, err = l.newMountIndex(ctx); err != nil {
		mountErr = err.Error()
	} else {
		zv = l.zurgView(ctx)
		if zv != nil {
			zv.ix = ix
		}
	}
	for _, k := range l.gate.Versions() {
		if version != "" && k != version {
			continue
		}
		for _, root := range l.gate.Roots(k) {
			sc := &LinkScan{Version: k, Root: root, Causes: map[string]int{}, DryRun: true, Mount: mountErr}
			zv.note(sc)
			if err := l.gate.Reachable(ctx, root); err != nil {
				l.skipRoot(sc, err)
				out = append(out, sc)
				continue
			}
			links, err := l.scanRoot(ctx, root, sc)
			if Unavailable(err) {
				l.skipRoot(sc, err)
				out = append(out, sc)
				continue
			}
			if err != nil {
				return out, err
			}
			if ix != nil {
				for _, ml := range links {
					ok, err := ix.has(ml.folder, ml.inner)
					if errors.Is(err, errFolderUnreadable) {
						continue // no verdict on a link into a folder the mount would not list
					}
					if err != nil {
						return out, err
					}
					if ok {
						continue
					}
					dl := DanglingLink{Path: ml.path, Target: ml.target, Cause: "unknown"}
					hash, names, err := l.candidates(ctx, l.st.Reader(), ml)
					if err != nil {
						return out, err
					}
					dl.Hash = hash
					for _, n := range names {
						if found, _ := ix.has(n, ml.inner); found {
							dl.Cause, dl.NewTarget = "renamed", l.mount.ToLink(path.Join(l.mount.AllPath(), n, ml.inner))
							break
						}
					}
					if dl.Cause == "unknown" {
						dl.Cause, dl.Reason = zv.classify(dl.Cause, hash, ml.folder)
					}
					sc.Dangling = append(sc.Dangling, dl)
					sc.Causes[dl.Cause]++
				}
			}
			sc.Names = l.checkNames(ctx, k, root)
			out = append(out, sc)
		}
	}
	return out, nil
}

// checkNames names every arr file of the version (version_file, with the arr's formatted media info) and compares.
// Custom formats are read back from the arr's own name, since the import does not store them per file.
func (l *Library) checkNames(ctx context.Context, version, root string) *NameCheck {
	nc := &NameCheck{}
	vn, err := NamingFor(l.pol, version)
	if err != nil {
		nc.Error = err.Error()
		return nc
	}
	nc.Naming = vn.Source
	vp := l.pol.Versions[version]
	db := l.st.Reader()
	rows, err := db.QueryContext(ctx, `
		SELECT vf.id, vf.version_id, vf.path, vf.relative_path, vf.quality, vf.quality_revision, vf.release_group, vf.scene_name,
		       vf.edition, vf.media_info, vf.original_file_path, t.name, COALESCE(t.year, 0), COALESCE(t.imdb_id, ''),
		       COALESCE(t.tmdb_id, 0), COALESCE(t.tvdb_id, 0), `+store.SeriesTypeSQL("t")+`
		FROM version_file vf JOIN version v ON v.id = vf.version_id JOIN title t ON t.id = vf.title_id
		WHERE v.policy_key = ? ORDER BY vf.id`, version)
	if err != nil {
		nc.Error = err.Error()
		return nc
	}
	type row struct {
		id, versionID                                                       int64
		p, rel, quality, group, scene, edition, mi, orig, name, imdb, stype string
		rev, year, tmdb, tvdb                                               int
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.versionID, &r.p, &r.rel, &r.quality, &r.rev, &r.group, &r.scene, &r.edition, &r.mi, &r.orig,
			&r.name, &r.year, &r.imdb, &r.tmdb, &r.tvdb, &r.stype); err != nil {
			rows.Close()
			nc.Error = err.Error()
			return nc
		}
		if strings.HasPrefix(r.p, root+"/") {
			all = append(all, r)
		}
	}
	rows.Close()
	for _, r := range all {
		nc.Files++
		base := path.Base(r.p)
		ext := path.Ext(r.p)
		q := naming.Quality{Title: r.quality, Version: max(r.rev, 1)}
		proper := ""
		if r.rev > 1 {
			proper = " Proper"
			if r.stype == "anime" {
				proper = " v" + strconv.Itoa(r.rev)
			}
		}
		media := mediaInfoFromAPI(r.mi, r.scene, r.orig, r.rel)
		cfs := cfFromName(base, q.Title+proper)
		var ours string
		if vp.Kind == "movie" {
			if !movieShaped.MatchString(base) {
				continue
			}
			nc.Shaped++
			n, err := naming.MovieFileName(&vn.Config, &naming.Movie{Title: r.name, Year: r.year, ImdbID: r.imdb, TmdbID: r.tmdb},
				&naming.MovieFile{Quality: q, ReleaseGroup: r.group, Edition: r.edition, SceneName: r.scene, RelativePath: r.rel,
					Path: r.p, MediaInfo: media, CustomFormats: cfs})
			if err != nil {
				nc.Error = err.Error()
				return nc
			}
			ours = n + ext
		} else {
			if !seriesShaped.MatchString(base) {
				continue
			}
			nc.Shaped++
			eps, err := loadFileEpisodes(ctx, db, r.id)
			if err != nil || len(eps) == 0 {
				continue
			}
			st := naming.SeriesType(r.stype)
			if st == "" {
				st = naming.SeriesStandard
			}
			n, err := naming.SeriesFileName(&vn.Config, &naming.Series{Title: r.name, Year: r.year, Type: st, ImdbID: r.imdb,
				TvdbID: r.tvdb, TmdbID: r.tmdb}, eps, &naming.EpisodeFile{ID: r.id, Quality: q, ReleaseGroup: r.group,
				SceneName: r.scene, RelativePath: r.rel, Path: r.p, MediaInfo: media, CustomFormats: cfs}, ext, path.Dir(r.p))
			if err != nil {
				nc.Error = err.Error()
				return nc
			}
			ours = n
		}
		if ours == base {
			nc.Equal++
		} else if len(nc.Differs) < 200 {
			nc.Differs = append(nc.Differs, NameDiff{Path: r.p, Ours: ours})
		}
	}
	return nc
}

func loadFileEpisodes(ctx context.Context, db *sql.DB, fileID int64) ([]naming.Episode, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT e.season, e.number, e.name, COALESCE(e.air_date, ''),
		       (SELECT n.number FROM episode_number n WHERE n.episode_id = e.id AND n.scheme = 'absolute_tvdb' ORDER BY n.id LIMIT 1)
		FROM version_file_episode vfe JOIN episode e ON e.id = vfe.episode_id
		WHERE vfe.version_file_id = ? ORDER BY e.season, e.number`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []naming.Episode
	for rows.Next() {
		var e naming.Episode
		var abs sql.NullInt64
		if err := rows.Scan(&e.Season, &e.Number, &e.Title, &e.AirDate, &abs); err != nil {
			return nil, err
		}
		if abs.Valid {
			v := int(abs.Int64)
			e.Absolute = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
