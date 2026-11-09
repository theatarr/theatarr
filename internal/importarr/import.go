package importarr

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/torrents/reffence"
)

// Source is one instance to import, resolved from the policy (import.arr and the version it names).
type Source struct {
	Instance string
	Flavour  string
	// Version is the policy key of the version every title of the instance becomes.
	Version string
	// Anime is the version's anime flag (nil: either); new titles take it.
	Anime *bool
	// Library is the version's library root; the import warns when the arr's root folders differ.
	Library string
	// Followers are the parallel versions that follow Version (policy Followers): they take its titles and
	// monitoring, never its files (follow.go).
	Followers []string
	// CollectionTargets are the policy's collection targets (policy CollectionTargets): of Version and its
	// followers, only these take the collections the import carries over (collections.go).
	CollectionTargets []string
}

// Importer writes arr snapshots into the database.
type Importer struct {
	Store *store.Store
	// Now is the clock for created_at and updated_at. Default time.Now.
	Now func() time.Time
	// ResolveIMDb, if set, finds the TMDb id of a title the arr has with an IMDb id but no TMDb id (theatarr import-arr
	// --enrich uses TMDb's find). Called before the write transaction.
	ResolveIMDb func(ctx context.Context, imdbID string) (int, error)
}

// FollowSearchTarget is a follower version's newly added or newly monitored title/episodes.
type FollowSearchTarget struct {
	TitleID    int64
	VersionKey string
	All        bool
	EpisodeIDs []int64
}

// FollowSearches are follower changes that should be searched immediately after this import.
type FollowSearches []FollowSearchTarget

// FollowSearcher starts the searches for follower changes after the import commits; the import has already recorded
// them as intents (SaveFollowSearches), so a failure here loses nothing.
type FollowSearcher interface {
	EnqueueFollowSearch(ctx context.Context, targets []FollowSearchTarget) error
}

var followSearchers sync.Map // *store.Store -> FollowSearcher

// RegisterFollowSearcher connects the acquirer for st to arr imports on the same store.
func RegisterFollowSearcher(st *store.Store, s FollowSearcher) {
	if st != nil && s != nil {
		followSearchers.Store(st, s)
	}
}

func (im *Importer) now() time.Time {
	if im.Now != nil {
		return im.Now()
	}
	return time.Now()
}

// KnownHistory returns a lookup of the history ids already stored for instance.
func (im *Importer) KnownHistory(ctx context.Context, instance string) (func(int) bool, error) {
	rows, err := im.Store.Reader().QueryContext(ctx, "SELECT arr_id FROM arr_history WHERE instance = ?", instance)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[int]bool{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return func(id int) bool { return ids[id] }, rows.Err()
}

// Run fetches the instance through g and writes it.
func (im *Importer) Run(ctx context.Context, src Source, g Getter) (*Summary, error) {
	known, err := im.KnownHistory(ctx, src.Instance)
	if err != nil {
		return nil, err
	}
	var snap *Snapshot
	switch src.Flavour {
	case "radarr":
		snap, err = FetchRadarr(ctx, g, src.Instance, known)
	case "sonarr":
		snap, err = FetchSonarr(ctx, g, src.Instance, known)
	default:
		return nil, fmt.Errorf("%s: %s import is not built yet", src.Instance, src.Flavour)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", src.Instance, err)
	}
	return im.Write(ctx, src, snap)
}

// Write stores a snapshot in one transaction and records the run in import_run.
func (im *Importer) Write(ctx context.Context, src Source, snap *Snapshot) (*Summary, error) {
	started := im.now()
	sum := &Summary{
		Instance: src.Instance, Flavour: snap.Flavour, Version: src.Version, AppVersion: snap.AppVersion,
		RootFolders: snap.RootFolders, Tags: len(snap.Tags), Profiles: len(snap.Profiles),
		Warnings: append(append([]string(nil), snap.Selection...), snap.Warnings...),
	}
	// Resolve missing TMDb ids outside the transaction (network).
	resolved := map[int]int{}
	if im.ResolveIMDb != nil {
		for _, t := range snap.Titles {
			if t.Kind == "movie" && t.TmdbID == 0 && t.ImdbID != "" {
				id, err := im.ResolveIMDb(ctx, t.ImdbID)
				if err != nil {
					sum.Warnings = append(sum.Warnings, fmt.Sprintf("arr id %d: TMDb find for %s: %v", t.ArrID, t.ImdbID, err))
					continue
				}
				if id != 0 {
					resolved[t.ArrID] = id
				}
			}
		}
	}

	write := func(tx *sql.Tx) error {
		if err := reffence.CheckTx(ctx, tx, im.now()); err != nil {
			return err
		}
		// WriteTx may retry: start every attempt from a clean summary.
		s := *sum
		s.Warnings = append([]string(nil), sum.Warnings...)
		s.FollowSearches = nil
		w := &wtx{tx: tx, changes: Changes{}, now: store.Millis(im.now()), nowTime: im.now()}
		if err := w.write(ctx, src, snap, resolved, &s); err != nil {
			return err
		}
		if err := SaveFollowSearches(ctx, tx, s.FollowSearches, w.now); err != nil {
			return err
		}
		s.Changes = w.changes
		b, err := json.Marshal(&s)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO import_run (instance, started_at, finished_at, summary)
			VALUES (?, ?, ?, ?)`, src.Instance, store.Millis(started), store.Millis(im.now()), string(b)); err != nil {
			return err
		}
		*sum = s
		return nil
	}
	// The arr's files are references the torrent manager's delete checks: written under its fence.
	err := reffence.Write(ctx, func() error { return im.Store.WriteTx(ctx, write) })
	if err != nil {
		return nil, fmt.Errorf("%s: write: %w", src.Instance, err)
	}
	if searcher, ok := followSearchers.Load(im.Store); ok && len(sum.FollowSearches) > 0 {
		if err := searcher.(FollowSearcher).EnqueueFollowSearch(ctx, sum.FollowSearches); err != nil {
			return nil, fmt.Errorf("%s: follow search: %w", src.Instance, err)
		}
	}
	return sum, nil
}

func (w *wtx) write(ctx context.Context, src Source, snap *Snapshot, resolved map[int]int, sum *Summary) error {
	roots, _ := json.Marshal(nonNil(snap.RootFolders))
	if _, err := w.upsert(ctx, "arr_instance", []kv{{"name", src.Instance}},
		[]kv{{"flavour", snap.Flavour}, {"policy_key", src.Version}, {"app_version", snap.AppVersion},
			{"root_folders", string(roots)}},
		[]kv{{"created_at", w.now}, {"updated_at", w.now}}, []kv{{"updated_at", w.now}}); err != nil {
		return err
	}
	if len(snap.PolicySnapshot) > 0 {
		body, err := json.Marshal(snap.PolicySnapshot)
		if err != nil {
			return err
		}
		if _, err := w.tx.ExecContext(ctx, `INSERT INTO arr_policy_snapshot (instance, body, fetched_at) VALUES (?, ?, ?)
			ON CONFLICT (instance) DO UPDATE SET body = excluded.body, fetched_at = excluded.fetched_at`,
			src.Instance, string(body), w.now); err != nil {
			return err
		}
		if err := w.naming(ctx, src.Instance, snap.PolicySnapshot); err != nil {
			return err
		}
	}
	if src.Library != "" && !contains(snap.RootFolders, src.Library) {
		sum.Warnings = append(sum.Warnings, fmt.Sprintf("version %s links into %s, which is not a root folder of %s",
			src.Version, src.Library, src.Instance))
	}
	if err := w.tags(ctx, src.Instance, snap.Tags); err != nil {
		return err
	}
	if err := w.profiles(ctx, src.Instance, snap.Profiles); err != nil {
		return err
	}

	// Titles, versions and the arr's view of them.
	arrTitles := map[int]titleRef{}     // arr id -> ours
	episodes := map[int]map[int]int64{} // series arr id -> arr episode id -> our episode id
	sum.Titles.Total = len(snap.Titles)
	for _, t := range snap.Titles {
		if id, ok := resolved[t.ArrID]; ok {
			t.TmdbID = id
		}
		if reason := unmappedReason(t); reason != "" {
			sum.Titles.Unmapped++
			sum.Unmapped = append(sum.Unmapped, Unmapped{ArrID: t.ArrID, Name: t.Name, Year: t.Year, ImdbID: t.ImdbID, Reason: reason})
			continue
		}
		ref, err := w.title(ctx, src, snap, t, sum)
		if err != nil {
			return fmt.Errorf("arr id %d: %w", t.ArrID, err)
		}
		arrTitles[t.ArrID] = ref
		if t.Kind == "series" {
			if episodes[t.ArrID], err = w.episodes(ctx, src.Instance, ref, t, sum); err != nil {
				return fmt.Errorf("arr id %d: episodes: %w", t.ArrID, err)
			}
		}
	}
	if err := w.unfollowStale(ctx, src, arrTitles, sum); err != nil {
		return err
	}
	if err := w.removeStale(ctx, src.Instance, arrTitles, sum); err != nil {
		return err
	}

	// History, then the registry from it, then files tied to their downloads.
	if err := w.history(ctx, src.Instance, snap, arrTitles, sum); err != nil {
		return err
	}
	contents, err := w.seedRegistry(ctx, src.Instance, sum)
	if err != nil {
		return err
	}
	if err := w.files(ctx, src.Instance, snap, arrTitles, episodes, contents, sum); err != nil {
		return err
	}
	if err := w.follow(ctx, src, snap, arrTitles, episodes, sum); err != nil {
		return err
	}
	if err := w.followerBlocklist(ctx, src, snap, arrTitles); err != nil {
		return err
	}
	if err := w.collections(ctx, src, snap, sum); err != nil {
		return err
	}
	return w.titleMonitored(ctx, arrTitles)
}

type titleRef struct {
	titleID, versionID int64
	owner              bool // this instance owns the version's state
}

func unmappedReason(t Title) string {
	switch {
	case t.Kind == "movie" && t.TmdbID == 0 && t.ImdbID != "":
		return "no TMDb id (run with --enrich to look it up by IMDb id)"
	case t.Kind == "movie" && t.TmdbID == 0:
		return "no TMDb id and no IMDb id"
	case t.Kind == "series" && t.TvdbID == 0:
		return "no TVDB id"
	}
	return ""
}

func (w *wtx) tags(ctx context.Context, instance string, tags map[int]string) error {
	ids := make([]int, 0, len(tags))
	for id := range tags {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		if _, err := w.upsert(ctx, "arr_tag", []kv{{"instance", instance}, {"arr_id", id}},
			[]kv{{"label", tags[id]}}, nil, nil); err != nil {
			return err
		}
	}
	return w.deleteMissing(ctx, "arr_tag", instance, ids)
}

func (w *wtx) profiles(ctx context.Context, instance string, ps []QualityProfile) error {
	ids := make([]int, 0, len(ps))
	for _, p := range ps {
		ids = append(ids, p.ArrID)
		if _, err := w.upsert(ctx, "arr_quality_profile", []kv{{"instance", instance}, {"arr_id", p.ArrID}},
			[]kv{{"quality_items", profileItems(p.QualityItems)}, {"name", p.Name}, {"cutoff", p.Cutoff}, {"upgrade_allowed", p.UpgradeAllowed},
				{"min_format_score", p.MinFormatScore}, {"cutoff_format_score", p.CutoffFormatScore}}, nil, nil); err != nil {
			return err
		}
	}
	return w.deleteMissing(ctx, "arr_quality_profile", instance, ids)
}

// deleteMissing removes the instance's rows of table whose arr_id is not in keep.
func (w *wtx) deleteMissing(ctx context.Context, table, instance string, keep []int) error {
	rows, err := w.tx.QueryContext(ctx, "SELECT arr_id FROM "+table+" WHERE instance = ?", instance)
	if err != nil {
		return err
	}
	in := map[int]bool{}
	for _, k := range keep {
		in[k] = true
	}
	var gone []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !in[id] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	for _, id := range gone {
		n, err := w.exec(ctx, "DELETE FROM "+table+" WHERE instance = ? AND arr_id = ?", instance, id)
		if err != nil {
			return err
		}
		w.changes.add(table, opDelete, int(n))
	}
	return nil
}

// title stores one arr title: our Title (by TMDb id), its Version for this instance's policy, the arr's record of
// it and the Seerr alias.
func (w *wtx) title(ctx context.Context, src Source, snap *Snapshot, t Title, sum *Summary) (titleRef, error) {
	var ref titleRef
	sum.Titles.Imported++

	// Refresh identity as the arr does (Radarr Movies/RefreshMovieService.cs:106-123;
	// Sonarr Tv/RefreshSeriesService.cs:90-114).
	var (
		id                   int64
		name, sortName, imdb sql.NullString
		year, secondaryYear  sql.NullInt64
		tmdb                 sql.NullInt64
		seriesType, origLang sql.NullString
		runtime              sql.NullInt64
		sceneNumbering       bool
	)
	key, keyVal := "tmdb_id", t.TmdbID
	if t.Kind == "series" {
		key, keyVal = "tvdb_id", t.TvdbID
	}
	err := w.tx.QueryRowContext(ctx, `SELECT id, name, sort_name, year, imdb_id, secondary_year, tmdb_id, series_type,
		original_language, runtime_minutes, use_scene_numbering FROM title WHERE kind = ? AND `+key+` = ?`,
		t.Kind, keyVal).Scan(&id, &name, &sortName, &year, &imdb, &secondaryYear, &tmdb, &seriesType, &origLang,
		&runtime, &sceneNumbering)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		anime := src.Anime != nil && *src.Anime
		var tvdb, st any
		if t.Kind == "series" {
			tvdb, st = t.TvdbID, nullStr(t.SeriesType)
		}
		res, err := w.tx.ExecContext(ctx, `INSERT INTO title (kind, name, sort_name, year, secondary_year, tmdb_id,
			tvdb_id, imdb_id, anime, monitored, series_type, use_scene_numbering, runtime_minutes, original_language,
			created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.Kind, t.Name, t.SortName, nullInt(t.Year), nullInt(t.SecondaryYear), nullInt(t.TmdbID), tvdb,
			nullStr(t.ImdbID), dbValue(anime), dbValue(t.Monitored), st, dbValue(t.UseSceneNumbering),
			nullInt(t.Runtime), nullStr(t.OriginalLanguage), w.now, w.now)
		if err != nil {
			return ref, fmt.Errorf("title: %w", err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return ref, err
		}
		w.changes.add("title", opInsert, 1)
	case err != nil:
		return ref, fmt.Errorf("title: %w", err)
	default:
		var sets []string
		var args []any
		if name.String != t.Name && t.Name != "" {
			sets, args = append(sets, "name = ?"), append(args, t.Name)
		}
		if sortName.String != t.SortName && t.SortName != "" {
			sets, args = append(sets, "sort_name = ?"), append(args, t.SortName)
		}
		if year.Int64 != int64(t.Year) && t.Year != 0 {
			sets, args = append(sets, "year = ?"), append(args, t.Year)
		}
		// The secondary year follows the arr (its metadata server sets it); an arr without one leaves it.
		if t.SecondaryYear != 0 && secondaryYear.Int64 != int64(t.SecondaryYear) {
			sets, args = append(sets, "secondary_year = ?"), append(args, t.SecondaryYear)
		}
		// The original language follows the arr's metadata server when the arr has one (both arrs).
		if t.OriginalLanguage != "" && origLang.String != t.OriginalLanguage {
			sets, args = append(sets, "original_language = ?"), append(args, t.OriginalLanguage)
		}
		// Both arrs supply runtime; do not erase a known value with an unknown one.
		if t.Runtime > 0 && runtime.Int64 != int64(t.Runtime) {
			sets, args = append(sets, "runtime_minutes = ?"), append(args, t.Runtime)
		}
		if t.Kind == "series" {
			if !tmdb.Valid && t.TmdbID != 0 {
				sets, args = append(sets, "tmdb_id = ?"), append(args, t.TmdbID)
			}
			if seriesType.String == "" && t.SeriesType != "" {
				sets, args = append(sets, "series_type = ?"), append(args, t.SeriesType)
			}
			// Metadata the arr's server keeps current: follow it when the arr has a value.
			if sceneNumbering != t.UseSceneNumbering {
				sets, args = append(sets, "use_scene_numbering = ?"), append(args, dbValue(t.UseSceneNumbering))
			}
		}
		if imdb.String == "" && t.ImdbID != "" {
			sets, args = append(sets, "imdb_id = ?"), append(args, t.ImdbID)
		} else if imdb.String != "" && t.ImdbID != "" && imdb.String != t.ImdbID {
			sum.Warnings = append(sum.Warnings, fmt.Sprintf("%s %d: IMDb id %s here, %s already stored",
				strings.TrimSuffix(key, "_id"), keyVal, t.ImdbID, imdb.String))
		}
		if len(sets) > 0 {
			args = append(args, w.now, id)
			if _, err := w.exec(ctx, "UPDATE title SET "+join(sets)+", updated_at = ? WHERE id = ?", args...); err != nil {
				return ref, err
			}
			w.changes.add("title", opUpdate, 1)
		}
		// Shared: another instance brought this title in already.
		var other int
		if err := w.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM arr_title WHERE title_id = ? AND instance <> ?`,
			id, snap.Instance).Scan(&other); err != nil {
			return ref, err
		}
		if other > 0 {
			sum.Titles.Shared++
		}
	}
	ref.titleID = id

	if t.CollectionTmdbID != 0 {
		n, err := w.exec(ctx, `INSERT OR IGNORE INTO title_external_id (title_id, source, external_id) VALUES (?, ?, ?)`,
			id, "tmdb_collection", strconv.Itoa(t.CollectionTmdbID))
		if err != nil {
			return ref, err
		}
		w.changes.add("title_external_id", opInsert, int(n))
	}
	if err := w.releaseDates(ctx, id, snap.Instance, t.Dates); err != nil {
		return ref, err
	}
	if err := w.aliases(ctx, id, snap.Instance, t.Aliases); err != nil {
		return ref, err
	}
	if t.Kind == "series" {
		if err := w.sceneMappings(ctx, id, snap.Instance, t.SceneMappings); err != nil {
			return ref, err
		}
	}

	// Version.
	state := versionState(t, w.nowTime)
	err = w.tx.QueryRowContext(ctx, `SELECT id FROM version WHERE title_id = ? AND policy_key = ?`, id, src.Version).
		Scan(&ref.versionID)
	newSource := errors.Is(err, sql.ErrNoRows)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := w.tx.ExecContext(ctx, `INSERT INTO version (title_id, policy_key, monitored, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, id, src.Version, dbValue(t.Monitored), state, w.now, w.now)
		if err != nil {
			return ref, fmt.Errorf("version: %w", err)
		}
		if ref.versionID, err = res.LastInsertId(); err != nil {
			return ref, err
		}
		w.changes.add("version", opInsert, 1)
	} else if err != nil {
		return ref, fmt.Errorf("version: %w", err)
	}

	// Ownership: the first instance to import a version owns its state.
	var ownerInst string
	var ownerArr int
	err = w.tx.QueryRowContext(ctx, `SELECT instance, arr_id FROM version_source WHERE version_id = ?`, ref.versionID).
		Scan(&ownerInst, &ownerArr)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := w.exec(ctx, `INSERT INTO version_source (version_id, instance, arr_id) VALUES (?, ?, ?)`,
			ref.versionID, snap.Instance, t.ArrID); err != nil {
			return ref, err
		}
		w.changes.add("version_source", opInsert, 1)
		ref.owner = true
	case err != nil:
		return ref, err
	case ownerInst == snap.Instance:
		ref.owner = true
		if ownerArr != t.ArrID { // the arr re-added the movie under a new id
			if _, err := w.exec(ctx, `UPDATE version_source SET arr_id = ? WHERE version_id = ?`, t.ArrID, ref.versionID); err != nil {
				return ref, err
			}
			w.changes.add("version_source", opUpdate, 1)
		}
	}
	if ref.owner {
		if _, err := w.upsert(ctx, "version", []kv{{"id", ref.versionID}},
			[]kv{{"monitored", t.Monitored}, {"state", state}}, nil, []kv{{"updated_at", w.now}}); err != nil {
			return ref, err
		}
	}
	if t.Monitored {
		sum.Titles.Monitored++
	}
	switch state {
	case "wanted":
		sum.Titles.Wanted++
	case "filled":
		sum.Titles.Filled++
	case "cutoff_met":
		sum.Titles.CutoffMet++
	}

	// The arr's record, and the Seerr alias.
	tagLabels := make([]string, 0, len(t.Tags))
	for _, tg := range t.Tags {
		if l, ok := snap.Tags[tg]; ok {
			tagLabels = append(tagLabels, l)
		} else {
			tagLabels = append(tagLabels, "#"+strconv.Itoa(tg))
		}
	}
	sort.Strings(tagLabels)
	tagsJSON, _ := json.Marshal(tagLabels)
	fileID := 0
	if t.File != nil {
		fileID = t.File.ArrID
	}
	var added any
	if !t.Added.IsZero() && t.Added.Year() > 1900 {
		added = store.Millis(t.Added)
	}
	if _, err := w.upsert(ctx, "arr_title", []kv{{"instance", snap.Instance}, {"arr_id", t.ArrID}},
		[]kv{{"kind", t.Kind}, {"title_id", id}, {"version_id", ref.versionID}, {"monitored", t.Monitored},
			{"quality_profile_id", t.QualityProfileID}, {"minimum_availability", t.MinimumAvailability},
			{"status", t.Status}, {"path", t.Path}, {"root_folder_path", t.RootFolderPath},
			{"tags", string(tagsJSON)}, {"arr_file_id", fileID}, {"added_at", added}, {"series_type", t.SeriesType}},
		nil, nil); err != nil {
		return ref, err
	}
	if _, err := w.upsert(ctx, "arr_alias", []kv{{"instance", snap.Instance}, {"kind", t.Kind}, {"arr_id", t.ArrID}},
		[]kv{{"target_id", id}}, []kv{{"created_at", w.now}}, nil); err != nil {
		return ref, err
	}
	// The versions this import gives the title: the source, and the followers when it owns the title (follow). One
	// that gets the title now (created above, or a follower follow will create) takes every current label.
	var versions, fresh []string
	if newSource {
		fresh = append(fresh, src.Version)
	} else {
		versions = append(versions, src.Version)
	}
	if ref.owner {
		for _, key := range src.Followers {
			var one int
			err := w.tx.QueryRowContext(ctx, `SELECT 1 FROM version WHERE title_id = ? AND policy_key = ?`, id, key).Scan(&one)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				fresh = append(fresh, key)
			case err != nil:
				return ref, err
			default:
				versions = append(versions, key)
			}
		}
	}
	if err := w.copyTags(ctx, snap.Instance, t.ArrID, id, versions, fresh, tagLabels, string(tagsJSON)); err != nil {
		return ref, err
	}
	return ref, nil
}

// requesterLabel matches a Seerr requester tag, "<user id>-<name>" or the older "<user id> - <name>".
var requesterLabel = regexp.MustCompile(`^(\d+)\s*-\s*([A-Za-z0-9-]+)$`)

// copyTags copies the arr's tag labels of a title to the views' tags (arrview_tag, arrview_title_tag) of the versions
// this import gives the title: versions that had it already, and fresh ones that get it in this run. Seerr requester
// tags take Seerr's current form ("1 - alice" becomes "1-alice"); every other label (behaviour tags such as
// best-available or debrid-upgrade, hand labels such as local) is copied as it is. A fresh version takes every current
// label once. For the others the copy is edge-triggered: a label is copied when it first appears on the arr's title
// (it is not in arr_title.tags_copied yet), then tags_copied becomes the arr's current labels (current, JSON as
// arr_title.tags). So a tag a view took off (an upgrade script clearing debrid-upgrade after its upgrade) is not put back
// while the arr keeps it. The copy only adds: nothing a view set is ever removed.
func (w *wtx) copyTags(ctx context.Context, instance string, arrID int, titleID int64, versions, fresh, labels []string,
	current string) error {
	var copiedJSON string
	if err := w.tx.QueryRowContext(ctx, `SELECT tags_copied FROM arr_title WHERE instance = ? AND arr_id = ?`,
		instance, arrID).Scan(&copiedJSON); err != nil {
		return fmt.Errorf("arr_title: %w", err)
	}
	if copiedJSON == current && len(fresh) == 0 {
		return nil
	}
	var copied []string
	_ = json.Unmarshal([]byte(copiedJSON), &copied)
	for _, l := range labels {
		if strings.HasPrefix(l, "#") { // #<id>: a tag id the arr does not list
			continue
		}
		targets := fresh
		if !slices.Contains(copied, l) {
			targets = append(slices.Clip(fresh), versions...)
		}
		if len(targets) == 0 {
			continue
		}
		label := strings.TrimSpace(l)
		if m := requesterLabel.FindStringSubmatch(label); m != nil {
			label = strings.ToLower(m[1] + "-" + m[2])
		}
		// Labels are unique ignoring case (COLLATE NOCASE): an existing tag keeps its id.
		var tagID int64
		err := w.tx.QueryRowContext(ctx, `SELECT id FROM arrview_tag WHERE label = ?`, label).Scan(&tagID)
		if errors.Is(err, sql.ErrNoRows) {
			res, err := w.tx.ExecContext(ctx, `INSERT INTO arrview_tag (label, created_at) VALUES (?, ?)`, label, w.now)
			if err != nil {
				return fmt.Errorf("arrview_tag: %w", err)
			}
			if tagID, err = res.LastInsertId(); err != nil {
				return err
			}
			w.changes.add("arrview_tag", opInsert, 1)
		} else if err != nil {
			return fmt.Errorf("arrview_tag: %w", err)
		}
		for _, v := range targets {
			n, err := w.exec(ctx, `INSERT INTO arrview_title_tag (version, title_id, tag_id) VALUES (?, ?, ?)
				ON CONFLICT DO NOTHING`, v, titleID, tagID)
			if err != nil {
				return err
			}
			w.changes.add("arrview_title_tag", opInsert, int(n))
		}
	}
	if copiedJSON == current {
		return nil
	}
	if _, err := w.exec(ctx, `UPDATE arr_title SET tags_copied = ? WHERE instance = ? AND arr_id = ?`, current,
		instance, arrID); err != nil {
		return err
	}
	w.changes.add("arr_title", opUpdate, 1)
	return nil
}

// versionState maps the arr's file state onto ours: no file is wanted; a file below the cutoff is filled. A series
// is wanted while a monitored episode that has aired has no file, filled while a file is below the cutoff.
func versionState(t Title, now time.Time) string {
	if t.Kind == "series" {
		return seriesState(t, now)
	}
	switch {
	case t.File == nil:
		return "wanted"
	case t.File.CutoffNotMet:
		return "filled"
	}
	return "cutoff_met"
}

// releaseDates makes the title's dates from this source match dates.
func (w *wtx) releaseDates(ctx context.Context, titleID int64, source string, dates []ReleaseDate) error {
	type key struct{ country, typ, date string }
	want := map[key]ReleaseDate{}
	for _, d := range dates {
		want[key{d.Country, d.Type, d.Date}] = d
	}
	rows, err := w.tx.QueryContext(ctx, `SELECT country, type, date, certification, note FROM title_release_date
		WHERE title_id = ? AND source = ?`, titleID, source)
	if err != nil {
		return err
	}
	have := map[key]ReleaseDate{}
	for rows.Next() {
		var d ReleaseDate
		if err := rows.Scan(&d.Country, &d.Type, &d.Date, &d.Certification, &d.Note); err != nil {
			rows.Close()
			return err
		}
		have[key{d.Country, d.Type, d.Date}] = d
	}
	rows.Close()
	for k, d := range have {
		if _, ok := want[k]; !ok {
			n, err := w.exec(ctx, `DELETE FROM title_release_date WHERE title_id = ? AND source = ? AND country = ?
				AND type = ? AND date = ?`, titleID, source, k.country, k.typ, k.date)
			if err != nil {
				return err
			}
			w.changes.add("title_release_date", opDelete, int(n))
		} else if want[k] != d {
			nd := want[k]
			if _, err := w.exec(ctx, `UPDATE title_release_date SET certification = ?, note = ? WHERE title_id = ?
				AND source = ? AND country = ? AND type = ? AND date = ?`, nd.Certification, nd.Note, titleID, source,
				k.country, k.typ, k.date); err != nil {
				return err
			}
			w.changes.add("title_release_date", opUpdate, 1)
		}
	}
	for k, d := range want {
		if _, ok := have[k]; ok {
			continue
		}
		if _, err := w.exec(ctx, `INSERT INTO title_release_date (title_id, source, country, type, date, certification,
			note) VALUES (?, ?, ?, ?, ?, ?, ?)`, titleID, source, d.Country, d.Type, d.Date, d.Certification, d.Note); err != nil {
			return err
		}
		w.changes.add("title_release_date", opInsert, 1)
	}
	return nil
}

// aliases makes the title's alternative titles and translations from this source match want.
func (w *wtx) aliases(ctx context.Context, titleID int64, source string, want []Alias) error {
	wanted := map[Alias]bool{}
	for _, a := range want {
		if a.Name != "" {
			wanted[a] = true
		}
	}
	rows, err := w.tx.QueryContext(ctx, `SELECT kind, name, language, country FROM title_alias
		WHERE title_id = ? AND source = ?`, titleID, source)
	if err != nil {
		return err
	}
	have := map[Alias]bool{}
	for rows.Next() {
		var a Alias
		if err := rows.Scan(&a.Kind, &a.Name, &a.Language, &a.Country); err != nil {
			rows.Close()
			return err
		}
		have[a] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for a := range have {
		if wanted[a] {
			continue
		}
		n, err := w.exec(ctx, `DELETE FROM title_alias WHERE title_id = ? AND source = ? AND kind = ? AND name = ?
			AND language = ? AND country = ?`, titleID, source, a.Kind, a.Name, a.Language, a.Country)
		if err != nil {
			return err
		}
		w.changes.add("title_alias", opDelete, int(n))
	}
	for a := range wanted {
		if have[a] {
			continue
		}
		if _, err := w.exec(ctx, `INSERT INTO title_alias (title_id, source, kind, name, language, country)
			VALUES (?, ?, ?, ?, ?, ?)`, titleID, source, a.Kind, a.Name, a.Language, a.Country); err != nil {
			return err
		}
		w.changes.add("title_alias", opInsert, 1)
	}
	return nil
}

// removeStale drops this instance's records of titles the arr no longer has. Our Title and Version stay (they are
// our state, and another instance may share them); the version's ownership is released.
func (w *wtx) removeStale(ctx context.Context, instance string, live map[int]titleRef, sum *Summary) error {
	rows, err := w.tx.QueryContext(ctx, `SELECT arr_id, version_id, title_id FROM arr_title WHERE instance = ?`, instance)
	if err != nil {
		return err
	}
	type gone struct {
		arrID              int
		versionID, titleID int64
	}
	var stale []gone
	for rows.Next() {
		var g gone
		if err := rows.Scan(&g.arrID, &g.versionID, &g.titleID); err != nil {
			rows.Close()
			return err
		}
		if _, ok := live[g.arrID]; !ok {
			stale = append(stale, g)
		}
	}
	rows.Close()
	liveTitle := map[int64]bool{}
	for _, r := range live {
		liveTitle[r.titleID] = true
	}
	for _, g := range stale {
		sum.Titles.Removed++
		for _, q := range []struct{ table, sql string }{
			{"version_file", `DELETE FROM version_file WHERE instance = ? AND version_id = ?`},
			{"version_source", `DELETE FROM version_source WHERE instance = ? AND version_id = ?`},
		} {
			n, err := w.exec(ctx, q.sql, instance, g.versionID)
			if err != nil {
				return err
			}
			w.changes.add(q.table, opDelete, int(n))
		}
		// The names this instance gave the title go with its record, unless the arr still has the title under
		// another id.
		if !liveTitle[g.titleID] {
			n, err := w.exec(ctx, `DELETE FROM title_alias WHERE title_id = ? AND source = ?`, g.titleID, instance)
			if err != nil {
				return err
			}
			w.changes.add("title_alias", opDelete, int(n))
			if n, err = w.exec(ctx, `DELETE FROM title_scene_mapping WHERE title_id = ? AND source = ?`, g.titleID,
				instance); err != nil {
				return err
			}
			w.changes.add("title_scene_mapping", opDelete, int(n))
		}
		n, err := w.exec(ctx, `DELETE FROM arr_title WHERE instance = ? AND arr_id = ?`, instance, g.arrID)
		if err != nil {
			return err
		}
		w.changes.add("arr_title", opDelete, int(n))
	}
	return nil
}

// history stores the new history events and ties stored events to titles.
func (w *wtx) history(ctx context.Context, instance string, snap *Snapshot, arrTitles map[int]titleRef, sum *Summary) error {
	h := &sum.History
	h.ArrTotal = snap.HistoryTotal
	for _, ev := range snap.History {
		var titleID, versionID any
		if ref, ok := arrTitles[ev.ArrTitleID]; ok {
			titleID, versionID = ref.titleID, ref.versionID
		}
		langs, _ := json.Marshal(nonNil(ev.Languages))
		data, _ := json.Marshal(ev.Data)
		var ep any
		if ev.ArrEpisodeID != 0 {
			ep = ev.ArrEpisodeID
		}
		n, err := w.exec(ctx, `INSERT OR IGNORE INTO arr_history (instance, arr_id, event_type, date_at, arr_title_id,
			arr_episode_id, title_id, version_id, download_id, info_hash, protocol, source_title, quality,
			custom_format_score, languages, data) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			instance, ev.ArrID, ev.EventType, store.Millis(ev.Date), ev.ArrTitleID, ep, titleID, versionID,
			nullStr(ev.DownloadID), nullStr(ev.InfoHash), ev.Protocol, ev.SourceTitle, ev.Quality,
			dbValue(ev.CustomFormatScore), string(langs), string(data))
		if err != nil {
			return err
		}
		h.New += int(n)
		w.changes.add("arr_history", opInsert, int(n))
	}
	// Events stored before their title was importable, and events whose title moved to another version.
	for arrID, ref := range arrTitles {
		n, err := w.exec(ctx, `UPDATE arr_history SET title_id = ?, version_id = ? WHERE instance = ? AND arr_title_id = ?
			AND (title_id IS NOT ? OR version_id IS NOT ?)`, ref.titleID, ref.versionID, instance, arrID, ref.titleID, ref.versionID)
		if err != nil {
			return err
		}
		h.Resolved += int(n)
		w.changes.add("arr_history", opUpdate, int(n))
	}

	if h.New > 0 || h.Resolved > 0 {
		if _, err := w.exec(ctx, "UPDATE source_ceiling SET next_check_at=0 WHERE title_id IN (SELECT title_id FROM arr_history WHERE instance=?)", instance); err != nil {
			return err
		}
	}
	// Counts over everything stored for the instance.
	h.ByEvent = map[string]int{}
	rows, err := w.tx.QueryContext(ctx, `SELECT event_type, protocol, title_id IS NULL, COUNT(*) FROM arr_history
		WHERE instance = ? GROUP BY 1, 2, 3`, instance)
	if err != nil {
		return err
	}
	unmappedIDs := 0
	for rows.Next() {
		var et, proto string
		var unmapped bool
		var n int
		if err := rows.Scan(&et, &proto, &unmapped, &n); err != nil {
			rows.Close()
			return err
		}
		h.Stored += n
		h.ByEvent[et] += n
		if et == EventGrabbed {
			switch proto {
			case "torrent":
				h.TorrentGrabs += n
			case "usenet":
				h.UsenetGrabs += n
			}
		}
		if unmapped {
			unmappedIDs += n
		}
	}
	rows.Close()
	if unmappedIDs > 0 {
		// Split by reason: the arr title is gone from the arr, or it is there but was not imported.
		inArr := map[int]bool{}
		for _, t := range snap.Titles {
			inArr[t.ArrID] = true
		}
		h.Unmapped = map[string]int{}
		rows, err := w.tx.QueryContext(ctx, `SELECT arr_title_id, COUNT(*) FROM arr_history WHERE instance = ?
			AND title_id IS NULL GROUP BY 1`, instance)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, n int
			if err := rows.Scan(&id, &n); err != nil {
				rows.Close()
				return err
			}
			switch {
			case id == 0:
				h.Unmapped["no arr title id"] += n
			case inArr[id]:
				h.Unmapped["arr title not imported"] += n
			default:
				h.Unmapped["arr title deleted from the arr"] += n
			}
		}
		rows.Close()
	}
	return nil
}

// files stores each title's current file and ties it to its download (history) and content (registry).
func (w *wtx) files(ctx context.Context, instance string, snap *Snapshot, arrTitles map[int]titleRef,
	episodes map[int]map[int]int64, contents map[string]int64, sum *Summary) error {
	imports, grabs, err := w.downloads(ctx, instance)
	if err != nil {
		return err
	}
	for _, t := range snap.Titles {
		ref, ok := arrTitles[t.ArrID]
		if !ok {
			continue
		}
		if t.Kind == "series" {
			if err := w.seriesFiles(ctx, instance, t, ref, episodes[t.ArrID], imports, grabs, contents, sum); err != nil {
				return err
			}
			continue
		}
		if t.File == nil {
			n, err := w.exec(ctx, `DELETE FROM version_file WHERE instance = ? AND version_id = ?`, instance, ref.versionID)
			if err != nil {
				return err
			}
			w.changes.add("version_file", opDelete, int(n))
			continue
		}
		vfID, err := w.file(ctx, instance, t.Kind, ref, t.File, imports, grabs, contents, sum)
		if err != nil {
			return err
		}
		// An upgrade replaced the file: drop the old one.
		n, err := w.exec(ctx, `DELETE FROM version_file WHERE instance = ? AND version_id = ? AND id <> ?`,
			instance, ref.versionID, vfID)
		if err != nil {
			return err
		}
		w.changes.add("version_file", opDelete, int(n))
	}
	// File aliases whose file is gone.
	n, err := w.exec(ctx, `DELETE FROM arr_alias WHERE instance = ? AND kind IN ('moviefile', 'episodefile')
		AND target_id NOT IN (SELECT id FROM version_file)`, instance)
	if err != nil {
		return err
	}
	w.changes.add("arr_alias", opDelete, int(n))
	return nil
}

type importEvent struct {
	downloadID, infoHash, title string
}

type grabEvent struct {
	title, protocol string
}

// downloads reads the instance's history: the latest import per arr file id, and the first grab per download id.
func (w *wtx) downloads(ctx context.Context, instance string) (map[int]importEvent, map[string]grabEvent, error) {
	imports := map[int]importEvent{}
	grabs := map[string]grabEvent{}
	rows, err := w.tx.QueryContext(ctx, `SELECT event_type, COALESCE(download_id, ''), COALESCE(info_hash, ''),
		protocol, source_title, data FROM arr_history WHERE instance = ? AND event_type IN (?, ?)
		ORDER BY date_at, arr_id`, instance, EventGrabbed, EventImported)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var et, dl, hash, proto, title, data string
		if err := rows.Scan(&et, &dl, &hash, &proto, &title, &data); err != nil {
			return nil, nil, err
		}
		if et == EventGrabbed {
			if _, ok := grabs[dl]; !ok && dl != "" {
				grabs[dl] = grabEvent{title: title, protocol: proto}
			}
			continue
		}
		var d map[string]string
		_ = json.Unmarshal([]byte(data), &d)
		fid, _ := strconv.Atoi(d["fileId"])
		if fid != 0 {
			imports[fid] = importEvent{downloadID: dl, infoHash: hash, title: title} // later rows win
		}
	}
	return imports, grabs, rows.Err()
}

// titleMonitored sets each touched title's monitored flag: monitored if any of its versions is.
func (w *wtx) titleMonitored(ctx context.Context, arrTitles map[int]titleRef) error {
	done := map[int64]bool{}
	for _, ref := range arrTitles {
		if done[ref.titleID] {
			continue
		}
		done[ref.titleID] = true
		n, err := w.exec(ctx, `UPDATE title SET monitored = m, updated_at = ? FROM (SELECT COALESCE(MAX(monitored), 0) AS m
			FROM version WHERE title_id = ?) WHERE id = ? AND monitored <> m`, w.now, ref.titleID, ref.titleID)
		if err != nil {
			return err
		}
		w.changes.add("title", opUpdate, int(n))
	}
	return nil
}

func compactJSON(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "{}"
	}
	b, _ := json.Marshal(v) // map keys sorted: stable across runs
	return string(b)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if path.Clean(x) == path.Clean(s) {
			return true
		}
	}
	return false
}

func join(sets []string) string {
	out := ""
	for i, s := range sets {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func profileItems(items string) string {
	if items == "" {
		return "[]"
	}
	return items
}
