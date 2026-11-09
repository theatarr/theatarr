package journal

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/router"
)

// Entry kinds. One entry is one decision or one thing that happened to a decision afterwards.
const (
	KindGrab      = "grab"      // a release was chosen and handed to a provider (Theatarr) or a download client (arr)
	KindUpgrade   = "upgrade"   // the same, for a version that already had a file
	KindShadow    = "shadow"    // an arr grab re-decided by Theatarr in shadow mode, or an RSS pick no arr grab followed
	KindPlan      = "plan"      // a router dry run that belongs to no shadow entry (router off)
	KindSearch    = "search"    // an theatarr search that routed nothing (no accepted release, or it failed)
	KindResearch  = "research"  // an theatarr search started by a failure (re-search)
	KindFailure   = "failure"   // a download failed
	KindStall     = "stall"     // a download failed as stalled
	KindBlocklist = "blocklist" // a hash went on a version's blocklist
	KindImport    = "import"    // the library's import of a download (linked, rejected, dry run), or an arr import
	KindLink      = "link"      // a library link action: link, replace, relink, dead
	KindVerdict   = "verdict"   // a person or an agent judged an entry
	KindAction    = "action"    // a person acted by hand through the web UI (grab, remove, retry, unblock, unlink)
)

// Kinds lists every kind, for filters.
var Kinds = []string{KindGrab, KindUpgrade, KindShadow, KindPlan, KindSearch, KindResearch, KindFailure, KindStall,
	KindBlocklist, KindImport, KindLink, KindVerdict, KindAction}

// Severities, lowest first.
const (
	SevInfo  = "info"
	SevWarn  = "warn"
	SevError = "error"
)

// SevRank orders severities: info 1, warn 2, error 3; anything else 0.
func SevRank(s string) int {
	switch s {
	case SevInfo:
		return 1
	case SevWarn:
		return 2
	case SevError:
		return 3
	}
	return 0
}

// Flag is one suspect check that fired. Acked is set once a verdict acknowledged it.
type Flag struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
	Acked    bool   `json:"acked,omitempty"`
}

// TitleRef names the title an entry is about.
type TitleRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Year int    `json:"year,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// Release is the release an entry is about. Links (GUIDs, download URLs, magnets) never appear.
type Release struct {
	Title     string               `json:"title"`
	Indexer   string               `json:"indexer,omitempty"`
	InfoHash  string               `json:"info_hash,omitempty"`
	Size      int64                `json:"size,omitempty"`
	Quality   string               `json:"quality,omitempty"`
	Score     *int                 `json:"score,omitempty"`
	Formats   []decide.FormatScore `json:"formats,omitempty"`
	Languages []string             `json:"languages,omitempty"`
	Episodes  []string             `json:"episodes,omitempty"`
	Group     string               `json:"release_group,omitempty"`
	// How is how the arr found it (Rss, Search, InteractiveSearch, ReleasePush) or what asked Theatarr (acquire:<run>, rss).
	How string `json:"how,omitempty"`
}

// Download is Theatarr's download row of an entry.
type Download struct {
	ID        int64   `json:"id"`
	State     string  `json:"state"`
	Provider  string  `json:"provider,omitempty"`
	TorrentID string  `json:"provider_torrent_id,omitempty"`
	Name      string  `json:"name,omitempty"`
	Cached    *bool   `json:"cached,omitempty"`
	Reused    bool    `json:"reused,omitempty"`
	OwnAdd    *bool   `json:"own_add,omitempty"`
	Progress  float64 `json:"progress"`
	Error     string  `json:"error,omitempty"`
	Source    string  `json:"source,omitempty"`
	// PolicyRev is the policy revision the router opened the download under.
	PolicyRev  string     `json:"policy_revision,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	AddedAt    *time.Time `json:"added_at,omitempty"`
	ReadyAt    *time.Time `json:"ready_at,omitempty"`
	ImportedAt *time.Time `json:"imported_at,omitempty"`
}

// Attempt is one provider considered for a download.
type Attempt struct {
	Provider string     `json:"provider"`
	Outcome  string     `json:"outcome"`
	Code     string     `json:"code,omitempty"`
	Message  string     `json:"message,omitempty"`
	At       time.Time  `json:"at"`
	RetryAt  *time.Time `json:"retry_at,omitempty"`
}

// Evidence is one provider_cache row for the release's hash.
type Evidence struct {
	Provider  string     `json:"provider"`
	State     string     `json:"state"`
	Source    string     `json:"source"`
	CheckedAt time.Time  `json:"checked_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Plan is a router dry run.
type Plan struct {
	ID       int64         `json:"id"`
	Mode     string        `json:"mode"`
	Outcome  string        `json:"outcome"`
	Release  string        `json:"release,omitempty"`
	Provider string        `json:"provider,omitempty"`
	Summary  string        `json:"summary"`
	Source   string        `json:"source,omitempty"`
	Steps    []router.Step `json:"steps,omitempty"`
}

// Reasoning is why the release was chosen: the chosen candidate with its score and formats, the runners-up with
// theirs and their rejections, and the counts of every rejection reason. For a shadow entry, Arr is the arr's
// release as Theatarr decided it, Chosen Theatarr's own pick, and Bucket the shadow classification.
type Reasoning struct {
	Basis      string               `json:"basis"`
	Results    int                  `json:"results,omitempty"`
	Accepted   int                  `json:"accepted,omitempty"`
	Chosen     *snapshot.Candidate  `json:"chosen,omitempty"`
	RunnersUp  []snapshot.Candidate `json:"runners_up,omitempty"`
	Rejections map[string]int       `json:"rejection_counts,omitempty"`
	Existing   *decide.Existing     `json:"existing,omitempty"`
	Arr        *snapshot.Candidate  `json:"arr,omitempty"`
	Bucket     string               `json:"bucket,omitempty"`
	Why        string               `json:"bucket_reason,omitempty"`
	Detail     string               `json:"detail,omitempty"`
	Notes      string               `json:"notes,omitempty"`
	Plan       *Plan                `json:"router_plan,omitempty"`
	Missing    string               `json:"missing,omitempty"`
	// PolicyRev is the revision of the policy files the search decided under (policy.Policy.Revision); empty for
	// decisions recorded before revisions were kept.
	PolicyRev string `json:"policy_revision,omitempty"`
}

// File is a file of an entry's import, with what a probe or the arr's media info said.
type File struct {
	Path           string `json:"path"`
	Size           int64  `json:"size,omitempty"`
	RuntimeSeconds int    `json:"runtime_seconds,omitempty"`
	Resolution     string `json:"resolution,omitempty"`
	VideoCodec     string `json:"video_codec,omitempty"`
	AudioLanguages string `json:"audio_languages,omitempty"`
	Source         string `json:"source,omitempty"` // probe, arr
	Note           string `json:"note,omitempty"`   // what the library's mislabel guard saw without refusing the file
	Score          *int   `json:"score,omitempty"`
	Quality        string `json:"quality,omitempty"`
}

// Link is a library link of an entry.
type Link struct {
	ID     int64  `json:"id,omitempty"`
	Path   string `json:"path"`
	Target string `json:"target,omitempty"`
	State  string `json:"state,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Kind   string `json:"kind,omitempty"`
}

// Import is the library's side of an entry.
type Import struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	Files []File `json:"files,omitempty"`
	Links []Link `json:"links,omitempty"`
}

// Event is something that happened to the entry's release afterwards. Ref is the journal id of the event's own
// entry, when it has one.
type Event struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
	Ref  string    `json:"ref,omitempty"`
}

// Verdict is a judgement on an entry.
type Verdict struct {
	ID          int64     `json:"id"`
	Entry       string    `json:"entry"`
	Verdict     string    `json:"verdict"`
	Reason      string    `json:"reason"`
	Acked       []string  `json:"acked,omitempty"`
	Action      string    `json:"action"`
	Note        string    `json:"action_note,omitempty"`
	ResearchJob int64     `json:"research_job,omitempty"`
	By          string    `json:"by,omitempty"`
	At          time.Time `json:"at"`
}

// Entry is one journal entry.
type Entry struct {
	ID   string    `json:"id"`
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	// Source is who decided or acted: Theatarr, shadow, router, library, arr:<instance>, journal.
	Source  string   `json:"source"`
	Title   TitleRef `json:"title"`
	Version string   `json:"version,omitempty"`
	// Mode is the version's mode in the running policy (active, shadow); "" when the policy has no such version.
	Mode string `json:"mode,omitempty"`
	// Summary is one line: what was decided or what happened.
	Summary   string     `json:"summary"`
	Release   *Release   `json:"release,omitempty"`
	Download  *Download  `json:"download,omitempty"`
	Reasoning *Reasoning `json:"reasoning,omitempty"`
	Attempts  []Attempt  `json:"attempts,omitempty"`
	Cache     []Evidence `json:"cache,omitempty"`
	Import    *Import    `json:"import,omitempty"`
	// After is what happened afterwards, oldest first.
	After   []Event  `json:"after,omitempty"`
	Related []string `json:"related,omitempty"`
	Flags   []Flag   `json:"flags"`
	// Suspect is true when a flag of severity warn or error is not acknowledged; Severity is the highest
	// unacknowledged severity ("" when none).
	Suspect  bool     `json:"suspect"`
	Severity string   `json:"severity,omitempty"`
	Verdict  *Verdict `json:"verdict,omitempty"`
	// Verdict entries: the entry judged.
	Judged string `json:"judged,omitempty"`
	// Action entries: what a person did by hand (action.go).
	Action *Action `json:"action,omitempty"`
	// PolicyRev is the revision of the policy files that made the decision (policy.Policy.Revision): the search's,
	// else the download's, else the queued run's. Empty for rows from before revisions were kept and for arr and
	// shadow entries. QueuedPolicyRev is set only when an acquire run was queued under a different revision.
	PolicyRev       string `json:"policy_revision,omitempty"`
	QueuedPolicyRev string `json:"queued_policy_revision,omitempty"`

	f facts
}

// facts are what the checks read, gathered while the entry is built.
type facts struct {
	episodeIDs    []int64
	prevScore     *int
	prevQuality   string
	prevRelease   string
	fileRuntime   int
	fileWidth     int
	fileHeight    int
	fileAudio     []string
	failureCount  int
	failuresAfter int
	sizeEpisodes  int
	matchType     string
	shadowBucket  string
	shadowWhy     string
	importRejects []string // the guard's refusals (reason and episodes), not-upgrade skips left out
	importLinked  bool     // the import made links (link rows), not only planned them
	// importBlocklisted: the failed import blocklisted the release (its router_blocklist row, source library).
	importBlocklisted bool
	importNotes       string // the mislabel guard's note on a file it did not refuse
}

func fmtSize(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	default:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

func (r *Release) line() string {
	if r == nil {
		return ""
	}
	var parts []string
	if r.Quality != "" {
		parts = append(parts, r.Quality)
	}
	if r.Score != nil {
		parts = append(parts, fmt.Sprintf("score %d", *r.Score))
	}
	if s := fmtSize(r.Size); s != "" {
		parts = append(parts, s)
	}
	if r.Indexer != "" {
		parts = append(parts, r.Indexer)
	}
	if r.InfoHash != "" {
		parts = append(parts, "hash "+shortHash(r.InfoHash))
	}
	if len(r.Episodes) > 0 {
		eps := r.Episodes
		if len(eps) > 4 {
			eps = append(append([]string{}, eps[:3]...), fmt.Sprintf("… %d episodes", len(r.Episodes)))
		}
		parts = append(parts, strings.Join(eps, " "))
	}
	if len(parts) == 0 {
		return r.Title
	}
	return r.Title + " (" + strings.Join(parts, ", ") + ")"
}

func candLine(c *snapshot.Candidate) string {
	if c == nil {
		return ""
	}
	s := fmt.Sprintf("%s (%s %d", c.Release, c.Quality, c.Score)
	if len(c.Rejections) > 0 {
		s += "; rejected: " + c.Rejections[0].Reason
	}
	return s + ")"
}

// Text renders the entry as a few lines of plain text: the header, then the release, the reasoning, provider
// attempts, the import, what followed, flags and the verdict, each when present.
func (e *Entry) Text() string {
	var b strings.Builder
	name := e.Title.Name
	if y := fmt.Sprintf("(%d)", e.Title.Year); e.Title.Year > 0 && !strings.HasSuffix(name, y) {
		name += fmt.Sprintf(" (%d)", e.Title.Year)
	}
	mode := ""
	if e.Mode != "" {
		mode = " [" + e.Mode + "]"
	}
	sus := ""
	if e.Suspect {
		sus = " SUSPECT(" + e.Severity + ")"
	}
	fmt.Fprintf(&b, "%s  %s  %s  %s  %s  %s%s%s\n", e.ID, e.At.UTC().Format("2006-01-02 15:04"), e.Kind, e.Source,
		e.Version, name, mode, sus)
	line := func(label, text string) {
		if text != "" {
			fmt.Fprintf(&b, "  %-9s %s\n", label, text)
		}
	}
	line("what", e.Summary)
	line("release", e.Release.line())
	if r := e.Reasoning; r != nil {
		var why []string
		if r.Bucket != "" {
			why = append(why, r.Bucket+"/"+r.Why)
		}
		if r.Results > 0 {
			why = append(why, fmt.Sprintf("%d accepted of %d results", r.Accepted, r.Results))
		}
		if r.Chosen != nil {
			why = append(why, "chosen "+candLine(r.Chosen))
		}
		if r.Arr != nil {
			why = append(why, "arr's "+candLine(r.Arr))
		}
		for i, c := range r.RunnersUp {
			if i == 2 {
				why = append(why, fmt.Sprintf("+%d more", len(r.RunnersUp)-2))
				break
			}
			why = append(why, "runner-up "+candLine(&c))
		}
		if r.Existing != nil {
			why = append(why, fmt.Sprintf("existing %s %d", r.Existing.Quality, r.Existing.Score))
		}
		if len(r.Rejections) > 0 {
			var rs []string
			for _, k := range sortedKeys(r.Rejections) {
				rs = append(rs, fmt.Sprintf("%s %d", k, r.Rejections[k]))
			}
			why = append(why, "rejected: "+strings.Join(rs, ", "))
		}
		if r.Detail != "" && r.Bucket == "" {
			why = append(why, r.Detail)
		}
		if r.Missing != "" {
			why = append(why, r.Missing)
		}
		line("why", strings.Join(why, "; "))
		if r.Plan != nil {
			line("plan", r.Plan.Summary)
		}
	}
	if e.PolicyRev != "" {
		pol := e.PolicyRev
		if e.QueuedPolicyRev != "" {
			pol += " (queued under " + e.QueuedPolicyRev + ")"
		}
		line("policy", pol)
	}
	if len(e.Attempts) > 0 {
		var as []string
		for _, a := range e.Attempts {
			s := a.Provider + " " + a.Outcome
			if a.Code != "" {
				s += " " + a.Code
			}
			as = append(as, s)
		}
		line("providers", strings.Join(as, "; "))
	}
	if len(e.Cache) > 0 {
		var cs []string
		for _, c := range e.Cache {
			cs = append(cs, c.Provider+" "+c.State+" ("+c.Source+")")
		}
		line("cache", strings.Join(cs, "; "))
	}
	if d := e.Download; d != nil {
		s := fmt.Sprintf("#%d %s", d.ID, d.State)
		if d.Provider != "" {
			s += " on " + d.Provider
		}
		if d.Error != "" {
			s += ": " + d.Error
		}
		line("download", s)
	}
	if im := e.Import; im != nil && e.Kind != KindLink {
		s := im.State
		if im.Error != "" {
			s += ": " + im.Error
		}
		for _, f := range im.Files {
			s += "; " + f.Path
			if f.Resolution != "" || f.RuntimeSeconds > 0 {
				s += fmt.Sprintf(" (%s %dmin)", f.Resolution, f.RuntimeSeconds/60)
			}
		}
		for _, l := range im.Links {
			s += "; " + l.Kind + " " + l.Path
		}
		line("import", s)
	}
	for _, ev := range e.After {
		line("then", ev.At.UTC().Format("01-02 15:04")+" "+ev.Kind+": "+ev.Text)
	}
	for _, f := range e.Flags {
		ack := ""
		if f.Acked {
			ack = " (acked)"
		}
		line("flag", strings.ToUpper(f.Severity)+" "+f.Code+": "+f.Reason+ack)
	}
	if v := e.Verdict; v != nil {
		line("verdict", fmt.Sprintf("%s by %s: %s (%s)", v.Verdict, orDash(v.By), v.Reason, v.Action))
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
