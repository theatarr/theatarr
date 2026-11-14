package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// GET /titles/{id}/releases: the title page's "Show releases" panel (admin), the decision engine's results for one
// version with scores and rejections. A live indexer search runs only for a version in mode active (live=1);
// otherwise the answer is the last stored decision: the journal's decided search (acquirer or RSS). A Watch-only
// version's panel sends no indexer traffic.

// Releases is the answer of GET /titles/{id}/releases.
type Releases struct {
	GrabState VersionGrabState `json:"grab_state"`
	TitleID   int64            `json:"title_id"`
	Version   string           `json:"version"`
	// Versions are the title's versions, for the panel's picker.
	Versions []string `json:"versions"`
	Mode     string   `json:"mode"`
	// LiveAllowed: live=1 would search the indexers (the version is active).
	LiveAllowed bool `json:"live_allowed"`
	// Source is live, journal or none.
	Source     string               `json:"source"`
	At         *time.Time           `json:"at,omitempty"`
	Target     string               `json:"target,omitempty"`
	Results    int                  `json:"results"`
	Accepted   int                  `json:"accepted"`
	Candidates []snapshot.Candidate `json:"candidates"`
	Rejections map[string]int       `json:"rejections"`
	Notes      string               `json:"notes,omitempty"`
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
	out := Releases{GrabState: versionGrabState(w.pol, key), TitleID: id, Version: key, Versions: have, Mode: string(vp.Mode), Candidates: []snapshot.Candidate{},
		Rejections: map[string]int{}, Source: "none"}
	if out.Versions == nil {
		out.Versions = []string{}
	}
	if out.Mode == "" {
		out.Mode = string(policy.ModeShadow)
	}
	out.LiveAllowed = vp.Mode == policy.ModeActive && w.add.Acquirer != nil
	live, _ := strconv.ParseBool(r.URL.Query().Get("live"))
	if live {
		switch {
		case vp.Mode != policy.ModeActive:
			out.Message = "Watch only: no live search for " + key + "; showing the last stored decision"
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
	if err := w.storedReleases(ctx, &out); err != nil {
		w.internal(rw, err)
		return
	}
	w.writeReleases(ctx, rw, &out, 0)
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
		oc, err := releaseOutcomes(ctx, w.st.Reader(), out.TitleID, out.Version, episode)
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
	// The newest active link, else the current arr file, as the title page and the episode window read them
	// (episodeFile): of the version, or of its episode.
	linkEp, fileEp, args := "", "", []any{vid}
	if episode > 0 {
		linkEp = ` JOIN link_episode le ON le.link_id = l.id AND le.episode_id = ?`
		fileEp = ` JOIN version_file_episode fe ON fe.version_file_id = vf.id AND fe.episode_id = ?`
		args = []any{episode, vid}
	}
	h := &HeldFile{Held: true}
	var score sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT COALESCE(l.quality, ''), `+linkScoreSQL+` FROM link l
		JOIN link_version lv ON lv.link_id = l.id`+linkEp+` WHERE lv.version_id = ? AND l.state = 'active'
		ORDER BY l.created_at DESC, l.id DESC LIMIT 1`, args...).Scan(&h.Quality, &score)
	if errors.Is(err, sql.ErrNoRows) {
		err = db.QueryRowContext(ctx, `SELECT vf.quality, vf.custom_format_score FROM version_file vf`+fileEp+`
			LEFT JOIN version_source vs ON vs.version_id = vf.version_id WHERE vf.version_id = ? AND `+curFile+`
			ORDER BY vf.added_at DESC, vf.id DESC LIMIT 1`, args...).Scan(&h.Quality, &score)
		if errors.Is(err, sql.ErrNoRows) {
			return &HeldFile{}, nil
		}
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
	if season != nil {
		out.Target = "s" + strconv.Itoa(100 + *season)[1:]
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

// rankedInto fills a live answer from a search's ranked decisions.
func rankedInto(out *Releases, sr *router.Searched, now time.Time) {
	ranked := sr.Ranked
	out.Source, out.Results = "live", len(ranked)
	now = now.UTC()
	out.At = &now
	for i, d := range ranked {
		if d == nil {
			continue
		}
		if d.Accepted {
			out.Accepted++
		} else {
			seen := map[string]bool{}
			for _, rj := range d.Rejections {
				if !seen[rj.Reason] {
					seen[rj.Reason] = true
					out.Rejections[rj.Reason]++
				}
			}
		}
		if len(out.Candidates) < maxLiveCandidates {
			cs, _ := snapshot.Candidates([]*decide.Decision{d}, sr.Releases)
			if len(cs) == 1 {
				cs[0].Rank = i + 1
				out.Candidates = append(out.Candidates, cs[0])
			}
		}
	}
	out.Notes = strings.Join(sr.Notes, "; ")
	out.Indexers = sr.Indexers
}

// storedReleases fills the answer from the newest stored decision for the title and version.
func (w *Web) storedReleases(ctx context.Context, out *Releases) error {
	db := w.st.Reader()
	var jAt int64
	var run sql.NullInt64
	var cj, rj, notes, target, jSource string
	var results, accepted int
	err := db.QueryRowContext(ctx, `SELECT created_at, candidates, rejections, notes, target, results, accepted,
		run_id, source FROM journal_decision WHERE title_id = ? AND version_key = ? ORDER BY created_at DESC, id DESC
		LIMIT 1`, out.TitleID, out.Version).Scan(&jAt, &cj, &rj, &notes, &target, &results, &accepted, &run, &jSource)
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !strings.Contains(err.Error(), "no such table") {
		return err
	}
	if err == nil {
		out.Source, out.Target, out.Results, out.Accepted, out.Notes = "journal", target, results, accepted, notes
		at := store.FromMillis(jAt)
		out.At = &at
		_ = json.Unmarshal([]byte(cj), &out.Candidates)
		_ = json.Unmarshal([]byte(rj), &out.Rejections)
		if out.Message == "" {
			out.Message = "From " + sourceWord(target)
		}
		if err := w.storedGrabbed(ctx, out, run, jSource, jAt, 0); err != nil {
			return err
		}
	} else if out.Message == "" {
		out.Message = "No search result is stored for this library yet"
	}
	if out.Candidates == nil {
		out.Candidates = []snapshot.Candidate{}
	}
	if out.Rejections == nil {
		out.Rejections = map[string]int{}
	}
	return nil
}

// storedGrabbed fills the stored decision's outcomes and its one-line summary: the downloads its acquire run
// opened, or for an RSS read (no run) the version's RSS downloads of its candidates since it. episode > 0 keeps to
// the downloads and files of that episode.
func (w *Web) storedGrabbed(ctx context.Context, out *Releases, run sql.NullInt64, source string, at, episode int64) error {
	db := w.st.Reader()
	oc, err := releaseOutcomes(ctx, db, out.TitleID, out.Version, episode)
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
		err := db.QueryRowContext(ctx, `SELECT state, message FROM acquire_run WHERE id = ?`, run.Int64).Scan(&state,
			&message)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		err = scanAll(ctx, db, `SELECT id, info_hash, release_title, COALESCE(provider, ''), state, COALESCE(error, '')
			FROM download WHERE source = ? AND (? = 0 OR EXISTS (SELECT 1 FROM json_each(download.episodes) j
			WHERE j.value = ?)) ORDER BY id`, []any{"acquire:" + strconv.FormatInt(run.Int64, 10), episode, episode}, scan)
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
	out.Grabbed = searchSummary(dls, out.Candidates, oc, state, message, w.providerTag)
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
