package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/ceiling"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Upgrade explanations (docs/design/manage.md). A version whose file is below its profile's cutoff is "below cutoff";
// it is "upgrade available" only when Theatarr has seen a release that would replace that file. The evidence is what Theatarr
// already stores: the journal's decided searches and RSS reads (journal_decision, with every kept candidate's
// accepted and upgrade flags). Nothing here searches.
//
// The rule, per item (a movie version's file, or one episode's file in a series version), for an item below cutoff:
//   - only evidence newer than the item's current file counts (an older decision judged an older file);
//   - the newest search after the file that covers the item stands for every earlier search: its accepted
//     upgrades count, and when it found none, older finds no longer count;
//   - RSS sightings (journal RSS reads) newer than both the file and that search add to it;
//   - a release counts when it was accepted and marked an upgrade over the file, and it maps to the item (a
//     candidate's episodes, or a search target's season and episode);
//   - the best counted release (highest score, then newest) is what Theatarr would take next, with the engine's reason.

// Cutoff is what a version's file is measured against: the profile that decides its state.
type Cutoff struct {
	// Quality is the cutoff quality (or group) name.
	Quality string `json:"quality,omitempty"`
	// Upgrades is false when the profile allows no upgrades at all.
	Upgrades bool `json:"upgrades"`
	// For callers who see files: the cutoff format score, the profile's name and where it comes from (an arr
	// instance that owns the version's state, or "policy").
	Score   *int64 `json:"score,omitempty"`
	Profile string `json:"profile,omitempty"`
	From    string `json:"from,omitempty"`
}

// UpgradeNext is the best release seen that would replace the file: what Theatarr would take next, and why.
type UpgradeNext struct {
	Release string `json:"release"`
	Indexer string `json:"indexer,omitempty"`
	Quality string `json:"quality,omitempty"`
	Score   int    `json:"score"`
	// Reason is the decision engine's upgrade reason ("better format score: 5000 over 1400").
	Reason string `json:"reason,omitempty"`
	// Source is search (Theatarr's search) or rss (Theatarr's RSS read).
	Source   string    `json:"source"`
	SeenAt   time.Time `json:"seen_at"`
	Episodes []string  `json:"episodes,omitempty"`
}

// UpgradeInfo explains a version's (or one episode's) state against its cutoff.
type UpgradeInfo struct {
	Ceiling         *ceiling.Info `json:"ceiling,omitempty"`
	EffectiveCutoff string        `json:"effective_cutoff,omitempty"`
	// State is none (no file), cutoff_met, below_cutoff (no better release seen) or upgrade_available.
	State   string  `json:"state"`
	Quality string  `json:"quality,omitempty"`
	Cutoff  *Cutoff `json:"cutoff,omitempty"`
	// Series: episodes below the cutoff, and how many of them have a better release seen.
	Below int `json:"below,omitempty"`
	Seen  int `json:"seen,omitempty"`
	// For callers who see files: the file's format score, the best release seen and the last search that covered
	// the item.
	Score      *int64       `json:"score,omitempty"`
	Next       *UpgradeNext `json:"next,omitempty"`
	LastSearch *time.Time   `json:"last_search,omitempty"`
}

// redactUpgrade keeps the state, the qualities and the counts.
func redactUpgrade(u *UpgradeInfo) {
	if u == nil {
		return
	}
	u.Score, u.Next, u.LastSearch = nil, nil, nil
	u.Ceiling = nil
	u.EffectiveCutoff = ""
	if u.State == "best_available" || u.State == "best_source_seen" || u.State == "higher_source_exists" {
		u.State = "below_cutoff"
	}
	if c := u.Cutoff; c != nil {
		*c = Cutoff{Quality: c.Quality, Upgrades: c.Upgrades}
	}
}

// evCand is a candidate as the stored decisions keep it (snapshot.Candidate's keys).
type evCand struct {
	Release    string   `json:"release"`
	Indexer    string   `json:"indexer"`
	Accepted   bool     `json:"accepted"`
	Quality    string   `json:"quality"`
	Score      int      `json:"score"`
	Upgrade    bool     `json:"upgrade"`
	UpgradeWhy string   `json:"upgrade_reason"`
	Episodes   []string `json:"episodes"`
}

// evRecord is one stored decision about a title's version.
type evRecord struct {
	id     int64
	at     int64
	search bool
	source string
	// target is the journal's search target: "" or movie (the whole title), sNN, sNNeMM, or rss.
	target string
	cands  []evCand
}

// evItem is one file below its cutoff: a movie version's (episodeID 0) or one episode's.
type evItem struct {
	episodeID int64
	season    int64
	number    int64
	fileAt    int64
}

func (it evItem) code() string { return episodeCode(it.season, it.number) }

// covers reports whether a record is about the item.
func (r evRecord) covers(it evItem) bool {
	if it.episodeID == 0 {
		return true
	}
	t := strings.ToLower(r.target)
	switch {
	case t == "" || t == "movie" || t == "rss":
		return true
	case len(t) == 3 && t[0] == 's':
		return t == strings.ToLower(it.code()[:3])
	}
	return t == strings.ToLower(it.code())
}

// candCovers reports whether a candidate maps to the item: a movie, a candidate with no episodes listed (the
// record's scope decides), or one whose episodes include it.
func candCovers(c evCand, it evItem) bool {
	if it.episodeID == 0 || len(c.Episodes) == 0 {
		return true
	}
	code := it.code()
	for _, e := range c.Episodes {
		if strings.EqualFold(e, code) {
			return true
		}
	}
	return false
}

// upgradeEvidencePool selects records using only their scope and time. Both hydration and judging use this rule,
// including first-on-tie search selection and LastSearch independent of the current file's time.
func upgradeEvidencePool(it evItem, recs []evRecord) ([]*evRecord, int64) {
	var newest *evRecord
	var lastSearch int64
	for i := range recs {
		r := &recs[i]
		if !r.search || !r.covers(it) {
			continue
		}
		lastSearch = max(lastSearch, r.at)
		if r.at > it.fileAt && (newest == nil || r.at > newest.at) {
			newest = r
		}
	}
	floor := it.fileAt
	var pool []*evRecord
	if newest != nil {
		floor = max(floor, newest.at)
		pool = append(pool, newest)
	}
	for i := range recs {
		r := &recs[i]
		if !r.search && r.at > floor && r.covers(it) {
			pool = append(pool, r)
		}
	}
	return pool, lastSearch
}

// judgeUpgrade applies the rule above to one item. It returns the best release seen (nil when none) and the time of
// the newest search that covered the item (0 when none).
func judgeUpgrade(it evItem, recs []evRecord) (*UpgradeNext, int64) {
	pool, lastSearch := upgradeEvidencePool(it, recs)
	var best *UpgradeNext
	for _, r := range pool {
		for _, c := range r.cands {
			if !c.Accepted || !c.Upgrade || !candCovers(c, it) {
				continue
			}
			if best == nil || c.Score > best.Score || (c.Score == best.Score && r.at > best.SeenAt.UnixMilli()) {
				best = &UpgradeNext{Release: c.Release, Indexer: c.Indexer, Quality: c.Quality, Score: c.Score,
					Reason: c.UpgradeWhy, Source: r.source, SeenAt: ts(r.at), Episodes: c.Episodes}
			}
		}
	}
	return best, lastSearch
}

type titleKey struct {
	titleID int64
	key     string
}

// loadEvidence reads the scope and time of every stored decision, then fetches candidate blobs only for the
// latest covering searches and subsequent RSS evidence needed by the requested current files. Keep all search
// metadata for LastSearch; an empty newer search still invalidates earlier discoveries. There is no history cap.
func loadEvidence(ctx context.Context, db *sql.DB, titleIDs []int64, items map[titleKey][]evItem) (map[titleKey][]evRecord, error) {
	out := map[titleKey][]evRecord{}
	if len(items) == 0 {
		return out, nil
	}
	// One JSON list instead of bound parameters: a caller may ask for every title at once (Home's upgrades row),
	// and each of these reads is a single pass over its table however many titles are asked for.
	list, _ := json.Marshal(titleIDs)
	ids := []any{string(list)}
	in := `SELECT value FROM json_each(?)`
	err := scanAll(ctx, db, `SELECT id, title_id, version_key, created_at, target FROM journal_decision
		WHERE title_id IN (`+in+`)`, ids, func(r *sql.Rows) error {
		var k titleKey
		var rec evRecord
		if err := r.Scan(&rec.id, &k.titleID, &k.key, &rec.at, &rec.target); err != nil {
			return err
		}
		if len(items[k]) == 0 {
			return nil
		}
		rec.search = rec.target != "rss"
		rec.source = "search"
		if !rec.search {
			rec.source = "rss"
		}
		out[k] = append(out[k], rec)
		return nil
	})
	if err != nil && !strings.Contains(err.Error(), "no such table") {
		return nil, err
	}
	journal := map[int64]*evRecord{}
	for k, recs := range out {
		for _, it := range items[k] {
			pool, _ := upgradeEvidencePool(it, recs)
			for _, r := range pool {
				journal[r.id] = r
			}
		}
	}
	if len(journal) > 0 {
		want := make([]int64, 0, len(journal))
		for id := range journal {
			want = append(want, id)
		}
		b, _ := json.Marshal(want)
		err := scanAll(ctx, db, `SELECT id, candidates FROM journal_decision
			WHERE id IN (SELECT value FROM json_each(?))`, []any{string(b)}, func(rows *sql.Rows) error {
			var id int64
			var candidates string
			if err := rows.Scan(&id, &candidates); err != nil {
				return err
			}
			_ = json.Unmarshal([]byte(candidates), &journal[id].cands)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// itemFile is one current file of a version: the movie's (episode 0) or one episode's.
type itemFile struct {
	// key identifies the file: a link's id, or minus an arr file's id. A multi-episode file appears once per episode.
	key     int64
	quality string
	score   *int64
	size    int64
	added   int64
}

// loadItemFiles reads the current files of versions, per episode (0 for a movie): arr files of the owning
// instance, then active links, which win.
func loadItemFiles(ctx context.Context, db *sql.DB, versionIDs []any) (map[int64]map[int64]itemFile, error) {
	out := map[int64]map[int64]itemFile{}
	if len(versionIDs) == 0 {
		return out, nil
	}
	in := store.Placeholders(len(versionIDs))
	set := func(vid, eid int64, f itemFile, win bool) {
		m := out[vid]
		if m == nil {
			m = map[int64]itemFile{}
			out[vid] = m
		}
		if old, ok := m[eid]; ok && !win && old.added >= f.added {
			return
		}
		m[eid] = f
	}
	err := scanAll(ctx, db, `SELECT -vf.id, vf.version_id, COALESCE(fe.episode_id, 0), vf.quality, vf.custom_format_score,
		vf.size_bytes, COALESCE(vf.added_at, 0) FROM version_file vf LEFT JOIN version_file_episode fe ON fe.version_file_id = vf.id
		LEFT JOIN version_source vs ON vs.version_id = vf.version_id WHERE vf.version_id IN (`+in+`) AND `+curFile,
		versionIDs, func(r *sql.Rows) error {
			var vid, eid int64
			var f itemFile
			var score sql.NullInt64
			if err := r.Scan(&f.key, &vid, &eid, &f.quality, &score, &f.size, &f.added); err != nil {
				return err
			}
			f.score = nullInt(score)
			set(vid, eid, f, false)
			return nil
		})
	if err != nil {
		return nil, err
	}
	err = scanAll(ctx, db, `SELECT l.id, lv.version_id, COALESCE(le.episode_id, 0), COALESCE(l.quality, ''), l.score,
		COALESCE(`+linkSizeSQL+`, 0), l.created_at FROM link l JOIN link_version lv ON lv.link_id = l.id
		LEFT JOIN link_episode le ON le.link_id = l.id LEFT JOIN content_file cf ON cf.id = l.content_file_id
		WHERE lv.version_id IN (`+in+`) AND l.state = 'active'`, versionIDs, func(r *sql.Rows) error {
		var vid, eid int64
		var f itemFile
		var score sql.NullInt64
		if err := r.Scan(&f.key, &vid, &eid, &f.quality, &score, &f.size, &f.added); err != nil {
			return err
		}
		f.score = nullInt(score)
		set(vid, eid, f, true)
		return nil
	})
	return out, err
}

// upgradeVersion is what the upgrade explanation needs of one version.
type upgradeVersion struct {
	id      int64
	titleID int64
	key     string
	kind    string
	state   string
}

// upgradeEpisode is one episode's state in a series version.
type upgradeEpisode struct {
	id, season, number int64
	state              string
}

// upgradeSet explains versions: their items below cutoff and the evidence about them.
type upgradeSet struct {
	ceilings map[ceilingKey]ceilingView
	profiles map[int64]*profile.Profile
	files    map[int64]map[int64]itemFile
	episodes map[int64][]upgradeEpisode
	evidence map[titleKey][]evRecord
	cutoffs  map[int64]*Cutoff
}

// upgradeLoadChunk is how many versions loadUpgrades reads files and episodes for at once (bound parameters).
const upgradeLoadChunk = 500

// loadUpgrades reads what explaining these versions needs. With cutoffs false the profiles are not read (the
// grids only ask whether an upgrade was seen).
func (w *Web) loadUpgrades(ctx context.Context, db *sql.DB, vs []upgradeVersion, cutoffs bool) (*upgradeSet, error) {
	s := &upgradeSet{episodes: map[int64][]upgradeEpisode{}, cutoffs: map[int64]*Cutoff{}}
	var vids []any
	var series []any
	titleSet := map[int64]bool{}
	var titles []int64
	for _, v := range vs {
		vids = append(vids, v.id)
		if v.kind == "series" {
			series = append(series, v.id)
		}
		if !titleSet[v.titleID] {
			titleSet[v.titleID] = true
			titles = append(titles, v.titleID)
		}
	}
	var err error
	// In chunks: these reads go by version id (indexed), and a caller may pass every version below its cutoff.
	s.files = map[int64]map[int64]itemFile{}
	for chunk := range slices.Chunk(vids, upgradeLoadChunk) {
		files, err := loadItemFiles(ctx, db, chunk)
		if err != nil {
			return nil, err
		}
		maps.Copy(s.files, files)
	}
	for chunk := range slices.Chunk(series, upgradeLoadChunk) {
		err = scanAll(ctx, db, `SELECT s.version_id, e.id, e.season, e.number, s.state FROM episode_version_state s
			JOIN episode e ON e.id = s.episode_id WHERE s.version_id IN (`+store.Placeholders(len(chunk))+`)
			ORDER BY e.season, e.number`, chunk, func(r *sql.Rows) error {
			var vid int64
			var e upgradeEpisode
			if err := r.Scan(&vid, &e.id, &e.season, &e.number, &e.state); err != nil {
				return err
			}
			s.episodes[vid] = append(s.episodes[vid], e)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	items := map[titleKey][]evItem{}
	for _, v := range vs {
		k := titleKey{v.titleID, v.key}
		if v.kind != "series" {
			if f, ok := s.files[v.id][0]; ok && v.state == "filled" {
				items[k] = append(items[k], evItem{fileAt: f.added})
			}
			continue
		}
		for _, e := range s.episodes[v.id] {
			if f, ok := s.files[v.id][e.id]; ok && e.state == "filled" {
				items[k] = append(items[k], evItem{episodeID: e.id, season: e.season, number: e.number, fileAt: f.added})
			}
		}
	}
	if s.evidence, err = loadEvidence(ctx, db, titles, items); err != nil {
		return nil, err
	}
	if cutoffs {
		if s.cutoffs, err = w.loadCutoffs(ctx, db, vs); err != nil {
			return nil, err
		}
	}
	if cutoffs {
		if err := w.loadCeilings(ctx, db, vs, s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// explain builds a version's UpgradeInfo; episode > 0 explains that one episode of a series version.
func (s *upgradeSet) explain(v upgradeVersion, episode int64) *UpgradeInfo {
	u := &UpgradeInfo{State: "none", Cutoff: s.cutoffs[v.id]}
	defer s.applyCeiling(v, episode, u)
	files := s.files[v.id]
	recs := s.evidence[titleKey{v.titleID, v.key}]
	var lastSearch int64
	note := func(next *UpgradeNext, at int64) {
		lastSearch = max(lastSearch, at)
		if next != nil && (u.Next == nil || next.Score > u.Next.Score) {
			u.Next = next
		}
	}
	if v.kind != "series" {
		f, ok := files[0]
		if !ok {
			return u
		}
		u.Quality, u.Score = f.quality, f.score
		switch v.state {
		case "cutoff_met":
			u.State = "cutoff_met"
		case "filled":
			u.State = "below_cutoff"
			next, at := judgeUpgrade(evItem{fileAt: f.added}, recs)
			note(next, at)
		}
	} else {
		// The quality and score most files below the cutoff have.
		type qs struct {
			q   string
			s   int64
			has bool
		}
		counts := map[qs]int{}
		var best qs
		have := false
		for _, e := range s.episodes[v.id] {
			if episode > 0 && e.id != episode {
				continue
			}
			f, ok := files[e.id]
			if !ok {
				continue
			}
			have = true
			if e.state != "filled" {
				continue
			}
			u.Below++
			k := qs{q: f.quality}
			if f.score != nil {
				k.s, k.has = *f.score, true
			}
			counts[k]++
			if counts[k] > counts[best] {
				best = k
			}
			next, at := judgeUpgrade(evItem{episodeID: e.id, season: e.season, number: e.number, fileAt: f.added}, recs)
			if next != nil {
				u.Seen++
			}
			note(next, at)
		}
		if !have {
			return u
		}
		u.State = "cutoff_met"
		if u.Below > 0 {
			u.State = "below_cutoff"
			u.Quality = best.q
			if best.has {
				sc := best.s
				u.Score = &sc
			}
		} else {
			// Every file at cutoff: the quality most episodes have.
			qc := map[string]int{}
			for _, e := range s.episodes[v.id] {
				if f, ok := files[e.id]; ok && (episode == 0 || e.id == episode) {
					qc[f.quality]++
					if qc[f.quality] > qc[u.Quality] {
						u.Quality = f.quality
					}
				}
			}
		}
		if episode > 0 {
			u.Below, u.Seen = 0, 0
			if f, ok := files[episode]; ok {
				u.Quality, u.Score = f.quality, f.score
			}
		}
	}
	if u.Next != nil {
		u.State = "upgrade_available"
	}
	if lastSearch > 0 {
		t := ts(lastSearch)
		u.LastSearch = &t
	}
	return u
}

// episodeSeen lists, per episode of a series version, whether a better release was seen for its file.
func (s *upgradeSet) episodeSeen(v upgradeVersion) map[int64]bool {
	out := map[int64]bool{}
	recs := s.evidence[titleKey{v.titleID, v.key}]
	if len(recs) == 0 {
		return out
	}
	for _, e := range s.episodes[v.id] {
		f, ok := s.files[v.id][e.id]
		if !ok || e.state != "filled" {
			continue
		}
		if next, _ := judgeUpgrade(evItem{episodeID: e.id, season: e.season, number: e.number, fileAt: f.added}, recs); next != nil {
			out[e.id] = true
		}
	}
	return out
}

// loadCutoffs finds each version's cutoff: the owning arr instance's profile for a version an arr imports (that
// profile decided the state), else the policy profile (an arr profile by name for from_arr).
func (w *Web) loadCutoffs(ctx context.Context, db *sql.DB, vs []upgradeVersion) (map[int64]*Cutoff, error) {
	out := map[int64]*Cutoff{}
	if len(vs) == 0 {
		return out, nil
	}
	ids := make([]any, len(vs))
	for i, v := range vs {
		ids[i] = v.id
	}
	err := scanAll(ctx, db, `SELECT vs.version_id, vs.instance, p.name, p.cutoff, p.cutoff_format_score, p.upgrade_allowed
		FROM version_source vs JOIN arr_title a ON a.instance = vs.instance AND a.arr_id = vs.arr_id
		JOIN arr_quality_profile p ON p.instance = a.instance AND p.arr_id = a.quality_profile_id
		WHERE vs.version_id IN (`+store.Placeholders(len(ids))+`)`, ids, func(r *sql.Rows) error {
		var vid int64
		var c Cutoff
		var score int64
		if err := r.Scan(&vid, &c.From, &c.Profile, &c.Quality, &score, &c.Upgrades); err != nil {
			return err
		}
		c.Score = &score
		out[vid] = &c
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, v := range vs {
		if out[v.id] != nil || w.pol == nil {
			continue
		}
		vp := w.pol.Versions[v.key]
		if vp == nil {
			continue
		}
		pr := w.pol.Profiles[vp.Profile]
		if pr == nil {
			continue
		}
		if inst, name, ok := policy.SplitFromArr(pr.FromArr); ok {
			var c Cutoff
			var score int64
			err := db.QueryRowContext(ctx, `SELECT name, cutoff, cutoff_format_score, upgrade_allowed FROM arr_quality_profile
				WHERE instance = ? AND name = ?`, inst, name).Scan(&c.Profile, &c.Quality, &score, &c.Upgrades)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			c.Score, c.From = &score, inst
			out[v.id] = &c
			continue
		}
		score := int64(pr.CutoffScore)
		out[v.id] = &Cutoff{Quality: pr.Cutoff, Score: &score, Profile: vp.Profile, From: "policy",
			Upgrades: pr.Upgrades == nil || *pr.Upgrades}
	}
	return out, nil
}

// rowUpgradeRank orders upgrade states for a list row's badge: the most pressing one wins.
var rowUpgradeRank = map[string]int{"cutoff_met": 1, "best_available": 2, "best_source_seen": 3, "below_cutoff": 4,
	"higher_source_exists": 5, "upgrade_available": 6}

// markUpgradeSeen sets upgrade_seen on list rows: on each version below cutoff with a better release seen, and on
// the row when any version has one. A row whose availability is upgrade also gets upgrade_state, with its source
// ceilings applied as on the title page.
func (w *Web) markUpgradeSeen(ctx context.Context, db *sql.DB, items []TitleRow) error {
	var vs []upgradeVersion
	where := map[int64][2]int{}
	for i := range items {
		for j, v := range items[i].Versions {
			if v.State == "filled" || (items[i].Kind == "series" && v.HasFile) {
				vs = append(vs, upgradeVersion{id: v.ID, titleID: items[i].ID, key: v.Key, kind: items[i].Kind, state: v.State})
				where[v.ID] = [2]int{i, j}
			}
		}
	}
	if len(vs) == 0 {
		return nil
	}
	s, err := w.loadUpgrades(ctx, db, vs, false)
	if err != nil {
		return fmt.Errorf("upgrade evidence: %w", err)
	}
	if err := w.loadCeilings(ctx, db, vs, s); err != nil {
		return fmt.Errorf("source ceilings: %w", err)
	}
	for _, v := range vs {
		u := s.explain(v, 0)
		p := where[v.id]
		row := &items[p[0]]
		if u.State == "upgrade_available" {
			row.Versions[p[1]].UpgradeSeen = true
			row.UpgradeSeen = true
		}
		if row.Availability == "upgrade" && rowUpgradeRank[u.State] > rowUpgradeRank[row.UpgradeState] {
			row.UpgradeState = u.State
		}
	}
	return nil
}
