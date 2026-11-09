package indexer

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// SeaDexRetirementDelay is the minimum separation between complete omission observations.
const SeaDexRetirementDelay = 12 * time.Hour

// SeaDexRecordVersion is the version of the per-entry standing record this binary writes. A version 1 row (no
// version) is read as it is and converted by upgradeRecord wherever a record is ranked or observed; the first
// stored observation of the entry writes it back as this version.
const SeaDexRecordVersion = 2

// SeaDexSnapshot is one AniList entry's standing record, and also an observation of it (an adapter answer, not
// stored). An empty set is meaningful: a successful refresh withdrew its previous recommendations.
//
// Members (Torrents) are listed (rank 1, or 2 for upstream best; the configured winner is derived under the
// reader's policy, ConfigureSeaDex), pending retirement (MissingSince), retired tombstones (rank 0), or legacy
// (listed without upstream facts: stored before facts were kept; ConfigureSeaDex reads them as tombstones until the
// next observation of the entry). Targets are the entry's resolver mappings.
type SeaDexSnapshot struct {
	Version  int              `json:"version,omitempty"`
	Targets  []AniListTarget  `json:"targets,omitempty"`
	AniList  int              `json:"anilist"`
	Torrents []SeaDexStanding `json:"torrents"`
	// Sets are the immutable comparison sets of torrents pending retirement, keyed by their first complete omission
	// time (MissingSince): the torrents the record listed before that omission, with their facts and sizes then.
	// Every torrent first omitted by the same answer shares one set.
	Sets map[int64][]SeaDexSetMember `json:"sets,omitempty"`
	// Partial marks presence evidence from an answer that is not a complete entry set: its fully identified
	// torrents clear a pending retirement, but nothing is added, re-ranked or marked missing from it. Never stored.
	Partial bool `json:"-"`
}

type SeaDexStanding struct {
	Entry int `json:"-"` // operation-local source entry for candidate filename mapping
	// Cache is operation-local live cache evidence for the torrent by provider ("miss" or "uncached"), read with the
	// standing (decide loadStanding) for curator eligibility. Never stored.
	Cache        map[string]string `json:"-"`
	Quality      string            `json:"quality,omitempty"`
	Facts        *sdTorrent        `json:"facts,omitempty"`
	Size         int64             `json:"size,omitempty"`
	MissingSince int64             `json:"missing_since,omitempty"`
	// Cohort and CohortFacts are read from version 1 rows only (a pending torrent's set as hashes, or with facts);
	// upgradeRecord turns them into the record's Sets and clears them, so they are never written.
	Cohort      []string          `json:"cohort,omitempty"`
	CohortFacts []SeaDexSetMember `json:"cohort_facts,omitempty"`
	IsBest      bool              `json:"is_best"`
	RecordID    string            `json:"record_id"`
	InfoHash    string            `json:"info_hash"`
	Preference  int               `json:"preference"`
}

// SeaDexSetMember is one torrent of a comparison set, with the facts (without file lists) and size it had then.
type SeaDexSetMember struct {
	InfoHash string     `json:"info_hash"`
	Facts    *sdTorrent `json:"facts"`
	Size     int64      `json:"size,omitempty"`
}

// Pending says SeaDex stopped listing the torrent and its retirement awaits confirmation.
func (t *SeaDexStanding) Pending() bool { return t.MissingSince > 0 && t.Preference > 0 }

// upgradeRecord converts a record to SeaDexRecordVersion; it is idempotent. A pending torrent without a comparison
// set gets the one it is ranked within now: its version 1 cohort facts, else its cohort's facts as the row holds
// them, else (pending since before cohorts were kept) the torrents the row lists. The next stored observation would
// have captured exactly these, so conversion changes no rank.
func upgradeRecord(s SeaDexSnapshot) SeaDexSnapshot {
	if s.Version == SeaDexRecordVersion {
		return s
	}
	s.Torrents = slices.Clone(s.Torrents)
	s.Sets = maps.Clone(s.Sets)
	for i := range s.Torrents {
		t := &s.Torrents[i]
		if _, ok := s.Sets[t.MissingSince]; t.Pending() && t.Facts != nil && !ok {
			set := t.CohortFacts
			for _, o := range s.Torrents {
				if len(t.CohortFacts) == 0 && o.Facts != nil && (len(t.Cohort) > 0 && slices.Contains(t.Cohort, o.InfoHash) ||
					len(t.Cohort) == 0 && o.MissingSince == 0 && o.Preference > 0) {
					set = append(set, setMember(o))
				}
			}
			s.Sets = addSet(s.Sets, t.MissingSince, set)
		}
		t.Cohort, t.CohortFacts = nil, nil
	}
	s.Version = SeaDexRecordVersion
	return s
}

// addSet merges members into the comparison set at key (one set per first omission; the first facts of a hash win).
func addSet(sets map[int64][]SeaDexSetMember, key int64, members []SeaDexSetMember) map[int64][]SeaDexSetMember {
	if sets == nil {
		sets = map[int64][]SeaDexSetMember{}
	}
	set := slices.Clone(sets[key])
	for _, m := range members {
		if !slices.ContainsFunc(set, func(o SeaDexSetMember) bool { return o.InfoHash == m.InfoHash }) {
			set = append(set, m)
		}
	}
	sets[key] = set
	return sets
}

func setMember(t SeaDexStanding) SeaDexSetMember {
	f := *t.Facts
	f.Files = nil
	f.Tags = slices.Clone(f.Tags)
	return SeaDexSetMember{InfoHash: t.InfoHash, Facts: &f, Size: t.Size}
}

// SeaDexRecordID recognizes only the bridge/native adapter's synthetic record suffix, never generic tracker flags.
func SeaDexRecordID(title string) string {
	title = strings.TrimSpace(title)
	for _, ext := range []string{".mkv", ".mp4", ".torrent"} {
		title = strings.TrimSuffix(title, ext)
	}
	i := strings.LastIndex(title, " (")
	if i < 0 || !strings.HasSuffix(title, ")") {
		return ""
	}
	id := title[i+2 : len(title)-1]
	if len(id) != 15 {
		return ""
	} // PocketBase record ids
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return ""
		}
	}
	return id
}

// observeSeaDex commits an answer while the caller holds the observation lock from before its fetch.
// Only mapped or held-referenced entries are stored, and unchanged rows are not rewritten.
func (e *Evidence) observeSeaDex(ctx context.Context, snapshots []SeaDexSnapshot) (err error) {
	if e == nil || e.st == nil || len(snapshots) == 0 {
		return nil
	}
	// The catalog (and, only when an entry is unmapped, the held references) is read once per batch, before the write
	// transaction, so no mapping scan holds the write lock.
	db := e.st.Reader()
	catalog, err := LoadSeaDexCatalog(ctx, db, nil)
	if err != nil {
		e.log.Warn("SeaDex standing not stored", "err", err)
		return err
	}
	var held *SeaDexHeldRefs
	for _, s := range snapshots {
		if s.AniList > 0 && !catalog.Mapped(s) {
			h, err := LoadSeaDexHeldRefs(ctx, db)
			if err != nil {
				e.log.Warn("SeaDex standing not stored", "err", err)
				return err
			}
			held = &h
			break
		}
	}
	err = e.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, s := range snapshots {
			if s.AniList <= 0 {
				continue
			}
			var oldBody []byte
			err := tx.QueryRowContext(ctx, `SELECT body FROM metadata_cache WHERE source='seadex-standing' AND key=?`, strconv.Itoa(s.AniList)).Scan(&oldBody)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			var old SeaDexSnapshot
			var prev *SeaDexSnapshot
			if len(oldBody) > 0 && json.Unmarshal(oldBody, &old) == nil {
				prev = &old
			}
			next, ok := ApplySeaDexObservation(prev, s, e.now())
			if !ok {
				continue
			}
			if !catalog.Mapped(next) && (held == nil || !held.References(next) && (prev == nil || !held.References(*prev))) {
				continue
			}
			body, err := json.Marshal(next)
			if err != nil {
				return err
			}
			if bytes.Equal(body, oldBody) {
				continue
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at)
    VALUES ('seadex-standing',?,200,?,?,?) ON CONFLICT(source,key) DO UPDATE SET
    status=excluded.status,body=excluded.body,fetched_at=excluded.fetched_at,expires_at=excluded.expires_at`,
				strconv.Itoa(next.AniList), body, e.now().UnixMilli(), time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC).UnixMilli())
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		e.log.Warn("SeaDex standing not stored", "err", err)
		return err
	}
	return nil
}

// prefersSeaDex says whether any version of the evidence's policy prefers SeaDex (its decisions read standing).
func (e *Evidence) prefersSeaDex() bool {
	if e == nil || e.pol == nil {
		return false
	}
	for _, v := range e.pol.Versions {
		if v != nil && v.PreferSeaDex {
			return true
		}
	}
	return false
}

// ApplySeaDexObservation is the one standing transition of an entry's record, for the stored rows and for
// operations that lay fresh answers over them without storing (the decide CLI). Per member, by kind of answer:
//
//	answer           member                  next
//	complete, lists  any                     listed with the answer's facts (pending cleared, tombstone revived)
//	complete, omits  listed                  pending since now, ranked within the set listed before (Sets)
//	complete, omits  pending, < 12 h         unchanged: keeps its last confirmed rank
//	complete, omits  pending, >= 12 h        retired: zero-rank tombstone (SeaDexRetirementDelay)
//	complete, omits  tombstone               unchanged
//	partial, lists   pending                 listed again (pending cleared), facts unchanged
//	partial          anything else           unchanged
//
// Failed or malformed answers are no observation (the adapter returns none), and time alone never retires: only a
// second complete omission does. Without a record a partial answer changes nothing (ok false). A complete answer
// without targets (a search by AniList id alone) keeps the stored ones. Callers write nothing when the result
// encodes like the stored row.
func ApplySeaDexObservation(old *SeaDexSnapshot, obs SeaDexSnapshot, now time.Time) (SeaDexSnapshot, bool) {
	var prev SeaDexSnapshot
	if old != nil {
		prev = upgradeRecord(*old)
	}
	var next SeaDexSnapshot
	switch {
	case obs.Partial && old == nil:
		return SeaDexSnapshot{}, false
	case obs.Partial:
		present := listedHashes(obs)
		next = prev
		next.Torrents = slices.Clone(prev.Torrents)
		for i := range next.Torrents {
			if t := &next.Torrents[i]; t.Pending() && present[t.InfoHash] {
				t.MissingSince = 0
			}
		}
		next.Sets = liveSets(next)
	case old == nil:
		next = storedFacts(obs)
	default:
		next = confirmSeaDex(prev, storedFacts(obs), now)
	}
	next.Version = SeaDexRecordVersion
	slices.SortFunc(next.Torrents, func(a, b SeaDexStanding) int {
		if c := cmp.Compare(a.InfoHash, b.InfoHash); c != 0 {
			return c
		}
		return cmp.Compare(a.RecordID, b.RecordID)
	})
	next.Targets = slices.DeleteFunc(append([]AniListTarget(nil), next.Targets...), func(t AniListTarget) bool { return !t.valid() })
	slices.SortFunc(next.Targets, func(a, b AniListTarget) int {
		if c := cmp.Compare(a.TMDb, b.TMDb); c != 0 {
			return c
		}
		if c := cmp.Compare(a.TVDB, b.TVDB); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Season, b.Season); c != 0 {
			return c
		}
		if a.Exact != b.Exact {
			if a.Exact {
				return 1
			}
			return -1
		}
		return 0
	})
	next.Targets = slices.Compact(next.Targets)
	return next, true
}

// confirmSeaDex applies a complete answer (next, with stored facts) to the record prev: the complete rows of the
// transition table.
func confirmSeaDex(prev, next SeaDexSnapshot, now time.Time) SeaDexSnapshot {
	prev = upgradeRecord(prev)
	if !slices.ContainsFunc(next.Targets, AniListTarget.valid) {
		next.Targets = prev.Targets
	}
	present := listedHashes(next)
	next.Sets = maps.Clone(prev.Sets)
	var listed []SeaDexSetMember
	for _, t := range prev.Torrents {
		if t.MissingSince == 0 && t.Preference > 0 && t.Facts != nil {
			listed = append(listed, setMember(t))
		}
	}
	for _, t := range prev.Torrents {
		switch {
		case present[t.InfoHash]:
			continue
		case t.MissingSince == 0:
			t.MissingSince = now.UnixMilli()
			if t.Preference > 0 && t.Facts != nil {
				next.Sets = addSet(next.Sets, t.MissingSince, listed)
			}
		case now.Sub(time.UnixMilli(t.MissingSince)) >= SeaDexRetirementDelay:
			t.Preference, t.IsBest = 0, false
		}
		next.Torrents = append(next.Torrents, t)
	}
	next.Sets = liveSets(next)
	return next
}

func listedHashes(s SeaDexSnapshot) map[string]bool {
	present := map[string]bool{}
	for _, t := range s.Torrents {
		if h := NormalizeHash(t.InfoHash); h != "" {
			present[h] = true
		}
	}
	return present
}

// liveSets are the record's comparison sets a pending torrent with facts still refers to.
func liveSets(s SeaDexSnapshot) map[int64][]SeaDexSetMember {
	var live map[int64][]SeaDexSetMember
	for _, t := range s.Torrents {
		if set, ok := s.Sets[t.MissingSince]; ok && t.Pending() && t.Facts != nil {
			if live == nil {
				live = map[int64][]SeaDexSetMember{}
			}
			live[t.MissingSince] = set
		}
	}
	return live
}

// storedFacts keeps scoring facts and feature filenames/sizes for conservative episode-size eligibility; every
// torrent of a complete answer is listed (rank 1, 2 for upstream best).
func storedFacts(s SeaDexSnapshot) SeaDexSnapshot {
	s.Torrents = append([]SeaDexStanding(nil), s.Torrents...)
	s.Sets = nil
	for i := range s.Torrents {
		t := &s.Torrents[i]
		t.MissingSince, t.Cohort, t.CohortFacts = 0, nil, nil
		if t.Facts != nil {
			f := *t.Facts
			f.Files = slices.Clone(f.Files)
			var largest int64
			for _, file := range f.Files {
				largest = max(largest, file.Length)
			}
			kept := f.Files[:0]
			for _, file := range f.Files {
				if file.Length > 0 && file.Length >= largest/2 {
					kept = append(kept, file)
				}
			}
			f.Files = kept
			f.Tags = append([]string(nil), f.Tags...)
			t.Facts = &f
			t.Preference = 1
			if t.IsBest {
				t.Preference = 2
			}
		}
	}
	return s
}

// ConfigureSeaDex derives ranks from upstream facts under the caller's pinned scoring policy (3 for the configured
// winner, else 2 for upstream best, 1 for other listed torrents). Torrents pending retirement keep their standing
// until it is confirmed, but are ranked apart: the torrents SeaDex lists now are scored among themselves, and each
// pending one within its comparison set, so neither takes or loses rank through the other.
func ConfigureSeaDex(s SeaDexSnapshot, sc *policy.SeaDexScoring) SeaDexSnapshot {
	s = upgradeRecord(s)
	s.Torrents = append([]SeaDexStanding(nil), s.Torrents...)
	var ts []*sdTorrent
	var sizes []int64
	var indices, pending []int
	adapter := seadex{scoring: sc}
	for i := range s.Torrents {
		t := &s.Torrents[i]
		if t.Preference == 0 {
			continue
		}
		if t.Facts == nil {
			// A legacy membership (stored before upstream facts were kept) is not a current member: no protection,
			// preference or curated alternative. The next observation of the entry rewrites it.
			t.Preference, t.IsBest = 0, false
			continue
		}
		t.Preference = 0
		if adapter.excluded(t.Facts.Tags) {
			continue
		}
		t.Preference = 1
		if t.IsBest {
			t.Preference = 2
		}
		if t.MissingSince > 0 {
			pending = append(pending, i)
			continue
		}
		ts = append(ts, t.Facts)
		sizes = append(sizes, t.Size)
		indices = append(indices, i)
	}
	for i, best := range seadexPriorities(sc, ts, sizes) {
		if best {
			s.Torrents[indices[i]].Preference = 3
		}
	}
	for _, p := range pending {
		// The set it was last listed with, as it was then, under current settings and exclusions: neither a torrent
		// added since nor a change to a set member's facts moves its rank before its retirement is confirmed.
		t := &s.Torrents[p]
		var set []*sdTorrent
		var setSizes []int64
		for _, o := range s.Sets[t.MissingSince] {
			if o.InfoHash != t.InfoHash && o.Facts != nil && !adapter.excluded(o.Facts.Tags) {
				set = append(set, o.Facts)
				setSizes = append(setSizes, o.Size)
			}
		}
		set = append(set, t.Facts)
		setSizes = append(setSizes, t.Size)
		if won := seadexPriorities(sc, set, setSizes); won[len(won)-1] {
			t.Preference = 3
		}
	}
	return s
}

// FeatureNames returns the known feature filenames used to establish torrent coverage.
func (t SeaDexStanding) FeatureNames() []string {
	if t.Facts == nil {
		return nil
	}
	var largest int64
	for _, f := range t.Facts.Files {
		largest = max(largest, f.Length)
	}
	var names []string
	for _, f := range t.Facts.Files {
		if f.Length > 0 && f.Length >= largest/2 {
			names = append(names, f.Name)
		}
	}
	return names
}
