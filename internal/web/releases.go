package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// GET /titles/{id}/releases: the title page's "Show releases" panel (admin), the decision engine's results for one
// version with scores and rejections. A live indexer search runs with live=1; otherwise the answer is the last
// stored decision: the journal's decided search (acquirer or RSS).

// Releases is the answer of GET /titles/{id}/releases.
type Releases struct {
	GrabState VersionGrabState `json:"grab_state"`
	TitleID   int64            `json:"title_id"`
	Version   string           `json:"version"`
	// Versions are the title's versions, for the panel's picker.
	Versions []string `json:"versions"`
	// LiveAllowed: live=1 would search the indexers (the acquirer runs).
	LiveAllowed bool `json:"live_allowed"`
	// Source is live, journal or none.
	Source     string         `json:"source"`
	At         *time.Time     `json:"at,omitempty"`
	Target     string         `json:"target,omitempty"`
	Results    int            `json:"results"`
	Accepted   int            `json:"accepted"`
	Candidates []ReleaseRow   `json:"candidates"`
	Rejections map[string]int `json:"rejections"`
	Notes      string         `json:"notes,omitempty"`
	// Indexers counts the indexers a live search asked; absent for a stored answer.
	Indexers int `json:"indexers,omitempty"`
	// Message says why nothing live was searched, or what the stored answer is.
	Message string `json:"message,omitempty"`
	// Grab: a live answer whose candidates may be grabbed for 30 minutes (POST /titles/{id}/releases/grab, ops.go).
	Grab bool `json:"grab"`
	// Outcomes says what came of each release of the version, by lower-case info hash (releaseOutcomes): for the
	// stored list and a live search alike; for an episode's releases, from that episode's downloads and files.
	Outcomes map[string]ReleaseOutcome `json:"outcomes"`
	// Grabbed says in one line what the stored decision's search grabbed (searchSummary); empty for a live answer.
	Grabbed string `json:"grabbed,omitempty"`
	// Current is the file the requested film or episode holds in this library now (interactive search's "Current
	// file" line); absent for a season or a whole series.
	Current *HeldFile `json:"current,omitempty"`
	// LeftOut, for a live search of a whole series (only packs of several seasons are kept),
	// counts the results left out: a season search finds those.
	LeftOut *int `json:"left_out,omitempty"`
}

// ReleaseRow is one row of a release list: one torrent (info hash), shown as its first copy in the list's ranked
// order, with every indexer that returned it. A release without an info hash is a row of its own. Only the native
// answer merges copies: the journal's stored rows, the grab cache and the Sonarr/Radarr-compatible list keep one
// release per indexer.
type ReleaseRow struct {
	snapshot.Candidate
	// Indexers are the indexers that returned the torrent: the shown copy's first, then in list order.
	Indexers []string `json:"indexers"`
	// Copies are the torrent's other copies, in list order.
	Copies []ReleaseCopy `json:"copies,omitempty"`
}

// ReleaseCopy is another indexer's copy of a row's torrent.
type ReleaseCopy struct {
	Indexer string `json:"indexer,omitempty"`
	Release string `json:"release"`
}

// mergeCopies makes one row per info hash of cs, which is in ranked order: the first copy stands for the row, the
// peers are those of the copy with the most seeders, cached on is the union.
func mergeCopies(cs []snapshot.Candidate) []ReleaseRow {
	out := make([]ReleaseRow, 0, len(cs))
	at := map[string]int{}
	for _, c := range cs {
		h := indexer.NormalizeHash(c.InfoHash)
		if i, ok := at[h]; ok && h != "" {
			r := &out[i]
			r.Copies = append(r.Copies, ReleaseCopy{Indexer: c.Indexer, Release: c.Release})
			if c.Indexer != "" && !slices.Contains(r.Indexers, c.Indexer) {
				r.Indexers = append(r.Indexers, c.Indexer)
			}
			if c.Seeders > r.Seeders {
				r.Seeders, r.Leechers = c.Seeders, c.Leechers
			}
			for _, p := range c.CachedOn {
				if !slices.Contains(r.CachedOn, p) {
					r.CachedOn = append(slices.Clip(r.CachedOn), p)
				}
			}
			continue
		}
		if h != "" {
			at[h] = len(out)
		}
		row := ReleaseRow{Candidate: c, Indexers: []string{}}
		if c.Indexer != "" {
			row.Indexers = append(row.Indexers, c.Indexer)
		}
		out = append(out, row)
	}
	return out
}

// candidatesOf is the rows' shown copies.
func candidatesOf(rows []ReleaseRow) []snapshot.Candidate {
	out := make([]snapshot.Candidate, len(rows))
	for i, r := range rows {
		out[i] = r.Candidate
	}
	return out
}

// HeldFile is the file a film or an episode holds in a library now: none (Held false), or its quality and score
// (absent when the file has none scored).
type HeldFile struct {
	Held    bool   `json:"held"`
	Quality string `json:"quality,omitempty"`
	Score   *int64 `json:"score,omitempty"`
}

// maxLiveCandidates caps a live answer's list.
const maxLiveCandidates = 300

func (w *Web) apiReleases(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	t, err := w.findTitle(ctx, id, "", 0)
	if err != nil {
		w.internal(rw, err)
		return
	}
	// A title every copy of which was removed answers as one never added.
	if t == nil || !t.inLib {
		notFound(rw)
		return
	}
	if w.pol == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "no policy loaded"})
		return
	}
	// The title's current libraries; one it was removed from is not offered, and naming it is refused (below).
	var have []string
	removed := map[string]bool{}
	err = scanAll(ctx, w.st.Reader(), `SELECT v.policy_key, NOT `+liveVersion+` FROM version v WHERE v.title_id = ?
		ORDER BY v.policy_key`,
		[]any{id}, func(r *sql.Rows) error {
			var k string
			var gone bool
			if err := r.Scan(&k, &gone); err != nil {
				return err
			}
			if gone {
				removed[k] = true
			} else {
				have = append(have, k)
			}
			return nil
		})
	if err != nil {
		w.internal(rw, err)
		return
	}
	key := r.URL.Query().Get("version")
	if key == "" && len(have) > 0 {
		key = have[0]
	}
	if key == "" {
		badRequest(rw, "the title has no library: give version")
		return
	}
	vp := w.pol.Versions[key]
	if vp == nil || vp.Kind != t.kind {
		badRequest(rw, "unknown library "+strconv.Quote(key)+" for this title")
		return
	}
	if removed[key] {
		badRequest(rw, "the title was removed from library "+strconv.Quote(key)+": add it there again first")
		return
	}
	out := Releases{GrabState: versionGrabState(w.pol, key), TitleID: id, Version: key, Versions: have, Candidates: []ReleaseRow{},
		Rejections: map[string]int{}, Source: "none"}
	if out.Versions == nil {
		out.Versions = []string{}
	}
	out.LiveAllowed = w.add.Acquirer != nil
	live, _ := strconv.ParseBool(r.URL.Query().Get("live"))
	if live {
		switch {
		case w.add.Acquirer == nil:
			out.Message = "The acquirer is not running; showing the last stored decision"
		default:
			if err := w.liveReleases(ctx, &out, r); err != nil {
				out.Message = "Live search failed: " + err.Error() + "; showing the last stored decision"
				w.log.Warn("releases: live search", "title", id, "version", key, "err", err)
			} else {
				w.writeReleases(ctx, rw, &out, 0)
				return
			}
		}
	}
	// A season's Last search (?season=N, without live): the newest decision stored for that season's own search.
	// The fallback after a failed live search stays the newest of any kind, labelled as such by the window.
	scope := ""
	if s := r.URL.Query().Get("season"); s != "" && !live {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			badRequest(rw, "season must be a non-negative number")
			return
		}
		scope = fmt.Sprintf("s%02d", n)
	}
	if err := w.storedReleases(ctx, &out, scope); err != nil {
		w.internal(rw, err)
		return
	}
	if out.Source == "none" { // nothing stored: do not promise a stored decision (as the episode answer)
		out.Message = strings.TrimSuffix(out.Message, "; showing the last stored decision")
	}
	w.writeReleases(ctx, rw, &out, 0)
}

// ReleaseOutcomes is the answer of GET /titles/{id}/release-outcomes.
type ReleaseOutcomes struct {
	Outcomes map[string]ReleaseOutcome `json:"outcomes"`
}

// GET /titles/{id}/release-outcomes?version=KEY[&episode=ID]: what came of each release of the version (or of one
// episode of it) now, by lower-case info hash: the release lists' outcomes (releaseOutcomes), read again so a list
// shows a grab's progress. The same permission and checks as the release lists.
func (w *Web) apiReleaseOutcomes(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	t, err := w.findTitle(ctx, id, "", 0)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if t == nil || !t.inLib {
		notFound(rw)
		return
	}
	key := r.URL.Query().Get("version")
	var live bool
	err = w.st.Reader().QueryRowContext(ctx, `SELECT `+liveVersion+` FROM version v WHERE v.title_id = ?
		AND v.policy_key = ?`, id, key).Scan(&live)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !live {
		badRequest(rw, "the title has no library "+strconv.Quote(key))
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	var episode int64
	if s := r.URL.Query().Get("episode"); s != "" {
		episode, err = strconv.ParseInt(s, 10, 64)
		if err != nil || episode <= 0 || w.episodesOf(ctx, id, []int64{episode}) != nil {
			notFound(rw)
			return
		}
	}
	oc, err := w.releaseOutcomes(ctx, w.st.Reader(), id, key, episode)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, ReleaseOutcomes{Outcomes: oc})
}

// writeReleases answers with the outcome of each of the version's releases filled in; episode > 0 scopes them to
// that episode (releaseOutcomes).
func (w *Web) writeReleases(ctx context.Context, rw http.ResponseWriter, out *Releases, episode int64) {
	if out.Current == nil {
		cur, err := w.heldFile(ctx, w.st.Reader(), out.TitleID, out.Version, episode)
		if err != nil {
			w.internal(rw, err)
			return
		}
		out.Current = cur
	}
	if out.Outcomes == nil {
		oc, err := w.releaseOutcomes(ctx, w.st.Reader(), out.TitleID, out.Version, episode)
		if err != nil {
			w.internal(rw, err)
			return
		}
		out.Outcomes = oc
	}
	writeJSON(rw, http.StatusOK, out)
}

// heldFile is the file the title's film (episode 0, a movie) or episode holds in library key now; nil for a series
// without an episode (a season or the whole series has no one file).
func (w *Web) heldFile(ctx context.Context, db *sql.DB, titleID int64, key string, episode int64) (*HeldFile, error) {
	var vid int64
	var kind string
	err := db.QueryRowContext(ctx, `SELECT v.id, t.kind FROM version v JOIN title t ON t.id = v.title_id
		WHERE v.title_id = ? AND v.policy_key = ?`, titleID, key).Scan(&vid, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if episode == 0 && kind != "movie" {
		return nil, nil
	}
	// The newest active link, as the title page and the episode window read it (episodeFile): of the version, or of
	// its episode.
	linkEp, args := "", []any{vid}
	if episode > 0 {
		linkEp = ` JOIN link_episode le ON le.link_id = l.id AND le.episode_id = ?`
		args = []any{episode, vid}
	}
	h := &HeldFile{Held: true}
	var score sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT COALESCE(l.quality, ''), `+linkScoreSQL+` FROM link l
		JOIN link_version lv ON lv.link_id = l.id`+linkEp+` WHERE lv.version_id = ? AND l.state = 'active'
		ORDER BY l.created_at DESC, l.id DESC LIMIT 1`, args...).Scan(&h.Quality, &score)
	if errors.Is(err, sql.ErrNoRows) {
		return &HeldFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	h.Score = nullInt(score)
	return h, nil
}

// liveReleases searches the version's indexers now and decides every release (acquire.Acquirer.Interactive):
// nothing is recorded or routed.
func (w *Web) liveReleases(ctx context.Context, out *Releases, r *http.Request) error {
	var season *int
	if s := r.URL.Query().Get("season"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return errors.New("season must be a non-negative number")
		}
		season = &n
	}
	sr, err := w.add.Acquirer.Interactive(ctx, out.TitleID, out.Version, season, nil)
	if err == nil {
		err = noAnswer(sr)
	}
	if err != nil {
		return err
	}
	rankedInto(out, sr, w.now())
	w.keepLive(out, sr)
	if sr.Whole {
		n := sr.Dropped
		out.LeftOut = &n
	}
	if season != nil {
		out.Target = fmt.Sprintf("s%02d", *season)
	}
	return nil
}

// noAnswer fails a search no indexer answered (router.Searched.NoAnswer): it says nothing about the releases.
func noAnswer(sr *router.Searched) error {
	if !sr.NoAnswer {
		return nil
	}
	if len(sr.Notes) > 0 {
		return errors.New("no indexer answered (" + strings.Join(sr.Notes, "; ") + ")")
	}
	return errors.New("no indexer answered")
}

// rankedInto fills a live answer from a search's ranked decisions, one row per torrent (mergeCopies): the counts
// describe the rows, and the list keeps the best maxLiveCandidates of them.
func rankedInto(out *Releases, sr *router.Searched, now time.Time) {
	now = now.UTC()
	out.Source, out.At = "live", &now
	var cands []snapshot.Candidate
	for i, d := range sr.Ranked {
		if d == nil {
			continue
		}
		cs, _ := snapshot.Candidates([]*decide.Decision{d}, sr.Releases)
		if len(cs) == 1 {
			cs[0].Rank = i + 1
			cands = append(cands, cs[0])
		}
	}
	rows := mergeCopies(cands)
	out.Results = len(rows)
	for _, c := range rows {
		if c.Accepted {
			out.Accepted++
			continue
		}
		seen := map[string]bool{}
		for _, rj := range c.Rejections {
			if !seen[rj.Reason] {
				seen[rj.Reason] = true
				out.Rejections[rj.Reason]++
			}
		}
	}
	if len(rows) > maxLiveCandidates {
		rows = rows[:maxLiveCandidates]
	}
	out.Candidates = rows
	out.Notes = strings.Join(sr.Notes, "; ")
	out.Indexers = sr.Indexers
}

// storedReleases fills the answer from the newest stored decision for the title and version; with a season target
// ("s02") from the newest decision of that target only (never an episode's "s02e05" or an RSS read).
func (w *Web) storedReleases(ctx context.Context, out *Releases, season string) error {
	db := w.st.Reader()
	var jAt int64
	var run sql.NullInt64
	var cj, rj, notes, target, jSource string
	var results, accepted int
	cond, args := "", []any{out.TitleID, out.Version}
	if season != "" {
		cond, args = " AND target = ?", append(args, season)
	}
	err := db.QueryRowContext(ctx, `SELECT created_at, candidates, rejections, notes, target, results, accepted,
		run_id, source FROM journal_decision WHERE title_id = ? AND version_key = ?`+cond+` ORDER BY created_at DESC, id DESC
		LIMIT 1`, args...).Scan(&jAt, &cj, &rj, &notes, &target, &results, &accepted, &run, &jSource)
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !strings.Contains(err.Error(), "no such table") {
		return err
	}
	if err == nil {
		out.Source, out.Target, out.Results, out.Accepted, out.Notes = "journal", target, results, accepted, notes
		at := store.FromMillis(jAt)
		out.At = &at
		// The stored counts stay the search's (every indexer copy); its kept sample is shown one row per torrent.
		var cands []snapshot.Candidate
		_ = json.Unmarshal([]byte(cj), &cands)
		snapshot.DropPublicVolumeFlags(cands)
		out.Candidates = mergeCopies(cands)
		_ = json.Unmarshal([]byte(rj), &out.Rejections)
		if out.Message == "" {
			out.Message = "From " + sourceWord(target)
		}
		if err := w.storedGrabbed(ctx, out, run, jSource, jAt, 0, ""); err != nil {
			return err
		}
	}
	// Nothing stored: the badge "Nothing stored" says it; a live-search failure keeps its message.
	if out.Candidates == nil {
		out.Candidates = []ReleaseRow{}
	}
	if out.Rejections == nil {
		out.Rejections = map[string]int{}
	}
	return nil
}

// storedGrabbed fills the stored decision's outcomes and its one-line summary: the downloads its acquire run
// opened, or for an RSS read (no run) the version's RSS downloads of its candidates since it. episode > 0 keeps to
// the downloads and files of that episode.
// scope ("season" or "series", episode > 0 only) names the wider search whose result count a run that accepted nothing
// gives: "no accepted release among N results of the season search".
func (w *Web) storedGrabbed(ctx context.Context, out *Releases, run sql.NullInt64, source string, at, episode int64,
	scope string) error {
	db := w.st.Reader()
	oc, current, err := w.releaseOutcomesCurrent(ctx, db, out.TitleID, out.Version, episode)
	if err != nil {
		return err
	}
	out.Outcomes = oc
	state, message := "routed", ""
	var dls []runDownload
	scan := func(r *sql.Rows) error {
		var d runDownload
		if err := scanRunDownload(r, &d); err != nil {
			return err
		}
		dls = append(dls, d)
		return nil
	}
	if run.Valid {
		var results, infra int
		err := db.QueryRowContext(ctx, `SELECT state, message, COALESCE(results, 0), infra FROM acquire_run WHERE id = ?`,
			run.Int64).Scan(&state, &message, &results, &infra)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if state == "nothing" && scope != "" {
			// The search's notes are the panel's own (Notes); the line names the count as the whole search's, and
			// keeps the run's early-retry hint (acquire's partialNote for a runPartial run, infra 2).
			message = fmt.Sprintf("no accepted release among %d results of the %s search", results, scope)
			if infra == 2 {
				message += "; partial answer: due again sooner"
			}
		}
		// A merged search's member: the downloads of the root's route that hold its episodes.
		err = scanAll(ctx, db, `SELECT download.id, info_hash, release_title, COALESCE(provider, ''), download.state,
			COALESCE(error, '') FROM download JOIN acquire_run r ON r.id = ? WHERE `+snapshot.RunDownloadsSQL("download", "r")+`
			AND (? = 0 OR EXISTS (SELECT 1 FROM json_each(download.episodes) j WHERE j.value = ?)) ORDER BY download.id`,
			[]any{run.Int64, episode, episode}, scan)
		if err != nil {
			return err
		}
	} else if source == "rss" {
		hashes := map[string]bool{}
		for _, c := range out.Candidates {
			if c.InfoHash != "" {
				hashes[strings.ToLower(c.InfoHash)] = true
			}
		}
		err := scanAll(ctx, db, `SELECT id, info_hash, release_title, COALESCE(provider, ''), state, COALESCE(error, '')
			FROM download WHERE title_id = ? AND version_key = ? AND source = 'rss' AND created_at >= ?
			AND (? = 0 OR EXISTS (SELECT 1 FROM json_each(download.episodes) j WHERE j.value = ?)) ORDER BY id`,
			[]any{out.TitleID, out.Version, at, episode, episode}, func(r *sql.Rows) error {
				var d runDownload
				if err := scanRunDownload(r, &d); err != nil {
					return err
				}
				if hashes[strings.ToLower(d.hash)] {
					dls = append(dls, d)
				}
				return nil
			})
		if err != nil {
			return err
		}
	} else {
		return nil
	}
	if episode > 0 {
		// The episode's own downloads say what the search did for it, not the run's state.
		if len(dls) == 0 {
			if state == "routed" {
				out.Grabbed = "No new grab for this episode from this search"
				if source == "rss" {
					out.Grabbed = "No new grab for this episode from this RSS match"
				}
				return nil
			}
		} else {
			files, err := episodeFiles(ctx, db, out.TitleID, out.Version, episode)
			if err != nil {
				return err
			}
			for i := range dls {
				switch files[dls[i].id] {
				case fileLacked:
					dls[i].state = "imported-without"
				case fileUnknown:
					dls[i].state = "imported-unknown"
				}
			}
		}
	}
	out.Grabbed = searchSummary(dls, pickDownload(dls, current), current, candidatesOf(out.Candidates), oc, state, message,
		w.providerTag)
	return nil
}

// scanRunDownload reads id, info_hash, release_title, provider, state and error.
func scanRunDownload(r *sql.Rows, d *runDownload) error {
	return r.Scan(&d.id, &d.hash, &d.release, &d.provider, &d.state, &d.err)
}

// sourceWord names what a stored decision came from, after "From": "RSS", "a search", "a search for S01".
func sourceWord(target string) string {
	switch {
	case target == "rss":
		return "RSS"
	case target == "" || target == "movie":
		return "a search"
	}
	return "a search for " + strings.ToUpper(target)
}
