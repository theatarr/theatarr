package torrents

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// Groups a torrent can be in (Torrent.Groups, the report's lists).
const (
	GroupUnused         = "unused"
	GroupDuplicateHash  = "duplicate_hash"
	GroupDuplicateTitle = "duplicate_title"
	GroupFailed         = "failed"
	GroupStalled        = "stalled"
	GroupLargest        = "largest"
)

// StalledAfter is how long a torrent may sit queued or downloading before the report calls it stalled; it is also
// the delete guard for recent torrents.
const StalledAfter = 24 * time.Hour

// largestN is how many torrents the largest group lists.
const largestN = 25

// Torrent is one provider torrent in the report.
type Torrent struct {
	Provider string         `json:"provider"`
	ID       string         `json:"id"`
	Hash     string         `json:"hash"`
	Name     string         `json:"name"`
	Bytes    int64          `json:"bytes"`
	Added    time.Time      `json:"added,omitzero"`
	Status   string         `json:"status"`
	State    provider.State `json:"state"`
	Progress float64        `json:"progress"`
	// Files is the number of files (selected ones on Real-Debrid), -1 when neither the list nor the registry says.
	Files int `json:"files"`
	// LinkedFiles counts the library links and scanned symlinks that point into it.
	LinkedFiles int   `json:"linked_files"`
	Usage       Usage `json:"usage"`
	// Reasons say why the usage is unknown.
	Reasons []string `json:"reasons,omitempty"`
	// Refs counts the references by kind; RefList lists the first of them.
	Refs    map[string]int `json:"refs,omitempty"`
	RefList []Ref          `json:"ref_list,omitempty"`
	// Titles are the distinct titles all its references belong to (not only those RefList lists), by name.
	Titles []LinkedTitle `json:"titles,omitempty"`
	// AddedByTheatarr: Theatarr's router added it (a download of ours names it and did not reuse it).
	AddedByTheatarr bool `json:"added_by_theatarr"`
	// Parsed is the title the name parses to ("Title (2020)", "Title S01E02"), Quality its quality.
	Parsed  string   `json:"parsed,omitempty"`
	Quality string   `json:"quality,omitempty"`
	Groups  []string `json:"groups,omitempty"`

	titleKey string
}

// Member names one torrent of a group.
type Member struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

// Group is a set of torrents that belong together: one hash, or one parsed title.
type Group struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Members []Member `json:"members"`
}

// ProviderReport is one account's torrents.
type ProviderReport struct {
	Provider string    `json:"provider"`
	Kind     string    `json:"kind"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Count    int       `json:"count"`
	Bytes    int64     `json:"bytes"`
	Usage    UsageSums `json:"usage"`
	// Limit is how many torrents the account may hold (AllDebrid's magnet_limit; 0: none known). NearLimit is set
	// from policy.TorrentLimitWarnPercent of it. Only reported.
	Limit     int       `json:"limit,omitempty"`
	NearLimit bool      `json:"near_limit,omitempty"`
	Torrents  []Torrent `json:"torrents"`
}

// UsageSums counts torrents and bytes per usage.
type UsageSums struct {
	Used         int   `json:"used"`
	Unused       int   `json:"unused"`
	Unknown      int   `json:"unknown"`
	UnusedBytes  int64 `json:"unused_bytes"`
	UnknownBytes int64 `json:"unknown_bytes"`
}

// Report is every configured provider's torrents with their usage, and the groups across them.
type Report struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Providers   []ProviderReport `json:"providers"`
	// Unknown are the reasons that make every torrent without references unknown (a scan root unreadable).
	Unknown        []string `json:"unknown,omitempty"`
	Unused         []Member `json:"unused"`
	DuplicateHash  []Group  `json:"duplicate_hash"`
	DuplicateTitle []Group  `json:"duplicate_title"`
	Failed         []Member `json:"failed"`
	Stalled        []Member `json:"stalled"`
	Largest        []Member `json:"largest"`
}

// Report lists every configured provider (only) and decides each torrent's usage. A provider whose list fails is in
// the report with its error. Read-only.
func (m *Manager) Report(ctx context.Context) (*Report, error) {
	ix, err := m.loadRefs(ctx)
	if err != nil {
		return nil, err
	}
	lists := map[string][]provider.Torrent{}
	errs := map[string]error{}
	for _, k := range m.keys() {
		lists[k], errs[k] = m.list(ctx, k)
	}
	return m.build(ix, lists, errs), nil
}

func (m *Manager) build(ix *refIndex, lists map[string][]provider.Torrent, errs map[string]error) *Report {
	now := m.now()
	r := &Report{GeneratedAt: now, Unknown: ix.unknown, Unused: []Member{}, DuplicateHash: []Group{},
		DuplicateTitle: []Group{}, Failed: []Member{}, Stalled: []Member{}, Largest: []Member{}}
	for _, k := range m.keys() {
		pr := ProviderReport{Provider: k, Kind: m.kind(k), Torrents: []Torrent{}}
		if err := errs[k]; err != nil {
			pr.Error = err.Error()
			r.Providers = append(r.Providers, pr)
			continue
		}
		pr.OK = true
		for _, lt := range lists[k] {
			t := ix.torrent(k, lt, now)
			pr.Count++
			pr.Bytes += t.Bytes
			switch t.Usage {
			case Used:
				pr.Usage.Used++
			case Unused:
				pr.Usage.Unused++
				pr.Usage.UnusedBytes += t.Bytes
			default:
				pr.Usage.Unknown++
				pr.Usage.UnknownBytes += t.Bytes
			}
			pr.Torrents = append(pr.Torrents, t)
		}
		sort.SliceStable(pr.Torrents, func(i, j int) bool { return pr.Torrents[i].Bytes > pr.Torrents[j].Bytes })
		pr.Limit = m.Policy.Providers[k].TorrentLimit()
		pr.NearLimit = policy.NearTorrentLimit(pr.Count, pr.Limit)
		r.Providers = append(r.Providers, pr)
	}
	r.group()
	return r
}

// torrent is lt with its usage and parsed title.
func (ix *refIndex) torrent(key string, lt provider.Torrent, now time.Time) Torrent {
	h := indexer.NormalizeHash(lt.Hash)
	if h == "" {
		h = strings.ToLower(lt.Hash)
	}
	t := Torrent{Provider: key, ID: lt.ID, Hash: h, Name: lt.Name, Bytes: lt.Bytes, Added: lt.Added,
		Status: lt.Status, State: lt.State, Progress: lt.Progress, Files: -1,
		AddedByTheatarr: ix.added[key+"\x00"+lt.ID]}
	if lt.Files != nil {
		t.Files = 0
		for _, f := range lt.Files {
			if f.Selected {
				t.Files++
			}
		}
	} else if n, ok := ix.files[h]; ok {
		t.Files = n
	}
	t.Usage, t.Refs, t.RefList, t.Reasons, t.Titles = ix.usage(key, lt)
	t.LinkedFiles = t.Refs[RefLink] + t.Refs[RefScan]
	t.titleKey, t.Parsed, t.Quality = parseName(lt.Name, now.Year())
	if t.Usage == Unused {
		t.Groups = append(t.Groups, GroupUnused)
	}
	if lt.State == provider.StateError {
		t.Groups = append(t.Groups, GroupFailed)
	}
	if (lt.State == provider.StateQueued || lt.State == provider.StateDownloading) && !lt.Added.IsZero() &&
		now.Sub(lt.Added) > StalledAfter {
		t.Groups = append(t.Groups, GroupStalled)
	}
	return t
}

// group fills the report's groups and each torrent's Groups.
func (r *Report) group() {
	type ref struct{ p, i int }
	byHash := map[string][]ref{}
	byTitle := map[string][]ref{}
	var all []ref
	for p := range r.Providers {
		for i := range r.Providers[p].Torrents {
			t := &r.Providers[p].Torrents[i]
			x := ref{p, i}
			all = append(all, x)
			byHash[t.Hash] = append(byHash[t.Hash], x)
			if t.titleKey != "" {
				byTitle[t.titleKey] = append(byTitle[t.titleKey], x)
			}
		}
	}
	at := func(x ref) *Torrent { return &r.Providers[x.p].Torrents[x.i] }
	member := func(x ref) Member { t := at(x); return Member{Provider: t.Provider, ID: t.ID} }
	for _, x := range all {
		t := at(x)
		for _, g := range t.Groups {
			switch g {
			case GroupUnused:
				r.Unused = append(r.Unused, member(x))
			case GroupFailed:
				r.Failed = append(r.Failed, member(x))
			case GroupStalled:
				r.Stalled = append(r.Stalled, member(x))
			}
		}
	}
	keys := make([]string, 0, len(byHash))
	for h, xs := range byHash {
		if len(xs) > 1 && h != "" {
			keys = append(keys, h)
		}
	}
	sort.Strings(keys)
	for _, h := range keys {
		g := Group{Key: h, Label: at(byHash[h][0]).Name}
		for _, x := range byHash[h] {
			g.Members = append(g.Members, member(x))
			at(x).Groups = append(at(x).Groups, GroupDuplicateHash)
		}
		r.DuplicateHash = append(r.DuplicateHash, g)
	}
	keys = keys[:0]
	for k, xs := range byTitle {
		hashes := map[string]bool{}
		for _, x := range xs {
			hashes[at(x).Hash] = true
		}
		if len(hashes) > 1 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		xs := byTitle[k]
		g := Group{Key: k, Label: at(xs[0]).Parsed}
		for _, x := range xs {
			g.Members = append(g.Members, member(x))
			at(x).Groups = append(at(x).Groups, GroupDuplicateTitle)
		}
		r.DuplicateTitle = append(r.DuplicateTitle, g)
	}
	sort.SliceStable(all, func(i, j int) bool { return at(all[i]).Bytes > at(all[j]).Bytes })
	for i, x := range all {
		if i == largestN {
			break
		}
		r.Largest = append(r.Largest, member(x))
		at(x).Groups = append(at(x).Groups, GroupLargest)
	}
}

// seriesMarker is an explicit episode or season in a name: S01, S01E02, 1x02, "season".
var seriesMarker = regexp.MustCompile(`(?i)\bS\d{1,4}(E\d{1,4})?\b|\b\d{1,2}x\d{2,4}\b|\bseason\b`)

// bareYear: the series parser read nothing but a year as the episode ("2012" as S20E12, or "2000" as absolute
// episode 2000), the movie parser reads that year (1900 to thisYear+1) under the same title, and the name has no
// explicit series marker (seriesMarker, a daily date) nor a leading "[Group]" tag (anime). The movie parser runs only
// for a single episode or absolute number with no marker; mv is then its result, else nil.
func bareYear(name string, ep *sonarr.ParsedEpisodeInfo, thisYear int) (bool, *radarr.ParsedMovieInfo) {
	n := 0
	switch {
	case len(ep.EpisodeNumbers) == 1 && len(ep.AbsoluteEpisodeNumbers) == 0:
		n = ep.SeasonNumber*100 + ep.EpisodeNumbers[0]
	case len(ep.AbsoluteEpisodeNumbers) == 1 && len(ep.EpisodeNumbers) == 0:
		n = ep.AbsoluteEpisodeNumbers[0]
	}
	if n < 1900 || n > thisYear+1 || ep.FullSeason || ep.IsDaily() || seriesMarker.MatchString(name) ||
		strings.HasPrefix(strings.TrimSpace(name), "[") {
		return false, nil
	}
	mv := radarr.ParseMovieTitle(name, false)
	return mv != nil && len(mv.MovieTitles) > 0 && mv.Year == n && norm(ep.SeriesTitle) == norm(mv.MovieTitles[0]), mv
}

// parseName parses a torrent name as an episode or season (title, season, episodes) or else as a movie (title,
// year): the duplicate-title key, a label, and the quality. A name that parses as neither has no key. A film whose
// year the series parser reads as its episode (bareYear) is the film. thisYear bounds a plausible film year.
func parseName(name string, thisYear int) (key, label, quality string) {
	if name == "" {
		return "", "", ""
	}
	var mv *radarr.ParsedMovieInfo
	if ep := sonarr.ParseTitle(name); ep != nil && ep.SeriesTitle != "" &&
		(len(ep.EpisodeNumbers) > 0 || len(ep.AbsoluteEpisodeNumbers) > 0 || ep.FullSeason || ep.SeasonNumber > 0) {
		var film bool
		if film, mv = bareYear(name, ep, thisYear); !film {
			return seriesName(ep)
		}
	}
	if mv == nil {
		mv = radarr.ParseMovieTitle(name, false)
	}
	if mv != nil && len(mv.MovieTitles) > 0 && mv.Year > 0 {
		return fmt.Sprintf("movie:%s:%d", norm(mv.MovieTitles[0]), mv.Year),
			fmt.Sprintf("%s (%d)", mv.MovieTitles[0], mv.Year), mv.Quality.Quality.Name
	}
	return "", "", ""
}

// seriesName is parseName's answer for an episode, a season pack or an absolute number.
func seriesName(ep *sonarr.ParsedEpisodeInfo) (key, label, quality string) {
	label = fmt.Sprintf("%s S%02d", ep.SeriesTitle, ep.SeasonNumber)
	key = "series:" + norm(ep.SeriesTitle) + fmt.Sprintf(":s%d", ep.SeasonNumber)
	switch {
	case len(ep.EpisodeNumbers) > 0:
		var es []string
		for _, e := range ep.EpisodeNumbers {
			es = append(es, fmt.Sprintf("E%02d", e))
		}
		label += strings.Join(es, "")
		key += ":" + strings.ToLower(strings.Join(es, ""))
	case len(ep.AbsoluteEpisodeNumbers) > 0:
		var es []string
		for _, e := range ep.AbsoluteEpisodeNumbers {
			es = append(es, fmt.Sprint(e))
		}
		label = fmt.Sprintf("%s %s", ep.SeriesTitle, strings.Join(es, "-"))
		key = "series:" + norm(ep.SeriesTitle) + ":abs" + strings.Join(es, "-")
	default:
		key += ":pack"
	}
	return key, label, ep.Quality.Quality.Name
}

// norm is a title folded for comparison: letters and digits, lower case.
func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
