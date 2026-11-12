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
// otherwise the answer is the last stored decision: the journal's decided search (acquirer or RSS) or the shadow
// runner's last grab for the version, whichever is newer. A shadow version's panel sends no indexer traffic.

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
	// Source is live, journal, shadow or none.
	Source     string               `json:"source"`
	At         *time.Time           `json:"at,omitempty"`
	Target     string               `json:"target,omitempty"`
	Results    int                  `json:"results"`
	Accepted   int                  `json:"accepted"`
	Candidates []snapshot.Candidate `json:"candidates"`
	Rejections map[string]int       `json:"rejections"`
	Notes      string               `json:"notes,omitempty"`
	// Message says why nothing live was searched, or what the stored answer is.
	Message string `json:"message,omitempty"`
	// Grab: a live answer whose candidates may be grabbed for 30 minutes (POST /titles/{id}/releases/grab, ops.go).
	Grab bool `json:"grab"`
	// Outcomes says what came of each release of the version, by lower-case info hash (releaseOutcomes): for the
	// stored list and a live search alike; for an episode's releases, from that episode's downloads and files.
	Outcomes map[string]ReleaseOutcome `json:"outcomes"`
	// Grabbed says in one line what the stored decision's search grabbed (searchSummary); empty for a live answer.
	Grabbed string `json:"grabbed,omitempty"`
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
	if t == nil {
		notFound(rw)
		return
	}
	if w.pol == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "no policy loaded"})
		return
	}
	var have []string
	err = scanAll(ctx, w.st.Reader(), `SELECT policy_key FROM version WHERE title_id = ? ORDER BY policy_key`,
		[]any{id}, func(r *sql.Rows) error {
			var k string
			if err := r.Scan(&k); err != nil {
				return err
			}
			have = append(have, k)
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
	var sAt int64
	var sj, note, arrRelease string
	var sres, sacc int
	serr := db.QueryRowContext(ctx, `SELECT COALESCE(searched_at, updated_at), results, search_note, result_count,
		accepted_count, release_title FROM shadow_grab WHERE title_id = ? AND version_key = ? AND state = 'done'
		AND searched = 1 ORDER BY COALESCE(searched_at, updated_at) DESC, id DESC LIMIT 1`, out.TitleID,
		out.Version).Scan(&sAt, &sj, &note, &sres, &sacc, &arrRelease)
	if serr != nil && !errors.Is(serr, sql.ErrNoRows) {
		return serr
	}
	haveJ, haveS := err == nil, serr == nil
	switch {
	case haveJ && (!haveS || jAt >= sAt):
		out.Source, out.Target, out.Results, out.Accepted, out.Notes = "journal", target, results, accepted, notes
		at := store.FromMillis(jAt)
		out.At = &at
		_ = json.Unmarshal([]byte(cj), &out.Candidates)
		_ = json.Unmarshal([]byte(rj), &out.Rejections)
		if out.Message == "" {
			out.Message = "The last search Theatarr decided for this library (" + sourceWord(target) + ")"
		}
		if err := w.storedGrabbed(ctx, out, run, jSource, jAt, 0); err != nil {
			return err
		}
	case haveS:
		out.Source, out.Results, out.Accepted, out.Notes = "shadow", sres, sacc, note
		at := store.FromMillis(sAt)
		out.At = &at
		_ = json.Unmarshal([]byte(sj), &out.Candidates)
		for i := range out.Candidates {
			out.Candidates[i].Rank = i + 1
			if !out.Candidates[i].Accepted {
				seen := map[string]bool{}
				for _, rj := range out.Candidates[i].Rejections {
					if !seen[rj.Reason] {
						seen[rj.Reason] = true
						out.Rejections[rj.Reason]++
					}
				}
			}
		}
		if out.Message == "" {
			out.Message = "The shadow runner's search for the arr's grab of " + arrRelease
		}
	default:
		if out.Message == "" {
			out.Message = "No stored decision for this library yet"
		}
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
				out.Grabbed = "Nothing grabbed for this episode"
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
	out.Grabbed = searchSummary(dls, out.Candidates, oc, state, message)
	return nil
}

// scanRunDownload reads id, info_hash, release_title, provider, state and error.
func scanRunDownload(r *sql.Rows, d *runDownload) error {
	return r.Scan(&d.id, &d.hash, &d.release, &d.provider, &d.state, &d.err)
}

func sourceWord(target string) string {
	switch {
	case target == "rss":
		return "RSS"
	case target == "" || target == "movie":
		return "search"
	}
	return "search " + target
}
