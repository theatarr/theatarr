package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/library"
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

// maxReason caps a failure's headline when it is the stored text itself (failureWords' last rule), in characters.
const maxReason = 120

// shortReason is a download's failure in plain words (failureWords' headline) from its stored error alone, with the
// provider named by its key in upper case: "miss on every provider" reads "No provider accepted this release".
func shortReason(err, provider string) string {
	return failureHeadline(err, provider, nil)
}

// failureHeadline is shortReason with the cache codes of the providers the download's attempts found it not cached
// on: "Not cached on RD, AD and TB".
func failureHeadline(err, provider string, missed []string) string {
	h, _ := failureWords(failureFacts{Err: err, Provider: provider, MissedOn: missed}, strings.ToUpper)
	return h
}

// failureFacts is what is stored about a download that failed or that the library refused: the download's error, its
// library import's state and reason, the blocklist row's reason, the file refusals the import's plan saved
// (Refusals, one per refused file), its provider (a key) and the cache codes (rd, ad, tb) of the providers its own
// attempts found it not cached on (missCodes). Err empty with the import rejected is an import held for review.
type failureFacts struct {
	Err, ImportState, ImportErr, Blocked string
	Refusals                             []string
	Provider                             string
	MissedOn                             []string
}

// failureExtrasSQL selects, for the download ids in its one argument (a JSON array), what failureFacts needs beyond
// the download's own error: its import's state and reason, its blocklist row's reason and the file refusals its
// import's plan saved (library.Plan.Rejected, a JSON array of their reasons).
const failureExtrasSQL = `SELECT d.id, COALESCE(li.state, ''), COALESCE(li.last_error, ''),
	COALESCE((SELECT b.reason FROM router_blocklist b WHERE b.download_id = d.id ORDER BY b.created_at DESC LIMIT 1), ''),
	(SELECT json_group_array(json_extract(j.value, '$.reason')) FROM json_each(CASE WHEN json_valid(li.plan) THEN li.plan
		ELSE '{}' END, '$.rejected') j)
	FROM download d LEFT JOIN library_import li ON li.download_id = d.id WHERE d.id IN (SELECT value FROM json_each(?))`

// failureExtras reads failureExtrasSQL for the downloads ids, by id; Err and the provider are the caller's.
func failureExtras(ctx context.Context, db *sql.DB, ids []int64) (map[int64]failureFacts, error) {
	out := map[int64]failureFacts{}
	if len(ids) == 0 {
		return out, nil
	}
	b, _ := json.Marshal(ids)
	err := scanAll(ctx, db, failureExtrasSQL, []any{string(b)}, func(r *sql.Rows) error {
		var id int64
		var f failureFacts
		var refusals sql.NullString
		if err := r.Scan(&id, &f.ImportState, &f.ImportErr, &f.Blocked, &refusals); err != nil {
			return err
		}
		f.Refusals = refusalList(refusals.String)
		out[id] = f
		return nil
	})
	return out, err
}

// refusalList is a JSON array of saved file refusals, without the empty ones.
func refusalList(js string) []string {
	var all []*string
	_ = json.Unmarshal([]byte(js), &all)
	var out []string
	for _, r := range all {
		if r != nil && strings.TrimSpace(*r) != "" {
			out = append(out, strings.TrimSpace(*r))
		}
	}
	return out
}

// storedFailure is the fullest stored text of a failure: the download's error, the library import's reason when the
// download row has none (an import held for review), and the blocklist row's reason when it says more (the library
// blocklists "no file passed the sample guard (<the first file's rejection>)").
func storedFailure(f failureFacts) string {
	s := strings.TrimSpace(f.Err)
	if s == "" && (f.ImportState == "rejected" || f.ImportState == "failed") && f.ImportErr != "" {
		s = "library: " + strings.TrimSpace(f.ImportErr)
	}
	b := strings.TrimSpace(f.Blocked)
	switch {
	case b == "":
	case s == "":
		s = b
	default:
		inner := strings.TrimPrefix(s, "library: ")
		if len(b) > len(inner) && strings.HasPrefix(b, inner) {
			s = strings.TrimSuffix(s, inner) + b
		}
	}
	return s
}

// The headlines failureWords gives the library's refusals; a retry of the same release ends the same way.
const (
	notUpgradeWords  = "Not imported: these files do not improve on the files you have"
	coverWords       = "Not imported: the files you have cover more episodes"
	mixedWords       = "Not imported: the library refused its files"
	mixedDetail      = "Different files were refused for different reasons."
	notUpgradeReason = "not an upgrade for the existing files"
	sampleWords      = "Not imported: the file is a sample or too short"
	mislabelWords    = "Not imported: the video does not match its quality label"
	invalidWords     = "Not imported: not a valid video file"
	noMatchWords     = "Not imported: none of its files matched the wanted episodes"
	notCheckedWords  = "Not imported: no file passed the library's checks"
	noProviderWords  = "No provider accepted this release"
	stalledWords     = "Download stalled"
	nothingReplaced  = "Nothing was replaced."
	sampleGuardWords = "no file passed the sample guard"
)

var (
	// decide.importupgrade's custom-format refusal: "… New: [a, b] (1202) do not improve on Existing: [c] (6200)".
	cfScoresRe = regexp.MustCompile(`\((-?\d+)\) do not improve on Existing: \[.*\] \((-?\d+)\)`)
	// Its quality refusal: "Not an upgrade for existing file(s). Existing quality: X. New Quality Y."
	qualityRe = regexp.MustCompile(`Existing quality: (.+?)\. New Quality (.+?)\.?$`)
	// library sampleCheck: "sample: runtime 240s is under 600s", "runtime 40 min is under 50% of the expected 128 min",
	// "resolution 1280x720 contradicts the release's 2160p".
	sampleRe     = regexp.MustCompile(`^sample: runtime (\d+)s is under (\d+)s`)
	shortRe      = regexp.MustCompile(`^runtime (\d+) min is under (\d+)% of the expected (\d+) min`)
	resolutionRe = regexp.MustCompile(`^resolution (\S+) contradicts the release's (\S+)`)
)

// failureWords turns a failed or refused download's stored outcome (failureFacts) into a headline and a detail, both
// plain sentences without a final stop in the headline (docs/design/ui.md "Failure reasons"). It maps the texts the
// router and the library write, plus the older library wrapper "no file passed the sample guard (…)", whose inner
// rejection is what is read; any other text is shown as stored, without its "library: " or "provider " prefix. tag
// names a provider key (RD).
func failureWords(f failureFacts, tag func(string) string) (headline, detail string) {
	h, d := failureSentences(f, tag)
	if rest, ok := strings.CutPrefix(h, "Not imported: "); ok && f.Err == "" && f.ImportState == "rejected" {
		// The download is ready and waits for review: the same sentence, said as held.
		h = "Held for review: " + rest
	}
	return h, d
}

// failureSentences is failureWords before a held import is said as held.
func failureSentences(f failureFacts, tag func(string) string) (headline, detail string) {
	s := storedFailure(f)
	if s == "" {
		return "", ""
	}
	rest := strings.TrimPrefix(s, "library: ")
	if h, d, ok := refusalsWords(rest, f.Refusals); ok {
		return h, d
	}
	if h, d, ok := importWords(rest); ok {
		return h, d
	}
	switch {
	case strings.HasPrefix(s, "miss on every provider"):
		if len(f.MissedOn) > 0 {
			return "Not cached on " + providerList(f.MissedOn), ""
		}
		return noProviderWords, ""
	case strings.HasPrefix(s, "stalled"):
		if _, pk, ok := strings.Cut(s, "; our add was deleted on "); ok {
			return stalledWords, "Deleted from " + tag(strings.TrimSpace(pk)) + "."
		}
		if _, kept, ok := strings.Cut(s, "; kept on "); ok {
			pk, _, _ := strings.Cut(kept, ":")
			return stalledWords, "Kept on " + tag(strings.TrimSpace(pk)) + "."
		}
		return stalledWords, ""
	case strings.HasPrefix(s, "ready timeout: not imported after "):
		d, err := time.ParseDuration(strings.Fields(strings.TrimPrefix(s, "ready timeout: not imported after "))[0])
		if err != nil || d <= 0 {
			return "Finished at the provider, but its files could not be imported in time", ""
		}
		return "Finished at the provider, but its files could not be imported within " + durationWords(d), ""
	case strings.HasPrefix(s, "provider status "):
		who := "The provider"
		if f.Provider != "" {
			who = tag(f.Provider)
		}
		return who + " reported an error", "Its status: " + strings.TrimPrefix(s, "provider status ") + "."
	}
	return storedWords(strings.TrimPrefix(rest, "provider ")), ""
}

// refusalsWords reads every file refusal the import's plan saved, for a refusal whose own text names one file at
// most (the wrappers "no file passed the sample guard" and "not an upgrade for the existing files"): one class reads
// as that class (its detail when one file was refused, else the class's own), different classes as mixedWords. ok is
// false when there is nothing to read: no saved refusal, another text, or a lone refusal fileWords does not know.
func refusalsWords(s string, refusals []string) (headline, detail string, ok bool) {
	reason, _, _ := strings.Cut(s, " (")
	if (reason != sampleGuardWords && reason != notUpgradeReason) || len(refusals) == 0 {
		return "", "", false
	}
	for i, r := range refusals {
		h, d, known := fileWords(r)
		switch {
		case !known && len(refusals) == 1:
			return "", "", false
		case !known, i > 0 && h != headline:
			return mixedWords, mixedDetail, true
		case i == 0:
			headline, detail = h, d
		}
	}
	if len(refusals) > 1 {
		detail = ""
		if headline == notUpgradeWords || headline == coverWords {
			detail = nothingReplaced
		}
	}
	return headline, detail, true
}

// importWords maps the library's refusals (internal/library import.go, mislabel.go; decide importupgrade.go): the
// import's own reasons and the per-file rejections inside the old wrapper. ok is false for any other text.
func importWords(s string) (headline, detail string, ok bool) {
	reason, inner := s, ""
	if i := strings.Index(s, " ("); i > 0 && strings.HasSuffix(s, ")") {
		reason, inner = s[:i], s[i+2:len(s)-1]
	}
	switch reason {
	case sampleGuardWords:
		// The wrapper does not prove a sample: the rejection recorded inside it says what failed.
		if h, d, ok := fileWords(inner); ok {
			return h, d, true
		}
		return notCheckedWords, "", true
	case notUpgradeReason:
		// The file's refusal says whether it was not better or would lose episodes the files you have hold.
		if h, d, ok := fileWords(inner); ok {
			return h, d, true
		}
		return notUpgradeWords, nothingReplaced, true
	case "no file matched the download's episodes":
		return noMatchWords, "", true
	}
	return fileWords(s)
}

// fileWords maps one file's rejection by the sample, fake and mislabel guards or the upgrade check.
func fileWords(r string) (headline, detail string, ok bool) {
	switch {
	case r == "":
		return "", "", false
	case strings.HasPrefix(r, "Not a Custom Format upgrade"), strings.HasPrefix(r, "Not an upgrade for existing file"),
		strings.HasPrefix(r, "Not a quality revision upgrade"):
		return notUpgradeWords, notUpgradeDetail(r), true
	case strings.HasPrefix(r, "Episode file on disk contains more episodes"):
		// Sonarr's SameEpisodesImportSpecification: replacing it would lose episodes.
		return coverWords, nothingReplaced, true
	case strings.HasPrefix(r, "runtime of 0"):
		return invalidWords, "", true
	case strings.HasPrefix(r, "mislabelled "):
		label, what, _ := strings.Cut(strings.TrimPrefix(r, "mislabelled "), ": ")
		switch {
		case what == "":
			return mislabelWords, "", true
		case label == "remux":
			// "x264 encode with crf=18 (disc video is never CRF)"
			return mislabelWords, "Labelled Remux, but it is an " + what + ".", true
		}
		return mislabelWords, "Labelled " + label + ", but " + what + ".", true
	}
	if m := resolutionRe.FindStringSubmatch(r); m != nil {
		return mislabelWords, "Labelled " + m[2] + ", but the video is " + m[1] + ".", true
	}
	if m := sampleRe.FindStringSubmatch(r); m != nil {
		got, _ := strconv.Atoi(m[1])
		least, _ := strconv.Atoi(m[2])
		return sampleWords, "The file runs " + secondsWords(got) + ", under the " + secondsWords(least) + " minimum.", true
	}
	if m := shortRe.FindStringSubmatch(r); m != nil {
		return sampleWords, "The file runs " + m[1] + " min, under " + m[2] + "% of the expected " + m[3] + " min.", true
	}
	return "", "", false
}

// notUpgradeDetail says what the upgrade check compared, when the stored rejection has it.
func notUpgradeDetail(r string) string {
	if m := cfScoresRe.FindStringSubmatch(r); m != nil {
		n, _ := strconv.Atoi(m[1])
		c, _ := strconv.Atoi(m[2])
		return "This release scores " + thousands(n) + ", yours " + thousands(c) + ". " + nothingReplaced
	}
	if m := qualityRe.FindStringSubmatch(r); m != nil {
		return "This release is " + m[2] + ", yours " + m[1] + ". " + nothingReplaced
	}
	return nothingReplaced
}

// storedWords is a stored text as a headline: its first letter in upper case, cut to maxReason.
func storedWords(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) > maxReason {
		r = append(r[:maxReason], '…')
	}
	return strings.ToUpper(string(r[:1])) + string(r[1:])
}

// providerList is cache codes as tags: "RD, AD and TB".
func providerList(codes []string) string {
	up := make([]string, len(codes))
	for i, c := range codes {
		up[i] = strings.ToUpper(c)
	}
	if len(up) < 2 {
		return strings.Join(up, "")
	}
	return strings.Join(up[:len(up)-1], ", ") + " and " + up[len(up)-1]
}

// attemptMisses are the cache codes (rd, ad, tb; missCodes) of the providers a download's attempts found it not
// cached on, as the title page lists them (cacheCodes).
func (w *Web) attemptMisses(as []journal.Attempt) []string {
	var keys []string
	for _, a := range as {
		switch a.Code {
		case "miss", "known_uncached", "uncached_deleted", "uncached_kept":
			if a.Provider != "" && !slices.Contains(keys, a.Provider) {
				keys = append(keys, a.Provider)
			}
		}
	}
	return w.cacheCodes(keys)
}

// durationWords is a stored duration in words: "6 hours", "2 days", "90 minutes".
func durationWords(d time.Duration) string {
	unit := func(n int64, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.FormatInt(n, 10) + " " + one + "s"
	}
	switch {
	case d%(24*time.Hour) == 0:
		return unit(int64(d/(24*time.Hour)), "day")
	case d%time.Hour == 0:
		return unit(int64(d/time.Hour), "hour")
	}
	return unit(int64(d.Round(time.Minute)/time.Minute), "minute")
}

// secondsWords is a runtime in seconds in words: "45 s", "4 min".
func secondsWords(s int) string {
	if s < 60 {
		return strconv.Itoa(s) + " s"
	}
	return strconv.Itoa((s+30)/60) + " min"
}

// thousands is n with comma separators: 6200 reads "6,200".
func thousands(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
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
	case o.research == "asked":
		// The search schedule's Search now row (Wanted page).
		return "Search now"
	case o.research == "added":
		// The schedule's search of a title a list or a timed collection sync added.
		return "added by a list"
	case o.research == "new" || o.research == "retry_new" || o.research == "fresh":
		// The schedule's retries of a new release, and the retry chains before it.
		return "retry of a new release"
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
	if from, ok := strings.CutPrefix(source, library.OtherSourcePrefix); ok {
		return "linked at the import for " + from
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
	case "offer": // the offers removed in 0.4.5
		return "offer from another library"
	case "api:import", "api:manualimport":
		return "manual import"
	}
	return source
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
// downloads aimed at it, and whether the newest download imported a file for the episode (episodeFiles); without
// that evidence an imported download of a pack has no badge for the episode.
func releaseOutcomes(ctx context.Context, db *sql.DB, titleID int64, version string, episode int64) (map[string]ReleaseOutcome, error) {
	type dl struct {
		id                   int64
		state, provider, err string
	}
	newest := map[string]dl{}
	imported := map[string]bool{}
	err := scanAll(ctx, db, `SELECT id, LOWER(info_hash), state, COALESCE(provider, ''), COALESCE(error, '')
		FROM download WHERE title_id = ? AND version_key = ? AND info_hash <> ''
		AND (? = 0 OR EXISTS (SELECT 1 FROM json_each(download.episodes) j WHERE j.value = ?)) ORDER BY id`,
		[]any{titleID, version, episode, episode},
		func(r *sql.Rows) error {
			var h string
			var d dl
			if err := r.Scan(&d.id, &h, &d.state, &d.provider, &d.err); err != nil {
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
	var files map[int64]episodeFile
	if episode > 0 {
		if files, err = episodeFiles(ctx, db, titleID, version, episode); err != nil {
			return nil, err
		}
		// Only the newest download's own evidence counts for the episode.
		imported = map[string]bool{}
		for h, d := range newest {
			if files[d.id] == fileHad {
				imported[h] = true
			}
		}
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
		case d.state == "imported" && episode > 0 && !imported[h]:
			// Imported, with no file known for this episode (none, or not recorded).
			continue
		case d.state == "imported":
			o.Outcome = "imported"
		default:
			o.Outcome = "grabbed"
		}
		out[h] = o
	}
	return out, nil
}

// episodeFile is what one download is known to have imported for an episode.
type episodeFile int

const (
	// fileNone: not imported, and no link of it holds the episode.
	fileNone episodeFile = iota
	// fileHad: a link of it holds the episode, or it was imported and its recorded files map one to the episode.
	fileHad
	// fileLacked: imported, and its recorded files (download_file) map none to the episode.
	fileLacked
	// fileUnknown: imported, with no recorded files (an import from before they were recorded).
	fileUnknown
)

// episodeFiles reads, per download of the version (by id), what it is known to have imported for the episode. Each
// download speaks only for itself: another download of the same release says nothing about it, and a manual choice
// recorded before its import is not a file until the download is imported.
func episodeFiles(ctx context.Context, db *sql.DB, titleID int64, version string, episode int64) (map[int64]episodeFile, error) {
	out := map[int64]episodeFile{}
	err := scanAll(ctx, db, `SELECT d.id, d.state, COUNT(f.rel), COALESCE(MAX(EXISTS (SELECT 1 FROM json_each(f.episodes) j
		WHERE j.value = ?)), 0) FROM download d LEFT JOIN download_file f ON f.download_id = d.id
		WHERE d.title_id = ? AND d.version_key = ? GROUP BY d.id`, []any{episode, titleID, version},
		func(r *sql.Rows) error {
			var id int64
			var state string
			var rows int
			var has bool
			if err := r.Scan(&id, &state, &rows, &has); err != nil {
				return err
			}
			switch {
			case state != "imported":
				out[id] = fileNone
			case has:
				out[id] = fileHad
			case rows > 0:
				out[id] = fileLacked
			default:
				out[id] = fileUnknown
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	err = scanAll(ctx, db, `SELECT DISTINCT l.download_id FROM version v JOIN link_version lv ON lv.version_id = v.id
		JOIN link l ON l.id = lv.link_id JOIN link_episode le ON le.link_id = l.id AND le.episode_id = ?
		WHERE v.title_id = ? AND v.policy_key = ? AND l.download_id IS NOT NULL`, []any{episode, titleID, version},
		func(r *sql.Rows) error {
			var id int64
			if err := r.Scan(&id); err != nil {
				return err
			}
			out[id] = fileHad
			return nil
		})
	return out, err
}

// runDownload is one download a search opened, with its own state and error.
type runDownload struct {
	id                                  int64
	hash, release, provider, state, err string
}

// searchSummary says in one line what a search grabbed: "Grabbed #1 via rd → imported" (the rank in the stored
// candidates, else the release's short title), or "Nothing grabbed: why". state and message are the acquire run's;
// a search without a run (an RSS read) passes routed.
func searchSummary(dls []runDownload, cands []snapshot.Candidate, outcomes map[string]ReleaseOutcome, state,
	message string, provider func(string) string) string {
	if len(dls) == 0 {
		switch state {
		case "routed":
			return "A release was chosen, but no download was started."
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
		s += " via " + provider(d.provider)
	}
	if n := len(dls) - 1; n > 0 {
		s += fmt.Sprintf(" and %d more", n)
	}
	// What came of this download, not of a later one of the same release; outcomes says whether its file still is.
	o := outcomes[strings.ToLower(d.hash)]
	switch d.state {
	case "imported-without":
		s += " → imported, without a file for this episode"
	case "imported-unknown":
		// Imported; which episodes it held is not known.
		s += " → imported"
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
