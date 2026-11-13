package tvrefresh

import (
	"context"
	"database/sql"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
)

// sceneSource cannot be an arr instance key (policy keys do not allow colons).
const sceneSource = "tvrefresh:tvdb"

// sceneMappings replaces only our names. The title itself and imported mappings take precedence, including season limits.
// Keep the display name; compare with the same CleanSeriesTitle that decide and Sonarr's SceneMappingService use.
func (s *Service) sceneMappings(ctx context.Context, tx *sql.Tx, titleID int64, names []string) error {
	if err := s.exec(ctx, tx, `DELETE FROM title_scene_mapping WHERE title_id = ? AND source = ?`, titleID,
		sceneSource); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM title WHERE id = ? UNION ALL
		SELECT name FROM title_scene_mapping WHERE title_id = ?`, titleID, titleID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		seen[sonarr.CleanSeriesTitle(name)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// A name another series already answers to stays theirs: an alias must not send its releases here.
	rows, err = tx.QueryContext(ctx, `SELECT name FROM title WHERE id != ? AND kind = 'series' UNION ALL
		SELECT name FROM title_scene_mapping WHERE title_id != ?`, titleID, titleID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		seen[sonarr.CleanSeriesTitle(name)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		clean := sonarr.CleanSeriesTitle(name)
		if clean == "" || seen[clean] {
			continue
		}
		seen[clean] = true
		if err := s.exec(ctx, tx, `INSERT INTO title_scene_mapping (title_id, source, name, scene_origin)
			VALUES (?, ?, ?, 'tvdb')`, titleID, sceneSource, name); err != nil {
			return err
		}
	}
	return nil
}
