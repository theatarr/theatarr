// Port of Sonarr DataAugmentation/Scene/SceneMappingService.cs:135-182,282-308,
// DataAugmentation/Xem/XemProxy.cs:21-124 and XemService.cs:36-210,235 @ cab419a.
package tvrefresh

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/store"
)

const sceneJob = "meta.scene"
const sceneAge = 3 * time.Hour

type sceneName struct {
	Title       string `json:"title"`
	SearchTitle string `json:"searchTitle"`
	TVDB        int    `json:"tvdbId"`
	Season      *int   `json:"season"`
	SceneSeason *int   `json:"sceneSeasonNumber"`
	Origin      string `json:"sceneOrigin"`
	Comment     string `json:"comment"`
}

type xemNumber struct {
	Season   int `json:"season"`
	Episode  int `json:"episode"`
	Absolute int `json:"absolute"`
}

type xemMapping struct {
	TVDB  xemNumber  `json:"tvdb"`
	Scene *xemNumber `json:"scene"`
}

// sceneGet persists successful responses for three hours, across restarts. Failed reads never replace mappings.
// Requests use the metadata target so the configured allowlist, budget, egress and back-offs apply.
func (s *Service) sceneGet(ctx context.Context, source, path string, out any) error {
	if s.pol.Metadata == nil || s.pol.Metadata.Sources[source] == nil {
		return errors.New("scene source not configured")
	}
	key := source + path
	var at int64
	var data []byte
	err := s.st.Reader().QueryRowContext(ctx, `SELECT refreshed_at, data FROM scene_refresh WHERE source = ?`, key).Scan(&at, &data)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && at > store.Millis(s.now().Add(-sceneAge)) {
		return json.Unmarshal(data, out)
	}
	previous := data
	if s.sceneDoer == nil {
		g, err := gateway.New(ctx, s.pol, gateway.Options{Store: s.st, Logger: s.log, Now: s.now})
		if err != nil {
			return err
		}
		s.sceneDoer = g
	}
	base := strings.TrimRight(s.pol.Metadata.Sources[source].URL, "/")
	if source == "thexem" && !strings.HasSuffix(base, "/map") {
		base += "/map"
	}
	if source == "sonarr" && !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return errors.New("scene: invalid source URL")
	}
	resp, err := s.sceneDoer.Do(ctx, gateway.Target{Class: gateway.ClassMetadata, Key: source, MaxBytes: 32 << 20}, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("scene %s: HTTP %d", source, resp.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 32<<20 {
		return errors.New("scene: response too large")
	}
	if source == "thexem" {
		var result struct{ Result, Message string }
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		if strings.EqualFold(result.Result, "failure") && !strings.Contains(result.Message, "no single connection") && !strings.Contains(result.Message, "no show with the tvdb_id") {
			return errors.New("scene: XEM failure response")
		}
		if path == "/havemap?origin=tvdb" && len(previous) > 0 {
			var ids struct{ Data []json.RawMessage }
			if err := json.Unmarshal(data, &ids); err != nil {
				return err
			}
			// XemService.cs:213-225 extends the previous id list when XEM returns none.
			if len(ids.Data) == 0 {
				data = previous
			}
		}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return err
	}
	_, err = s.st.Writer().ExecContext(ctx, `INSERT INTO scene_refresh (source, refreshed_at, data) VALUES (?, ?, ?)
  ON CONFLICT(source) DO UPDATE SET refreshed_at = excluded.refreshed_at, data = excluded.data`, key, store.Millis(s.now()), data)
	return err
}

func (s *Service) sceneNames(ctx context.Context, id int64, tvdb int, source string, names []sceneName) error {
	// Sonarr keeps the previous provider's names when it returns an empty list.
	if len(names) == 0 {
		return nil
	}
	return s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := s.exec(ctx, tx, `DELETE FROM title_scene_mapping WHERE title_id = ? AND source = ?`, id, "tvrefresh:"+source); err != nil {
			return err
		}
		for _, n := range names {
			if n.TVDB != tvdb || strings.TrimSpace(n.Title) == "" || strings.TrimSpace(n.SearchTitle) == "" {
				continue
			}
			if err := s.exec(ctx, tx, `INSERT OR IGNORE INTO title_scene_mapping
    (title_id, source, name, season_number, scene_season_number, scene_origin, comment) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				id, "tvrefresh:"+source, n.Title, n.Season, n.SceneSeason, n.Origin, n.Comment); err != nil {
				return err
			}
		}
		return nil
	})
}

// RefreshScene refreshes all catalog series independently of their TVDB refresh cadence.
func (s *Service) RefreshScene(ctx context.Context) error { return s.refreshScene(ctx, 0) }

func (s *Service) refreshScene(ctx context.Context, titleID int64) error {
	s.sceneMu.Lock()
	defer s.sceneMu.Unlock()
	if s.pol.Metadata == nil {
		return nil
	}
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT id, tvdb_id FROM title WHERE kind = 'series' AND tvdb_id IS NOT NULL AND (? = 0 OR id = ?)`, titleID, titleID)
	if err != nil {
		return err
	}
	type series struct {
		id   int64
		tvdb int
	}
	var titles []series
	for rows.Next() {
		var t series
		if err := rows.Scan(&t.id, &t.tvdb); err != nil {
			rows.Close()
			return err
		}
		titles = append(titles, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(titles) == 0 {
		return nil
	}
	var errs []error
	if s.pol.Metadata.Sources["sonarr"] != nil {
		var names []sceneName
		if err := s.sceneGet(ctx, "sonarr", "/scenemapping", &names); err != nil {
			errs = append(errs, err)
		} else {
			for _, t := range titles {
				if err := s.sceneNames(ctx, t.id, t.tvdb, "sonarr", names); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	if s.pol.Metadata.Sources["thexem"] == nil {
		return errors.Join(errs...)
	}
	var named struct {
		Data map[string][]map[string]json.RawMessage `json:"data"`
	}
	if err := s.sceneGet(ctx, "thexem", "/allNames?seasonNumbers=true&origin=tvdb", &named); err != nil {
		errs = append(errs, err)
	} else {
		var names []sceneName
		for key, entries := range named.Data {
			tvdb, _ := strconv.Atoi(key)
			for _, entry := range entries {
				for name, raw := range entry {
					n, err := strconv.Atoi(strings.Trim(string(raw), `"`))
					if err != nil || tvdb == 79151 && n > 1 {
						continue
					}
					names = append(names, sceneName{Title: name, SearchTitle: name, TVDB: tvdb, SceneSeason: &n})
				}
			}
		}
		for _, t := range titles {
			if err := s.sceneNames(ctx, t.id, t.tvdb, "thexem", names); err != nil {
				errs = append(errs, err)
			}
		}
	}
	var mapped struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := s.sceneGet(ctx, "thexem", "/havemap?origin=tvdb", &mapped); err != nil {
		return errors.Join(append(errs, err)...)
	}
	have := map[int]bool{}
	for _, raw := range mapped.Data {
		n, _ := strconv.Atoi(strings.Trim(string(raw), `"`))
		have[n] = true
	}
	for _, t := range titles {
		var used bool
		if err := s.st.Reader().QueryRowContext(ctx, `SELECT use_scene_numbering FROM title WHERE id = ?`, t.id).Scan(&used); err != nil {
			errs = append(errs, err)
			continue
		}
		if !have[t.tvdb] && !used {
			continue
		}
		var result struct {
			Data []xemMapping `json:"data"`
		}
		if err := s.sceneGet(ctx, "thexem", fmt.Sprintf("/all?id=%d&origin=tvdb", t.tvdb), &result); err != nil {
			errs = append(errs, err)
			continue
		}
		var mappings []xemMapping
		for _, m := range result.Data {
			if m.Scene != nil {
				mappings = append(mappings, m)
			}
		}
		if err := s.sceneNumbers(ctx, t.id, mappings); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) sceneNumbers(ctx context.Context, id int64, mappings []xemMapping) error {
	return s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		// Imported scene numbers must also be replaced; absolute TVDB/anime numbers are separate.
		if err := s.exec(ctx, tx, `DELETE FROM episode_number WHERE title_id = ? AND scheme IN ('scene', 'scene_absolute')`, id); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id, season, number FROM episode WHERE title_id = ? AND removed_at IS NULL`, id)
		if err != nil {
			return err
		}
		type ep struct {
			id   int64
			slot slot
		}
		var eps []ep
		for rows.Next() {
			var e ep
			if err := rows.Scan(&e.id, &e.slot.season, &e.slot.number); err != nil {
				rows.Close()
				return err
			}
			eps = append(eps, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		assigned := map[slot]xemNumber{}
		absoluteKnown := map[slot]bool{}
		mappedSeasons := map[int]bool{}
		extrapolate := false
		lastTVDB, lastScene := 0, 0
		for _, m := range mappings {
			lastTVDB = max(lastTVDB, m.TVDB.Season)
			lastScene = max(lastScene, m.Scene.Season)
			if *m.Scene == (xemNumber{}) {
				continue
			}
			for _, e := range eps {
				if e.slot == (slot{m.TVDB.Season, m.TVDB.Episode}) {
					assigned[e.slot] = *m.Scene
					absoluteKnown[e.slot] = true
					extrapolate = extrapolate || m.Scene.Season != 0
					if e.slot.season > 0 {
						mappedSeasons[e.slot.season] = true
					}
				}
			}
		}
		// XemService.cs:100-210: extrapolate only beyond the last mapping, in the last mapped TVDB season or later.
		if extrapolate {
			for _, e := range eps {
				if _, ok := assigned[e.slot]; ok || e.slot.season == 0 || e.slot.season < lastTVDB {
					continue
				}
				var last *xemMapping
				later := false
				for i := range mappings {
					m := &mappings[i]
					if m.TVDB.Season != e.slot.season {
						continue
					}
					if m.TVDB.Episode >= e.slot.number {
						later = true
					}
					if last == nil || m.TVDB.Episode > last.TVDB.Episode {
						last = m
					}
				}
				if later {
					continue
				}
				if last != nil && mappedSeasons[e.slot.season] {
					tail := last
					for i := range mappings {
						m := &mappings[i]
						if m.Scene.Season == last.Scene.Season && m.Scene.Episode > tail.Scene.Episode {
							tail = m
						}
					}
					if tail.TVDB.Season == 0 {
						continue
					}
					offset := e.slot.number - last.TVDB.Episode
					assigned[e.slot] = xemNumber{last.Scene.Season, last.Scene.Episode + offset, last.Scene.Absolute + offset}
					absoluteKnown[e.slot] = true
				} else if last == nil && lastTVDB != lastScene && e.slot.season > lastTVDB {
					assigned[e.slot] = xemNumber{Season: lastScene + e.slot.season - lastTVDB, Episode: e.slot.number}
				}
			}
		}
		for _, e := range eps {
			n, ok := assigned[e.slot]
			if !ok {
				continue
			}
			if err := s.exec(ctx, tx, `INSERT INTO episode_number (episode_id, title_id, scheme, season, number, source)
    VALUES (?, ?, 'scene', ?, ?, 'tvrefresh:thexem')`, e.id, id, n.Season, n.Episode); err != nil {
				return err
			}
			if absoluteKnown[e.slot] {
				if err := s.exec(ctx, tx, `INSERT INTO episode_number (episode_id, title_id, scheme, number, source)
     VALUES (?, ?, 'scene_absolute', ?, 'tvrefresh:thexem')`, e.id, id, n.Absolute); err != nil {
					return err
				}
			}
		}
		return s.exec(ctx, tx, `UPDATE title SET use_scene_numbering = ? WHERE id = ?`, len(mappings) > 0, id)
	})
}
