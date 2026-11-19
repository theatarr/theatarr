// Package catalog is the one add path for titles and versions (docs/design/ui.md §3 "Requests", step 4): find or create
// the title by its TMDb or TVDB id, turn the chosen versions on (a new version starts wanted, a removed one is
// restored), apply season monitoring for a series, and keep the title's monitored flag in step with its versions.
// The arr views' POST movie|series (internal/arrview), the native POST /titles (internal/web) and, later, request
// approval all call Add, so one set of tests covers every way a title gets into the library.
//
// Add writes only to the database, in the caller's transaction. It never searches: a search is the caller's to
// start after the commit, and only for versions Searchable lets through (router.enabled true).
package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Season is one season's monitoring in an add.
type Season struct {
	Number    int  `json:"season"`
	Monitored bool `json:"monitored"`
}

// Input is one add.
type Input struct {
	Kind policy.Kind
	// TitleID, when set, is the stored title to add versions to; it must exist and be of Kind. The ids are then not
	// needed to find it (a series stored with only a TVDB id, or a title whose TMDb id is only in title_meta), and
	// any given are filled in where the title lacks them.
	TitleID int64
	// TmdbID and TvdbID find the title. A movie is found by TMDb id; a series by TVDB id, else by TMDb id. At least
	// one is required (a series added without a TVDB id is stored with its TMDb id only).
	TmdbID, TvdbID int64
	ImdbID         string
	// Title and Year name a new title; an empty Title becomes "Tmdb 123" or "Tvdb 456", as the arr views did.
	Title string
	Year  int
	// Anime is what the caller knows about the title being anime (nil: nothing). A new title stores it; a stored
	// title flagged anime stays anime.
	Anime *bool
	// Versions chooses the version keys to turn on once the title's anime flag is known. Required.
	Versions func(anime bool) ([]string, error)
	// Monitored is the monitored flag of the versions turned on.
	Monitored bool
	// Seasons sets per-version episode monitoring of a series, season by season, for the title's episodes that
	// exist (episode rows come from the metadata and arr imports).
	Seasons []Season
	// SeasonVersions are the version keys whose episodes Seasons applies to (an arr view passes its own versions);
	// nil means the versions this add turns on. Versions removed from the views are skipped.
	SeasonVersions []string
	// MonitorNew is Sonarr's monitorNewItems for the series versions turned on: "all" (new seasons are monitored)
	// or "none". "" keeps a version's choice, and a new version takes "all". Seasons are kept as the versions'
	// season choices too, so episodes that arrive with a later metadata refresh follow them (series_monitor).
	MonitorNew string
	// Monitor is Sonarr's addOptions.monitor (MonitorTypes: all, future, missing, ...), applied after Seasons to the
	// same versions (ApplyMonitor); "" keeps Seasons as given. Status is the series status the request gives
	// (continuing, ended, upcoming), which the future option reads.
	Monitor, Status string
	// SeriesType is Sonarr's user-selected standard, daily or anime type, for a series that has none stored: an add
	// never changes a stored type. Empty stores nothing.
	SeriesType string
	// Source names the caller in the log: an arr view key, "ui".
	Source string
	// AddMethod is Radarr's movie add method: manual (default), list or collection.
	AddMethod string
	// OriginalLanguage is a movie's TMDb original_language code ("en", "cn"), when the caller has its details: a new
	// title stores it, a stored movie without one takes it; a stored language is never replaced. Empty stores
	// nothing. A series ignores it (its language comes from its metadata, decide.LoadTitle).
	OriginalLanguage string
	// Policy's sync groups widen the add (TurnOnSynced): the libraries in sync with one that gains a copy get one
	// too, with the same monitoring and, for a series, the same season choices. Nil: no widening (the one-time
	// import from an arr).
	Policy *policy.Policy
}

// Result is what an add did.
type Result struct {
	TitleID int64
	// Created is true when the title row is new.
	Created bool
	Anime   bool
	// Versions are the keys turned on (new, restored or already there); New are the version rows this add created.
	// Both include Synced, the libraries the sync groups added (TurnOnSynced).
	Versions []string
	New      []string
	Synced   []string
	// Restored are the libraries of Synced whose removed copy came back (a person's add): each keeps its own saved
	// minimum availability (ChoiceKeys).
	Restored []string
}

// ErrNoID is returned for an input with neither a title id nor a TMDb or TVDB id.
var ErrNoID = errors.New("catalog: a TMDb or TVDB id is required")

// ErrNoTitle is returned when Input.TitleID names no title of the input's kind.
var ErrNoTitle = errors.New("catalog: no such title")

// Add finds or creates the title and turns the chosen versions on, in tx.
func Add(ctx context.Context, tx *sql.Tx, in Input, now time.Time, log *slog.Logger) (*Result, error) {
	if log == nil {
		log = slog.Default()
	}
	if in.Versions == nil {
		return nil, errors.New("catalog: no version chooser")
	}
	if in.Kind != policy.KindMovie && in.Kind != policy.KindSeries {
		return nil, fmt.Errorf("catalog: bad kind %q", in.Kind)
	}
	if in.TitleID <= 0 && in.TmdbID <= 0 && in.TvdbID <= 0 {
		return nil, ErrNoID
	}
	ms := store.Millis(now)
	res := &Result{}
	titleID, stored, found, err := find(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if in.TitleID > 0 && !found {
		return nil, ErrNoTitle
	}
	anime := stored || (in.Anime != nil && *in.Anime)
	if !found {
		anime = in.Anime != nil && *in.Anime
		if titleID, err = create(ctx, tx, in, anime, ms); err != nil {
			return nil, err
		}
		res.Created = true
		log.Info("title created", "source", in.Source, "title", titleID, "kind", in.Kind, "tmdb", in.TmdbID,
			"tvdb", in.TvdbID)
	} else if err := fillIDs(ctx, tx, titleID, in); err != nil {
		return nil, err
	}
	if lang := strings.TrimSpace(in.OriginalLanguage); lang != "" && in.Kind == policy.KindMovie && !res.Created {
		if _, err := tx.ExecContext(ctx, `UPDATE title SET original_language = ? WHERE id = ?
			AND COALESCE(original_language, '') = ''`, lang, titleID); err != nil {
			return nil, err
		}
	}
	res.TitleID, res.Anime = titleID, anime
	keys, err := in.Versions(anime)
	if err != nil {
		return nil, err
	}
	if in.Kind == policy.KindSeries {
		// Before the libraries are turned on: the series type is part of whether a library in sync fits.
		if err := AddSeriesType(ctx, tx, titleID, in.SeriesType); err != nil {
			return nil, err
		}
	}
	// A person's add fills the whole group, restoring a removed partner copy; import lists and collections never
	// restore one.
	restore := in.AddMethod != "list" && in.AddMethod != "collection"
	var partners Partners
	if res.New, partners, err = TurnOnSynced(ctx, tx, in.Policy, titleID, keys, in.Monitored, restore, now); err != nil {
		return nil, err
	}
	res.Synced, res.Restored = partners.Keys, partners.Restored
	if in.Kind == policy.KindSeries && len(in.Seasons) == 0 && in.Monitor == "" {
		// An add that names no seasons: each partner follows its named copy's own season choices.
		if err := PartnerSeasonIntent(ctx, tx, titleID, res.New, partners, now); err != nil {
			return nil, err
		}
	}
	res.New = append(res.New, res.Synced...)
	for _, k := range res.New {
		method := in.AddMethod
		if method != "list" && method != "collection" {
			method = "manual"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE version SET add_method = ? WHERE title_id = ? AND policy_key = ?`,
			method, titleID, k); err != nil {
			return nil, err
		}
		log.Info("version turned on", "source", in.Source, "title", titleID, "version", k)
	}
	res.Versions = keys
	if len(res.Synced) > 0 {
		res.Versions = append(slices.Clone(keys), res.Synced...)
		keys = res.Versions
	}
	if in.Kind == policy.KindSeries {
		if err := SetMonitorNew(ctx, tx, titleID, keys, in.MonitorNew, now); err != nil {
			return nil, err
		}
		sv := in.SeasonVersions
		if sv == nil {
			sv = keys
		} else if len(res.Synced) > 0 {
			// The libraries in sync take the add's seasons as the ones it names.
			sv = append(slices.Clone(sv), res.Synced...)
		}
		for _, se := range in.Seasons {
			if err := SetSeasonMonitored(ctx, tx, titleID, sv, se.Number, se.Monitored, now); err != nil {
				return nil, err
			}
		}
		if in.Monitor != "" {
			nums := make([]int, len(in.Seasons))
			for i, se := range in.Seasons {
				nums[i] = se.Number
			}
			if err := ApplyMonitor(ctx, tx, titleID, sv, in.Monitor, nums, in.Status, now); err != nil {
				return nil, err
			}
		}
		if res.Created || len(res.New) > 0 {
			// The series' first search waits for its metadata, and the add's monitoring applies to what arrives.
			if _, err := tx.ExecContext(ctx, `INSERT INTO series_meta (title_id, added_at) VALUES (?, ?)
				ON CONFLICT (title_id) DO UPDATE SET added_at = excluded.added_at`, titleID, ms); err != nil {
				return nil, err
			}
		}
	}
	return res, SyncMonitored(ctx, tx, titleID, now)
}

// find looks the title up: by Input.TitleID when set; else a movie by TMDb id, a series by TVDB id, then by TMDb id.
func find(ctx context.Context, tx *sql.Tx, in Input) (id int64, anime, found bool, err error) {
	if in.TitleID > 0 {
		err = tx.QueryRowContext(ctx, `SELECT id, anime FROM title WHERE id = ? AND kind = ?`, in.TitleID,
			string(in.Kind)).Scan(&id, &anime)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, false, nil
		}
		return id, anime, err == nil, err
	}
	return Lookup(ctx, tx, in.Kind, in.TmdbID, in.TvdbID)
}

// Lookup finds a title by its ids as an add without a title id does: a movie by TMDb id; a series by TVDB id, then
// by TMDb id.
func Lookup(ctx context.Context, q Querier, kind policy.Kind, tmdbID, tvdbID int64) (id int64, anime, found bool,
	err error) {
	try := func(col string, v int64) error {
		if v <= 0 || found {
			return nil
		}
		err := q.QueryRowContext(ctx, `SELECT id, anime FROM title WHERE kind = ? AND `+col+` = ?`, string(kind),
			v).Scan(&id, &anime)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err == nil {
			found = true
		}
		return err
	}
	if kind == policy.KindSeries {
		if err = try("tvdb_id", tvdbID); err != nil {
			return
		}
	}
	err = try("tmdb_id", tmdbID)
	return
}

func create(ctx context.Context, tx *sql.Tx, in Input, anime bool, ms int64) (int64, error) {
	name := strings.TrimSpace(in.Title)
	if name == "" {
		if in.Kind == policy.KindSeries && in.TvdbID > 0 {
			name = "Tvdb " + strconv.FormatInt(in.TvdbID, 10)
		} else {
			name = "Tmdb " + strconv.FormatInt(in.TmdbID, 10)
		}
	}
	var tmdb, tvdb, imdb, year any
	if in.TmdbID > 0 {
		tmdb = in.TmdbID
	}
	if in.TvdbID > 0 && in.Kind == policy.KindSeries {
		tvdb = in.TvdbID
	}
	if in.ImdbID != "" {
		imdb = in.ImdbID
	}
	if in.Year > 0 {
		year = in.Year
	}
	// A movie's language as TMDb's code, as the artwork refresh writes it (meta.art); decide.LoadTitle maps it.
	var lang any
	if l := strings.TrimSpace(in.OriginalLanguage); l != "" && in.Kind == policy.KindMovie {
		lang = l
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO title (kind, name, sort_name, year, tmdb_id, tvdb_id, imdb_id, anime,
		original_language, monitored, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		string(in.Kind), name, strings.ToLower(name), year, tmdb, tvdb, imdb, anime, lang, ms, ms)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// fillIDs sets external ids a found title lacks (a series found by TVDB id gets the TMDb id the add knows, and so
// on). An id another title already holds is left alone.
func fillIDs(ctx context.Context, tx *sql.Tx, titleID int64, in Input) error {
	set := func(col string, v any, cond string, args ...any) error {
		_, err := tx.ExecContext(ctx, `UPDATE title SET `+col+` = ? WHERE id = ? AND `+col+` IS NULL AND NOT EXISTS
			(SELECT 1 FROM title o WHERE o.id <> ? AND `+cond+`)`, append([]any{v, titleID, titleID}, args...)...)
		return err
	}
	if in.TmdbID > 0 {
		if err := set("tmdb_id", in.TmdbID, `o.kind = ? AND o.tmdb_id = ?`, string(in.Kind), in.TmdbID); err != nil {
			return err
		}
	}
	if in.TvdbID > 0 && in.Kind == policy.KindSeries {
		if err := set("tvdb_id", in.TvdbID, `o.kind = 'series' AND o.tvdb_id = ?`, in.TvdbID); err != nil {
			return err
		}
	}
	if in.ImdbID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE title SET imdb_id = ? WHERE id = ? AND COALESCE(imdb_id, '') = ''`, in.ImdbID,
			titleID); err != nil {
			return err
		}
	}
	return nil
}

// TurnOn makes sure the title has each version, monitored as asked: a new version starts wanted; one removed from
// the arr views is restored. It returns the keys of the version rows it created.
func TurnOn(ctx context.Context, tx *sql.Tx, titleID int64, keys []string, monitored bool, now time.Time) ([]string,
	error) {
	ms := store.Millis(now)
	var created []string
	for _, k := range keys {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM version WHERE title_id = ? AND policy_key = ?`, titleID, k).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.ExecContext(ctx, `INSERT INTO version (title_id, policy_key, monitored, state, created_at,
				updated_at) VALUES (?, ?, ?, 'wanted', ?, ?)`, titleID, k, monitored, ms, ms); err != nil {
				return nil, err
			}
			created = append(created, k)
		case err != nil:
			return nil, err
		default:
			if _, err := tx.ExecContext(ctx, `DELETE FROM arrview_removed_version WHERE version_id = ?`, id); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE version SET monitored = ?, updated_at = ? WHERE id = ?
				AND monitored <> ?`, monitored, ms, id, monitored); err != nil {
				return nil, err
			}
		}
	}
	return created, nil
}

// Partners are the copies TurnOnSynced gave a title for the sync groups.
type Partners struct {
	// Keys are the libraries that gained a copy in sync, created or restored; sorted.
	Keys []string
	// Restored are those of Keys whose removed copy came back (a person's add).
	Restored []string
	// Of is the named library each partner is in sync with.
	Of map[string]string
}

// TurnOnSynced is TurnOn for every route that gives a title a copy, plus the sync groups (policy library.sync_groups,
// docs/design/ui.md "Keep in sync"): the partners SyncPartners answers for the title's copies before this call are
// created (or, with restore, restored) with the same monitored flag. created are the named keys' new rows (as TurnOn
// returns them). A nil policy, or one without groups, is TurnOn.
func TurnOnSynced(ctx context.Context, tx *sql.Tx, pol *policy.Policy, titleID int64, keys []string, monitored,
	restore bool, now time.Time) (created []string, partners Partners, err error) {
	var held Copies
	if slices.ContainsFunc(keys, func(k string) bool { return pol.SyncGroup(k) != nil }) {
		if held, err = LoadCopies(ctx, tx, titleID); err != nil {
			return nil, partners, err
		}
		kind, anime, err := TitleFit(ctx, tx, titleID)
		if err != nil {
			return nil, partners, err
		}
		partners.Of = syncPartnersOf(pol, kind, anime, held, keys, restore)
	}
	if created, err = TurnOn(ctx, tx, titleID, keys, monitored, now); err != nil || len(partners.Of) == 0 {
		return created, Partners{}, err
	}
	for p := range partners.Of {
		partners.Keys = append(partners.Keys, p)
		if _, had := held[p]; had {
			partners.Restored = append(partners.Restored, p)
		}
	}
	slices.Sort(partners.Keys)
	slices.Sort(partners.Restored)
	if _, err := TurnOn(ctx, tx, titleID, partners.Keys, monitored, now); err != nil {
		return nil, Partners{}, err
	}
	return created, partners, nil
}

// PartnerSeasonIntent gives each series partner in sync (TurnOnSynced) the season and episode choices of the named
// library it is in sync with, monitor-new included, as Bring in sync does (CopySeasonIntent with fill), when that
// library's copy was there before this turn-on (not in created). For a turn-on that names no seasons; a partner of a
// brand-new copy is left as it is.
func PartnerSeasonIntent(ctx context.Context, tx *sql.Tx, titleID int64, created []string, partners Partners,
	now time.Time) error {
	for _, p := range partners.Keys {
		named := partners.Of[p]
		if slices.Contains(created, named) {
			continue
		}
		src, err := SeasonSource(ctx, tx, titleID, []string{named}, nil)
		if err != nil {
			return err
		}
		if err := CopySeasonIntent(ctx, tx, titleID, src, []string{p}, true, now); err != nil {
			return err
		}
	}
	return nil
}

// Copies are a title's version rows, in any state: library key → removed from the views.
type Copies map[string]bool

// RowsQuerier runs a query that returns rows (a *sql.Tx or *sql.DB).
type RowsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// LoadCopies reads the title's version rows (Copies).
func LoadCopies(ctx context.Context, q RowsQuerier, titleID int64) (Copies, error) {
	rows, err := q.QueryContext(ctx, `SELECT v.policy_key, EXISTS (SELECT 1 FROM arrview_removed_version r
		WHERE r.version_id = v.id) FROM version v WHERE v.title_id = ?`, titleID)
	if err != nil {
		return nil, err
	}
	out := Copies{}
	for rows.Next() {
		var k string
		var removed bool
		if err := rows.Scan(&k, &removed); err != nil {
			rows.Close()
			return nil, err
		}
		out[k] = removed
	}
	return out, rows.Close()
}

// TitleFit is what decides whether a library fits the title: its kind, and anime by the title's flag or a series
// type of anime.
func TitleFit(ctx context.Context, q Querier, titleID int64) (kind policy.Kind, anime bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT t.kind, t.anime OR `+store.SeriesTypeSQL("t")+` = 'anime' FROM title t
		WHERE t.id = ?`, titleID).Scan(&kind, &anime)
	return kind, anime, err
}

// SyncPartners answers, without writing, which libraries would gain a copy because they are in sync with keys
// (policy library.sync_groups, docs/design/ui.md "Keep in sync") when a title of this kind and anime rule, holding
// held (nil or empty: a title not in the catalogue), gains copies in keys: a named key that gains a copy (none yet, or
// a removed one restored) brings each other library of its group that fits the title, is not named itself and of
// which the title has no copy, or (restore: a person's add) only a removed one. An unmonitored partner copy is left
// as it is, and so is a removed one without restore (import lists, collections, Bring in sync); monitoring an
// existing copy again brings nothing. Sorted; nil when none. This is the one place of the rule.
func SyncPartners(pol *policy.Policy, kind policy.Kind, anime bool, held Copies, keys []string, restore bool) []string {
	var partners []string
	for p := range syncPartnersOf(pol, kind, anime, held, keys, restore) {
		partners = append(partners, p)
	}
	slices.Sort(partners)
	return partners
}

// syncPartnersOf is SyncPartners with the named key each partner is in sync with (the first in keys).
func syncPartnersOf(pol *policy.Policy, kind policy.Kind, anime bool, held Copies, keys []string,
	restore bool) map[string]string {
	of := map[string]string{}
	for _, k := range keys {
		g := pol.SyncGroup(k)
		if removed, has := held[k]; g == nil || has && !removed {
			continue
		}
		for _, p := range g {
			if _, seen := of[p]; seen {
				continue
			}
			if removed, has := held[p]; (!has || restore && removed) && !slices.Contains(keys, p) &&
				Fits(pol.Versions[p], kind, anime) {
				of[p] = k
			}
		}
	}
	return of
}

// SyncRemovals answers, without writing, which libraries a removal of keys from a title holding held also takes out
// because they are in sync with them (policy library.sync_groups, docs/design/ui.md "Keep in sync"): for each named
// key that holds a live copy (not removed), each other library of its group that holds a live copy and is not named
// itself, with the named library it is in sync with (the first in keys). Monitored or not, the partner leaves; its fit
// is not checked. A named key with no live copy (the files kept from a library removed before) brings none. Nil when
// none. Removals have this one rule, beside SyncPartners for adds.
func SyncRemovals(pol *policy.Policy, held Copies, keys []string) map[string]string {
	var of map[string]string
	for _, k := range keys {
		if removed, has := held[k]; !has || removed {
			continue
		}
		for _, p := range pol.SyncGroup(k) {
			if _, seen := of[p]; seen || slices.Contains(keys, p) {
				continue
			}
			if removed, has := held[p]; has && !removed {
				if of == nil {
					of = map[string]string{}
				}
				of[p] = k
			}
		}
	}
	return of
}

// ChoiceKeys are the libraries of keys an add stores its minimum availability choice for: every one it names, and
// each library in sync (synced) that has no minimum availability of its own and whose copy is new; one that has keeps
// it, as a library's own value wins over a client's, and a restored partner (restored) keeps its saved choice.
func ChoiceKeys(pol *policy.Policy, keys, synced, restored []string) []string {
	return slices.DeleteFunc(slices.Clone(keys), func(k string) bool {
		vp := pol.Versions[k]
		return slices.Contains(restored, k) || slices.Contains(synced, k) && vp != nil && vp.MinimumAvailability != ""
	})
}

// versionIDs are the ids of the title's versions with these keys that are not removed from the views.
func versionIDs(ctx context.Context, tx *sql.Tx, titleID int64, keys []string) ([]int64, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	args := []any{titleID}
	for _, k := range keys {
		args = append(args, k)
	}
	rows, err := tx.QueryContext(ctx, `SELECT v.id FROM version v WHERE v.title_id = ? AND v.policy_key IN (`+
		store.Placeholders(len(keys))+`) AND NOT EXISTS (SELECT 1 FROM
		arrview_removed_version r WHERE r.version_id = v.id) ORDER BY v.id`, args...)
	if err != nil {
		return nil, err
	}
	var vids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		vids = append(vids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return vids, rows.Err()
}

// SetEpisodesMonitored sets the per-version monitored flag of the title's episodes matching where (on episode e)
// for its versions with these keys that are not removed, creating wanted state rows where none exist. An episode the
// metadata source removed (removed_at; kept for its files and history) is left as it is.
func SetEpisodesMonitored(ctx context.Context, tx *sql.Tx, titleID int64, keys []string, where string,
	whereArgs []any, monitored bool, now time.Time) error {
	vids, err := versionIDs(ctx, tx, titleID, keys)
	if err != nil {
		return err
	}
	ms := store.Millis(now)
	for _, vid := range vids {
		a := append([]any{vid, monitored, ms, titleID}, whereArgs...)
		if _, err := tx.ExecContext(ctx, `INSERT INTO episode_version_state (version_id, episode_id, monitored, state,
			updated_at) SELECT ?, e.id, ?, 'wanted', ? FROM episode e WHERE e.title_id = ? AND e.removed_at IS NULL
			AND (`+where+`)
			ON CONFLICT (version_id, episode_id) DO UPDATE SET monitored = excluded.monitored,
			updated_at = excluded.updated_at WHERE monitored <> excluded.monitored`, a...); err != nil {
			return err
		}
	}
	return nil
}

// SetSeasonMonitored sets the per-version monitored flag of a season's episodes (SetEpisodesMonitored) and keeps the
// choice as the versions' intent for the season (series_monitor), so its episodes that a metadata refresh adds
// later take it too (Sonarr's Season.Monitored).
func SetSeasonMonitored(ctx context.Context, tx *sql.Tx, titleID int64, keys []string, season int, monitored bool,
	now time.Time) error {
	if err := SetEpisodesMonitored(ctx, tx, titleID, keys, `e.season = ?`, []any{season}, monitored, now); err != nil {
		return err
	}
	vids, err := versionIDs(ctx, tx, titleID, keys)
	if err != nil {
		return err
	}
	for _, vid := range vids {
		m, err := LoadMonitor(ctx, tx, vid)
		if err != nil {
			return err
		}
		if cur, ok := m.Seasons[season]; ok && cur == monitored {
			continue
		}
		m.Seasons[season] = monitored
		if err := SaveMonitor(ctx, tx, vid, m, now); err != nil {
			return err
		}
	}
	return nil
}

// SetMonitorNew records Sonarr's monitorNewItems ("all" or "none") for the title's series versions with these keys.
// "" gives a version without a choice yet "all" and leaves the others alone.
func SetMonitorNew(ctx context.Context, tx *sql.Tx, titleID int64, keys []string, monitorNew string,
	now time.Time) error {
	switch monitorNew {
	case "", MonitorAll, MonitorNone:
	default:
		return fmt.Errorf("catalog: monitorNewItems %q: want all or none", monitorNew)
	}
	vids, err := versionIDs(ctx, tx, titleID, keys)
	if err != nil {
		return err
	}
	set := monitorNew
	if set == "" {
		set = MonitorAll
	}
	for _, vid := range vids {
		q := `INSERT INTO series_monitor (version_id, monitor_new, updated_at) VALUES (?, ?, ?)
			ON CONFLICT (version_id) DO NOTHING`
		if monitorNew != "" {
			q = `INSERT INTO series_monitor (version_id, monitor_new, updated_at) VALUES (?, ?, ?)
				ON CONFLICT (version_id) DO UPDATE SET monitor_new = excluded.monitor_new,
				updated_at = excluded.updated_at WHERE monitor_new <> excluded.monitor_new`
		}
		if _, err := tx.ExecContext(ctx, q, vid, set, store.Millis(now)); err != nil {
			return err
		}
	}
	return nil
}

// Monitor new items (Sonarr's NewItemMonitorTypes).
const (
	MonitorAll  = "all"
	MonitorNone = "none"
)

// Monitor is a series version's durable monitoring intent (series_monitor).
type Monitor struct {
	// New is "all" or "none": whether a season that appears later is monitored (season 0 never is).
	New string
	// Seasons are the known seasons' choices.
	Seasons map[int]bool
}

// Querier is a transaction or a database.
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// LoadMonitor reads a version's monitoring intent; a version without a row gets "all" and no seasons.
func LoadMonitor(ctx context.Context, q Querier, versionID int64) (Monitor, error) {
	m := Monitor{New: MonitorAll, Seasons: map[int]bool{}}
	var seasons string
	err := q.QueryRowContext(ctx, `SELECT monitor_new, seasons FROM series_monitor WHERE version_id = ?`, versionID).
		Scan(&m.New, &seasons)
	if errors.Is(err, sql.ErrNoRows) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal([]byte(seasons), &m.Seasons); err != nil || m.Seasons == nil {
		m.Seasons = map[int]bool{}
	}
	return m, nil
}

// SaveMonitor writes a version's monitoring intent.
func SaveMonitor(ctx context.Context, tx *sql.Tx, versionID int64, m Monitor, now time.Time) error {
	if m.New != MonitorNone {
		m.New = MonitorAll
	}
	b, err := json.Marshal(m.Seasons)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO series_monitor (version_id, monitor_new, seasons, updated_at)
		VALUES (?, ?, ?, ?) ON CONFLICT (version_id) DO UPDATE SET monitor_new = excluded.monitor_new,
		seasons = excluded.seasons, updated_at = excluded.updated_at`, versionID, m.New, string(b), store.Millis(now))
	return err
}

// SyncMonitored sets the title's monitored flag: monitored when any of its versions not removed is.
func SyncMonitored(ctx context.Context, tx *sql.Tx, titleID int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE title SET monitored = m, updated_at = ? FROM (SELECT COALESCE(MAX(v.monitored), 0)
		AS m FROM version v WHERE v.title_id = ? AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r
		WHERE r.version_id = v.id)) WHERE id = ? AND monitored <> m`, store.Millis(now), titleID, titleID)
	return err
}

// Fits reports whether a version takes a title of this kind and anime flag.
func Fits(vp *policy.VersionPolicy, kind policy.Kind, anime bool) bool {
	return vp != nil && vp.Kind == kind && (vp.Anime == nil || *vp.Anime == anime)
}

// DefaultVersions is what an add or request asks for when the caller names none (docs/design/ui.md §8.3): every
// version of the policy that fits the title (movie: movie-remux and movie-web; series: tv-2160p;
// anime: the anime versions), sorted.
func DefaultVersions(pol *policy.Policy, kind policy.Kind, anime bool) []string {
	var out []string
	for k, vp := range pol.Versions {
		if Fits(vp, kind, anime) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// Skip is a version a search leaves out, and why.
type Skip struct {
	Version string `json:"version"`
	Reason  string `json:"reason"`
}

// Searchable splits version keys into the ones a search may run for and the ones it skips, with the reason: a
// version is searched only while Pause is off.
func Searchable(pol *policy.Policy, keys []string) (active []string, skipped []Skip) {
	on := pol != nil && pol.RouterSettings().Enabled
	for _, k := range keys {
		var vp *policy.VersionPolicy
		if pol != nil {
			vp = pol.Versions[k]
		}
		switch {
		case vp == nil:
			skipped = append(skipped, Skip{k, "not in the policy"})
		case !on:
			skipped = append(skipped, Skip{k, "Paused: " + policy.PauseStops})
		case !slices.Contains(active, k):
			active = append(active, k)
		}
	}
	slices.Sort(active)
	return active, skipped
}

// SeasonSource is the version id of the title's first version in from that is not in to and not removed (0 when
// none): the copy CopySeasonIntent copies from.
func SeasonSource(ctx context.Context, tx *sql.Tx, titleID int64, from, to []string) (int64, error) {
	for _, k := range from {
		if slices.Contains(to, k) {
			continue
		}
		var src int64
		err := tx.QueryRowContext(ctx, `SELECT v.id FROM version v WHERE v.title_id = ? AND v.policy_key = ?
			AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)`, titleID, k).Scan(&src)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		return src, err
	}
	return 0, nil
}

// CopySeasonIntent gives the title's series versions with keys to (just created) the season monitoring of version
// src: its series_monitor intent (seasons and monitor new) and its episodes' monitored flags. fill also creates the
// wanted state rows the new copy lacks (Bring in sync); without it only existing rows take the flags, and the rest
// come with the next metadata refresh from the intent (a compatible view's reclassify). Nothing when src is 0.
func CopySeasonIntent(ctx context.Context, tx *sql.Tx, titleID, src int64, to []string, fill bool, now time.Time) error {
	if len(to) == 0 || src == 0 {
		return nil
	}
	m, err := LoadMonitor(ctx, tx, src)
	if err != nil {
		return err
	}
	ms := store.Millis(now)
	for _, k := range to {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM version WHERE title_id = ? AND policy_key = ?`, titleID,
			k).Scan(&id); err != nil {
			return err
		}
		if err := SaveMonitor(ctx, tx, id, m, now); err != nil {
			return err
		}
		q := `UPDATE episode_version_state SET monitored = s.monitored, updated_at = ? FROM episode_version_state s
			WHERE s.version_id = ? AND s.episode_id = episode_version_state.episode_id
			AND episode_version_state.version_id = ?`
		args := []any{ms, src, id}
		if fill {
			q = `INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at)
				SELECT ?, s.episode_id, s.monitored, 'wanted', ? FROM episode_version_state s WHERE s.version_id = ?
				ON CONFLICT (version_id, episode_id) DO UPDATE SET monitored = excluded.monitored,
				updated_at = excluded.updated_at WHERE monitored <> excluded.monitored`
			args = []any{id, ms, src}
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return err
		}
	}
	return nil
}
