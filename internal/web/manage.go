package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Season and episode management (docs/design/manage.md): per-version controls on the title page (who owns a version's
// monitoring, whether a search would run), season and episode monitoring, season and episode search, the episode
// detail, and episode-scoped releases.
//
// Shadow ownership. A version an arr instance owns (version_source) while that instance is still imported
// (policy import.arr) has its monitoring and files rewritten from the arr every 15 minutes (arr.import), so Theatarr's
// controls show the arr's state read-only: every monitor write here refuses such a version ("Managed by <instance>
// until cutover"). Searches follow the existing rule (catalog.Searchable: mode active and router.enabled), and
// releases answer from what is stored unless the version is active, so a shadow version gets no writes and no
// indexer traffic from these controls.

func (w *Web) mountManageAPI(mux *http.ServeMux) {
	b := APIBase + "/titles/{id}"
	w.handle(mux, "PUT "+b+"/ceiling", SignedIn, auth.PermManage, w.apiSetCeiling)
	w.handle(mux, "DELETE "+b+"/ceiling", SignedIn, auth.PermManage, w.apiClearCeiling)
	w.handle(mux, "PUT "+b+"/seasons/{season}/monitor", SignedIn, auth.PermManage, w.apiMonitorSeason)
	w.handle(mux, "PUT "+b+"/episodes/monitor", SignedIn, auth.PermManage, w.apiMonitorEpisodes)
	w.handle(mux, "POST "+b+"/seasons/{season}/search", SignedIn, auth.PermManage, w.apiSearchSeason)
	w.handle(mux, "POST "+b+"/episodes/search", SignedIn, auth.PermManage, w.apiSearchEpisodes)
	w.handle(mux, "GET "+b+"/episodes/{eid}", SignedIn, auth.PermBrowse, w.apiEpisode)
	w.handle(mux, "GET "+b+"/episodes/{eid}/releases", SignedIn, auth.PermManage, w.apiEpisodeReleases)
}

// canManage is the permission these controls need: monitor, search and releases are for managers and admins
// (docs/design/users.md: the manage permission; the controls themselves are redacted with the "see files" tier).
func canManage(p Perms) bool { return p.Has(auth.PermManage) }

// VersionControl says what the title page's controls may do for one version, and why not.
type VersionControl struct {
	GrabState VersionGrabState `json:"grab_state"`
	// Mode is the version's policy mode: shadow or active.
	Mode string `json:"mode"`
	// ManagedBy is the arr instance that owns the version's monitoring and files until cutover; empty when Theatarr does.
	ManagedBy string `json:"managed_by,omitempty"`
	// CanEdit: monitoring may be changed here; EditReason says why not.
	CanEdit    bool   `json:"can_edit"`
	EditReason string `json:"edit_reason,omitempty"`
	// CanSearch: a search would run (mode active, router on, acquirer running); SearchReason says why not.
	CanSearch    bool   `json:"can_search"`
	SearchReason string `json:"search_reason,omitempty"`
	// LiveReleases: the releases panel may search the indexers now (mode active).
	LiveReleases bool `json:"live_releases"`
}

func redactControl(c *VersionControl) {
	if c == nil {
		return
	}
	c.ManagedBy = ""
	c.CanEdit, c.CanSearch, c.LiveReleases = false, false, false
	c.EditReason, c.SearchReason = noManage, noManage
}

const noManage = "Your account may not change this"

// managedBy is the instance that owns the version while it is still imported, or "" (policy.ManagedBy).
func (w *Web) managedBy(instance string) string {
	return w.pol.ManagedBy(instance)
}

// versionManagedBy is managedBy for a title's version by key ("" when the title lacks it).
func (w *Web) versionManagedBy(ctx context.Context, titleID int64, key string) (string, error) {
	var inst string
	err := w.st.Reader().QueryRowContext(ctx, `SELECT vs.instance FROM version v JOIN version_source vs
		ON vs.version_id = v.id WHERE v.title_id = ? AND v.policy_key = ?`, titleID, key).Scan(&inst)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return w.managedBy(inst), err
}

func managedReason(instance string) string {
	return policy.ManagedReason(instance)
}

// control builds a version's controls; source is the owning arr instance ("" when none).
func (w *Web) control(key, source string) *VersionControl {
	c := &VersionControl{GrabState: versionGrabState(w.pol, key), Mode: string(policy.ModeShadow), CanEdit: true}
	if w.pol != nil {
		if vp := w.pol.Versions[key]; vp != nil && vp.Mode != "" {
			c.Mode = string(vp.Mode)
		}
	}
	if m := w.managedBy(source); m != "" {
		c.ManagedBy, c.CanEdit, c.EditReason = m, false, managedReason(m)
	}
	c.SearchReason = w.searchBlock(key)
	c.CanSearch = c.SearchReason == ""
	c.LiveReleases = c.Mode == string(policy.ModeActive) && w.add.Acquirer != nil
	return c
}

// searchBlock is why a search for the version would not run, or "".
func (w *Web) searchBlock(key string) string {
	if w.pol == nil {
		return "No policy loaded"
	}
	if _, skipped := catalog.Searchable(w.pol, []string{key}); len(skipped) > 0 {
		return skipped[0].Reason
	}
	if w.add.Acquirer == nil {
		return "The acquirer is not running"
	}
	return ""
}

// manageVersion is one version of a title as these handlers see it.
type manageVersion struct {
	id        int64
	key       string
	state     string
	monitored bool
	source    string
}

func (w *Web) titleVersions(ctx context.Context, titleID int64) ([]manageVersion, error) {
	var out []manageVersion
	err := scanAll(ctx, w.st.Reader(), `SELECT v.id, v.policy_key, v.state, v.monitored, COALESCE(vs.instance, '')
		FROM version v LEFT JOIN version_source vs ON vs.version_id = v.id WHERE v.title_id = ? AND NOT EXISTS
		(SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id) ORDER BY v.policy_key`, []any{titleID},
		func(r *sql.Rows) error {
			var v manageVersion
			if err := r.Scan(&v.id, &v.key, &v.state, &v.monitored, &v.source); err != nil {
				return err
			}
			out = append(out, v)
			return nil
		})
	return out, err
}

// MediaInfo is a file's media as the arr reported it (mediaInfo), in brief.
type MediaInfo struct {
	Resolution        string  `json:"resolution,omitempty"`
	VideoCodec        string  `json:"video_codec,omitempty"`
	VideoBitDepth     int     `json:"video_bit_depth,omitempty"`
	VideoDynamicRange string  `json:"video_dynamic_range,omitempty"`
	AudioCodec        string  `json:"audio_codec,omitempty"`
	AudioChannels     float64 `json:"audio_channels,omitempty"`
	AudioLanguages    string  `json:"audio_languages,omitempty"`
	Subtitles         string  `json:"subtitles,omitempty"`
	RunTime           string  `json:"run_time,omitempty"`
}

func parseMedia(s string) *MediaInfo {
	var raw struct {
		Resolution            string  `json:"resolution"`
		VideoCodec            string  `json:"videoCodec"`
		VideoBitDepth         int     `json:"videoBitDepth"`
		VideoDynamicRangeType string  `json:"videoDynamicRangeType"`
		VideoDynamicRange     string  `json:"videoDynamicRange"`
		AudioCodec            string  `json:"audioCodec"`
		AudioChannels         float64 `json:"audioChannels"`
		AudioLanguages        string  `json:"audioLanguages"`
		Subtitles             string  `json:"subtitles"`
		RunTime               string  `json:"runTime"`
	}
	if s == "" || json.Unmarshal([]byte(s), &raw) != nil {
		return nil
	}
	m := &MediaInfo{Resolution: raw.Resolution, VideoCodec: raw.VideoCodec, VideoBitDepth: raw.VideoBitDepth,
		VideoDynamicRange: raw.VideoDynamicRangeType, AudioCodec: raw.AudioCodec, AudioChannels: raw.AudioChannels,
		AudioLanguages: raw.AudioLanguages, Subtitles: raw.Subtitles, RunTime: raw.RunTime}
	if m.VideoDynamicRange == "" {
		m.VideoDynamicRange = raw.VideoDynamicRange
	}
	if *m == (MediaInfo{}) {
		return nil
	}
	return m
}

// enrichManage adds the title page's upgrade explanation and controls to each version, and the media of a movie
// version's single current file. Redaction for callers without "see files" happens in redactVersion.
func (w *Web) enrichManage(ctx context.Context, db *sql.DB, d *TitleDetail) error {
	if len(d.Versions) == 0 {
		return nil
	}
	vs := make([]upgradeVersion, len(d.Versions))
	for i, v := range d.Versions {
		vs[i] = upgradeVersion{id: v.ID, titleID: d.ID, key: v.Key, kind: d.Kind, state: v.State}
	}
	s, err := w.loadUpgrades(ctx, db, vs, true)
	if err != nil {
		return err
	}
	for i := range d.Versions {
		v := &d.Versions[i]
		v.Upgrade = s.explain(vs[i], 0)
		v.Control = w.control(v.Key, v.Source)
		if c := v.Current; c != nil && c.Files == 1 && d.Kind == "movie" {
			var mi string
			err := db.QueryRowContext(ctx, `SELECT vf.media_info FROM version_file vf LEFT JOIN version_source vs
				ON vs.version_id = vf.version_id WHERE vf.version_id = ? AND `+curFile+` ORDER BY vf.added_at DESC LIMIT 1`,
				v.ID).Scan(&mi)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			c.Media = parseMedia(mi)
		}
	}
	return nil
}

// enrichEpisodes adds controls, cutoffs and upgrade counts to an episode list.
func (w *Web) enrichEpisodes(ctx context.Context, db *sql.DB, out *EpisodeList, seeFiles bool) error {
	if len(out.Versions) == 0 {
		return nil
	}
	mvs, err := w.titleVersions(ctx, out.TitleID)
	if err != nil {
		return err
	}
	source := map[int64]string{}
	for _, v := range mvs {
		source[v.id] = v.source
	}
	vs := make([]upgradeVersion, len(out.Versions))
	for i, v := range out.Versions {
		vs[i] = upgradeVersion{id: v.ID, titleID: out.TitleID, key: v.Key, kind: "series"}
	}
	s, err := w.loadUpgrades(ctx, db, vs, true)
	if err != nil {
		return err
	}
	seen := map[string]map[int64]bool{}
	for i := range out.Versions {
		v := &out.Versions[i]
		v.Control = w.control(v.Key, source[v.ID])
		v.Cutoff = s.cutoffs[v.ID]
		seen[v.Key] = s.episodeSeen(vs[i])
		if !seeFiles {
			redactControl(v.Control)
			if v.Cutoff != nil {
				v.Cutoff = &Cutoff{Quality: v.Cutoff.Quality, Upgrades: v.Cutoff.Upgrades}
			}
		}
	}
	for i := range out.Episodes {
		e := &out.Episodes[i]
		for k, c := range e.Versions {
			for _, v := range vs {
				if v.key == k {
					c.Upgrade = s.explain(v, e.ID)
					if !seeFiles {
						redactUpgrade(c.Upgrade)
					}
				}
			}
			e.Versions[k] = c
			if seen[k][e.ID] {
				c.UpgradeSeen = true
				e.Versions[k] = c
			}
		}
	}
	// Season figures per version: below cutoff, upgrades seen, monitored, size, and the quality and score most of
	// the season's files below cutoff have.
	type qs struct {
		q   string
		s   int64
		has bool
	}
	bySeason := map[int64]*Season{}
	for i := range out.Seasons {
		bySeason[out.Seasons[i].Season] = &out.Seasons[i]
	}
	counts := map[int64]map[string]map[qs]int{}
	for _, v := range out.Versions {
		files := s.files[v.ID]
		for _, ue := range s.episodes[v.ID] {
			se := bySeason[ue.season]
			if se == nil {
				continue
			}
			sc := se.Versions[v.Key]
			f, ok := files[ue.id]
			if ok {
				if seeFiles && f.size > 0 {
					n := f.size
					if sc.SizeBytes != nil {
						n += *sc.SizeBytes
					}
					sc.SizeBytes = &n
				}
				if ue.state == "filled" {
					sc.BelowCutoff++
					if seen[v.Key][ue.id] {
						sc.UpgradeSeen++
					}
					k := qs{q: f.quality}
					if f.score != nil {
						k.s, k.has = *f.score, true
					}
					if counts[ue.season] == nil {
						counts[ue.season] = map[string]map[qs]int{}
					}
					if counts[ue.season][v.Key] == nil {
						counts[ue.season][v.Key] = map[qs]int{}
					}
					counts[ue.season][v.Key][k]++
				}
			}
			se.Versions[v.Key] = sc
		}
	}
	for _, se := range bySeason {
		for _, v := range vs {
			scoped := *s
			scoped.episodes = map[int64][]upgradeEpisode{}
			for _, e := range s.episodes[v.id] {
				if e.season == se.Season {
					scoped.episodes[v.id] = append(scoped.episodes[v.id], e)
				}
			}
			c := se.Versions[v.key]
			c.Upgrade = scoped.explain(v, 0)
			if !seeFiles {
				redactUpgrade(c.Upgrade)
			}
			se.Versions[v.key] = c
		}
	}
	// Monitored episodes per season and version, from the episode list itself (all seasons are listed in Seasons
	// even when one season's episodes are asked for, so count from the state rows).
	err = scanAll(ctx, db, `SELECT s.version_id, e.season, SUM(s.monitored) FROM episode_version_state s JOIN episode e
		ON e.id = s.episode_id WHERE e.title_id = ? GROUP BY s.version_id, e.season`, []any{out.TitleID},
		func(r *sql.Rows) error {
			var vid, season int64
			var n int
			if err := r.Scan(&vid, &season, &n); err != nil {
				return err
			}
			for _, v := range out.Versions {
				if v.ID == vid && bySeason[season] != nil {
					sc := bySeason[season].Versions[v.Key]
					sc.Monitored = n
					bySeason[season].Versions[v.Key] = sc
				}
			}
			return nil
		})
	if err != nil {
		return err
	}
	for season, byKey := range counts {
		for key, m := range byKey {
			var best qs
			for k, n := range m {
				if n > m[best] || (n == m[best] && k.q < best.q) {
					best = k
				}
			}
			sc := bySeason[season].Versions[key]
			sc.Quality = best.q
			if best.has && seeFiles {
				x := best.s
				sc.Score = &x
			}
			bySeason[season].Versions[key] = sc
		}
	}
	return nil
}

// MonitorRequest is the body of the season and episode monitor routes.
type MonitorRequest struct {
	Monitored *bool `json:"monitored"`
	// Versions are the version keys to change; absent means every version of the title Theatarr may change.
	Versions []string `json:"versions"`
	// EpisodeIDs are the episodes (PUT episodes/monitor only).
	EpisodeIDs []int64 `json:"episode_ids"`
}

// ManageOutcome is what a monitor change did: the versions changed, the ones refused and why.
type ManageOutcome struct {
	Versions []string       `json:"versions"`
	Skipped  []catalog.Skip `json:"skipped"`
	Episodes int            `json:"episodes"`
	Message  string         `json:"message"`
	// Error repeats the message when nothing was changed (409), as every API error carries one.
	Error string `json:"error,omitempty"`
}

// SearchRequest is the body of the season and episode search routes.
type SearchRequest struct {
	Versions   []string `json:"versions"`
	EpisodeIDs []int64  `json:"episode_ids"`
}

// seriesTitle reads a series title for these handlers; it answers 404, 400 or 403 itself and returns nil then.
func (w *Web) seriesTitle(rw http.ResponseWriter, r *http.Request, write bool) *titleBits {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return nil
	}
	if write && !canManage(w.permsOf(r)) {
		writeJSON(rw, http.StatusForbidden, apiError{Error: noManage})
		return nil
	}
	t, err := w.findTitle(r.Context(), id, "", 0)
	if err != nil {
		w.internal(rw, err)
		return nil
	}
	if t == nil {
		notFound(rw)
		return nil
	}
	if t.kind != policy.KindSeries {
		badRequest(rw, "seasons and episodes are for series")
		return nil
	}
	if w.pol == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "no policy loaded"})
		return nil
	}
	return t
}

// pickVersions resolves the versions a request names (all of the title's when none), refusing keys the title lacks.
func pickVersions(all []manageVersion, keys []string) ([]manageVersion, error) {
	if keys == nil {
		return all, nil
	}
	if len(keys) == 0 {
		return nil, errors.New("pick at least one version")
	}
	var out []manageVersion
	for _, k := range keys {
		i := slices.IndexFunc(all, func(v manageVersion) bool { return v.key == k })
		if i < 0 {
			return nil, fmt.Errorf("the title has no version %q", k)
		}
		out = append(out, all[i])
	}
	return out, nil
}

// episodesOf checks that the ids are the title's episodes (and, for season >= 0, in that season).
func (w *Web) episodesOf(ctx context.Context, titleID int64, ids []int64) error {
	if len(ids) == 0 {
		return errors.New("episode_ids is required")
	}
	args := []any{titleID}
	for _, id := range ids {
		args = append(args, id)
	}
	var n int
	if err := w.st.Reader().QueryRowContext(ctx, `SELECT COUNT(DISTINCT id) FROM episode WHERE title_id = ? AND id IN (`+
		store.Placeholders(len(ids))+`)`, args...).Scan(&n); err != nil {
		return err
	}
	uniq := slices.Clone(ids)
	slices.Sort(uniq)
	if n != len(slices.Compact(uniq)) {
		return errNotFound
	}
	return nil
}

func (w *Web) apiMonitorSeason(rw http.ResponseWriter, r *http.Request) {
	season, err := strconv.Atoi(r.PathValue("season"))
	if err != nil || season < 0 {
		badRequest(rw, "season must be a non-negative integer")
		return
	}
	w.monitor(rw, r, &season)
}

func (w *Web) apiMonitorEpisodes(rw http.ResponseWriter, r *http.Request) { w.monitor(rw, r, nil) }

// monitor sets the per-version monitored flag of a season's episodes, or of the episodes named, for the versions Theatarr
// may change; versions an arr still manages are refused with the reason. 409 when no version could be changed.
func (w *Web) monitor(rw http.ResponseWriter, r *http.Request, season *int) {
	ctx := r.Context()
	t := w.seriesTitle(rw, r, true)
	if t == nil {
		return
	}
	var req MonitorRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	if req.Monitored == nil {
		badRequest(rw, "monitored is required")
		return
	}
	where, args := "e.season = ?", []any{0}
	if season != nil {
		args[0] = *season
		var n int
		if err := w.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM episode WHERE title_id = ? AND season = ?`,
			t.id, *season).Scan(&n); err != nil {
			w.internal(rw, err)
			return
		}
		if n == 0 {
			notFound(rw)
			return
		}
	} else {
		if err := w.episodesOf(ctx, t.id, req.EpisodeIDs); errors.Is(err, errNotFound) {
			badRequest(rw, "some episode_ids are not episodes of this title")
			return
		} else if err != nil {
			badRequest(rw, err.Error())
			return
		}
		where, args = "e.id IN ("+store.Placeholders(len(req.EpisodeIDs))+")", nil
		for _, id := range req.EpisodeIDs {
			args = append(args, id)
		}
	}
	all, err := w.titleVersions(ctx, t.id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	picked, err := pickVersions(all, req.Versions)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	out := ManageOutcome{Versions: []string{}, Skipped: []catalog.Skip{}}
	for _, v := range picked {
		if m := w.managedBy(v.source); m != "" {
			out.Skipped = append(out.Skipped, catalog.Skip{Version: v.key, Reason: managedReason(m)})
			continue
		}
		out.Versions = append(out.Versions, v.key)
	}
	if len(out.Versions) == 0 {
		out.Message = "Nothing changed: " + skipWords(out.Skipped)
		if len(picked) == 0 {
			out.Message = "Nothing changed: the title is in no library"
		}
		out.Error = out.Message
		writeJSON(rw, http.StatusConflict, out)
		return
	}
	now := w.now()
	err = w.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if season != nil {
			// A season's choice is kept for its episodes a metadata refresh adds later.
			if err := catalog.SetSeasonMonitored(ctx, tx, t.id, out.Versions, *season, *req.Monitored, now); err != nil {
				return err
			}
		} else if err := catalog.SetEpisodesMonitored(ctx, tx, t.id, out.Versions, where, args, *req.Monitored,
			now); err != nil {
			return err
		}
		// An episode is monitored when any version monitors it (as arr import keeps it).
		_, err := tx.ExecContext(ctx, `UPDATE episode SET monitored = m, updated_at = ? FROM (SELECT episode_id,
			MAX(monitored) AS m FROM episode_version_state GROUP BY episode_id) s WHERE s.episode_id = episode.id
			AND episode.title_id = ? AND episode.monitored <> s.m`, store.Millis(now), t.id)
		return err
	})
	if err != nil {
		w.internal(rw, err)
		return
	}
	if err := w.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM episode e WHERE e.title_id = ? AND (`+where+`)`,
		append([]any{t.id}, args...)...).Scan(&out.Episodes); err != nil {
		w.internal(rw, err)
		return
	}
	word := "Monitored"
	if !*req.Monitored {
		word = "Unmonitored"
	}
	out.Message = fmt.Sprintf("%s %d episode(s) in %s", word, out.Episodes, strings.Join(out.Versions, ", "))
	if len(out.Skipped) > 0 {
		out.Message += "; not changed: " + skipWords(out.Skipped)
	}
	w.log.Info("episodes monitored", "title", t.id, "versions", strings.Join(out.Versions, ","), "monitored",
		*req.Monitored, "episodes", out.Episodes)
	writeJSON(rw, http.StatusOK, out)
}

// skipWords joins skips as "reason (versions)" by reason.
func skipWords(skipped []catalog.Skip) string {
	why := map[string][]string{}
	var order []string
	for _, s := range skipped {
		if _, ok := why[s.Reason]; !ok {
			order = append(order, s.Reason)
		}
		why[s.Reason] = append(why[s.Reason], s.Version)
	}
	var parts []string
	for _, reason := range order {
		parts = append(parts, reason+" ("+strings.Join(why[reason], ", ")+")")
	}
	return strings.Join(parts, "; ")
}

func (w *Web) apiSearchSeason(rw http.ResponseWriter, r *http.Request) {
	season, err := strconv.Atoi(r.PathValue("season"))
	if err != nil || season < 0 {
		badRequest(rw, "season must be a non-negative integer")
		return
	}
	w.searchScoped(rw, r, &season)
}

func (w *Web) apiSearchEpisodes(rw http.ResponseWriter, r *http.Request) { w.searchScoped(rw, r, nil) }

// searchScoped searches one season, or the episodes named, for the title's monitored versions (or the ones named),
// through the same gate as search now: only versions in mode active while router.enabled is true are searched, and
// the answer says why the rest were not. The acquirer searches monitored episodes only.
func (w *Web) searchScoped(rw http.ResponseWriter, r *http.Request, season *int) {
	ctx := r.Context()
	t := w.seriesTitle(rw, r, true)
	if t == nil {
		return
	}
	var req SearchRequest
	if err := decodeJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	sel := acquire.Selection{TitleIDs: []int64{t.id}}
	scope := ""
	if season != nil {
		sel.Season = season
		scope = fmt.Sprintf("season %d", *season)
	} else {
		if err := w.episodesOf(ctx, t.id, req.EpisodeIDs); errors.Is(err, errNotFound) {
			badRequest(rw, "some episode_ids are not episodes of this title")
			return
		} else if err != nil {
			badRequest(rw, err.Error())
			return
		}
		sel.EpisodeIDs = req.EpisodeIDs
		scope = fmt.Sprintf("%d episode(s)", len(req.EpisodeIDs))
	}
	on, err := w.versionsOn(ctx, t.id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	keys := on
	if req.Versions != nil {
		keys = nil
		for _, k := range req.Versions {
			if !slices.Contains(on, k) {
				badRequest(rw, "the title has no monitored version "+strconv.Quote(k))
				return
			}
			keys = append(keys, k)
		}
	}
	active, skipped := catalog.Searchable(w.pol, keys)
	out := &SearchOutcome{Versions: active, Skipped: skipped}
	if out.Versions == nil {
		out.Versions = []string{}
	}
	if out.Skipped == nil {
		out.Skipped = []catalog.Skip{}
	}
	switch {
	case len(keys) == 0:
		out.Message = "Nothing to search: the title has no monitored library"
	case len(active) == 0:
		out.Message = "Nothing searched: " + skipWords(skipped)
	case w.add.Acquirer == nil:
		out.Versions = []string{}
		out.Message = "Nothing searched: the acquirer is not running"
	default:
		sel.Versions = active
		queued, found, err := w.add.Acquirer.Submit(ctx, 0, sel)
		if err != nil {
			w.internal(rw, err)
			return
		}
		out.Queued, out.Found, out.Started = queued, found, queued > 0
		if queued == 0 {
			out.Message = "Nothing to search: no monitored episode in " + scope
		} else {
			out.Message = fmt.Sprintf("Searching %s (%d search(es)) for %s", scope, queued, strings.Join(active, ", "))
		}
		if len(skipped) > 0 {
			out.Message += "; not searched: " + skipWords(skipped)
		}
		w.log.Info("search now", "title", t.id, "scope", scope, "versions", strings.Join(active, ","), "queued", queued)
	}
	writeJSON(rw, http.StatusOK, out)
}

// EpisodeFile is the file that holds an episode in one version.
type EpisodeFile struct {
	// LinkID is Theatarr's link holding the episode (0 for an arr's file): the target of "Change episodes".
	LinkID       int64      `json:"link_id,omitempty"`
	Quality      string     `json:"quality,omitempty"`
	Score        *int64     `json:"score,omitempty"`
	SizeBytes    *int64     `json:"size_bytes,omitempty"`
	ReleaseTitle string     `json:"release_title,omitempty"`
	SceneName    string     `json:"scene_name,omitempty"`
	ReleaseGroup string     `json:"release_group,omitempty"`
	Path         string     `json:"path,omitempty"`
	Instance     string     `json:"instance,omitempty"`
	Languages    []string   `json:"languages,omitempty"`
	AddedAt      *time.Time `json:"added_at,omitempty"`
	Media        *MediaInfo `json:"media,omitempty"`
	// Episodes lists every episode the file holds (S01E01), for a multi-episode file or a link.
	Episodes []string `json:"episodes,omitempty"`
}

// EpisodeVersionDetail is one version of an episode.
type EpisodeVersionDetail struct {
	Key       string          `json:"key"`
	Label     string          `json:"label"`
	State     string          `json:"state"`
	Monitored bool            `json:"monitored"`
	HasFile   bool            `json:"has_file"`
	Upgrade   *UpgradeInfo    `json:"upgrade,omitempty"`
	Control   *VersionControl `json:"control"`
	// For callers who see files: the file, and the journal's entries about the episode (newest first).
	File    *EpisodeFile     `json:"file,omitempty"`
	History []HistoryEvent   `json:"history,omitzero"`
	Journal []*journal.Entry `json:"journal,omitempty"`
}

// EpisodeDetail is the answer of GET titles/{id}/episodes/{eid}.
type EpisodeDetail struct {
	TitleID   int64                  `json:"title_id"`
	TitleName string                 `json:"title_name"`
	Episode   Episode                `json:"episode"`
	Versions  []EpisodeVersionDetail `json:"versions"`
}

// episodeJournalLimit caps the journal entries listed per version of an episode.
const episodeJournalLimit = 20

func (w *Web) episodePath(rw http.ResponseWriter, r *http.Request) (*titleBits, int64, bool) {
	t := w.seriesTitle(rw, r, false)
	if t == nil {
		return nil, 0, false
	}
	eid, err := strconv.ParseInt(r.PathValue("eid"), 10, 64)
	if err != nil || eid <= 0 {
		notFound(rw)
		return nil, 0, false
	}
	if err := w.episodesOf(r.Context(), t.id, []int64{eid}); err != nil {
		notFound(rw)
		return nil, 0, false
	}
	return t, eid, true
}

// GET titles/{id}/episodes/{eid}: one episode with, per version, its state, upgrade explanation, controls, file,
// history and journal entries.
func (w *Web) apiEpisode(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t, eid, ok := w.episodePath(rw, r)
	if !ok {
		return
	}
	perms := w.permsOf(r)
	seeFiles := perms.SeeFiles
	db := w.st.Reader()
	var season int
	if err := db.QueryRowContext(ctx, `SELECT season FROM episode WHERE id = ?`, eid).Scan(&season); err != nil {
		w.internal(rw, err)
		return
	}
	list, err := w.titleEpisodes(ctx, t.id, season, seeFiles)
	if err != nil {
		w.internal(rw, err)
		return
	}
	out := EpisodeDetail{TitleID: t.id, TitleName: t.name, Versions: []EpisodeVersionDetail{}}
	for _, e := range list.Episodes {
		if e.ID == eid {
			out.Episode = e
		}
	}
	mvs, err := w.titleVersions(ctx, t.id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	vs := make([]upgradeVersion, len(mvs))
	for i, v := range mvs {
		vs[i] = upgradeVersion{id: v.id, titleID: t.id, key: v.key, kind: "series", state: v.state}
	}
	us, err := w.loadUpgrades(ctx, db, vs, true)
	if err != nil {
		w.internal(rw, err)
		return
	}
	code := episodeCode(int64(season), out.Episode.Number)
	// Named apart among the title's copies as the title page names them; ownership only for callers who see it.
	named := make([]copyName, len(mvs))
	for i, v := range mvs {
		named[i] = copyName{key: v.key, label: w.versionLabel(v.key)}
		if seeFiles {
			named[i].owner = ownerOf(w.managedBy(v.source))
		}
	}
	labels := distinctLabels(named)
	for i, v := range mvs {
		c := out.Episode.Versions[v.key]
		d := EpisodeVersionDetail{Key: v.key, Label: labels[v.key], State: c.State, Monitored: c.Monitored,
			HasFile: c.HasFile, Upgrade: us.explain(vs[i], eid), Control: w.control(v.key, v.source),
			History: []HistoryEvent{}}
		if d.State == "" {
			d.State = "wanted"
		}
		if seeFiles {
			if d.File, err = w.episodeFile(ctx, db, v.id, eid); err != nil {
				w.internal(rw, err)
				return
			}
		}
		if seeFiles {
			if d.History, err = w.episodeHistory(ctx, db, t.id, v.id, eid); err != nil {
				w.internal(rw, err)
				return
			}
		}
		if seeFiles && w.journal != nil {
			page, err := w.journal.List(ctx, journal.Filter{Title: strconv.FormatInt(t.id, 10), Version: v.key, Limit: 200})
			if err != nil {
				w.log.Warn("episode journal", "title", t.id, "version", v.key, "err", err)
			} else {
				for _, e := range page.Items {
					if e.Release != nil && slices.ContainsFunc(e.Release.Episodes, func(s string) bool {
						return strings.EqualFold(s, code)
					}) && len(d.Journal) < episodeJournalLimit {
						d.Journal = append(d.Journal, e)
					}
				}
			}
		}
		if !seeFiles {
			// Availability only (perms.go): no upgrade explanation or history.
			d.State, d.Upgrade, d.History = plainState(d.State), nil, nil
			redactControl(d.Control)
		}
		if !canManage(perms) {
			redactCeiling(d.Upgrade)
		}
		out.Versions = append(out.Versions, d)
	}
	if !canManage(perms) {
		for k, c := range out.Episode.Versions {
			redactCeiling(c.Upgrade)
			out.Episode.Versions[k] = c
		}
	}
	if !seeFiles {
		redactEpisodeCells(&out.Episode)
	}
	writeJSON(rw, http.StatusOK, out)
}

// episodeFile reads the current file that holds an episode in a version: an active link wins over the arr's file.
func (w *Web) episodeFile(ctx context.Context, db *sql.DB, versionID, eid int64) (*EpisodeFile, error) {
	var f EpisodeFile
	var linkID int64
	var size, score sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT l.id, COALESCE(l.quality, ''), l.score, cf.size_bytes, COALESCE(l.release_title, ''),
		l.path, l.created_at FROM link l JOIN link_version lv ON lv.link_id = l.id JOIN link_episode le ON le.link_id = l.id
		LEFT JOIN content_file cf ON cf.id = l.content_file_id WHERE lv.version_id = ? AND le.episode_id = ?
		AND l.state = 'active' ORDER BY l.created_at DESC LIMIT 1`, versionID, eid).Scan(&linkID, &f.Quality, &score, &size,
		&f.ReleaseTitle, &f.Path, new(int64))
	switch {
	case err == nil:
		f.LinkID, f.Score, f.SizeBytes, f.Instance = linkID, nullInt(score), nullInt(size), "theatarr"
		if err := scanAll(ctx, db, `SELECT e.season, e.number FROM link_episode le JOIN episode e ON e.id = le.episode_id
			WHERE le.link_id = ? ORDER BY e.season, e.number`, []any{linkID}, func(r *sql.Rows) error {
			var s, n int64
			if err := r.Scan(&s, &n); err != nil {
				return err
			}
			f.Episodes = append(f.Episodes, episodeCode(s, n))
			return nil
		}); err != nil {
			return nil, err
		}
		return &f, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	var fileID int64
	var langs, media string
	var added sql.NullInt64
	var sz int64
	err = db.QueryRowContext(ctx, `SELECT vf.id, vf.quality, vf.custom_format_score, vf.size_bytes,
		COALESCE(vf.release_title, ''), vf.scene_name, vf.release_group, vf.path, vf.instance, vf.languages, vf.added_at,
		vf.media_info FROM version_file vf JOIN version_file_episode fe ON fe.version_file_id = vf.id
		LEFT JOIN version_source vs ON vs.version_id = vf.version_id WHERE vf.version_id = ? AND fe.episode_id = ? AND `+
		curFile+` ORDER BY vf.added_at DESC LIMIT 1`, versionID, eid).Scan(&fileID, &f.Quality, &score, &sz,
		&f.ReleaseTitle, &f.SceneName, &f.ReleaseGroup, &f.Path, &f.Instance, &langs, &added, &media)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f.Score, f.SizeBytes, f.AddedAt, f.Media = nullInt(score), &sz, nullTS(added), parseMedia(media)
	_ = json.Unmarshal([]byte(langs), &f.Languages)
	err = scanAll(ctx, db, `SELECT e.season, e.number FROM version_file_episode fe JOIN episode e ON e.id = fe.episode_id
		WHERE fe.version_file_id = ? ORDER BY e.season, e.number`, []any{fileID}, func(r *sql.Rows) error {
		var s, n int64
		if err := r.Scan(&s, &n); err != nil {
			return err
		}
		f.Episodes = append(f.Episodes, episodeCode(s, n))
		return nil
	})
	return &f, err
}

// episodeHistory is one episode's history in a version: the arr's events for it, then Theatarr's downloads that held it,
// newest first, at most historyLimit.
func (w *Web) episodeHistory(ctx context.Context, db *sql.DB, titleID, versionID, eid int64) ([]HistoryEvent, error) {
	out := []HistoryEvent{}
	err := scanAll(ctx, db, `SELECT h.event_type, h.date_at, h.quality, h.instance, h.source_title, h.custom_format_score,
		COALESCE(h.info_hash, '') FROM arr_history h JOIN arr_alias a ON a.instance = h.instance AND a.kind = 'episode'
		AND a.arr_id = h.arr_episode_id WHERE h.title_id = ? AND h.version_id = ? AND a.target_id = ?`,
		[]any{titleID, versionID, eid}, func(r *sql.Rows) error {
			var typ string
			var at int64
			var score sql.NullInt64
			var h HistoryEvent
			if err := r.Scan(&typ, &at, &h.Quality, &h.Source, &h.ReleaseTitle, &score, &h.InfoHash); err != nil {
				return err
			}
			ev, ok := arrEvents[typ]
			if !ok {
				return nil
			}
			if ev == "renamed" || ev == "deleted" {
				// Their source title is a path, not a release.
				h.ReleaseTitle = ""
			}
			h.Event, h.Date, h.Score, h.Trigger = ev, ts(at), nullInt(score), arrWords(h.Source)
			out = append(out, h)
			return nil
		})
	if err != nil {
		return nil, err
	}
	origins, err := runOrigins(ctx, db, titleID)
	if err != nil {
		return nil, err
	}
	err = scanAll(ctx, db, `SELECT state, created_at, COALESCE(imported_at, updated_at), release_title, info_hash,
		COALESCE(provider, ''), quality, score, source, COALESCE(error, '') FROM download d WHERE d.version_id = ?
		AND EXISTS (SELECT 1 FROM json_each(d.episodes) j WHERE j.value = ?)`, []any{versionID, eid},
		func(r *sql.Rows) error {
			var state, source, errText string
			var created, done, score int64
			var h HistoryEvent
			if err := r.Scan(&state, &created, &done, &h.ReleaseTitle, &h.InfoHash, &h.Source, &h.Quality, &score,
				&source, &errText); err != nil {
				return err
			}
			var run *runOrigin
			if o, ok := origins[runIDOf(source)]; ok {
				run = &o
			}
			h.Score = &score
			out = downloadEvents(out, h, state, created, done, source, errText, run)
			return nil
		})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(out, func(a, b HistoryEvent) int { return b.Date.Compare(a.Date) })
	// An import after an earlier import of the same episode replaced its file: an upgrade.
	imports := 0
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Event == "imported" {
			if imports > 0 {
				out[i].Event = "upgraded"
			}
			imports++
		}
	}
	if len(out) > historyLimit {
		out = out[:historyLimit]
	}
	return out, nil
}

// GET titles/{id}/episodes/{eid}/releases?version=&live=: the releases panel for one episode. live=1 searches the
// indexers for that episode now, for a version in mode active only; otherwise (and for any shadow version) the answer
// is the newest stored decision that covered the episode: a journal search of it or its season, a journal RSS read
// with a release for it, or the shadow runner's search for an arr grab of it. Candidates for other episodes are left
// out.
func (w *Web) apiEpisodeReleases(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !canManage(w.permsOf(r)) {
		writeJSON(rw, http.StatusForbidden, apiError{Error: "your account may not see releases"})
		return
	}
	t, eid, ok := w.episodePath(rw, r)
	if !ok {
		return
	}
	mvs, err := w.titleVersions(ctx, t.id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	var have []string
	for _, v := range mvs {
		have = append(have, v.key)
	}
	key := r.URL.Query().Get("version")
	if key == "" && len(have) > 0 {
		key = have[0]
	}
	if !slices.Contains(have, key) {
		badRequest(rw, "the title has no version "+strconv.Quote(key))
		return
	}
	vp := w.pol.Versions[key]
	out := Releases{GrabState: versionGrabState(w.pol, key), TitleID: t.id, Version: key, Versions: have, Mode: string(policy.ModeShadow),
		Candidates: []snapshot.Candidate{}, Rejections: map[string]int{}, Source: "none"}
	if vp != nil && vp.Mode != "" {
		out.Mode = string(vp.Mode)
	}
	var season, number int64
	if err := w.st.Reader().QueryRowContext(ctx, `SELECT season, number FROM episode WHERE id = ?`, eid).Scan(&season,
		&number); err != nil {
		w.internal(rw, err)
		return
	}
	it := evItem{episodeID: eid, season: season, number: number}
	out.Target = strings.ToLower(it.code())
	out.LiveAllowed = vp != nil && vp.Mode == policy.ModeActive && w.add.Acquirer != nil
	if live, _ := strconv.ParseBool(r.URL.Query().Get("live")); live {
		switch {
		case !out.LiveAllowed && (vp == nil || vp.Mode != policy.ModeActive):
			out.Message = "Shadow mode: no live search for " + key + "; showing the last stored decision"
		case !out.LiveAllowed:
			out.Message = "The acquirer is not running; showing the last stored decision"
		default:
			s := int(season)
			sr, err := w.add.Acquirer.Interactive(ctx, t.id, key, &s, []int64{eid})
			if err == nil {
				rankedInto(&out, sr, w.now())
				w.keepLive(&out, sr)
				out.Target = strings.ToLower(it.code())
				w.writeReleases(ctx, rw, &out)
				return
			}
			out.Message = "Live search failed: " + err.Error() + "; showing the last stored decision"
			w.log.Warn("episode releases: live search", "title", t.id, "episode", eid, "version", key, "err", err)
		}
	}
	if err := w.storedEpisodeReleases(ctx, &out, it); err != nil {
		w.internal(rw, err)
		return
	}
	w.writeReleases(ctx, rw, &out)
}

// storedEpisodeReleases fills the answer from the newest stored decision that covered the episode.
func (w *Web) storedEpisodeReleases(ctx context.Context, out *Releases, it evItem) error {
	db := w.st.Reader()
	type stored struct {
		at                int64
		source, target    string
		results, accepted int
		notes             string
		cands             []snapshot.Candidate
		rejections        map[string]int
		episodeIDs        []int64
	}
	var best *stored
	consider := func(s stored) {
		rec := evRecord{target: s.target, episodeIDs: s.episodeIDs}
		if !rec.covers(it) {
			return
		}
		var keep []snapshot.Candidate
		for _, c := range s.cands {
			if candCovers(evCand{Episodes: c.Episodes}, it) {
				keep = append(keep, c)
			}
		}
		if len(keep) == 0 && s.target == "rss" {
			return
		}
		s.cands = keep
		if best == nil || s.at > best.at {
			best = &s
		}
	}
	err := scanAll(ctx, db, `SELECT created_at, target, results, accepted, notes, candidates, rejections
		FROM journal_decision WHERE title_id = ? AND version_key = ? ORDER BY created_at DESC, id`, []any{out.TitleID, out.Version},
		func(r *sql.Rows) error {
			var s stored
			var cj, rj string
			if err := r.Scan(&s.at, &s.target, &s.results, &s.accepted, &s.notes, &cj, &rj); err != nil {
				return err
			}
			s.source = "journal"
			if (best != nil && s.at <= best.at) || !(evRecord{target: s.target}).covers(it) {
				return nil
			}
			_ = json.Unmarshal([]byte(cj), &s.cands)
			_ = json.Unmarshal([]byte(rj), &s.rejections)
			consider(s)
			return nil
		})
	if err != nil && !strings.Contains(err.Error(), "no such table") {
		return err
	}
	err = scanAll(ctx, db, `SELECT COALESCE(searched_at, updated_at), result_count, accepted_count, search_note,
		episodes, results FROM shadow_grab WHERE title_id = ? AND version_key = ? AND state = 'done' AND searched = 1
		ORDER BY COALESCE(searched_at, updated_at) DESC, grabbed_at, id`,
		[]any{out.TitleID, out.Version}, func(r *sql.Rows) error {
			var s stored
			var ej, rj string
			if err := r.Scan(&s.at, &s.results, &s.accepted, &s.notes, &ej, &rj); err != nil {
				return err
			}
			s.source = "shadow"
			if best != nil && s.at <= best.at {
				return nil
			}
			_ = json.Unmarshal([]byte(ej), &s.episodeIDs)
			if !(evRecord{episodeIDs: s.episodeIDs}).covers(it) {
				return nil
			}
			_ = json.Unmarshal([]byte(rj), &s.cands)
			s.rejections = map[string]int{}
			for i := range s.cands {
				s.cands[i].Rank = i + 1
				if !s.cands[i].Accepted {
					seen := map[string]bool{}
					for _, rj := range s.cands[i].Rejections {
						if !seen[rj.Reason] {
							seen[rj.Reason] = true
							s.rejections[rj.Reason]++
						}
					}
				}
			}
			consider(s)
			return nil
		})
	if err != nil {
		return err
	}
	if best == nil {
		if out.Message == "" {
			out.Message = "No stored decision covers " + it.code() + " in this library yet"
		}
		return nil
	}
	at := ts(best.at)
	out.Source, out.At, out.Results, out.Accepted, out.Notes = best.source, &at, best.results, best.accepted, best.notes
	if best.target != "" {
		out.Target = best.target
	}
	if best.cands != nil {
		out.Candidates = best.cands
	}
	if best.rejections != nil {
		out.Rejections = best.rejections
	}
	if out.Message == "" {
		switch best.source {
		case "shadow":
			out.Message = "The shadow runner's search for the arr's grab of " + it.code()
		default:
			out.Message = "The last search Theatarr decided that covered " + it.code() + " (" + sourceWord(best.target) + ")"
		}
	}
	return nil
}
