package decide

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

type standingMatch struct {
	entry   int
	torrent indexer.SeaDexStanding
}
type seaDexTitleContext struct{}
type seaDexContext struct{}

// seaDexOp is one operation's pinned standing: the entry records and catalog mappings, read once in one read
// transaction the first time a prefer-on version needs them (or given in memory), shared by every version and
// goroutine of the operation, with each version policy's view built once.
type seaDexOp struct {
	db      *sql.DB
	fresh   []indexer.SeaDexSnapshot // answers laid over the stored records in memory (an operation that cannot store)
	overlay bool                     // WithSeaDexOverlay: later batches' answers are laid over the stored records too
	once    sync.Once
	base    seaDexState // the records as read, unconfigured
	mu      sync.Mutex
	views   map[*policy.VersionPolicy]seaDexState
}

// seaDexState is an immutable view of an operation's standing: the records, the catalog mappings and their
// indexes, configured for one version policy or as stored. A prefer-off version's view is empty and was never read.
type seaDexState struct {
	op  *seaDexOp
	err error
	// stored: the operation pinned stored standing (later in-memory answers do not replace it). configured: the
	// view ranks under a prefer-on version's settings; only such a view resolves held files. off: the version does
	// not prefer SeaDex; nothing it decides or loads takes standing.
	stored, configured, off bool
	snapshots               []indexer.SeaDexSnapshot
	titles                  map[int64][]int
	cover                   map[int64]map[int]*indexer.SeaDexEntryMapping
	series                  map[int64]bool
	byEntry                 map[int][]indexer.SeaDexStanding
	hashes                  map[string][]standingMatch
	records                 map[string][]standingMatch
	entries                 map[int]bool
	// cache is the live cache evidence (provider_cache miss or uncached rows) for the standing's hashes, by hash and
	// provider, read in the same transaction as the records; curated alternatives carry their own (Cache).
	cache map[string]map[string]string
}

func seaDexView(ctx context.Context) seaDexState {
	state, _ := ctx.Value(seaDexContext{}).(seaDexState)
	return state
}

// WithSeaDex supplies in-memory answers to every candidate of the batch, laid over the operation's in-memory
// records, or over its stored ones for an operation that cannot store (WithSeaDexOverlay). An operation that pinned
// stored standing keeps it: its answers were stored first.
func WithSeaDex(ctx context.Context, snapshots []indexer.SeaDexSnapshot) context.Context {
	state := seaDexView(ctx)
	if op := state.op; op != nil && op.overlay {
		return WithSeaDexOverlay(ctx, op.db, append(slices.Clone(op.fresh), snapshots...))
	}
	if state.stored {
		return ctx
	}
	var base seaDexState
	if state.op != nil {
		base = state.op.base
	}
	next := seaDexState{snapshots: overlaySeaDex(base.snapshots, snapshots, time.Now()), titles: base.titles,
		cover: base.cover, series: base.series}
	return context.WithValue(ctx, seaDexContext{}, indexStanding(next))
}

// SameTorrent requires two known matching hashes. Record ids can be edited upstream.
func SameTorrent(a, b *ExistingFile) bool {
	if a == nil || b == nil {
		return false
	}
	hash := indexer.NormalizeHash(a.InfoHash)
	return hash != "" && hash == indexer.NormalizeHash(b.InfoHash)
}

// indexStanding builds the lookups of state.snapshots once. Torrents pending retirement or left as tombstones still
// identify held files, but only currently listed ones are curated alternatives. A view built without an operation
// becomes its own in-memory operation.
func indexStanding(state seaDexState) seaDexState {
	if state.op == nil {
		state.op = &seaDexOp{base: state, views: map[*policy.VersionPolicy]seaDexState{}}
		state.op.base.configured = false
		state.op.once.Do(func() {})
	}
	state.byEntry = map[int][]indexer.SeaDexStanding{}
	state.hashes = map[string][]standingMatch{}
	state.records = map[string][]standingMatch{}
	state.entries = map[int]bool{}
	for _, s := range state.snapshots {
		state.entries[s.AniList] = true
		for _, t := range s.Torrents {
			t.InfoHash = indexer.NormalizeHash(t.InfoHash)
			if t.InfoHash == "" {
				continue
			}
			if t.Preference > 0 && t.MissingSince == 0 {
				t.Cache = state.cache[t.InfoHash]
				state.byEntry[s.AniList] = append(state.byEntry[s.AniList], t)
			}
			m := standingMatch{s.AniList, t}
			state.hashes[t.InfoHash] = append(state.hashes[t.InfoHash], m)
			if t.RecordID != "" {
				state.records[t.RecordID] = append(state.records[t.RecordID], m)
			}
		}
	}
	return state
}

// seaDexResolve says what is resolved: a held file (stored standing may be kept), an incoming release (grab-time
// identity, no episodes), or an incoming file at import (verified episodes; standing only from covering entries).
type seaDexResolve int

const (
	resolveHeld seaDexResolve = iota
	resolveRelease
	resolveImport
)

// resolveSeaDex gives a held file its standing in the operation's view (resolveStanding).
func resolveSeaDex(ctx context.Context, f *ExistingFile) ExistingFile {
	titleID, _ := ctx.Value(seaDexTitleContext{}).(int64)
	return resolveStanding(seaDexView(ctx), titleID, f, resolveHeld)
}

// resolveStanding gives a held file (or an incoming release or file) its standing in the view.
//
// Scope: the file's own entry when it has one, else the entries mapped to the title. Matches outside the scope are
// not this title's standing and are ignored without retiring anything. A file with verified episodes (and every
// series file at import) takes standing only from entries covering all its episodes, mapped or not: a split-cour
// hash listed under the other cour's entry, a saved or explicit entry not covering them, or a hash an unmapped
// series shares with another title gives it nothing (a held file's saved entry stays its scope unless another entry of
// the title covers all its episodes). A held file with unknown episodes is matched by its hash alone, as stored. Within the scope, zero-rank tombstones are ignored where another entry lists the hash actively. A hashless
// file matched by record id whose record now lists several hashes keeps its stored standing while one of them awaits
// retirement and gets none afterwards: the file's own hash is unknown, so a new hash is never claimed as held. A
// file without a title scope (and no own entry) is never given an entry.
//
// Rank across several in-scope entries (no own entry), the rows of the held-rank transition:
//
//	memberships          held rank               next
//	agree on r           <= r                    r
//	agree on r           > r                     r when every mapped membership is known, else kept
//	disagree, highest h  < h (even below all)    kept: disagreement never promotes
//	disagree, highest h  >= h                    h when every mapped membership is known, else kept
//
// Every row but a kept rank without a mapping takes the matched entries' alternatives, so a file kept below the
// curated ranks can still be searched up. Incoming evidence starts empty and is never promoted by
// disagreement; without a mapping matches must agree.
//
// Alternatives (curated upgrades it could be searched for) come from the matched or own entry; a file without an
// in-scope match takes the entries covering it, whether or not its hash is listed elsewhere: every mapped entry of
// a movie, the entries covering all of a series file's episodes. A series file without known episodes has none.
func resolveStanding(state seaDexState, titleID int64, f *ExistingFile, how seaDexResolve) ExistingFile {
	x := *f
	x.SeaDexAlternatives = nil
	if state.off {
		return x // a prefer-off version's files keep the standing they were loaded with
	}
	own := x.SeaDexEntry
	mapped := state.titles[titleID]
	covers := func(entry int) bool {
		c := state.cover[titleID][entry]
		return c != nil && c.CoversAll(x.seaDexEpisodes)
	}
	// Known episodes need coverage evidence, mapped or not; a series import always does.
	scoped := how == resolveImport && state.series[titleID] || how != resolveRelease && len(x.seaDexEpisodes) > 0
	switch {
	case !scoped || own == 0 || covers(own):
	case how == resolveImport || slices.ContainsFunc(mapped, covers):
		own = 0 // an explicit entry, or a saved one another entry contradicts, only breaks ties between covering entries
		x.SeaDexEntry = 0
	default:
		scoped = false // a held file's saved entry stays its scope unless another entry covers all its episodes
	}
	inScope := func(entry int) bool {
		switch {
		case scoped && !covers(entry):
			return false
		case own != 0:
			return entry == own
		}
		return len(mapped) == 0 || slices.Contains(mapped, entry)
	}
	var all []standingMatch
	byRecord := x.InfoHash == ""
	if byRecord {
		all = state.records[indexer.SeaDexRecordID(x.SceneName)]
	} else {
		all = state.hashes[indexer.NormalizeHash(x.InfoHash)]
	}
	var matches []standingMatch
	for _, m := range all {
		if inScope(m.entry) {
			matches = append(matches, m)
		}
	}
	// complete: every entry mapped to the title has a record. Only
	// then is the file's standing known, so only then does it take curated alternatives (unknown is not
	// curator-wanted), judged over all the title's entries whatever the file's own entry.
	complete := len(mapped) > 0
	for _, entry := range mapped {
		complete = complete && state.entries[entry]
	}
	if len(matches) == 0 {
		if complete {
			x.SeaDexAlternatives = state.coveringAlternatives(titleID, own, &x)
		}
		if len(all) == 0 && (state.entries[own] || own == 0 && complete) {
			x.SeaDexPreference, x.SeaDex = 0, format.SeaDexNone
		}
		return x // listed only outside this file's scope: no evidence either way
	}
	if byRecord {
		hashes := map[string]bool{}
		pending := false
		for _, m := range matches {
			hashes[m.torrent.InfoHash] = true
			pending = pending || m.torrent.Pending()
		}
		if len(hashes) > 1 {
			if pending {
				return x // the record changed hash; keep the held standing until the omission is confirmed
			}
			x.SeaDexPreference, x.SeaDex = 0, format.SeaDexNone
			return x
		}
	}
	active := false
	for _, m := range matches {
		active = active || m.torrent.Preference > 0
	}
	var entries []int
	pref, best, agree := -1, false, true
	for _, m := range matches {
		if active && m.torrent.Preference == 0 {
			continue
		}
		if !slices.Contains(entries, m.entry) {
			entries = append(entries, m.entry)
		}
		if pref >= 0 && (m.torrent.Preference != pref || m.torrent.IsBest != best) {
			agree = false
		}
		pref, best = max(pref, m.torrent.Preference), best || m.torrent.IsBest
	}
	kept := false // the held rank and label stay
	if held := x.SeaDexPreference; own == 0 {
		switch {
		case !agree && (how != resolveHeld || len(mapped) == 0):
			return x
		case held > pref && len(mapped) > 0:
			kept = !complete
		case !agree:
			kept = held < pref || !complete
		}
	}
	if !kept {
		x.SeaDexPreference = pref
		x.SeaDex = format.SeaDexNone
		if pref > 0 {
			x.SeaDex = format.SeaDexAlt
			if best {
				x.SeaDex = format.SeaDexBest
			}
		}
	}
	if len(entries) == 1 && !kept && (own != 0 || titleID != 0) {
		x.SeaDexEntry = entries[0] // a kept rank names no entry: the next resolution would take that entry's rank
	}
	if complete && (titleID != 0 || own != 0) {
		for _, e := range entries {
			x.SeaDexAlternatives = append(x.SeaDexAlternatives, state.alternatives(titleID, e, &x)...)
		}
	}
	return x
}

// coveringAlternatives are the curated torrents an unmatched file could be upgraded to: its own entry's, else those
// of the title's entries that cover it.
func (state seaDexState) coveringAlternatives(titleID int64, own int, x *ExistingFile) []indexer.SeaDexStanding {
	if own != 0 {
		return state.alternatives(titleID, own, x)
	}
	if titleID == 0 {
		return nil
	}
	var out []indexer.SeaDexStanding
	for _, entry := range state.titles[titleID] {
		if state.series[titleID] {
			m := state.cover[titleID][entry]
			if m == nil || len(x.seaDexEpisodes) == 0 || !m.CoversAll(x.seaDexEpisodes) {
				continue
			}
		}
		out = append(out, state.alternatives(titleID, entry, x)...)
	}
	return out
}

// alternatives requires coverage independently of the torrent identity used for held standing.
func (state seaDexState) alternatives(titleID int64, entry int, x *ExistingFile) []indexer.SeaDexStanding {
	c := state.cover[titleID][entry]
	if c == nil || !c.CoversAll(x.seaDexEpisodes) {
		return nil
	}
	out := append([]indexer.SeaDexStanding(nil), state.byEntry[entry]...)
	for i := range out {
		out[i].Entry = entry
	}
	return out
}

// ResolveSeaDexFiles resolves a deduplicated set of held files against the operation's view, configured for a
// prefer-on version (WithSeaDexPolicy). Anything else leaves the files exactly as stored: prefer-off versions and
// unconfigured loads never take standing. Unavailable upstream metadata never clears a previous snapshot.
func ResolveSeaDexFiles(ctx context.Context, db *sql.DB, files []*ExistingFile) error {
	state := seaDexView(ctx)
	if !state.configured {
		return state.err
	}
	seen := map[*ExistingFile]bool{}
	for _, f := range files {
		if f != nil && !seen[f] {
			seen[f] = true
			*f = resolveSeaDex(ctx, f)
		}
	}
	return nil
}

// FileSeaDexPreference is the verified/current native standing used by router upgrade checks.
func FileSeaDexPreference(f *ExistingFile) int {
	if f == nil {
		return 0
	}
	return fileSeaDexPreference(f, format.Radarr)
}

// WithStoredSeaDex pins the stored entry records for an operation. Nothing is read until a prefer-on version first
// needs standing; from then on every version and goroutine of the operation shares that one read.
func WithStoredSeaDex(ctx context.Context, db *sql.DB) (context.Context, error) {
	if seaDexView(ctx).stored {
		return ctx, nil
	}
	op := &seaDexOp{db: db, views: map[*policy.VersionPolicy]seaDexState{}}
	return context.WithValue(ctx, seaDexContext{}, seaDexState{op: op, stored: true}), nil
}

// WithSeaDexOverlay is WithStoredSeaDex for an operation whose fresh SeaDex answers are not stored (the decide
// CLI): each answer is applied to its entry's stored record in memory exactly as recording it would have
// (indexer.ApplySeaDexObservation: an omission starts a retirement, it does not retire), and the catalog mappings
// still come from the database. Without a database it is WithSeaDex.
func WithSeaDexOverlay(ctx context.Context, db *sql.DB, snapshots []indexer.SeaDexSnapshot) context.Context {
	if db == nil {
		return WithSeaDex(ctx, snapshots)
	}
	op := &seaDexOp{db: db, fresh: slices.Clone(snapshots), overlay: true, views: map[*policy.VersionPolicy]seaDexState{}}
	return context.WithValue(ctx, seaDexContext{}, seaDexState{op: op, stored: true})
}

// load reads the operation's records and mappings once. Shared by the whole operation: one caller giving up must
// not fail every later decision.
func (op *seaDexOp) load(ctx context.Context) seaDexState {
	op.once.Do(func() {
		if op.db != nil {
			op.base, op.base.err = loadStanding(context.WithoutCancel(ctx), op.db, op.fresh)
		}
	})
	return op.base
}

// loadStanding reads the stored records and the catalog mappings in one read transaction, so an observation
// committed meanwhile is seen by both or neither. A row that does not decode as a record is skipped and logged;
// mappings are built from the decoded rows only.
func loadStanding(ctx context.Context, db *sql.DB, fresh []indexer.SeaDexSnapshot) (seaDexState, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return seaDexState{}, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT key, body FROM metadata_cache WHERE source='seadex-standing' AND status=200`)
	if err != nil {
		return seaDexState{}, err
	}
	defer rows.Close()
	var state seaDexState
	for rows.Next() {
		var key string
		var body []byte
		if err := rows.Scan(&key, &body); err != nil {
			return seaDexState{}, err
		}
		var s indexer.SeaDexSnapshot
		if err := json.Unmarshal(body, &s); err != nil {
			slog.Warn("invalid SeaDex standing snapshot skipped", "key", key, "err", err)
			continue
		}
		if s.AniList <= 0 || strconv.Itoa(s.AniList) != key {
			slog.Warn("invalid SeaDex standing entry skipped", "key", key, "anilist", s.AniList)
			continue
		}
		state.snapshots = append(state.snapshots, s)
	}
	if err := rows.Err(); err != nil {
		return seaDexState{}, err
	}
	rows.Close()
	state.snapshots = overlaySeaDex(state.snapshots, fresh, time.Now())
	catalog, err := indexer.LoadSeaDexCatalog(ctx, tx, state.snapshots)
	if err != nil {
		return seaDexState{}, err
	}
	if state.cache, err = liveCacheEvidence(ctx, tx, state.snapshots, time.Now()); err != nil {
		return seaDexState{}, err
	}
	state.titles = map[int64][]int{}
	state.cover = map[int64]map[int]*indexer.SeaDexEntryMapping{}
	state.series = map[int64]bool{}
	for title, ms := range catalog.Titles() {
		state.cover[title] = map[int]*indexer.SeaDexEntryMapping{}
		for _, m := range ms {
			state.titles[title] = append(state.titles[title], m.Entry)
			state.cover[title][m.Entry] = m
		}
		state.series[title] = catalog.Series(title)
	}
	return state, nil
}

// liveCacheEvidence reads the live provider_cache miss and uncached rows of the standing's hashes (curator eligibility,
// curatedUpgradeAvailable): local evidence only, nothing is asked.
func liveCacheEvidence(ctx context.Context, tx *sql.Tx, snapshots []indexer.SeaDexSnapshot, now time.Time) (map[string]map[string]string, error) {
	hashes := map[string]bool{}
	for _, s := range snapshots {
		for _, t := range s.Torrents {
			if h := indexer.NormalizeHash(t.InfoHash); h != "" && t.Preference > 0 {
				hashes[h] = true
			}
		}
	}
	out := map[string]map[string]string{}
	if len(hashes) == 0 {
		return out, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT info_hash, provider, state FROM provider_cache
		WHERE state IN ('miss', 'uncached') AND (expires_at IS NULL OR expires_at > ?)`, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hash, provider, state string
		if err := rows.Scan(&hash, &provider, &state); err != nil {
			return nil, err
		}
		if hash = indexer.NormalizeHash(hash); hashes[hash] {
			if out[hash] == nil {
				out[hash] = map[string]string{}
			}
			out[hash][provider] = state
		}
	}
	return out, rows.Err()
}

// overlaySeaDex lays the answers of over on base by entry: an entry base holds takes the stored transition of the
// answer (indexer.ApplySeaDexObservation), a new entry takes a complete answer as it is; partial answers for entries
// base does not hold add nothing.
func overlaySeaDex(base, over []indexer.SeaDexSnapshot, now time.Time) []indexer.SeaDexSnapshot {
	merged := slices.Clone(base)
	for _, s := range over {
		if s.AniList <= 0 {
			continue
		}
		i := slices.IndexFunc(merged, func(m indexer.SeaDexSnapshot) bool { return m.AniList == s.AniList })
		switch {
		case i >= 0:
			if next, ok := indexer.ApplySeaDexObservation(&merged[i], s, now); ok {
				merged[i] = next
			}
		case !s.Partial:
			merged = append(merged, s)
		}
	}
	return merged
}

// SeaDexError is the error loading the operation's pinned standing, if any.
func SeaDexError(ctx context.Context) error {
	return seaDexView(ctx).err
}

// RefreshSeaDexRelease updates queued/held releases from the same view as their current-file checks.
func RefreshSeaDexRelease(ctx context.Context, r indexer.Release) indexer.Release {
	// A release's identity comes only from its hash or magnet, never its title. A release naming two torrents has
	// no identity and takes no standing (the router rejects it).
	h, conflict := indexer.ReleaseIdentity(r)
	r.Flags = slices.DeleteFunc(slices.Clone(r.Flags), func(f string) bool {
		return f == indexer.FlagSeaDex || f == indexer.FlagSeaDexBest || f == indexer.FlagSeaDexPreferred
	})
	if conflict {
		return r
	}
	r.InfoHash = h
	f := LinkedFile("movie", "", Release{Release: r}, "Unknown")
	f.SceneName = ""
	f.SeaDexPreference, f.SeaDex = 0, format.SeaDexNone
	state := seaDexView(ctx)
	titleID, _ := ctx.Value(seaDexTitleContext{}).(int64)
	x := resolveStanding(state, titleID, f, resolveRelease)
	if x.SeaDexPreference > 0 {
		r.Flags = append(r.Flags, indexer.FlagSeaDex)
	}
	if x.SeaDex >= format.SeaDexBest {
		r.Flags = append(r.Flags, indexer.FlagSeaDexBest)
	}
	if x.SeaDexPreference >= 3 {
		r.Flags = append(r.Flags, indexer.FlagSeaDexPreferred)
	}
	if x.SeaDexEntry > 0 {
		extra := make(map[string]string, len(r.Extras)+1)
		for k, v := range r.Extras {
			extra[k] = v
		}
		extra["anilist"] = strconv.Itoa(x.SeaDexEntry)
		r.Extras = extra
	}
	if x.InfoHash != "" {
		r.InfoHash = x.InfoHash
	}
	return r
}

// WithSeaDexPolicy gives the version its view of the operation's standing: current settings applied to the pinned
// upstream facts, built once per version policy. A prefer-off version gets an empty view without anything being
// read: nothing it decides or loads takes standing. A version that lists no SeaDex indexer is scored with the
// configured SeaDex indexers' scoring (the standing came from them), and only without any with the defaults.
func WithSeaDexPolicy(ctx context.Context, pol *policy.Policy, vp *policy.VersionPolicy) context.Context {
	state := seaDexView(ctx)
	if vp == nil || !vp.PreferSeaDex {
		return context.WithValue(ctx, seaDexContext{}, seaDexState{op: state.op, stored: true, off: true})
	}
	op := state.op
	if op == nil {
		return ctx
	}
	base := op.load(ctx)
	if base.err != nil {
		return context.WithValue(ctx, seaDexContext{}, seaDexState{op: op, stored: true, err: base.err})
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if view, ok := op.views[vp]; ok {
		return context.WithValue(ctx, seaDexContext{}, view)
	}
	scorings := seaDexScorings(pol, vp)
	view := seaDexState{op: op, stored: state.stored, configured: true, titles: base.titles, cover: base.cover, series: base.series,
		cache: base.cache}
	view.snapshots = make([]indexer.SeaDexSnapshot, len(base.snapshots))
	for i, s := range base.snapshots {
		configured := indexer.ConfigureSeaDex(s, scorings[0])
		for _, sc := range scorings[1:] {
			other := indexer.ConfigureSeaDex(s, sc)
			for j := range configured.Torrents {
				configured.Torrents[j].Preference = max(configured.Torrents[j].Preference, other.Torrents[j].Preference)
			}
		}
		view.snapshots[i] = configured
	}
	view = indexStanding(view)
	op.views[vp] = view
	return context.WithValue(ctx, seaDexContext{}, view)
}

func seaDexScorings(pol *policy.Policy, vp *policy.VersionPolicy) []*policy.SeaDexScoring {
	var scorings []*policy.SeaDexScoring
	if pol != nil {
		for _, key := range vp.Indexers {
			if ix := pol.Indexers[key]; ix != nil && ix.Kind == "seadex" {
				scorings = append(scorings, ix.SeaDex)
			}
		}
		if len(scorings) == 0 {
			keys := make([]string, 0, len(pol.Indexers))
			for key, ix := range pol.Indexers {
				if ix != nil && ix.Kind == "seadex" {
					keys = append(keys, key)
				}
			}
			slices.Sort(keys)
			for _, key := range keys {
				scorings = append(scorings, pol.Indexers[key].SeaDex)
			}
		}
	}
	if len(scorings) == 0 {
		scorings = append(scorings, nil)
	}
	return scorings
}

func WithSeaDexTitle(ctx context.Context, titleID int64) context.Context {
	return context.WithValue(ctx, seaDexTitleContext{}, titleID)
}

// seaDexEpisodesOf is the (season, number) of each of the title's episodes ids names.
func seaDexEpisodesOf(title *Title, ids []int64) [][2]int {
	var out [][2]int
	if title != nil {
		for _, ep := range title.Episodes {
			if slices.Contains(ids, ep.ID) {
				out = append(out, [2]int{ep.Season, ep.Number})
			}
		}
	}
	return out
}

// ResolveSeaDexFileEpisodes scopes a held (including adopted) file to its verified episode ids.
func ResolveSeaDexFileEpisodes(ctx context.Context, title *Title, f *ExistingFile, ids []int64) {
	f.seaDexEpisodes = seaDexEpisodesOf(title, ids)
	*f = resolveSeaDex(ctx, f)
}

// SeaDexEntryPartlyCovers says whether entry, mapped to the operation's title, covers some but not all of the
// title's episodes ids, for a file of two or more episodes in a prefer-on view. Without coverage data it is false.
func SeaDexEntryPartlyCovers(ctx context.Context, title *Title, entry int, ids []int64) bool {
	state := seaDexView(ctx)
	if !state.configured || entry <= 0 || len(ids) < 2 {
		return false
	}
	titleID, _ := ctx.Value(seaDexTitleContext{}).(int64)
	m := state.cover[titleID][entry]
	eps := seaDexEpisodesOf(title, ids)
	return m != nil && len(eps) >= 2 && m.Covers(eps) && !m.CoversAll(eps)
}

// ResolveIncomingSeaDexFile resolves fresh incoming evidence for one verified file, never held rank: a series file takes standing only from entries covering all its verified episodes, where an explicit
// release entry only breaks ties. A file without a usable identity (no hash, or no single identity) takes no standing but still the
// alternatives of the entries covering it. Its episode scope and alternatives also drive the import-time cutoff
// state.
func ResolveIncomingSeaDexFile(ctx context.Context, title *Title, f *ExistingFile, ids []int64) {
	state := seaDexView(ctx)
	if state.off {
		return
	}
	f.seaDexEpisodes = seaDexEpisodesOf(title, ids)
	f.SeaDexPreference, f.SeaDex = 0, format.SeaDexNone
	f.SeaDexAlternatives = nil
	titleID, _ := ctx.Value(seaDexTitleContext{}).(int64)
	if f.seaDexNoIdentity || indexer.NormalizeHash(f.InfoHash) == "" {
		f.SeaDexEntry = 0
		f.SeaDexAlternatives = state.coveringAlternatives(titleID, 0, f)
		return
	}
	scene := f.SceneName
	f.SceneName = "" // incoming identity comes from the verified hash, never a filename
	x := resolveStanding(state, titleID, f, resolveImport)
	x.SceneName = scene
	*f = x
}
