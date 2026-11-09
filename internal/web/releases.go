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
}

// maxLiveCandidates caps a live answer's list.
const maxLiveCandidates = 100

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
		badRequest(rw, "the title has no version: give version")
		return
	}
	vp := w.pol.Versions[key]
	if vp == nil || vp.Kind != t.kind {
		badRequest(rw, "unknown version "+strconv.Quote(key)+" for this title")
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
			out.Message = "Shadow mode: no live search for " + key + "; showing the last stored decision"
		case w.add.Acquirer == nil:
			out.Message = "The acquirer is not running; showing the last stored decision"
		default:
			if err := w.liveReleases(ctx, &out, r); err != nil {
				out.Message = "Live search failed: " + err.Error() + "; showing the last stored decision"
				w.log.Warn("releases: live search", "title", id, "version", key, "err", err)
			} else {
				writeJSON(rw, http.StatusOK, out)
				return
			}
		}
	}
	if err := w.storedReleases(ctx, &out); err != nil {
		w.internal(rw, err)
		return
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
	var cj, rj, notes, target string
	var results, accepted int
	err := db.QueryRowContext(ctx, `SELECT created_at, candidates, rejections, notes, target, results, accepted
		FROM journal_decision WHERE title_id = ? AND version_key = ? ORDER BY created_at DESC, id DESC LIMIT 1`,
		out.TitleID, out.Version).Scan(&jAt, &cj, &rj, &notes, &target, &results, &accepted)
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

func sourceWord(target string) string {
	switch {
	case target == "rss":
		return "RSS"
	case target == "" || target == "movie":
		return "search"
	}
	return "search " + target
}
