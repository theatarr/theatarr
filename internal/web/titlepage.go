package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/store"
)

// The title page's additions to GET titles/{id} (docs/design/ui.md): per version a label, the current file in
// brief, providers, episode counts for a series, and history (grabs, imports, upgrades, failures); the anime
// franchises that map into the title, for the order panel. Fields for files and hashes are stripped for callers
// without "see files" (perms.go).

// historyLimit caps the history kept per version on the title page.
const historyLimit = 50

// CurrentFile is a version's current file in brief; for a series, its episode files together.
type CurrentFile struct {
	Quality   string     `json:"quality,omitempty"`
	Files     int        `json:"files,omitempty"`
	AddedAt   *time.Time `json:"added_at,omitempty"`
	Languages []string   `json:"languages,omitempty"`
	Edition   string     `json:"edition,omitempty"`
	// CutoffMet is true when the version's state is cutoff_met: no upgrade wanted.
	CutoffMet bool `json:"cutoff_met,omitempty"`
	// The rest is for callers who see files. Release, path and hash are set for a single file only.
	SizeBytes    *int64 `json:"size_bytes,omitempty"`
	Score        *int64 `json:"score,omitempty"`
	ReleaseTitle string `json:"release_title,omitempty"`
	ReleaseGroup string `json:"release_group,omitempty"`
	Path         string `json:"path,omitempty"`
	InfoHash     string `json:"info_hash,omitempty"`
	Instance     string `json:"instance,omitempty"`
	// Media is a movie file's media info (manage.go).
	Media *MediaInfo `json:"media,omitempty"`
}

// HistoryEvent is one step in a version's history.
type HistoryEvent struct {
	DecisionID *int64 `json:"decision_id,omitempty"`
	RunID      *int64 `json:"run_id,omitempty"`
	// Event is grabbed, imported, upgraded, failed, deleted, renamed, ignored or searched.
	Event   string    `json:"event"`
	Date    time.Time `json:"date"`
	Quality string    `json:"quality,omitempty"`
	// Episode is S01E02 for a series event about one episode.
	Episode string `json:"episode,omitempty"`
	// For callers who see files: where it came from (an arr instance, or Theatarr), the release and its hash.
	Source       string `json:"source,omitempty"`
	ReleaseTitle string `json:"release_title,omitempty"`
	Score        *int64 `json:"score,omitempty"`
	InfoHash     string `json:"info_hash,omitempty"`
	// Trigger says in plain words what started the grab or search: automatic search, RSS, manual search, manual
	// grab by a person, approved upgrade, Seerr request, an arr (grabWords, runOrigin.words).
	Trigger string `json:"trigger,omitempty"`
	// Theatarr's grabbed and failed events: the provider, and a failure's short reason.
	Provider string `json:"provider,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// Searched events: the search's results and accepted releases, what it grabbed in one line (searchSummary;
	// InfoHash is then the first release it grabbed) and Latest when its results are the ones the releases view
	// shows (the version's newest decision).
	Results  *int   `json:"results,omitempty"`
	Accepted *int   `json:"accepted,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Latest   bool   `json:"latest,omitempty"`
}

// downloadEvents appends a Theatarr download's events: grabbed, then imported or failed. h carries the release
// fields; run is the acquire run of an acquire:<run> source.
func downloadEvents(out []HistoryEvent, h HistoryEvent, state string, created, done int64, source, errText string,
	run *runOrigin) []HistoryEvent {
	h.Provider = h.Source
	if h.Source == "" {
		h.Source = "theatarr"
	} else {
		h.Source = "theatarr via " + h.Source
	}
	h.Trigger = grabWords(source, run)
	h.Event, h.Date = "grabbed", ts(created)
	out = append(out, h)
	switch state {
	case "imported":
		h.Event, h.Date = "imported", ts(done)
		out = append(out, h)
	case "failed":
		h.Event, h.Date, h.Reason = "failed", ts(done), shortReason(errText, h.Provider)
		out = append(out, h)
	}
	return out
}

// EpisodeCounts is a series version's episodes outside the specials.
type EpisodeCounts struct {
	Total int `json:"total"`
	// Have: episodes with a file (filled or cutoff_met). Missing: aired, monitored and wanted.
	Have    int `json:"have"`
	Missing int `json:"missing"`
}

// FranchiseRef is an anime franchise that maps into a title.
type FranchiseRef struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

var arrEvents = map[string]string{
	"grabbed": "grabbed", "downloadFolderImported": "imported", "downloadFailed": "failed",
	"movieFileDeleted": "deleted", "episodeFileDeleted": "deleted", "movieFileRenamed": "renamed",
	"episodeFileRenamed": "renamed", "downloadIgnored": "ignored",
}

// enrichTitle adds the title page's fields to d (from getTitle).
func (w *Web) enrichTitle(ctx context.Context, d *TitleDetail) error {
	db := w.st.Reader()
	byID := map[int64]*Version{}
	var ids []any
	states := make([]versionState, len(d.Versions))
	for i := range d.Versions {
		v := &d.Versions[i]
		v.Label = w.versionLabel(v.Key)
		v.History = []HistoryEvent{}
		byID[v.ID] = v
		ids = append(ids, v.ID)
		states[i] = versionState{v.State, v.HasFile}
	}
	d.Availability = availabilityOf(states)
	d.Franchises = []FranchiseRef{}
	if len(ids) > 0 {
		aggs, provs, err := w.currentFiles(ctx, db, ids)
		if err != nil {
			return err
		}
		var size, added int64
		for id, v := range byID {
			v.Providers = provs[id]
			v.Current = currentOf(v, briefFiles(v.Label, aggs[id], nil))
			if v.Current != nil {
				if v.Current.SizeBytes != nil {
					size += *v.Current.SizeBytes
				}
				if v.Current.AddedAt != nil {
					added = max(added, v.Current.AddedAt.UnixMilli())
				}
			}
		}
		if size > 0 {
			d.SizeBytes = &size
		}
		if added > 0 {
			t := ts(added)
			d.AddedAt = &t
		}
		if err := w.versionHistory(ctx, db, d.ID, d.Kind, byID); err != nil {
			return err
		}
		if d.Kind == "series" {
			if err := w.episodeCounts(ctx, db, ids, byID); err != nil {
				return err
			}
		}
		if err := w.enrichManage(ctx, db, d); err != nil {
			return err
		}
		if err := w.fillVersionAcquisition(ctx, db, d); err != nil {
			return err
		}
	}
	return w.titleFranchises(ctx, db, d)
}

// currentOf builds the current-file brief: the single current file's details when there is one.
func currentOf(v *Version, files VersionFiles) *CurrentFile {
	if files.Files == 0 {
		return nil
	}
	c := &CurrentFile{Quality: files.Quality, Files: files.Files, AddedAt: files.AddedAt, SizeBytes: files.SizeBytes,
		CutoffMet: v.State == "cutoff_met"}
	var cur []File
	for _, f := range v.Files {
		if f.Current {
			cur = append(cur, f)
		}
	}
	if len(cur) == 0 && v.Source == "" {
		cur = v.Files
	}
	if len(cur) == 1 && len(v.Links) == 0 {
		f := cur[0]
		c.Languages, c.Edition, c.Score, c.ReleaseGroup, c.Path, c.InfoHash, c.Instance = f.Languages, f.Edition,
			f.CustomFormatScore, f.ReleaseGroup, f.Path, f.InfoHash, f.Instance
		c.ReleaseTitle = f.ReleaseTitle
		if c.ReleaseTitle == "" {
			c.ReleaseTitle = f.SceneName
		}
	}
	var active []Link
	for _, l := range v.Links {
		if l.State == "active" {
			active = append(active, l)
		}
	}
	if len(active) == 1 {
		l := active[0]
		c.Score, c.ReleaseTitle, c.Path, c.InfoHash, c.Instance = l.Score, l.ReleaseTitle, l.Path, l.InfoHash, "theatarr"
	}
	return c
}

// versionHistory loads the title's versions' arr history, downloads and acquisition searches.
func (w *Web) versionHistory(ctx context.Context, db *sql.DB, titleID int64, kind string, byID map[int64]*Version) error {
	err := scanAll(ctx, db, `SELECT h.version_id, h.event_type, h.date_at, h.quality, h.instance, h.source_title,
		h.custom_format_score, COALESCE(h.info_hash, ''), e.season, e.number
		FROM arr_history h LEFT JOIN arr_alias a ON a.instance = h.instance AND a.kind = 'episode' AND a.arr_id = h.arr_episode_id
		LEFT JOIN episode e ON e.id = a.target_id
		WHERE h.title_id = ? AND h.version_id IS NOT NULL ORDER BY h.date_at, h.arr_id`, []any{titleID},
		func(r *sql.Rows) error {
			var vid, at int64
			var typ string
			var score, season, number sql.NullInt64
			var h HistoryEvent
			if err := r.Scan(&vid, &typ, &at, &h.Quality, &h.Source, &h.ReleaseTitle, &score, &h.InfoHash, &season,
				&number); err != nil {
				return err
			}
			v := byID[vid]
			ev, ok := arrEvents[typ]
			if v == nil || !ok {
				return nil
			}
			h.Event, h.Date, h.Score, h.Trigger = ev, ts(at), nullInt(score), arrWords(h.Source)
			if ev == "renamed" || ev == "deleted" {
				// Their source title is the file's path, not a release.
				h.ReleaseTitle = ""
			}
			if season.Valid && number.Valid {
				h.Episode = episodeCode(season.Int64, number.Int64)
			}
			v.History = append(v.History, h)
			return nil
		})
	if err != nil {
		return err
	}
	origins, err := runOrigins(ctx, db, titleID)
	if err != nil {
		return err
	}
	runDLs := map[int64][]runDownload{}
	err = scanAll(ctx, db, `SELECT version_id, state, created_at, COALESCE(imported_at, updated_at), release_title,
		info_hash, COALESCE(provider, ''), quality, score, source, COALESCE(error, '') FROM download WHERE title_id = ?
		ORDER BY created_at, id`,
		[]any{titleID}, func(r *sql.Rows) error {
			var vid, created, done, score int64
			var state, source, errText string
			var h HistoryEvent
			if err := r.Scan(&vid, &state, &created, &done, &h.ReleaseTitle, &h.InfoHash, &h.Source, &h.Quality,
				&score, &source, &errText); err != nil {
				return err
			}
			v := byID[vid]
			if v == nil {
				return nil
			}
			var run *runOrigin
			if id := runIDOf(source); id > 0 {
				runDLs[id] = append(runDLs[id], runDownload{hash: h.InfoHash, release: h.ReleaseTitle,
					provider: h.Source, state: state, err: errText})
				if o, ok := origins[id]; ok {
					run = &o
				}
			}
			h.Score = &score
			v.History = downloadEvents(v.History, h, state, created, done, source, errText, run)
			return nil
		})
	if err != nil {
		return err
	}

	for _, v := range byID {
		h := v.History
		sort.SliceStable(h, func(i, j int) bool { return h[i].Date.Before(h[j].Date) })
		// A movie import after an earlier one replaced a file: an upgrade.
		if kind == "movie" {
			imported := false
			for i := range h {
				if h[i].Event == "imported" {
					if imported {
						h[i].Event = "upgraded"
					}
					imported = true
				}
			}
		}
		// Newest first, at most historyLimit.
		for i, j := 0, len(h)-1; i < j; i, j = i+1, j-1 {
			h[i], h[j] = h[j], h[i]
		}
		if len(h) > historyLimit {
			h = h[:historyLimit]
		}
		v.History = h
	}
	// The releases view shows each version's newest decision, unless the shadow runner searched since
	// (storedReleases).
	newest := map[string]int64{}
	newestAt := map[string]int64{}
	err = scanAll(ctx, db, `SELECT version_key, id, created_at FROM journal_decision WHERE title_id = ?
		ORDER BY created_at, id`, []any{titleID}, func(rows *sql.Rows) error {
		var key string
		var id, at int64
		if err := rows.Scan(&key, &id, &at); err != nil {
			return err
		}
		newest[key], newestAt[key] = id, at
		return nil
	})
	if err != nil {
		return err
	}
	err = scanAll(ctx, db, `SELECT version_key, MAX(COALESCE(searched_at, updated_at)) FROM shadow_grab
		WHERE title_id = ? AND state = 'done' AND searched = 1 GROUP BY version_key`, []any{titleID},
		func(rows *sql.Rows) error {
			var key string
			var at int64
			if err := rows.Scan(&key, &at); err != nil {
				return err
			}
			if at > newestAt[key] {
				delete(newest, key)
			}
			return nil
		})
	if err != nil {
		return err
	}
	outcomes := map[int64]map[string]ReleaseOutcome{}
	err = scanAll(ctx, db, `SELECT r.version_id, r.version_key, r.id, r.created_at, r.target, r.state, r.message,
		r.results, r.accepted, d.id, COALESCE(d.candidates, '[]')
		FROM acquire_run r LEFT JOIN journal_decision d ON d.run_id = r.id
		AND d.id = (SELECT MAX(j.id) FROM journal_decision j WHERE j.run_id = r.id)
		WHERE r.title_id = ? ORDER BY r.created_at DESC, r.id DESC LIMIT 10`, []any{titleID}, func(rows *sql.Rows) error {
		var vid, id, at int64
		var results, accepted int
		var decision sql.NullInt64
		var key, target, state, message, cj string
		if err := rows.Scan(&vid, &key, &id, &at, &target, &state, &message, &results, &accepted, &decision,
			&cj); err != nil {
			return err
		}
		v := byID[vid]
		if v == nil {
			return nil
		}
		oc, ok := outcomes[vid]
		if !ok {
			var err error
			if oc, err = releaseOutcomes(ctx, db, titleID, key, 0); err != nil {
				return err
			}
			outcomes[vid] = oc
		}
		var cands []snapshot.Candidate
		_ = json.Unmarshal([]byte(cj), &cands)
		h := HistoryEvent{Event: "searched", Date: ts(at), Episode: target, RunID: &id, DecisionID: nullInt(decision),
			Results: &results, Accepted: &accepted, Trigger: origins[id].words(),
			Summary: searchSummary(runDLs[id], cands, oc, state, message),
			Latest:  decision.Valid && newest[key] == decision.Int64}
		if dls := runDLs[id]; len(dls) > 0 {
			h.InfoHash, h.Provider = dls[0].hash, dls[0].provider
		}
		v.History = append(v.History, h)
		return nil
	})
	if err != nil {
		return err
	}

	for _, v := range byID {
		sort.SliceStable(v.History, func(i, j int) bool { return v.History[i].Date.After(v.History[j].Date) })
	}

	return nil
}

func episodeCode(season, number int64) string {
	return fmt.Sprintf("S%02dE%02d", season, number)
}

// episodeCounts sets each series version's episode counts, specials left out.
func (w *Web) episodeCounts(ctx context.Context, db *sql.DB, ids []any, byID map[int64]*Version) error {
	today := w.now().UTC().Format(time.DateOnly)
	return scanAll(ctx, db, `SELECT s.version_id, COUNT(*),
		SUM(s.state IN ('filled', 'cutoff_met')),
		SUM(s.state = 'wanted' AND s.monitored = 1 AND e.air_date IS NOT NULL AND e.air_date <= ?)
		FROM episode_version_state s JOIN episode e ON e.id = s.episode_id
		WHERE s.version_id IN (`+store.Placeholders(len(ids))+`) AND e.season > 0 GROUP BY s.version_id`,
		append([]any{today}, ids...), func(r *sql.Rows) error {
			var vid int64
			var c EpisodeCounts
			if err := r.Scan(&vid, &c.Total, &c.Have, &c.Missing); err != nil {
				return err
			}
			if v := byID[vid]; v != nil {
				v.Episodes = &c
			}
			return nil
		})
}

// titleFranchises lists the anime franchises that map into the title: by TVDB series for a series, by TMDb movie
// for a movie.
func (w *Web) titleFranchises(ctx context.Context, db *sql.DB, d *TitleDetail) error {
	var q string
	var arg any
	switch {
	case d.Kind == "series" && d.TVDbID != nil:
		q, arg = `SELECT DISTINCT f.slug, f.name FROM anime_mapping m JOIN anime_franchise f ON f.slug = m.franchise
			WHERE m.tvdb_series = ? ORDER BY f.slug`, *d.TVDbID
	case d.Kind == "movie" && d.TMDbID != nil:
		q, arg = `SELECT DISTINCT f.slug, f.name FROM anime_entry e, json_each(e.tmdb_movie) j
			JOIN anime_franchise f ON f.slug = e.franchise WHERE j.value = ? ORDER BY f.slug`, *d.TMDbID
	default:
		return nil
	}
	return scanAll(ctx, db, q, []any{arg}, func(r *sql.Rows) error {
		var f FranchiseRef
		if err := r.Scan(&f.Slug, &f.Name); err != nil {
			return err
		}
		d.Franchises = append(d.Franchises, f)
		return nil
	})
}
