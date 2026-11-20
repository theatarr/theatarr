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
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/policy"
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
}

// HistoryEvent is one step in a version's history.
type HistoryEvent struct {
	DecisionID *int64 `json:"decision_id,omitempty"`
	RunID      *int64 `json:"run_id,omitempty"`
	// DownloadID is a Theatarr download's id on its grabbed, imported and failed events (its provider attempts:
	// GET activity/downloads/{id}/attempts).
	DownloadID int64 `json:"download_id,omitempty"`
	// Event is grabbed, imported, upgraded (an import that replaced a file), failed, not_started (a download never
	// tried because downloads were off; Reason says why), deleted, renamed, ignored or searched.
	Event string `json:"event"`
	// Redownload (an upgraded event): every file it replaced was the same release, by name (a lost file fetched
	// again): "Re-downloaded", as History says it (journal HistoryRow.Redownload).
	Redownload bool      `json:"redownload,omitempty"`
	Date       time.Time `json:"date"`
	Quality    string    `json:"quality,omitempty"`
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
	// Upgrade (a failed event): the download was to replace a file. What it was for (the movie in its library, or
	// each of its episodes there) holds a file now (heldNowSQL).
	Upgrade bool `json:"upgrade,omitempty"`
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

// heldNowSQL is true for a download (the table download, unaliased) whose movie, or each of whose episodes, in its
// library holds a file now: its failure was an upgrade attempt. A series download with no episodes listed is never
// one.
const heldNowSQL = `(CASE WHEN json_array_length(COALESCE(download.episodes, '[]')) = 0 THEN
	(SELECT t.kind = 'movie' FROM title t WHERE t.id = download.title_id) AND EXISTS (SELECT 1 FROM version v
		WHERE v.id = download.version_id AND v.state IN ('filled', 'cutoff_met'))
	ELSE NOT EXISTS (SELECT 1 FROM json_each(download.episodes) e WHERE NOT EXISTS (SELECT 1 FROM episode_version_state s
		WHERE s.version_id = download.version_id AND s.episode_id = e.value AND s.state IN ('filled', 'cutoff_met'))) END)`

// notStartedCol is a column of a query on the table download (unaliased): the download failed without ever being
// tried, because downloads were off when its turn came (activity.NotStartedSQL, as Activity reads it).
var notStartedCol = `(state = 'failed' AND ` + activity.NotStartedSQL + `)`

// redownloadSQL is whether download's upgrade rows (history, deleted) fetched the same release again: some file was
// replaced and every replaced file has a release name, the row's own, at a score no lower than the row's (journal
// HistoryRow.Redownload).
const redownloadSQL = `(EXISTS (SELECT 1 FROM history x, json_each(COALESCE(x.replaced, '[]')) WHERE x.download_id = download.id
		AND x.event = 'deleted')
	AND NOT EXISTS (SELECT 1 FROM history x, json_each(COALESCE(x.replaced, '[]')) f WHERE x.download_id = download.id
		AND x.event = 'deleted' AND (COALESCE(json_extract(f.value, '$.release'), '') = ''
			OR json_extract(f.value, '$.release') IS NOT x.release
			OR COALESCE(x.score > json_extract(f.value, '$.score'), 0))))`

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
	missed, upgrade := h.MissedOn, h.Upgrade
	h.Event, h.Date, h.MissedOn, h.Upgrade = "grabbed", ts(created), nil, false
	out = append(out, h)
	switch state {
	case "imported":
		// An import that replaced a file (History's Upgraded row) is an upgrade.
		h.Event, h.Date = "imported", ts(done)
		if upgrade {
			h.Event = "upgraded"
		}
		out = append(out, h)
	case "failed":
		if r := activity.NotStarted(errText); notStarted && r != "" {
			h.Event, h.Date, h.Reason = "not_started", ts(done), activity.NotStartedHistory(r)
		} else {
			h.Event, h.Date, h.Reason, h.MissedOn, h.Upgrade = "failed", ts(done), failureHeadline(errText, h.Provider, missed), missed, upgrade
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
func (w *Web) enrichTitle(ctx context.Context, d *TitleDetail, seeFiles bool) error {
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
	var held map[[2]int64]bool // the held (version, episode) pairs (versionFiles)
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
	if seeFiles {
		// Tags are file detail: not read for an account without see files.
		tags, err := titleTags(ctx, db, []any{d.ID})
		if err != nil {
			return err
		}
		d.Tags = tags[d.ID]
	}
	if d.Kind == "movie" {
		dates, err := activity.ReadMovieDates(ctx, db, []any{d.ID})
		if err != nil {
			return err
		}
		// The home release (availability.HomeRelease: from the first cinema run anywhere without a home date).
		if h := availability.HomeRelease(dates[d.ID].Canonical); !h.IsZero() {
			d.HomeRelease = h.Format(time.DateOnly)
		}
		d.Status = movieStatus(d.Status, dates[d.ID].Canonical, w.now())
		if d.Meta != nil { // what About › Status reads
			d.Meta.Status = movieStatus(d.Meta.Status, dates[d.ID].Canonical, w.now())
		}
		// The dates the About facts show: the release-date choice Home, the calendar and the lists use.
		fr := dates[d.ID].First
		d.InCinemas, d.DigitalRelease, d.PhysicalRelease = fr["theatrical"], fr["digital"], fr["physical"]
	}
	if len(ids) > 0 {
		vf, err := readVersionFiles(ctx, db, ids)
		if err != nil {
			return err
		}
		aggs, provs := vf.aggs, vf.provs
		held = vf.held
		var added int64
		var current []*Version
		targets := func([]string) []string { return nil }
		if d.Kind == "movie" {
			targets = w.copyTargets(ctx)
		}
		for id, v := range byID {
			v.Providers = provs[id]
			v.CachedOn = w.cacheCodes(provs[id])
			v.Current = currentOf(v, briefFiles(v.Label, aggs[id], nil))
			if d.Kind == "movie" && v.Current != nil && indexer.NormalizeHash(v.Current.InfoHash) != "" {
				v.AddTo = targets(provs[id])
			}
			if v.Current != nil && !v.Removed {
				current = append(current, v)
				if v.Current.AddedAt != nil {
					added = max(added, v.Current.AddedAt.UnixMilli())
				}
			}
		}
		size := onDisk(current)
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
		if err := w.fillVersionAcquisition(ctx, db, d, held); err != nil {
			return err
		}
		vids := make([]int64, len(d.Versions))
		for i, v := range d.Versions {
			vids[i] = v.ID
		}
		facts, err := w.searchFacts(ctx, db, d.ID, vids, nil)
		if err != nil {
			return err
		}
		for i := range d.Versions {
			d.Versions[i].RSS = w.readsRSS(ctx, d.Versions[i].Key)
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
			out = append(out, versionState{availOr(v.availState, v.State), v.currentFile, v.Monitored})
		}
	}
	return out
}

// onDisk is the size on disk of the current files of versions, each real file counted once: links by their target.
// It reads what the title page has already loaded (Version.Links).
func onDisk(versions []*Version) int64 {
	sizes := map[string]int64{}
	for _, v := range versions {
		for _, l := range v.Links {
			if l.State != "active" {
				continue
			}
			var n int64
			if l.SizeBytes != nil {
				n = *l.SizeBytes
			}
			sizes[l.Target] = max(sizes[l.Target], n)
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
	err = scanAll(ctx, db, `SELECT id, version_id, state, created_at, COALESCE(imported_at, updated_at), release_title,
		info_hash, COALESCE(provider, ''), quality, score, source, COALESCE(error, ''), `+notStartedCol+`,
		CASE WHEN state = 'failed' THEN `+heldNowSQL+`
			WHEN state = 'imported' THEN EXISTS (SELECT 1 FROM history x WHERE x.download_id = download.id
				AND x.event = 'deleted') ELSE 0 END,
		CASE WHEN state = 'imported' THEN `+redownloadSQL+` ELSE 0 END,
		CASE WHEN state = 'failed' AND COALESCE(error, '') GLOB 'miss on every provider*' THEN (SELECT json_group_array(DISTINCT a.provider) FROM download_attempt a
			WHERE a.download_id = download.id AND a.code IN (`+missCodes+`)) END,
		CASE WHEN state = 'failed' THEN (SELECT b.reason FROM router_blocklist b WHERE b.download_id = download.id
			ORDER BY b.created_at DESC LIMIT 1) END,
		CASE WHEN state = 'failed' THEN (SELECT json_group_array(json_extract(j.value, '$.reason')) FROM library_import li,
			json_each(CASE WHEN json_valid(li.plan) THEN li.plan ELSE '{}' END, '$.rejected') j
			WHERE li.download_id = download.id) END
		FROM download WHERE title_id = ? ORDER BY created_at, id`,
		[]any{titleID}, func(r *sql.Rows) error {
			var dlID, vid, created, done, score int64
			var state, source, errText string
			var notStarted bool
			var missed, blocked, refusal sql.NullString
			var upgrade, redownload bool
			var h HistoryEvent
			if err := r.Scan(&dlID, &vid, &state, &created, &done, &h.ReleaseTitle, &h.InfoHash, &h.Source, &h.Quality,
				&score, &source, &errText, &notStarted, &upgrade, &redownload, &missed, &blocked, &refusal); err != nil {
				return err
			}
			if missed.Valid {
				var provs []string
				_ = json.Unmarshal([]byte(missed.String), &provs)
				provs = slices.DeleteFunc(provs, func(p string) bool { return p == "" })
				slices.Sort(provs)
				h.MissedOn = w.cacheCodes(provs)
			}
			// The library's refusal is read from its blocklist row and the file refusals its plan saved (failureWords).
			fail := failureFacts{Err: errText, Blocked: blocked.String, Refusals: refusalList(refusal.String),
				Provider: h.Source, MissedOn: h.MissedOn}
			errText = storedFailure(fail)
			v := byID[vid]
			if v == nil {
				return nil
			}
			var run *runOrigin
			if id := runIDOf(source); id > 0 {
				runDLs[id] = append(runDLs[id], runDownload{id: dlID, hash: h.InfoHash, release: h.ReleaseTitle,
					provider: h.Source, state: state, err: errText})
				if o, ok := origins[id]; ok {
					run = &o
				}
			}
			h.Score, h.DownloadID, h.Upgrade = &score, dlID, upgrade
			v.History = downloadEvents(v.History, h, state, created, done, source, errText, notStarted, run)
			if last := &v.History[len(v.History)-1]; last.Event == "upgraded" {
				last.Redownload = redownload
			}
			if last := &v.History[len(v.History)-1]; last.Event == "failed" {
				last.Reason, _ = failureWords(fail, strings.ToUpper)
			}
			return nil
		})
	if err != nil {
		return err
	}

	for _, v := range byID {
		h := v.History
		sort.SliceStable(h, func(i, j int) bool { return h[i].Date.Before(h[j].Date) })
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
	currents := map[int64]map[int64]bool{}
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
			if oc, currents[vid], err = w.releaseOutcomesCurrent(ctx, db, titleID, key, 0); err != nil {
				return err
			}
			outcomes[vid] = oc
		}
		var cands []snapshot.Candidate
		_ = json.Unmarshal([]byte(cj), &cands)
		h := HistoryEvent{Event: "searched", Date: ts(at), Episode: target, RunID: &id, DecisionID: nullInt(decision),
			Results: &results, Accepted: &accepted, Trigger: origins[id].words(),
			Latest: decision.Valid && newest[key] == decision.Int64}
		// One pick for the line and the row's release and provider.
		dls := runDLs[id]
		pick := pickDownload(dls, currents[vid])
		h.Summary = searchSummary(dls, pick, currents[vid], cands, oc, state, message, w.providerTag)
		if pick >= 0 {
			h.InfoHash, h.Provider = dls[pick].hash, dls[pick].provider
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

// RSSCoverage is the RSS reader's rule for which libraries its read covers (acquire/rss Runner.Covers).
type RSSCoverage interface {
	Covers(ctx context.Context, version string) bool
}

// readsRSS says whether the RSS read picks up new releases for a library, by the reader's own rule; false without
// one.
func (w *Web) readsRSS(ctx context.Context, key string) bool {
	return w.rss != nil && w.rss.Covers(ctx, key)
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

// searchFacts reads each library's (versionIDs) last targeted search and a search under way, by version id: one read
// of their acquire runs (newest first, by the acquire_run_target index) and one of Search now's due rows. A run older
// than an hour that never finished is not under way (acquire's busySet). With ep set (an episode window) only the runs
// that searched that episode count, and a Search now row of its season.
func (w *Web) searchFacts(ctx context.Context, db *sql.DB, titleID int64, versionIDs []int64, ep *struct{ id, season int64 }) (map[int64]*searchFact, error) {
	out := map[int64]*searchFact{}
	if len(versionIDs) == 0 {
		return out, nil
	}
	fact := func(vid int64) *searchFact {
		if out[vid] == nil {
			out[vid] = &searchFact{}
		}
		return out[vid]
	}
	args := make([]any, 0, len(versionIDs)+2)
	for _, id := range versionIDs {
		args = append(args, id)
	}
	cond := ""
	if ep != nil {
		cond, args = " AND "+coverSQL, append(args, ep.id, ep.season)
	}
	stale := store.Millis(w.now().Add(-time.Hour))
	err := scanAll(ctx, db, `SELECT r.version_id, r.id, r.created_at, r.state, r.results, r.accepted, r.episodes
		FROM acquire_run r WHERE r.version_id IN (`+store.Placeholders(len(versionIDs))+`)`+cond+`
		ORDER BY r.created_at DESC, r.id DESC`, args, func(rows *sql.Rows) error {
		var vid, at int64
		var l LastSearch
		var results, accepted int
		var eps string
		if err := rows.Scan(&vid, &l.RunID, &at, &l.State, &results, &accepted, &eps); err != nil {
			return err
		}
		f := fact(vid)
		if (l.State == "queued" || l.State == "searching") && at > stale && f.searching != "searching" {
			f.searching = l.State
		}
		if f.last != nil {
			return nil
		}
		l.At = ts(at)
		var ids []int64
		_ = json.Unmarshal([]byte(eps), &ids)
		if ep == nil || (len(ids) == 1 && ids[0] == ep.id) {
			l.Results, l.Accepted = &results, &accepted
		}
		f.last = &l
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Search now writes its row due now with why asked, a list addition with why added; the next round starts it,
	// whatever the backlog switches say.
	scond, sargs := "", []any{titleID, store.Millis(w.now())}
	if ep != nil {
		scond, sargs = " AND sr.season IN (-1, ?)", append(sargs, ep.season)
	}
	err = scanAll(ctx, db, `SELECT DISTINCT v.id FROM search_request sr JOIN version v ON v.title_id = sr.title_id
		AND v.policy_key = sr.version_key WHERE sr.title_id = ? AND sr.why IN ('asked', 'added') AND sr.next_at <= ?`+scond, sargs,
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
		WHERE s.version_id IN (`+store.Placeholders(len(ids))+`) AND e.season > 0 AND e.removed_at IS NULL
		GROUP BY s.version_id`,
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

// metadataPending reports whether the title's first metadata refresh is still to come: a series added since its
// series refresh last tried it (series_meta, the rule tvrefresh's enqueueAdded queues by), a film the artwork job has
// not tried yet (no title_meta row). False when that refresh is not configured (no series refresh; no artwork service
// or no TMDb key), and once tried, whatever came of it.
func (w *Web) metadataPending(ctx context.Context, db *sql.DB, d *TitleDetail) (bool, error) {
	if d.Kind != string(policy.KindSeries) {
		return d.Meta == nil && w.art != nil && w.add.TMDb != nil, nil
	}
	if w.seriesLookup == nil || !w.seriesLookup.Enabled() {
		return false, nil
	}
	var pending bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM series_meta WHERE title_id = ? AND added_at IS NOT NULL
		AND (attempted_at IS NULL OR attempted_at < added_at))`, d.ID).Scan(&pending)
	return pending, err
}
