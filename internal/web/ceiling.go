package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/ceiling"
	"github.com/theatarr/theatarr/internal/store"
)

type ceilingKey struct{ versionID, season int64 }
type ceilingView struct {
	info   *ceiling.Info
	cutoff string
}

func (w *Web) loadCeilings(ctx context.Context, db *sql.DB, vs []upgradeVersion, s *upgradeSet) error {
	s.ceilings = map[ceilingKey]ceilingView{}
	var ids []any
	for _, v := range vs {
		ids = append(ids, v.id)
	}
	has := map[int64]bool{}
	for start := 0; start < len(ids); start += 900 {
		batch := ids[start:min(start+900, len(ids))]
		err := scanAll(ctx, db, `SELECT version_id,season,effective_cutoff,ceiling FROM version_ceiling
 WHERE version_id IN (`+store.Placeholders(len(batch))+`)`, batch, func(rows *sql.Rows) error {
			var k ceilingKey
			var cutoff, raw string
			if err := rows.Scan(&k.versionID, &k.season, &cutoff, &raw); err != nil {
				return err
			}
			var info ceiling.Info
			if err := json.Unmarshal([]byte(raw), &info); err != nil {
				return err
			}
			s.ceilings[k] = ceilingView{&info, cutoff}
			has[k.versionID] = true
			return nil
		})
		if err != nil {
			return err
		}
	}
	var profiles []ceiling.ProfileVersion
	for _, v := range vs {
		if has[v.id] {
			profiles = append(profiles, ceiling.ProfileVersion{ID: v.id, Key: v.key, Kind: v.kind})
		}
	}
	var err error
	s.profiles, err = ceiling.Profiles(ctx, db, w.pol, profiles)
	return err
}

func (s *upgradeSet) ceilingItem(v upgradeVersion, season int64, f itemFile) (string, ceilingView) {
	cv := s.ceilings[ceilingKey{v.id, season}]
	if cv.info == nil || cv.info.Tier == "unknown" {
		return "below_cutoff", cv
	}
	return ceiling.State(cv.info, s.profiles[v.id], f.quality, v.kind), cv
}

func (s *upgradeSet) applyCeiling(v upgradeVersion, episode int64, u *UpgradeInfo) {
	if v.kind != "series" {
		cv := s.ceilings[ceilingKey{v.id, -1}]
		u.Ceiling, u.EffectiveCutoff = cv.info, cv.cutoff
		if u.State == "below_cutoff" {
			u.State, _ = s.ceilingItem(v, -1, s.files[v.id][0])
		}
		return
	}
	states := map[string]bool{}
	var cv ceilingView
	seasons := map[int64]bool{}
	for _, e := range s.episodes[v.id] {
		if episode > 0 && e.id != episode {
			continue
		}
		seasons[e.season] = true
		cv = s.ceilings[ceilingKey{v.id, e.season}]
		f, ok := s.files[v.id][e.id]
		if !ok {
			continue
		}
		if e.state == "filled" {
			state, _ := s.ceilingItem(v, e.season, f)
			states[state] = true
		}
	}
	if len(seasons) == 1 {
		u.Ceiling, u.EffectiveCutoff = cv.info, cv.cutoff
	}
	if u.State != "below_cutoff" {
		return
	}
	for _, state := range []string{"higher_source_exists", "below_cutoff", "best_source_seen", "best_available"} {
		if states[state] {
			u.State = state
			return
		}
	}
}

// ceilingScope validates title/season scope so movie overrides never silently apply to a series' whole title.
func (w *Web) ceilingScope(rw http.ResponseWriter, r *http.Request, season *int) (int64, int, bool) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return 0, 0, false
	}
	var kind string
	err := w.st.Reader().QueryRowContext(r.Context(), `SELECT kind FROM title WHERE id=?`, id).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return 0, 0, false
	}
	if err != nil {
		w.internal(rw, err)
		return 0, 0, false
	}
	if kind == "movie" {
		if season != nil {
			badRequest(rw, "movies have no season")
			return 0, 0, false
		}
		return id, -1, true
	}
	if season == nil || *season < 0 {
		badRequest(rw, "a series ceiling needs a season")
		return 0, 0, false
	}
	var n int
	if err := w.st.Reader().QueryRowContext(r.Context(), `SELECT COUNT(*) FROM episode WHERE title_id=? AND season=?`, id, *season).Scan(&n); err != nil {
		w.internal(rw, err)
		return 0, 0, false
	}
	if n == 0 {
		notFound(rw)
		return 0, 0, false
	}
	return id, *season, true
}

type ceilingOverrideRequest struct {
	Season    *int       `json:"season,omitempty"`
	Tier      string     `json:"tier"`
	Note      string     `json:"note,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (w *Web) apiSetCeiling(rw http.ResponseWriter, r *http.Request) {
	var req ceilingOverrideRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	if !ceiling.ValidTier(req.Tier) {
		badRequest(rw, "invalid ceiling tier")
		return
	}
	if len(req.Note) > 2000 {
		badRequest(rw, "ceiling note is too long")
		return
	}
	now := w.now()
	if req.ExpiresAt != nil && !req.ExpiresAt.After(now) {
		badRequest(rw, "expires_at must be in the future")
		return
	}
	id, season, ok := w.ceilingScope(rw, r, req.Season)
	if !ok {
		return
	}
	var by, until any
	if who := auth.FromContext(r.Context()); who != nil {
		by = who.UserID
	}
	if req.ExpiresAt != nil {
		until = req.ExpiresAt.UnixMilli()
	}
	err := w.st.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO source_ceiling(title_id,season,override_tier,override_note,override_by,override_at,override_until)
   VALUES(?,?,?,?,?,?,?) ON CONFLICT(title_id,season) DO UPDATE SET override_tier=excluded.override_tier,override_note=excluded.override_note,
   override_by=excluded.override_by,override_at=excluded.override_at,override_until=excluded.override_until`, id, season, req.Tier, req.Note, by, now.UnixMilli(), until); err != nil {
			return err
		}
		return ceiling.Compute(r.Context(), tx, w.pol, id, now)
	})
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]bool{"ok": true})
}
func (w *Web) apiClearCeiling(rw http.ResponseWriter, r *http.Request) {
	var season *int
	if raw := r.URL.Query().Get("season"); raw != "" {
		s, err := strconv.Atoi(raw)
		if err != nil || s < 0 {
			badRequest(rw, "season must be a non-negative integer")
			return
		}
		season = &s
	}
	id, s, ok := w.ceilingScope(rw, r, season)
	if !ok {
		return
	}
	err := w.st.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), `UPDATE source_ceiling SET override_tier=NULL,override_note='',override_by=NULL,override_at=NULL,override_until=NULL WHERE title_id=? AND season=?`, id, s); err != nil {
			return err
		}
		return ceiling.Compute(r.Context(), tx, w.pol, id, w.now())
	})
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]bool{"ok": true})
}

// Confidence and evidence require manage, even if an account has been granted see_files separately.
func redactCeiling(u *UpgradeInfo) {
	if u == nil {
		return
	}
	u.Ceiling = nil
	u.EffectiveCutoff = ""
	switch u.State {
	case "best_available", "best_source_seen", "higher_source_exists":
		u.State = "below_cutoff"
	}
}
func redactEpisodeCeilings(out *EpisodeList) {
	for i := range out.Seasons {
		for k, c := range out.Seasons[i].Versions {
			redactCeiling(c.Upgrade)
			out.Seasons[i].Versions[k] = c
		}
	}
	for i := range out.Episodes {
		for k, c := range out.Episodes[i].Versions {
			redactCeiling(c.Upgrade)
			out.Episodes[i].Versions[k] = c
		}
	}
}
