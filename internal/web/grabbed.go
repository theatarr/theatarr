package web

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/theatarr/theatarr/internal/journal/snapshot"
)

// What came of a version's releases, in the words the title page's history and the releases panel share
// (docs/design/ui.md "Title page" and "Releases page"): what started a grab or a search, a failure's short reason,
// each release's outcome by info hash, and one line for what a search grabbed.

// ReleaseOutcome is what came of one release (by info hash) for the version, or for one episode of it: its newest
// download, and whether its file is the current one.
type ReleaseOutcome struct {
	// Outcome is grabbed (in flight), imported, current (the version's file now), failed or replaced (was the
	// version's file, later replaced by another release).
	Outcome  string `json:"outcome"`
	Provider string `json:"provider,omitempty"`
	// Reason is a failure's short reason.
	Reason string `json:"reason,omitempty"`
	// ReplacedBy names the release that replaced it, when the version has one current release.
	ReplacedBy string `json:"replaced_by,omitempty"`
	// replaced: an import of it was the file and another release's is now, whatever its newest download did.
	replaced bool
}

// maxReason caps a short failure reason, in characters.
const maxReason = 80

// shortReason cuts a download's failure to its first clause, and names the provider in a provider's status:
// "provider status error" from rd reads "RD status error".
func shortReason(err, provider string) string {
	s := strings.TrimSpace(err)
	if i := strings.IndexAny(s, ":(;"); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	if rest, ok := strings.CutPrefix(s, "provider "); ok && provider != "" {
		s = strings.ToUpper(provider) + " " + rest
	}
	if utf8.RuneCountInString(s) > maxReason {
		s = string([]rune(s)[:maxReason]) + "…"
	}
	return s
}

// runOrigin is what started an acquire run: acquire_run's source, reason and research, and for a search asked
// through an arr view (command_id set) the view and the client's user agent.
type runOrigin struct {
	source, reason, research string
	command                  bool
	view, agent              string
}

// words says what started the run, in plain words.
func (o runOrigin) words() string {
	switch {
	case o.research != "":
		return "search again after a failure"
	case o.source == "wanted" && o.reason == "cutoff":
		return "automatic upgrade search"
	case o.source == "wanted":
		return "automatic search"
	case o.source == "command" && !o.command:
		// Search now, an add with a search, an approved request.
		return "manual search"
	case o.source == "command" && strings.Contains(strings.ToLower(o.agent), "seerr"):
		// Seerr sends User-Agent: Seerr/<version> on its arr calls.
		return "Seerr request"
	case o.source == "command" && o.view != "":
		return "arr search (" + o.view + ")"
	case o.source == "command":
		return "arr search"
	}
	return "search"
}

// grabWords says what started a download (download.source), in plain words; run is the acquire run of an
// acquire:<run> source, nil when unknown.
func grabWords(source string, run *runOrigin) string {
	if _, ok := strings.CutPrefix(source, "acquire:"); ok {
		if run != nil {
			return run.words()
		}
		return "search"
	}
	if by, ok := strings.CutPrefix(source, "ui:grab:"); ok {
		if name, ok := strings.CutPrefix(by, "key:"); ok {
			return "manual grab by API key " + name
		}
		if by == "" {
			return "manual grab"
		}
		return "manual grab by " + by
	}
	if strings.HasPrefix(source, "ui:retry:") {
		return "manual retry"
	}
	if view, ok := strings.CutPrefix(source, "arrview:release:"); ok {
		return "manual grab through the arr API (" + view + ")"
	}
	if view, ok := strings.CutPrefix(source, "arrview:push:"); ok {
		return "release push (" + view + ")"
	}
	if strings.HasPrefix(source, "upgrade:") {
		return "approved upgrade"
	}
	switch source {
	case "rss":
		return "RSS"
	case "wanted", "job":
		return "automatic search"
	case "research":
		return "search again after a failure"
	case "offer":
		return "offer from another library"
	case "api:import", "api:manualimport":
		return "manual import"
	}
	return source
}

// arrWords says an arr's own history event came from that arr instance: the old manager.
func arrWords(instance string) string {
	if instance == "" {
		return "old manager"
	}
	return "old manager (" + instance + ")"
}

// runIDOf is the acquire run of an acquire:<run> source, 0 for any other.
func runIDOf(source string) int64 {
	s, ok := strings.CutPrefix(source, "acquire:")
	if !ok {
		return 0
	}
	id, _ := strconv.ParseInt(s, 10, 64)
	return id
}

// runOrigins reads what started the title's acquire runs, by run id.
func runOrigins(ctx context.Context, db *sql.DB, titleID int64) (map[int64]runOrigin, error) {
	out := map[int64]runOrigin{}
	err := scanAll(ctx, db, `SELECT r.id, r.source, r.reason, COALESCE(r.research, ''), r.command_id IS NOT NULL,
		COALESCE(c.view, ''), COALESCE(c.user_agent, '') FROM acquire_run r
		LEFT JOIN arrview_command c ON c.id = r.command_id WHERE r.title_id = ?`, []any{titleID},
		func(r *sql.Rows) error {
			var id int64
			var o runOrigin
			if err := r.Scan(&id, &o.source, &o.reason, &o.research, &o.command, &o.view, &o.agent); err != nil {
				return err
			}
			out[id] = o
			return nil
		})
	return out, err
}

// releaseOutcomes reads what came of each release of the title's version, by lower-case info hash: the newest
// download per hash, and the version's current files (active links). episode > 0 scopes both to that episode: the
// downloads and the links that hold it.
func releaseOutcomes(ctx context.Context, db *sql.DB, titleID int64, version string, episode int64) (map[string]ReleaseOutcome, error) {
	type dl struct{ state, provider, err string }
	newest := map[string]dl{}
	imported := map[string]bool{}
	err := scanAll(ctx, db, `SELECT LOWER(info_hash), state, COALESCE(provider, ''), COALESCE(error, '')
		FROM download WHERE title_id = ? AND version_key = ? AND info_hash <> ''
		AND (? = 0 OR EXISTS (SELECT 1 FROM json_each(download.episodes) j WHERE j.value = ?)) ORDER BY id`,
		[]any{titleID, version, episode, episode},
		func(r *sql.Rows) error {
			var h string
			var d dl
			if err := r.Scan(&h, &d.state, &d.provider, &d.err); err != nil {
				return err
			}
			newest[h] = d
			if d.state == "imported" {
				imported[h] = true
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	current := map[string]bool{}
	titles := map[string]bool{}
	err = scanAll(ctx, db, `SELECT DISTINCT LOWER(COALESCE(c.info_hash, d.info_hash, '')), COALESCE(l.release_title, '')
		FROM version v JOIN link_version lv ON lv.version_id = v.id JOIN link l ON l.id = lv.link_id
		LEFT JOIN content_file cf ON cf.id = l.content_file_id LEFT JOIN content c ON c.id = cf.content_id
		LEFT JOIN download d ON d.id = l.download_id
		WHERE v.title_id = ? AND v.policy_key = ? AND l.state = 'active'
		AND (? = 0 OR EXISTS (SELECT 1 FROM link_episode le WHERE le.link_id = l.id AND le.episode_id = ?))`,
		[]any{titleID, version, episode, episode},
		func(r *sql.Rows) error {
			var h, title string
			if err := r.Scan(&h, &title); err != nil {
				return err
			}
			if h != "" {
				current[h] = true
			}
			titles[title] = true
			return nil
		})
	if err != nil {
		return nil, err
	}
	replacedBy := ""
	if len(titles) == 1 {
		for t := range titles {
			replacedBy = t
		}
	}
	out := map[string]ReleaseOutcome{}
	for h, d := range newest {
		o := ReleaseOutcome{Provider: d.provider, replaced: !current[h] && imported[h] && len(titles) > 0}
		switch {
		case current[h]:
			o.Outcome = "current"
		case d.state == "failed":
			// The newest attempt's failure, over an older import's replacement.
			o.Outcome, o.Reason = "failed", shortReason(d.err, d.provider)
		case o.replaced:
			o.Outcome, o.ReplacedBy = "replaced", replacedBy
		case d.state == "imported":
			o.Outcome = "imported"
		default:
			o.Outcome = "grabbed"
		}
		out[h] = o
	}
	return out, nil
}

// runDownload is one download a search opened, with its own state and error.
type runDownload struct{ hash, release, provider, state, err string }

// searchSummary says in one line what a search grabbed: "Grabbed #1 via rd → imported" (the rank in the stored
// candidates, else the release's short title), or "Nothing grabbed: why". state and message are the acquire run's;
// a search without a run (an RSS read) passes routed.
func searchSummary(dls []runDownload, cands []snapshot.Candidate, outcomes map[string]ReleaseOutcome, state,
	message string) string {
	if len(dls) == 0 {
		switch state {
		case "routed":
			return "Sent to download; no download opened"
		case "queued", "searching":
			return "Searching"
		}
		if message == "" {
			return "Nothing grabbed"
		}
		return "Nothing grabbed: " + message
	}
	d := dls[0]
	what := shortRelease(d.release)
	for _, c := range cands {
		if strings.EqualFold(c.InfoHash, d.hash) && c.Rank > 0 {
			what = "#" + strconv.Itoa(c.Rank)
			break
		}
	}
	s := "Grabbed " + what
	if d.provider != "" {
		s += " via " + d.provider
	}
	if n := len(dls) - 1; n > 0 {
		s += fmt.Sprintf(" and %d more", n)
	}
	// What came of this download, not of a later one of the same release; outcomes says whether its file still is.
	o := outcomes[strings.ToLower(d.hash)]
	switch d.state {
	case "imported":
		switch {
		case o.Outcome == "current":
			s += " → imported (the current file)"
		case o.replaced:
			s += " → imported, later replaced"
		default:
			s += " → imported"
		}
	case "failed":
		s += " → failed"
		if r := shortReason(d.err, d.provider); r != "" {
			s += " (" + r + ")"
		}
	}
	return s
}

// maxShortRelease caps a release title in a summary line, in characters.
const maxShortRelease = 60

// shortRelease is a release title cut for one line.
func shortRelease(title string) string {
	if utf8.RuneCountInString(title) > maxShortRelease {
		return string([]rune(title)[:maxShortRelease]) + "…"
	}
	return title
}
