package importarr

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/store"
)

// Conflicts lists titles that several instances map to the same version but with different files (or a file in
// one and none in another). Files count as the same when their sizes match and they share a content, a release
// title, a scene name or a file name.
func Conflicts(ctx context.Context, st *store.Store) ([]Conflict, error) {
	rows, err := st.Reader().QueryContext(ctx, `
		SELECT a.version_id, v.policy_key, t.id, COALESCE(t.tmdb_id, 0), t.name, a.instance, a.arr_id,
		       f.id IS NOT NULL, COALESCE(f.size_bytes, 0), COALESCE(f.relative_path, ''), COALESCE(f.scene_name, ''),
		       COALESCE(f.release_title, ''), COALESCE(f.content_id, 0), COALESCE(f.quality, '')
		FROM arr_title a
		JOIN version v ON v.id = a.version_id
		JOIN title t ON t.id = a.title_id
		LEFT JOIN version_file f ON f.instance = a.instance AND f.version_id = a.version_id
		WHERE a.kind = 'movie'
		  AND a.version_id IN (SELECT version_id FROM arr_title GROUP BY version_id HAVING COUNT(DISTINCT instance) > 1)
		ORDER BY v.policy_key, t.tmdb_id, a.instance`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type side struct {
		instance                     string
		arrID                        int
		hasFile                      bool
		size                         int64
		rel, scene, release, quality string
		content                      int64
	}
	type group struct {
		c     Conflict
		sides []side
	}
	var order []int64
	groups := map[int64]*group{}
	for rows.Next() {
		var vid int64
		var g Conflict
		var s side
		if err := rows.Scan(&vid, &g.Version, &g.TitleID, &g.TmdbID, &g.Name, &s.instance, &s.arrID, &s.hasFile, &s.size,
			&s.rel, &s.scene, &s.release, &s.content, &s.quality); err != nil {
			return nil, err
		}
		gr := groups[vid]
		if gr == nil {
			gr = &group{c: g}
			groups[vid] = gr
			order = append(order, vid)
		}
		gr.sides = append(gr.sides, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sameFile := func(a, b side) bool {
		if a.size != b.size {
			return false
		}
		return (a.content != 0 && a.content == b.content) ||
			(a.release != "" && a.release == b.release) ||
			(a.scene != "" && a.scene == b.scene) ||
			(a.rel != "" && path.Base(a.rel) == path.Base(b.rel))
	}
	var out []Conflict
	for _, vid := range order {
		gr := groups[vid]
		first := gr.sides[0]
		var detail string
		for _, s := range gr.sides[1:] {
			switch {
			case first.hasFile != s.hasFile:
				detail = "a file in one instance, none in another"
			case first.hasFile && !sameFile(first, s):
				detail = "different files"
			}
			if detail != "" {
				break
			}
		}
		if detail == "" {
			continue
		}
		c := gr.c
		c.Detail = detail
		for _, s := range gr.sides {
			if !s.hasFile {
				c.Sides = append(c.Sides, fmt.Sprintf("%s movie %d: no file", s.instance, s.arrID))
				continue
			}
			name := s.release
			if name == "" {
				name = path.Base(s.rel)
			}
			c.Sides = append(c.Sides, fmt.Sprintf("%s movie %d: %s, %d bytes, %s", s.instance, s.arrID, s.quality, s.size, name))
		}
		out = append(out, c)
	}
	return out, nil
}

// VersionReport counts each version's state over every instance.
func VersionReport(ctx context.Context, st *store.Store) ([]VersionCounts, error) {
	db := st.Reader()
	rows, err := db.QueryContext(ctx, `
		SELECT v.policy_key, COUNT(*), SUM(v.monitored), SUM(v.state = 'wanted'), SUM(v.state = 'filled'),
		       SUM(v.state = 'cutoff_met')
		FROM version v GROUP BY v.policy_key ORDER BY v.policy_key`)
	if err != nil {
		return nil, err
	}
	var out []VersionCounts
	for rows.Next() {
		var v VersionCounts
		if err := rows.Scan(&v.Version, &v.Titles, &v.Monitored, &v.Wanted, &v.Filled, &v.CutoffMet); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, v)
	}
	rows.Close()
	for i := range out {
		v := &out[i]
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(f.content_id) FROM version_file f
			JOIN version v ON v.id = f.version_id WHERE v.policy_key = ?`, v.Version).Scan(&v.Files, &v.FilesHash); err != nil {
			return nil, err
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT cv.content_id) FROM content_version cv
			JOIN version v ON v.id = cv.version_id WHERE v.policy_key = ?`, v.Version).Scan(&v.Contents); err != nil {
			return nil, err
		}
		var inst sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT GROUP_CONCAT(name) FROM (SELECT name FROM arr_instance
			WHERE policy_key = ? ORDER BY name)`, v.Version).Scan(&inst); err != nil {
			return nil, err
		}
		if inst.String != "" {
			v.Instances = strings.Split(inst.String, ",")
			sort.Strings(v.Instances)
		}
	}
	return out, nil
}

// Totals counts titles and contents in the database.
func Totals(ctx context.Context, st *store.Store) (titles, contents int, err error) {
	db := st.Reader()
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM title`).Scan(&titles); err != nil {
		return
	}
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM content`).Scan(&contents)
	return
}
