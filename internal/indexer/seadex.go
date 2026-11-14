package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

// seadex reads SeaDex (releases.moe) best releases per AniList id.
//
// API shape from seadexerr v3.2.0 (Ryder-C/seadexerr src/releases.rs): GET
// <url>/api/collections/entries/records?filter=alID=<id>&expand=trs&page=1&perPage=100 answers PocketBase JSON
// {"items": [{"alID", "expand": {"trs": [{"id", "url", "infoHash", "isBest", "dualAudio", "tags", "tracker",
// "files": [{"length", "name"}], "releaseGroup", "created", "updated"}]}}]}. The recent feed sorts by -updated.
//
// Nyaa entries only: entries on any other tracker are dropped (SeaDex redacts their hashes, and AnimeBytes and the
// other private ones need a passkey, so there is nothing to add), as are entries tagged Incomplete (seadexerr does
// the same).
//
// What seadexerr (a SeaDex Torznab bridge for the arrs) does, and is done here too (src/service.rs, scoring.rs,
// torznab.rs):
//   - A movie search keeps entries with one feature file, a season search entries with more than one (a single
//     file is not a season pack); the recent feed keeps both.
//   - The policy's seadex scoring table (seadexerr's scoring.toml) drops entries with an excluded tag and scores
//     the rest; the highest scorers of a search are preferred (FlagSeaDexPreferred). Like seadexerr, the preference
//     is told to the ranking through seeders: 1000 for a preferred Nyaa entry, 10 for the others (the arrs compare
//     seeders on a log scale after quality and score). The recent feed prefers best entries, as seadexerr's does.
//   - Every entry carries seadexerr's rare indexer flag combination (download factor 0.75, upload factor 2,
//     internal, scene), as in the arrs. Deliberate deviation: the decider builds an arr-style "Seadex" format
//     requiring all four (the one the arr profiles score +5000) as SeaDex best (format.SeaDexBestForSeaDexerr), so
//     it scores SeaDex's best torrent wherever it was found, and not alternatives.
//   - The AniList id comes from the query, else from the service's resolver (TVDB id and season, or TMDb/IMDb id
//     for a movie), as seadexerr maps them through anibridge.
//   - Titles retain seadexerr's mapped identity and record id, but use the feature filenames' actual quality
//     instead of its fixed 1080p remux label (deliberate deviation). Mixed or unidentified features are Unknown.
//     The name is the query's title, else the name our database holds for the ids (Metadata.Names), else
//     "AniList <id>". Curator preference never supplies a source or resolution.
//   - The recent feed (QueryRSS) is filed under our titles as seadexerr's generic search files it: each entry's
//     AniList id is mapped back (Metadata.Targets) to the earliest regular season it maps into when it has more
//     than one feature, or to its movie when it has one; the release carries that TVDB or TMDb id, the season or
//     movie title above and the matching categories. An entry with no mapping, or one to a title we do not hold
//     (Metadata.Names), is dropped, as seadexerr drops one Sonarr or Radarr cannot name.
type seadex struct {
	base
	endpoint *url.URL
	scoring  *policy.SeaDexScoring
	resolver AniListResolver
	targets  AniListMapper
	names    TitleNamer
	torrents TorrentNamer
}

// Flags the seadex indexer sets: every SeaDex entry has FlagSeaDex, the ones SeaDex marks best FlagSeaDexBest too,
// and the ones the scoring table prefers FlagSeaDexPreferred.
const (
	FlagSeaDex          = "seadex"
	FlagSeaDexBest      = "seadex-best"
	FlagSeaDexPreferred = "seadex-preferred"
)

// ExtraSeaDexEntries lists, comma-separated, the AniList entries SeaDex lists one torrent under when there is more
// than one (a whole-season batch listed under each of the season's cours); the release's "anilist" extra stays the
// first. The decision of a seadex listing covers the union of those entries' episodes (decide/anime.go).
const ExtraSeaDexEntries = "seadex_entries"

// JoinSeaDexEntries is the ExtraSeaDexEntries value for ids: empty for fewer than two.
func JoinSeaDexEntries(ids []int) string {
	if len(ids) < 2 {
		return ""
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strconv.Itoa(id)
	}
	return strings.Join(out, ",")
}

// SeaDexEntries reads an ExtraSeaDexEntries value.
func SeaDexEntries(v string) []int {
	var ids []int
	for _, x := range strings.Split(v, ",") {
		if id, err := strconv.Atoi(strings.TrimSpace(x)); err == nil && id > 0 {
			ids = appendUnique(ids, id)
		}
	}
	return ids
}

// seadexerr's priority seeders for a Nyaa entry: a value has to be ten times the next for the arrs to rank by it.
const (
	seadexSeedersPreferred = 1000
	seadexSeedersOther     = 10
)

// MarkSeaDex copies the SeaDex flags of the seadex indexer's releases to every other release of the same torrent
// (by info hash) in rs, so a Nyaa result found through Prowlarr is known as SeaDex's pick too. It returns how many
// releases it marked. Entry provenance (anilist, seadex_id) is copied only to a release that has none of its own,
// and only when every SeaDex source of that hash names the same one: a hash listed under two entries stays without
// an entry, so standing is resolved against the title's own mappings rather than whichever source came last.
func MarkSeaDex(rs []Release) int {
	best := map[string]bool{}
	sources := map[string]map[string]map[string]bool{}
	preferred := map[string]bool{}
	for i := range rs {
		r := &rs[i]
		if r.InfoHash == "" || !slices.Contains(r.Flags, FlagSeaDex) {
			continue
		}
		if sources[r.InfoHash] == nil {
			sources[r.InfoHash] = map[string]map[string]bool{"anilist": {}, "seadex_id": {}}
		}
		for key, seen := range sources[r.InfoHash] {
			if v := r.Extras[key]; v != "" {
				seen[v] = true
			}
		}
		best[r.InfoHash] = best[r.InfoHash] || slices.Contains(r.Flags, FlagSeaDexBest)
		preferred[r.InfoHash] = preferred[r.InfoHash] || slices.Contains(r.Flags, FlagSeaDexPreferred)
	}
	n := 0
	for i := range rs {
		r := &rs[i]
		b, ok := best[r.InfoHash]
		if !ok || r.InfoHash == "" {
			continue
		}
		if r.Extras["anilist"] == "" {
			var copied map[string]string
			for _, key := range []string{"anilist", "seadex_id"} {
				if seen := sources[r.InfoHash][key]; len(seen) == 1 && r.Extras[key] == "" {
					for v := range seen {
						if copied == nil {
							copied = map[string]string{}
						}
						copied[key] = v
					}
				}
			}
			if copied != nil {
				owned := make(map[string]string, len(r.Extras)+len(copied))
				for k, v := range r.Extras {
					owned[k] = v
				}
				r.Extras = owned
				for k, v := range copied {
					r.setExtra(k, v)
				}
			}
		}
		r.Flags = append([]string(nil), r.Flags...)
		changed := false
		if !slices.Contains(r.Flags, FlagSeaDex) {
			r.Flags = append(r.Flags, FlagSeaDex)
			changed = true
		}
		if b && !slices.Contains(r.Flags, FlagSeaDexBest) {
			r.Flags = append(r.Flags, FlagSeaDexBest)
			changed = true
		}
		if preferred[r.InfoHash] && !slices.Contains(r.Flags, FlagSeaDexPreferred) {
			r.Flags = append(r.Flags, FlagSeaDexPreferred)
			changed = true
		}
		if changed {
			n++
		}
	}
	return n
}

// SeaDexPreference keeps the configured scoring winner ahead of best and other listed alternatives. It is a
// curator preference, never a quality or resolution. MarkSeaDex carries it to other sources of the same hash.
func SeaDexPreference(r *Release) int {
	if !slices.Contains(r.Flags, FlagSeaDex) {
		return 0
	}
	if slices.Contains(r.Flags, FlagSeaDexPreferred) {
		return 3
	}
	if slices.Contains(r.Flags, FlagSeaDexBest) {
		return 2
	}
	return 1
}

// seadexPublic are the trackers whose entries we keep.
var seadexPublic = map[string]bool{"Nyaa": true}

func newSeadex(b base) (*seadex, error) {
	u, err := url.Parse(b.ix.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("indexers[%s].url: not an http(s) URL", b.key)
	}
	if u.RawQuery != "" || u.User != nil {
		return nil, fmt.Errorf("indexers[%s].url: no query string or credentials", b.key)
	}
	if b.ix.APIKeyEnv != "" {
		return nil, fmt.Errorf("indexers[%s]: seadex is public; api_key_env is not used", b.key)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/api/collections/entries/records"
	return &seadex{base: b, endpoint: u, scoring: b.ix.SeaDex}, nil
}

func (s *seadex) Supports(q Query) bool {
	switch q.Type {
	case QueryMovie:
		return q.AniList > 0 || s.resolver != nil && (q.TMDb > 0 || q.IMDb != "")
	case QuerySeries:
		return q.AniList > 0 || s.resolver != nil && q.TVDB > 0 && q.Season > 0
	case QueryRSS:
		return true
	}
	return false
}

type sdResponse struct {
	PerPage    *int `json:"perPage"`
	Page       *int `json:"page"`
	TotalItems *int `json:"totalItems"`
	TotalPages *int `json:"totalPages"`
	Items      []struct {
		AlID   int      `json:"alID"`
		Trs    []string `json:"trs"`
		Expand *struct {
			Trs []sdTorrent `json:"trs"`
		} `json:"expand"`
	} `json:"items"`
}

type sdTorrent struct {
	ID           string   `json:"id"`
	URL          string   `json:"url"`
	InfoHash     string   `json:"infoHash"`
	IsBest       bool     `json:"isBest"`
	DualAudio    bool     `json:"dualAudio"`
	Tags         []string `json:"tags"`
	Tracker      string   `json:"tracker"`
	ReleaseGroup string   `json:"releaseGroup"`
	Created      string   `json:"created"`
	Updated      string   `json:"updated"`
	Files        []struct {
		Length int64  `json:"length"`
		Name   string `json:"name"`
	} `json:"files"`
}

func (s *seadex) Search(ctx context.Context, q Query) (*Result, error) {
	if !s.Supports(q) {
		return nil, fmt.Errorf("%w: seadex needs an AniList id", ErrUnsupported)
	}
	res := &Result{}
	var ids []int
	switch {
	case q.Type == QueryRSS:
		ids = []int{0}
	case q.AniList > 0:
		ids = []int{q.AniList}
	default:
		var err error
		if ids, err = s.resolver.AniListIDs(ctx, q); err != nil {
			return nil, fmt.Errorf("indexer %s: AniList id: %w", s.key, err)
		}
		if len(ids) == 0 {
			res.Notes = append(res.Notes, "no AniList id maps to this title: nothing to ask SeaDex")
			return res, nil
		}
	}
	type entry struct {
		r         Release
		t         *sdTorrent
		feats     int
		preferred bool
	}
	var entries []entry
	seen := map[string]bool{}
	members := map[string][]int{} // every entry listing a hash, before the de-duplication below keeps the first
	dropped, excluded, shape, unmapped, unlisted, wrongEntry, noRelations := 0, 0, 0, 0, 0, 0, 0
	var mapErr error
	name, year := s.feedName(ctx, q)
	for _, id := range ids {
		p := url.Values{"expand": {"trs"}, "page": {"1"}, "perPage": {"100"}}
		if q.Type == QueryRSS {
			p.Set("sort", "-updated")
		} else {
			p.Set("filter", "alID="+strconv.Itoa(id))
		}
		u := *s.endpoint
		u.RawQuery = p.Encode()
		body, err := s.get(ctx, u.String(), "application/json")
		res.Requests++
		if err != nil {
			var refused *gateway.Refused
			if errors.As(err, &refused) && !refused.Sent && refused.Reason != gateway.ReasonTooLarge {
				res.Requests--
			}
			return nil, &spentError{requests: res.Requests, err: err}
		}
		var resp sdResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, &spentError{requests: res.Requests, err: fmt.Errorf("indexer %s: not a SeaDex answer: %w", s.key, err)}
		}
		foundID := false
		for _, entry := range resp.Items {
			if entry.AlID == id {
				foundID = true
				break
			}
		}
		complete := completeSDResponse(resp)
		if id > 0 {
			complete = complete && resp.TotalItems != nil && *resp.TotalItems == len(resp.Items)
		}
		if id > 0 && !foundID && complete && len(resp.Items) == 0 {
			res.SeaDex = append(res.SeaDex, s.standingSnapshot(ctx, id, q))
		}
		for _, e := range resp.Items {
			if id > 0 && e.AlID != id {
				// An answer for another entry says nothing about the one asked for: no release is filed under the
				// requested title from it, and no standing is taken.
				wrongEntry++
				continue
			}
			if e.Expand == nil {
				if complete && e.Trs != nil && len(e.Trs) == 0 {
					res.SeaDex = append(res.SeaDex, s.standingSnapshot(ctx, e.AlID, q))
				}
				continue
			}
			// Only a record the entry's own relation list names belongs to it: an expanded record it does not list
			// is unrelated (a contradictory answer) and is neither ranked, stored nor offered as a release.
			// An answer without a relation list (no "trs" field, or null) gives no evidence that any expanded
			// record is the entry's: none of them is used, and it never counts as complete standing.
			if e.Trs == nil {
				noRelations++
			}
			names := make(map[string]bool, len(e.Trs))
			for _, rid := range e.Trs {
				names[rid] = true
			}
			// Validate each record the entry lists once into the observation: its release, feature count and
			// quality. Releases and standing are both rendered from it.
			var eligible []sdListed
			for i := range e.Expand.Trs {
				t := &e.Expand.Trs[i]
				if !names[t.ID] {
					unlisted++
					continue
				}
				r, feats, ok := s.release(ctx, t, e.AlID, q)
				if !ok {
					dropped++
					continue
				}
				eligible = append(eligible, sdListed{r: r, t: t, feats: feats, quality: s.torrentQuality(ctx, t)})
			}
			// Rank the complete eligible entry identically for RSS and search, before query shape/mapping filters.
			var ranked []*sdTorrent
			var rankedSizes []int64
			for _, l := range eligible {
				if !s.excluded(l.t.Tags) {
					ranked = append(ranked, l.t)
					rankedSizes = append(rankedSizes, l.r.Size)
				}
			}
			priorities := seadexPriorities(s.scoring, ranked, rankedSizes)
			preferred := map[string]bool{}
			for i, t := range ranked {
				preferred[t.ID] = priorities[i]
			}
			snapshot := s.standingSnapshot(ctx, e.AlID, q)
			for _, l := range eligible {
				rank := 1
				if l.t.IsBest {
					rank = 2
				}
				if preferred[l.t.ID] {
					rank = 3
				}
				snapshot.Torrents = append(snapshot.Torrents, SeaDexStanding{Facts: l.t, Size: l.r.Size, Quality: l.quality.Name, IsBest: l.t.IsBest, RecordID: l.t.ID, InfoHash: l.r.InfoHash, Preference: rank})
			}
			// The relation list and the expansion must be the same set of unique ids, checked both ways: a repeated
			// id or a record named on one side only leaves the set unproven, so the answer cannot advance a
			// retirement.
			relationsComplete := e.Trs != nil && len(names) == len(e.Trs) && len(e.Trs) == len(e.Expand.Trs)
			expanded := map[string]bool{}
			for _, t := range e.Expand.Trs {
				if expanded[t.ID] || !names[t.ID] {
					relationsComplete = false
				}
				expanded[t.ID] = true
				// An expanded record must identify itself: a missing id or tracker, or a public torrent without a
				// valid hash, leaves the set unproven (its absence from the eligible list is not an omission).
				if !identifiedSDTorrent(t) {
					relationsComplete = false
				}
			}
			for _, id := range e.Trs {
				if !expanded[id] {
					relationsComplete = false
				}
			}
			if complete && relationsComplete {
				res.SeaDex = append(res.SeaDex, snapshot)
			} else {
				// Verified presence still clears a pending retirement; incomplete absence never advances one. Only
				// a record the entry's own relation list names is presence (eligible holds no other): an expanded
				// record it does not list proves nothing about the entry.
				if len(snapshot.Torrents) > 0 {
					snapshot.Partial = true
					res.SeaDex = append(res.SeaDex, snapshot)
				}
			}
			for _, l := range eligible {
				r, t, feats := l.r, l.t, l.feats
				members[r.InfoHash] = appendUnique(members[r.InfoHash], e.AlID)
				if s.excluded(t.Tags) {
					excluded++
					continue
				}
				if q.Type == QueryRSS {
					ok, err := s.fileUnder(ctx, &r, l, e.AlID)
					if err != nil && mapErr == nil {
						mapErr = err
					}
					if !ok {
						unmapped++
						continue
					}
				} else {
					n := name
					if n == "" {
						n = "AniList " + strconv.Itoa(e.AlID)
					}
					r.Title = seadexReleaseTitle(seadexFeedTitle(q.Type, n, q.Season, year), l)
				}
				// seadexerr: a movie is one feature, a season pack more than one.
				if q.Type == QueryMovie && feats != 1 || q.Type == QuerySeries && feats <= 1 {
					shape++
					continue
				}
				if seen[r.InfoHash] {
					continue
				}
				seen[r.InfoHash] = true
				entries = append(entries, entry{r, t, feats, preferred[t.ID]})
			}
		}
	}
	// Preserve the legacy search seeder tie-break over the returned result set; curator flags use per-entry scoring.
	var legacy []*sdTorrent
	var legacySizes []int64
	for _, e := range entries {
		legacy = append(legacy, e.t)
		legacySizes = append(legacySizes, e.r.Size)
	}
	legacyPreferred := seadexPriorities(s.scoring, legacy, legacySizes)
	for i, e := range entries {
		r := e.r
		r.Seeders = seadexSeedersOther
		if e.preferred {
			r.Flags = append(r.Flags, FlagSeaDexPreferred)
		}
		r.setExtra(ExtraSeaDexEntries, JoinSeaDexEntries(members[r.InfoHash]))
		if q.Type == QueryRSS && e.t.IsBest || q.Type != QueryRSS && legacyPreferred[i] {
			r.Seeders = seadexSeedersPreferred
		}
		res.Releases = append(res.Releases, r)
	}
	if dropped > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("dropped %d entries: private tracker, incomplete or no hash", dropped))
	}
	if unlisted > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("dropped %d expanded records their entry does not list", unlisted))
	}
	if noRelations > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("ignored the expanded records of %d entries without a relation list (no trs)", noRelations))
	}
	if wrongEntry > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("dropped %d answers for an entry other than the one asked for", wrongEntry))
	}
	if excluded > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("dropped %d entries with an excluded tag", excluded))
	}
	if unmapped > 0 {
		note := fmt.Sprintf("dropped %d recent entries not filed under one of our titles", unmapped)
		if mapErr != nil {
			note += fmt.Sprintf(" (first lookup error: %v)", mapErr)
		}
		res.Notes = append(res.Notes, note)
	}
	if shape > 0 {
		kind := "single-file entries (not a season pack)"
		if q.Type == QueryMovie {
			kind = "entries with more than one feature (not a movie)"
		}
		res.Notes = append(res.Notes, fmt.Sprintf("dropped %d %s", shape, kind))
	}
	return res, nil
}

func (s *seadex) excluded(tags []string) bool {
	if s.scoring == nil {
		return false
	}
	for _, x := range s.scoring.ExcludeTags {
		if slices.Contains(tags, x) {
			return true
		}
	}
	return false
}

// seadexPriorities is seadexerr's ScoringConfig::priorities: each entry's base score is the sum of the weights it
// matches (best, dual audio, tags); with a size span, base*span + size_weight*(size-min) is compared, so size only
// breaks ties between equal bases unless its weight outweighs a base step. Every highest scorer is preferred.
func seadexPriorities(sc *policy.SeaDexScoring, ts []*sdTorrent, sizes []int64) []bool {
	out := make([]bool, len(ts))
	if len(ts) == 0 {
		return out
	}
	minS, maxS := sizes[0], sizes[0]
	for _, z := range sizes {
		minS, maxS = min(minS, z), max(maxS, z)
	}
	span := new(big.Int).SetInt64(maxS - minS)
	best := sc.BestWeight()
	var dual, sizeW int
	var tags map[string]int
	if sc != nil {
		dual, sizeW, tags = sc.DualAudio, sc.SizeWeight, sc.Tags
	}
	scores := make([]*big.Int, len(ts))
	for i, t := range ts {
		base := int64(0)
		if t.IsBest {
			base += int64(best)
		}
		if t.DualAudio {
			base += int64(dual)
		}
		for _, tag := range t.Tags {
			base += int64(tags[tag])
		}
		v := big.NewInt(base)
		if span.Sign() != 0 {
			v.Mul(v, span)
			v.Add(v, new(big.Int).Mul(big.NewInt(int64(sizeW)), big.NewInt(sizes[i]-minS)))
		}
		scores[i] = v
	}
	top := scores[0]
	for _, v := range scores[1:] {
		if v.Cmp(top) > 0 {
			top = v
		}
	}
	for i, v := range scores {
		out[i] = v.Cmp(top) == 0
	}
	return out
}

var nyaaViewRE = regexp.MustCompile(`^https?://(?:www\.)?nyaa\.si/view/(\d+)(?:[/?#]|$)`)

func (s *seadex) release(ctx context.Context, t *sdTorrent, alID int, q Query) (Release, int, bool) {
	if !seadexPublic[t.Tracker] {
		return Release{}, 0, false
	}
	for _, tag := range t.Tags {
		if strings.EqualFold(tag, "Incomplete") {
			return Release{}, 0, false
		}
	}
	if NormalizeHash(t.InfoHash) == "" {
		return Release{}, 0, false
	}
	r, features := seadexRelease(t, alID, q.Type)
	s.stamp(ctx, &r)
	return r, features, true
}

// seadexRelease is the release a listed SeaDex torrent becomes, and its feature count, without its title
// (seadexReleaseTitle) or indexer stamp: the same for a search and for a stored candidate (SeaDexCandidateRelease).
func seadexRelease(t *sdTorrent, alID int, typ QueryType) (Release, int) {
	hash := NormalizeHash(t.InfoHash)
	// seadexerr's flags: Freeleech25 + DoubleUpload + Internal + Scene, a combination other indexers almost never
	// send, which the arrs' "Seadex" format requires.
	r := Release{InfoHash: hash, Seeders: -1, Peers: -1, Magnet: BareMagnet(hash),
		Flags: []string{FlagSeaDex, "internal", "scene"}}
	r.setExtra("downloadvolumefactor", "0.75")
	r.setExtra("uploadvolumefactor", "2")
	if t.IsBest {
		r.Flags = append(r.Flags, FlagSeaDexBest)
	}
	var largest int64
	for _, f := range t.Files {
		r.Size += f.Length
		largest = max(largest, f.Length)
	}
	// Files at least half the size of the largest are features; the rest are extras (seadexerr count_features).
	// The title is set by the caller (seadexReleaseTitle).
	features := 0
	for _, f := range t.Files {
		if f.Length >= largest/2 {
			features++
		}
	}
	if m := nyaaViewRE.FindStringSubmatch(t.URL); m != nil {
		r.Link = NewLink("https://nyaa.si/download/" + m[1] + ".torrent")
		r.setExtra("source", "https://nyaa.si/view/"+m[1])
	}
	r.PublishDate = parseDate(t.Updated)
	if r.PublishDate.IsZero() {
		r.PublishDate = parseDate(t.Created)
	}
	r.setExtra("seadex_id", t.ID)
	r.setExtra("tracker", t.Tracker)
	r.setExtra("group", t.ReleaseGroup)
	r.setExtra("features", strconv.Itoa(features))
	r.setExtra("tags", strings.Join(t.Tags, ","))
	if t.DualAudio {
		r.setExtra("dual_audio", "true")
	}
	if alID > 0 {
		r.setExtra("anilist", strconv.Itoa(alID))
	}
	if typ == QueryMovie {
		r.Categories = []int{2000}
	} else {
		r.Categories = []int{5000, 5070}
	}
	return r, features
}

// SeaDexCandidateRelease is the release a search for name (a movie of year, or a series' season) would return for a
// stored curated torrent: the indexer's own title, flags and size from the stored record and facts, with the stored
// rank's flags. A stored record already passed the search's torrent checks (public tracker, complete, hash) when it
// was stored; false where the search would still drop it: no quality, or the wrong shape for the query (a movie is
// one feature, a season pack more than one; known only from the facts' file list). It sends no request: curator
// eligibility (decide) runs the decider's checks on it.
func SeaDexCandidateRelease(t SeaDexStanding, typ QueryType, name string, season, year int) (Release, bool) {
	if t.Quality == "" {
		return Release{}, false
	}
	facts := sdTorrent{IsBest: t.IsBest}
	if t.Facts != nil {
		facts = *t.Facts
	}
	facts.InfoHash = t.InfoHash // the stored record's hash is the identity
	r, feats := seadexRelease(&facts, t.Entry, typ)
	if len(facts.Files) > 0 && (typ == QueryMovie && feats != 1 || typ == QuerySeries && feats <= 1) {
		return Release{}, false
	}
	if t.Size > 0 {
		r.Size = t.Size
	}
	r.Title = seadexFeedTitle(typ, name, season, year) + " " + strings.ReplaceAll(t.Quality, "-", " ") + " (" + t.RecordID + ")"
	if t.Preference >= 3 {
		r.Flags = append(r.Flags, FlagSeaDexPreferred)
	}
	return r, true
}

// feedName is the name and year a search's titles take: the query's, else the ones our database holds for its ids
// (seadexerr asks Sonarr and Radarr). An empty name leaves the release named "AniList <id>".
func (s *seadex) feedName(ctx context.Context, q Query) (string, int) {
	if q.Type == QueryRSS || q.Title != "" || s.names == nil {
		return q.Title, q.Year
	}
	name, year, ok, err := s.names.TitleName(ctx, q)
	if err != nil || !ok {
		return "", q.Year
	}
	if q.Year > 0 {
		year = q.Year
	}
	return name, year
}

// fileUnder files a recent-feed entry under one of our titles (seadexerr service.rs title_keys): more than one
// feature is a season pack, filed under the earliest regular season the AniList id maps into
// (select_tvdb_and_season); one feature is a movie, filed under its TMDb id. It reports false when the entry has no
// such mapping or the title is not ours.
func (s *seadex) fileUnder(ctx context.Context, r *Release, l sdListed, alID int) (bool, error) {
	feats := l.feats
	if alID <= 0 || s.targets == nil || s.names == nil || feats < 1 {
		return false, nil
	}
	ts, err := s.targets.AniListTargets(ctx, alID)
	if err != nil {
		return false, err
	}
	var q Query
	if feats > 1 {
		for _, x := range ts {
			if x.TVDB > 0 && x.Season > 0 && (q.TVDB == 0 || x.Season < q.Season) {
				q = Query{Type: QuerySeries, TVDB: x.TVDB, Season: x.Season}
			}
		}
	} else {
		for _, x := range ts {
			if x.TMDb > 0 {
				q = Query{Type: QueryMovie, TMDb: x.TMDb}
				break
			}
		}
	}
	if q.Type == "" {
		return false, nil
	}
	name, year, ok, err := s.names.TitleName(ctx, q)
	if err != nil || !ok || name == "" {
		return false, err
	}
	r.Title = seadexReleaseTitle(seadexFeedTitle(q.Type, name, q.Season, year), l)
	if q.Type == QueryMovie {
		r.TMDb, r.Categories = q.TMDb, []int{2000}
	} else {
		r.TVDB, r.Categories = q.TVDB, []int{5000, 5070}
	}
	return true, nil
}

// seadexFeedTitle is seadexerr's feed title: "<Series> SNN" for a season (format_tv_feed_title), "<Movie> (<year>)"
// for a movie, or the bare name without a year (format_movie_feed_title).
func seadexFeedTitle(typ QueryType, name string, season, year int) string {
	if typ == QueryMovie {
		if year == 0 {
			return name
		}
		return fmt.Sprintf("%s (%d)", name, year)
	}
	return fmt.Sprintf("%s S%02d", name, season)
}

// sdListed is one record an entry lists, validated once (seadex.release, torrentQuality).
type sdListed struct {
	r       Release
	t       *sdTorrent
	feats   int
	quality sonarr.Quality
}

// seadexReleaseTitle keeps the mapped identity and record id, but takes the quality from the feature filenames
// rather than copying seadexerr's fixed 1080p remux label. Curator preference stays in flags/scoring, independently
// of media quality.
func seadexReleaseTitle(feed string, l sdListed) string {
	return feed + " " + strings.ReplaceAll(l.quality.Name, "-", " ") + " (" + l.t.ID + ")"
}

// torrentQuality is the quality evidence of one SeaDex torrent, shared by release titles and stored standing: the
// feature filenames, else agreeing provider-reported names for that exact hash.
func (s *seadex) torrentQuality(ctx context.Context, t *sdTorrent) sonarr.Quality {
	q := seadexQuality(t)
	if q == sonarr.QualityUnknown && s.torrents != nil {
		if names, err := s.torrents.TorrentNames(ctx, t.InfoHash); err == nil {
			q = seadexQuality(t, names...)
		}
	}
	return q
}

func identifiedSDTorrent(t sdTorrent) bool {
	if t.ID == "" || t.Tracker == "" {
		return false
	}
	return !seadexPublic[t.Tracker] || NormalizeHash(t.InfoHash) != ""
}

// A synthetic pack title can describe a quality only when all its features agree. Missing filename quality may
// use agreeing provider-reported names for that exact hash; known filename qualities still take precedence.
// Small extras are excluded by the same half-largest rule used for shape. Conflicting or unidentified qualities
// remain Unknown. Name-only parsing prevents a container extension from supplying source/resolution evidence.
func seadexQuality(t *sdTorrent, torrentNames ...string) sonarr.Quality {
	fallback := sonarr.QualityUnknown
	for _, name := range torrentNames {
		if SeaDexRecordID(name) != "" && strings.Contains(strings.ToLower(name), "1080p remux") {
			continue
		}
		q := explicitSeaDexQuality(name)
		if q == sonarr.QualityUnknown {
			continue
		}
		if fallback != sonarr.QualityUnknown && fallback != q {
			return sonarr.QualityUnknown
		}
		fallback = q
	}
	var largest int64
	for _, f := range t.Files {
		largest = max(largest, f.Length)
	}
	quality := sonarr.QualityUnknown
	seen := false
	for _, f := range t.Files {
		if f.Length <= 0 || f.Length < largest/2 {
			continue
		}
		name := path.Base(strings.ReplaceAll(f.Name, `\`, "/"))
		parsed := sonarr.ParseQualityName(name)
		q := explicitSeaDexQuality(name)
		if q == sonarr.QualityUnknown {
			if fallback != sonarr.QualityUnknown && (parsed.SourceDetectionSource == sonarr.DetectionName && parsed.Quality.Source != fallback.Source || parsed.ResolutionDetectionSource == sonarr.DetectionName && parsed.Quality.Resolution != fallback.Resolution) {
				return sonarr.QualityUnknown
			}
			q = fallback
		}
		if q == sonarr.QualityUnknown || seen && q != quality {
			return sonarr.QualityUnknown
		}
		quality, seen = q, true
	}
	return quality
}

// Both dimensions must be present in the name; Sonarr's defaults are useful for arr parity, not evidence here.
func explicitSeaDexQuality(name string) sonarr.Quality {
	p := sonarr.ParseQualityName(name)
	if p.SourceDetectionSource != sonarr.DetectionName || p.ResolutionDetectionSource != sonarr.DetectionName {
		return sonarr.QualityUnknown
	}
	return p.Quality
}

func completeSDResponse(r sdResponse) bool {
	if r.Items == nil || r.Page == nil || r.PerPage == nil || r.TotalItems == nil || r.TotalPages == nil {
		return false
	}
	page, per, total, pages := *r.Page, *r.PerPage, *r.TotalItems, *r.TotalPages
	if page != 1 || per <= 0 || total < 0 || pages < 0 {
		return false
	}
	expectedPages := total / per
	if total%per != 0 {
		expectedPages++
	}
	return pages == expectedPages && len(r.Items) == min(per, total)
}

// standingSnapshot starts an entry's snapshot: its resolver targets, else the title the search asked for. A search
// by AniList id alone names no title, so it supplies no target; the stored row then keeps the targets it had
// (confirmSeaDex), never an empty one that would leave it unmapped.
func (s *seadex) standingSnapshot(ctx context.Context, id int, q Query) SeaDexSnapshot {
	snapshot := SeaDexSnapshot{AniList: id}
	if s.targets != nil {
		snapshot.Targets, _ = s.targets.AniListTargets(ctx, id)
	}
	if len(snapshot.Targets) == 0 && q.Type != QueryRSS {
		if t := (AniListTarget{TVDB: q.TVDB, TMDb: q.TMDb, Season: q.Season}); t.valid() {
			snapshot.Targets = []AniListTarget{t}
		}
	}
	return snapshot
}
