package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
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
	// Formats are the custom formats a movie's single file matched, with their scores (see files only). A Theatarr
	// link keeps them from its import (link.file_metadata); an adopted link keeps none. Score is left out for a link
	// that stored no scoring evidence (linkFacts).
	Formats []decide.FormatScore `json:"formats,omitempty"`
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
	// Copies are the file's torrent's copies to other providers ("Add to other providers", router copy.go), by
	// InfoHash; omitted when none.
	Copies []router.CopyView `json:"copies,omitempty"`
}

// HistoryEvent is one step in a version's history.
type HistoryEvent struct {
	DecisionID *int64 `json:"decision_id,omitempty"`
	RunID      *int64 `json:"run_id,omitempty"`
	// Event is grabbed, imported, upgraded, failed, not_started (a download never tried because downloads were off;
	// Reason says why), deleted, renamed, ignored, searched or adopted (a file taken over from an arr: the episode window).
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
	// MissedOn (a failed event that was a miss on every provider): the cache codes (rd, ad, tb) of the providers the
	// download's own attempts found the release not cached on (missCodes); absent when no attempt says so, and the
	// reason stands alone.
	MissedOn []string `json:"missed_on,omitempty"`
	// Searched events: the search's results and accepted releases, what it grabbed in one line (searchSummary;
	// InfoHash is then the first release it grabbed) and Latest when its results are the ones the releases view
	// shows (the version's newest decision).
	Results  *int   `json:"results,omitempty"`
	Accepted *int   `json:"accepted,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Latest   bool   `json:"latest,omitempty"`
}

// missCodes are the provider attempt codes (download_attempt.code, internal/router) that say the release was not
// cached there: a known miss, known uncached, and an uncached add of ours deleted or kept.
const missCodes = `'miss', 'known_uncached', 'uncached_deleted', 'uncached_kept'`

// notStartedCol is a column of a query on the table download (unaliased): the download failed without ever being
// tried, because downloads were off when its turn came (activity.NotStartedSQL, as Activity reads it).
var notStartedCol = `(state = 'failed' AND ` + activity.NotStartedSQL + `)`

// downloadEvents appends a Theatarr download's events: grabbed, then imported or failed; a download that was never
// tried (notStarted, notStartedCol) ends with not_started and Activity's words as the reason, not failed. h carries
// the release fields; run is the acquire run of an acquire:<run> source.
func downloadEvents(out []HistoryEvent, h HistoryEvent, state string, created, done int64, source, errText string,
	notStarted bool, run *runOrigin) []HistoryEvent {
	h.Provider = h.Source
	if h.Source == "" {
		h.Source = "theatarr"
	} else {
		h.Source = "theatarr via " + h.Source
	}
	h.Trigger = grabWords(source, run)
	missed := h.MissedOn
	h.Event, h.Date, h.MissedOn = "grabbed", ts(created), nil
	out = append(out, h)
	switch state {
	case "imported":
		h.Event, h.Date = "imported", ts(done)
		out = append(out, h)
	case "failed":
		if r := activity.NotStarted(errText); notStarted && r != "" {
			h.Event, h.Date, h.Reason = "not_started", ts(done), activity.NotStartedHistory(r)
		} else {
			h.Event, h.Date, h.Reason, h.MissedOn = "failed", ts(done), shortReason(errText, h.Provider), missed
		}
		out = append(out, h)
	}
	return out
}

// EpisodeCounts is a series version's episodes outside the specials. Aired is airClock's rule.
type EpisodeCounts struct {
	// Total: episodes aired or held, what "of N" counts (an episode still to air is not one to be ready).
	Total int `json:"total"`
	// Have: episodes with a file (filled or cutoff_met). Missing: aired, monitored and wanted, in a monitored
	// library (0 in one that is not monitored).
	Have    int `json:"have"`
	Missing int `json:"missing"`
	// Monitored: the episodes of Total the library monitors (episode monitoring, whatever the library's);
	// MonitoredHave: those of them with a file. For callers who see files.
	Monitored     *int `json:"monitored,omitempty"`
	MonitoredHave *int `json:"monitored_have,omitempty"`
	// watchMissing is Missing whatever the library's monitoring: what a caller without "see files" gets (redactVersion).
	watchMissing int
}

// FranchiseRef is an anime franchise that maps into a title.
type FranchiseRef struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// enrichTitle adds the title page's fields to d (from getTitle). A version removed from the library (Removed, set by
// getTitle) counts in none of the title's rollups (availability, size, added).
func (w *Web) enrichTitle(ctx context.Context, d *TitleDetail) error {
	db := w.st.Reader()
	byID := map[int64]*Version{}
	var ids []any
	for i := range d.Versions {
		v := &d.Versions[i]
		v.Label = w.versionLabel(v.Key)
		v.History = []HistoryEvent{}
		byID[v.ID] = v
		ids = append(ids, v.ID)
	}
	d.Franchises = []FranchiseRef{}
	if len(ids) > 0 {
		if err := scanAll(ctx, db, `SELECT v.id, `+availStateSQL(newAirClock(w.now()))+` FROM version v
			WHERE v.title_id = ?`, []any{d.ID}, func(r *sql.Rows) error {
			var vid int64
			var st string
			if err := r.Scan(&vid, &st); err != nil {
				return err
			}
			if v := byID[vid]; v != nil {
				v.availState = st
			}
			return nil
		}); err != nil {
			return err
		}
	}
	avail, err := w.titleAvailability(ctx, db, []any{d.ID})
	if err != nil {
		return err
	}
	d.Availability = avail[d.ID]
	if d.Kind == "movie" {
		dates, err := activity.ReadMovieDates(ctx, db, []any{d.ID})
		if err != nil {
			return err
		}
		if h := availability.HomeRelease(dates[d.ID].Canonical); !h.IsZero() {
			d.HomeRelease = h.Format(time.DateOnly)
		}
		// The dates the About facts show: the release-date choice Home, the calendar and the lists use.
		fr := dates[d.ID].First
		d.InCinemas, d.DigitalRelease, d.PhysicalRelease = fr["theatrical"], fr["digital"], fr["physical"]
	}
	if len(ids) > 0 {
		aggs, provs, err := w.currentFiles(ctx, db, ids)
		if err != nil {
			return err
		}
		var added int64
		var current []*Version
		for id, v := range byID {
			v.Providers = provs[id]
			v.CachedOn = w.cacheCodes(provs[id])
			v.Current = currentOf(v, briefFiles(v.Label, aggs[id], nil))
			if v.Current != nil && !v.Removed {
				current = append(current, v)
				if v.Current.AddedAt != nil {
					added = max(added, v.Current.AddedAt.UnixMilli())
				}
			}
		}
		size := onDisk(current)
		// The current file's copies, and for a single current file Cached on too, follow the file's own hash, the one
		// the Torrent link and "Add to other providers" use (linkHashSQL, fileHashSQL).
		var hashes []string
		for _, v := range byID {
			if v.Current != nil && v.Current.InfoHash != "" {
				hashes = append(hashes, v.Current.InfoHash)
			}
		}
		if len(hashes) > 0 {
			held, err := heldByHash(ctx, db, hashes...)
			if err != nil {
				return err
			}
			copies := w.fileCopies(ctx, hashes)
			for _, v := range byID {
				if v.Current == nil || v.Current.InfoHash == "" {
					continue
				}
				h := strings.ToLower(v.Current.InfoHash)
				v.Current.Copies = copies[h]
				if v.Current.Files == 1 {
					// Only a single current file's Cached on follows its hash; several files keep the aggregate.
					v.Providers, v.CachedOn = held[h], w.cacheCodes(held[h])
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
		facts, err := w.searchFacts(ctx, db, d.ID, nil)
		if err != nil {
			return err
		}
		for i := range d.Versions {
			if f := facts[d.Versions[i].ID]; f != nil {
				d.Versions[i].LastSearch, d.Versions[i].Searching = f.last, f.searching
			}
		}
	}
	d.AutoSearch = w.autoSearch()
	if err := w.titleCollection(ctx, db, d); err != nil {
		return err
	}
	return w.titleFranchises(ctx, db, d)
}

// liveStates is what plainAvailability needs of the versions still in the library (not Removed).
func liveStates(vs []Version) []versionState {
	var out []versionState
	for _, v := range vs {
		if !v.Removed {
			out = append(out, versionState{availOr(v.availState, v.State), v.HasFile, v.Monitored})
		}
	}
	return out
}

// onDisk is the size on disk of the current files of versions, each real file counted once: links by their target,
// and an old-manager file whose path is a tracked link's path or target is that link's file. It reads what the
// title page has already loaded (Version.Links, Version.Files).
func onDisk(versions []*Version) int64 {
	sizes := map[string]int64{}
	add := func(key string, n int64) { sizes[key] = max(sizes[key], n) }
	byPath := map[string]string{} // a link's path and its target, each to the target
	for _, v := range versions {
		for _, l := range v.Links {
			if l.State != "active" {
				continue
			}
			byPath[l.Path], byPath[l.Target] = l.Target, l.Target
			var n int64
			if l.SizeBytes != nil {
				n = *l.SizeBytes
			}
			add("t:"+l.Target, n)
		}
	}
	for _, v := range versions {
		for _, f := range v.Files {
			if v.Source != "" && !f.Current {
				continue // another instance's file: not the version's current one (curFile)
			}
			if t, ok := byPath[f.Path]; ok {
				add("t:"+t, f.SizeBytes)
			} else {
				add("p:"+f.Path, f.SizeBytes)
			}
		}
	}
	var size int64
	for _, n := range sizes {
		size += n
	}
	return size
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

// versionHistory loads the title's versions' downloads and acquisition searches.
func (w *Web) versionHistory(ctx context.Context, db *sql.DB, titleID int64, kind string, byID map[int64]*Version) error {
	origins, err := runOrigins(ctx, db, titleID)
	if err != nil {
		return err
	}
	runDLs := map[int64][]runDownload{}
	err = scanAll(ctx, db, `SELECT version_id, state, created_at, COALESCE(imported_at, updated_at), release_title,
		info_hash, COALESCE(provider, ''), quality, score, source, COALESCE(error, ''), `+notStartedCol+`,
		CASE WHEN state = 'failed' AND COALESCE(error, '') GLOB 'miss on every provider*' THEN (SELECT json_group_array(DISTINCT a.provider) FROM download_attempt a
			WHERE a.download_id = download.id AND a.code IN (`+missCodes+`)) END
		FROM download WHERE title_id = ? ORDER BY created_at, id`,
		[]any{titleID}, func(r *sql.Rows) error {
			var vid, created, done, score int64
			var state, source, errText string
			var notStarted bool
			var missed sql.NullString
			var h HistoryEvent
			if err := r.Scan(&vid, &state, &created, &done, &h.ReleaseTitle, &h.InfoHash, &h.Source, &h.Quality,
				&score, &source, &errText, &notStarted, &missed); err != nil {
				return err
			}
			if missed.Valid {
				var provs []string
				_ = json.Unmarshal([]byte(missed.String), &provs)
				provs = slices.DeleteFunc(provs, func(p string) bool { return p == "" })
				slices.Sort(provs)
				h.MissedOn = w.cacheCodes(provs)
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
			v.History = downloadEvents(v.History, h, state, created, done, source, errText, notStarted, run)
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
	// The releases view shows each version's newest decision (storedReleases).
	newest := map[string]int64{}
	err = scanAll(ctx, db, `SELECT version_key, id, created_at FROM journal_decision WHERE title_id = ?
		ORDER BY created_at, id`, []any{titleID}, func(rows *sql.Rows) error {
		var key string
		var id, at int64
		if err := rows.Scan(&key, &id, &at); err != nil {
			return err
		}
		newest[key] = id
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
			Summary: searchSummary(runDLs[id], cands, oc, state, message, w.providerTag),
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

// LastSearch is a library's last targeted search (an acquire run: automatic, Search now, or after a failure; RSS reads
// are not searches): when, the run's state (routed, nothing, skipped or failed; queued or searching while it runs) and
// what it found. For an episode, Results and Accepted are set only when the run searched that episode alone: a
// season's counts are not the episode's. For callers who see files.
type LastSearch struct {
	At       time.Time `json:"at"`
	RunID    int64     `json:"run_id"`
	State    string    `json:"state"`
	Results  *int      `json:"results,omitempty"`
	Accepted *int      `json:"accepted,omitempty"`
}

// AutoSearch is whether Theatarr searches by itself (router.wanted): for missing items, for upgrades, and whether
// anything is searched or grabbed at all (router.enabled: off while Pause is on). For callers who see files.
type AutoSearch struct {
	Missing  bool `json:"missing"`
	Upgrades bool `json:"upgrades"`
	Enabled  bool `json:"enabled"`
}

func (w *Web) autoSearch() *AutoSearch {
	if w.pol == nil {
		return nil
	}
	s := w.pol.WantedSettings()
	return &AutoSearch{Missing: s.Missing, Upgrades: s.CutoffUnmet != policy.CutoffOff, Enabled: w.pol.RouterSettings().Enabled}
}

// searchFact is one library's last targeted search (nil: never) and the search under way: "searching" (a run is
// searching), "queued" (a run waits for its turn, or Search now made the item due); "" for none.
type searchFact struct {
	last      *LastSearch
	searching string
}

// coverSQL selects the acquire runs (alias r) that searched an episode (its two arguments: the episode id, its
// season): a run naming it, or a run naming no episodes for its season or the whole series.
const coverSQL = `(EXISTS (SELECT 1 FROM json_each(r.episodes) j WHERE j.value = ?) OR (COALESCE(r.episodes, '[]') IN ('', '[]', 'null')
	AND (r.season IS NULL OR r.season = ?)))`

// searchFacts reads, in three queries for the whole title, each library's last targeted search and a search under
// way, by version id. A run older than an hour that never finished is not under way (acquire's busySet). With ep set
// (an episode window) only the runs that searched that episode count, and a Search now row of its season.
func (w *Web) searchFacts(ctx context.Context, db *sql.DB, titleID int64, ep *struct{ id, season int64 }) (map[int64]*searchFact, error) {
	out := map[int64]*searchFact{}
	fact := func(vid int64) *searchFact {
		if out[vid] == nil {
			out[vid] = &searchFact{}
		}
		return out[vid]
	}
	cond, args := "", []any{titleID}
	if ep != nil {
		cond, args = " AND "+coverSQL, append(args, ep.id, ep.season)
	}
	err := scanAll(ctx, db, `SELECT version_id, id, created_at, state, results, accepted, episodes FROM (SELECT r.*,
		ROW_NUMBER() OVER (PARTITION BY r.version_id ORDER BY r.created_at DESC, r.id DESC) AS n FROM acquire_run r
		WHERE r.title_id = ?`+cond+`) WHERE n = 1`, args, func(rows *sql.Rows) error {
		var vid, at int64
		var l LastSearch
		var results, accepted int
		var eps string
		if err := rows.Scan(&vid, &l.RunID, &at, &l.State, &results, &accepted, &eps); err != nil {
			return err
		}
		l.At = ts(at)
		var ids []int64
		_ = json.Unmarshal([]byte(eps), &ids)
		if ep == nil || (len(ids) == 1 && ids[0] == ep.id) {
			l.Results, l.Accepted = &results, &accepted
		}
		fact(vid).last = &l
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = scanAll(ctx, db, `SELECT r.version_id, MAX(r.state = 'searching') FROM acquire_run r WHERE r.title_id = ?`+cond+`
		AND r.state IN ('queued', 'searching') AND r.created_at > ? GROUP BY r.version_id`,
		append(args, store.Millis(w.now().Add(-time.Hour))), func(rows *sql.Rows) error {
			var vid int64
			var running bool
			if err := rows.Scan(&vid, &running); err != nil {
				return err
			}
			fact(vid).searching = "queued"
			if running {
				fact(vid).searching = "searching"
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	// Search now writes its row due now with why asked; the next round starts it.
	scond, sargs := "", []any{titleID, store.Millis(w.now())}
	if ep != nil {
		scond, sargs = " AND sr.season IN (-1, ?)", append(sargs, ep.season)
	}
	err = scanAll(ctx, db, `SELECT DISTINCT v.id FROM search_request sr JOIN version v ON v.title_id = sr.title_id
		AND v.policy_key = sr.version_key WHERE sr.title_id = ? AND sr.why = 'asked' AND sr.next_at <= ?`+scond, sargs,
		func(rows *sql.Rows) error {
			var vid int64
			if err := rows.Scan(&vid); err != nil {
				return err
			}
			if f := fact(vid); f.searching == "" {
				f.searching = "queued"
			}
			return nil
		})
	return out, err
}

func episodeCode(season, number int64) string {
	return fmt.Sprintf("S%02dE%02d", season, number)
}

// episodeCounts sets each series version's episode counts, specials left out.
func (w *Web) episodeCounts(ctx context.Context, db *sql.DB, ids []any, byID map[int64]*Version) error {
	aired, airedArgs := newAirClock(w.now()).airedSQL("e")
	return scanAll(ctx, db, `SELECT s.version_id,
		SUM(s.state IN ('filled', 'cutoff_met') OR `+aired+`),
		SUM(s.state IN ('filled', 'cutoff_met')),
		SUM(s.state = 'wanted' AND s.monitored = 1 AND `+aired+`),
		SUM(s.monitored = 1 AND (s.state IN ('filled', 'cutoff_met') OR `+aired+`)),
		SUM(s.monitored = 1 AND s.state IN ('filled', 'cutoff_met'))
		FROM episode_version_state s JOIN episode e ON e.id = s.episode_id
		WHERE s.version_id IN (`+store.Placeholders(len(ids))+`) AND e.season > 0 GROUP BY s.version_id`,
		append(append(append(airedArgs, airedArgs...), airedArgs...), ids...), func(r *sql.Rows) error {
			var vid int64
			var c EpisodeCounts
			var mon, monHave int
			if err := r.Scan(&vid, &c.Total, &c.Have, &c.Missing, &mon, &monHave); err != nil {
				return err
			}
			c.Monitored, c.MonitoredHave = &mon, &monHave
			if v := byID[vid]; v != nil {
				c.watchMissing = c.Missing
				if !v.Monitored {
					c.Missing = 0 // a gap in a library that is not monitored is not missing (titleAvailabilitySQL)
				}
				v.Episodes = &c
			}
			return nil
		})
}

// titleCollection sets the stored collection a movie is part of (its TMDb collection id, title_external_id
// tmdb_collection), when one is stored.
func (w *Web) titleCollection(ctx context.Context, db *sql.DB, d *TitleDetail) error {
	if d.Kind != "movie" {
		return nil
	}
	var c CollectionRef
	err := db.QueryRowContext(ctx, `SELECT c.tmdb_id, c.name FROM title_external_id x
		JOIN movie_collection c ON c.tmdb_id = CAST(x.external_id AS INTEGER)
		WHERE x.title_id = ? AND x.source = 'tmdb_collection' AND c.name <> '' LIMIT 1`, d.ID).Scan(&c.TmdbID, &c.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	d.Collection = &c
	return nil
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
