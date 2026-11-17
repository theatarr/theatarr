package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/store"
)

// Upgrade explanations (docs/design/manage.md). A version whose file is below its profile's cutoff is "below cutoff";
// it is "upgrade available" only when Theatarr has seen a release that would replace that file. The evidence is what Theatarr
// already stores: the journal's decided searches and RSS reads (journal_decision, with every kept candidate's
// accepted and upgrade flags). Nothing here searches.
//
// The rule, per item (a movie version's file, or one episode's file in a series version), for an item below cutoff:
//   - only evidence newer than the item's current file counts (an older decision judged an older file): newer than
//     the later of its link's created time and its download's (an import that replaces a file at the same path keeps
//     the link's first created time);
//   - a sighting of the torrent the item's file came from (its download's hash) is not a better release of it; other
//     episodes of the same pack still count it;
//   - the newest search after the file that covers the item stands for every earlier search: its accepted
//     upgrades count, and when it found none, older finds no longer count;
//   - RSS sightings (journal RSS reads) newer than both the file and that search add to it;
//   - a release counts when it was accepted and marked an upgrade over the file, and it maps to the item (a
//     candidate's episodes, or a search target's season and episode); a pack that only fills episodes with no file
//     (its reason starts "fills ", decide's episodes.go) is not an upgrade of a held file;
//   - a release with a failed download of the title and library newer than the sighting (by its hash) was tried and
//     failed: it is not found, so the best of the others is chosen; when every counted release was tried, the best
//     tried one is still said (Why?'s "Tried …") without being found;
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
	// tried: the release has a failed download of the title and library newer than the sighting (judgeUpgrade). It
	// is said (Why?: "Tried …") but is not found: the state stays below_cutoff.
	tried bool
}

// UpgradeInfo explains a version's (or one episode's) state against its cutoff.
type UpgradeInfo struct {
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
	// QualityMet (below cutoff or upgrade available, for callers who see files): whether the file's quality reaches
	// the cutoff quality, from the stored judging (decide.StoreQualityCutoff; for a series, every file below the
	// cutoff). Absent where it is not stored: a prefer-SeaDex library, or a profile without qualities of its own.
	QualityMet *bool `json:"quality_met,omitempty"`
}

// redactUpgrade keeps the state, the qualities and the counts.
func redactUpgrade(u *UpgradeInfo) {
	if u == nil {
		return
	}
	u.Score, u.Next, u.LastSearch, u.QualityMet = nil, nil, nil, nil
	if c := u.Cutoff; c != nil {
		*c = Cutoff{Quality: c.Quality, Upgrades: c.Upgrades}
	}
}

// evCand is a candidate as the stored decisions keep it (snapshot.Candidate's keys).
type evCand struct {
	Release    string   `json:"release"`
	Indexer    string   `json:"indexer"`
	InfoHash   string   `json:"info_hash"`
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
	// fileAt is the evidence floor: the later of the file's link and download created times.
	fileAt int64
	// hash is the file's download hash ("" when unknown), compared in any case.
	hash string
}

// fileItem is the item of a current file: a movie's (e nil) or one episode's.
func fileItem(f itemFile, e *upgradeEpisode) evItem {
	it := evItem{fileAt: f.since, hash: f.hash}
	if e != nil {
		it.episodeID, it.season, it.number = e.id, e.season, e.number
	}
	return it
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

// judgeUpgrade applies the rule above to one item. failed is the newest failed download time per hash (lower case)
// of the item's title and library. It returns the best release seen (nil when none; tried when every one was tried
// after it was seen) and the time of the newest search that covered the item (0 when none).
func judgeUpgrade(it evItem, recs []evRecord, failed map[string]int64) (*UpgradeNext, int64) {
	pool, lastSearch := upgradeEvidencePool(it, recs)
	var best *UpgradeNext
	for _, r := range pool {
		for _, c := range r.cands {
			if !c.Accepted || !c.Upgrade || strings.HasPrefix(c.UpgradeWhy, "fills ") || !candCovers(c, it) ||
				(it.hash != "" && strings.EqualFold(c.InfoHash, it.hash)) {
				continue
			}
			at, ok := failed[strings.ToLower(c.InfoHash)]
			tried := c.InfoHash != "" && ok && at >= r.at
			better := best == nil || (best.tried && !tried) ||
				(best.tried == tried && (c.Score > best.Score || (c.Score == best.Score && r.at > best.SeenAt.UnixMilli())))
			if better {
				best = &UpgradeNext{Release: c.Release, Indexer: c.Indexer, Quality: c.Quality, Score: c.Score,
					Reason: c.UpgradeWhy, Source: r.source, SeenAt: ts(r.at), Episodes: c.Episodes, tried: tried}
			}
		}
	}
	return best, lastSearch
}

// found reports whether a judged release counts as an upgrade found: seen and not tried since.
func (n *UpgradeNext) found() bool { return n != nil && !n.tried }

// loadFailedHashes reads, per title and library, the newest time a download of each hash failed. Display only. Only
// a real try counts, as History and Why? say it: a download never started (Pause was on, activity.NotStartedSQL) or
// ended by a removal (activity.EndedByRemovalSQL) is not one. The time is its History failed row's (when it failed;
// a Dismiss later does not move it), else the download's updated_at.
func loadFailedHashes(ctx context.Context, db *sql.DB, titleIDs []int64) (map[titleKey]map[string]int64, error) {
	out := map[titleKey]map[string]int64{}
	if len(titleIDs) == 0 {
		return out, nil
	}
	list, _ := json.Marshal(titleIDs)
	err := scanAll(ctx, db, `SELECT title_id, version_key, LOWER(info_hash),
		MAX(COALESCE((SELECT h.at FROM history h WHERE h.ref = 'dl:' || download.id || ':fail'), updated_at)) FROM download
		WHERE state = 'failed' AND COALESCE(info_hash, '') <> '' AND title_id IN (SELECT value FROM json_each(?))
		AND NOT `+activity.NotStartedSQL+` AND NOT `+activity.EndedByRemovalSQL+`
		GROUP BY title_id, version_key, LOWER(info_hash)`, []any{string(list)}, func(r *sql.Rows) error {
		var k titleKey
		var hash string
		var at int64
		if err := r.Scan(&k.titleID, &k.key, &hash, &at); err != nil {
			return err
		}
		if out[k] == nil {
			out[k] = map[string]int64{}
		}
		out[k][hash] = at
		return nil
	})
	return out, err
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
	// key identifies the file: a link's id. A multi-episode file appears once per episode.
	key     int64
	quality string
	score   *int64
	size    int64
	added   int64
	// For the upgrade evidence (evItem): the later of the link's and its download's created times, and the
	// download's hash (judgeUpgrade compares it in any case).
	since int64
	hash  string
}

// itemFileUpgradeSQL selects itemFile's since and hash for link l joined to its download d.
const itemFileUpgradeSQL = `MAX(l.created_at, COALESCE(d.created_at, 0)), COALESCE(d.info_hash, '')`

// loadItemFiles reads the current files of versions, per episode (0 for a movie): their active links. Without
// scores each file's score is left out.
func loadItemFiles(ctx context.Context, db *sql.DB, versionIDs []any, scores bool) (map[int64]map[int64]itemFile, error) {
	out := map[int64]map[int64]itemFile{}
	if len(versionIDs) == 0 {
		return out, nil
	}
	in := store.Placeholders(len(versionIDs))
	score := "NULL"
	if scores {
		score = linkScoreSQL
	}
	err := scanAll(ctx, db, `SELECT l.id, lv.version_id, COALESCE(le.episode_id, 0), COALESCE(l.quality, ''), `+score+`,
		COALESCE(`+linkSizeSQL+`, 0), l.created_at, `+itemFileUpgradeSQL+` FROM link l JOIN link_version lv ON lv.link_id = l.id
		LEFT JOIN link_episode le ON le.link_id = l.id LEFT JOIN content_file cf ON cf.id = l.content_file_id
		LEFT JOIN download d ON d.id = l.download_id
		WHERE lv.version_id IN (`+in+`) AND l.state = 'active'`, versionIDs, func(r *sql.Rows) error {
		var vid, eid int64
		var f itemFile
		var score sql.NullInt64
		if err := r.Scan(&f.key, &vid, &eid, &f.quality, &score, &f.size, &f.added, &f.since, &f.hash); err != nil {
			return err
		}
		f.score = nullInt(score)
		if out[vid] == nil {
			out[vid] = map[int64]itemFile{}
		}
		out[vid][eid] = f
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
	// belowQuality is the stored quality-cutoff flag of the episode's file (episode_version_state.below_quality_cutoff).
	belowQuality bool
}

// upgradeSet explains versions: their items below cutoff and the evidence about them.
type upgradeSet struct {
	files    map[int64]map[int64]itemFile
	episodes map[int64][]upgradeEpisode
	evidence map[titleKey][]evRecord
	// failed is the newest failed download time per hash of each title and library (loadFailedHashes).
	failed  map[titleKey]map[string]int64
	cutoffs map[int64]*Cutoff
	// With cutoffs: the versions whose quality-cutoff flags are stored (not prefer-SeaDex, a cutoff known), and a
	// movie version's flag (version.below_quality_cutoff).
	qualityJudged map[int64]bool
	belowQuality  map[int64]bool
}

// upgradeLoadChunk is how many versions loadUpgrades reads files and episodes for at once (bound parameters).
const upgradeLoadChunk = 500

// loadUpgrades reads what explaining these versions needs. With cutoffs false the profiles and the files' scores
// are not read (the grids and Discover only ask whether an upgrade was seen).
func (w *Web) loadUpgrades(ctx context.Context, db *sql.DB, vs []upgradeVersion, cutoffs bool) (*upgradeSet, error) {
	var vids []any
	for _, v := range vs {
		vids = append(vids, v.id)
	}
	// In chunks: these reads go by version id (indexed), and a caller may pass every version below its cutoff.
	files := map[int64]map[int64]itemFile{}
	for chunk := range slices.Chunk(vids, upgradeLoadChunk) {
		f, err := loadItemFiles(ctx, db, chunk, cutoffs)
		if err != nil {
			return nil, err
		}
		maps.Copy(files, f)
	}
	return w.upgradesWith(ctx, db, vs, files, cutoffs)
}

// upgradesWith is loadUpgrades with the versions' current files already read (loadItemFiles, or versionFiles' items).
func (w *Web) upgradesWith(ctx context.Context, db *sql.DB, vs []upgradeVersion, files map[int64]map[int64]itemFile,
	cutoffs bool) (*upgradeSet, error) {
	s := &upgradeSet{files: files, episodes: map[int64][]upgradeEpisode{}, cutoffs: map[int64]*Cutoff{}}
	var series []any
	titleSet := map[int64]bool{}
	var titles []int64
	for _, v := range vs {
		if v.kind == "series" {
			series = append(series, v.id)
		}
		if !titleSet[v.titleID] {
			titleSet[v.titleID] = true
			titles = append(titles, v.titleID)
		}
	}
	var err error
	for chunk := range slices.Chunk(series, upgradeLoadChunk) {
		err = scanAll(ctx, db, `SELECT s.version_id, e.id, e.season, e.number, s.state, s.below_quality_cutoff
			FROM episode_version_state s JOIN episode e ON e.id = s.episode_id
			WHERE s.version_id IN (`+store.Placeholders(len(chunk))+`) ORDER BY e.season, e.number`, chunk, func(r *sql.Rows) error {
			var vid int64
			var e upgradeEpisode
			if err := r.Scan(&vid, &e.id, &e.season, &e.number, &e.state, &e.belowQuality); err != nil {
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
				items[k] = append(items[k], fileItem(f, nil))
			}
			continue
		}
		for _, e := range s.episodes[v.id] {
			if f, ok := s.files[v.id][e.id]; ok && e.state == "filled" {
				items[k] = append(items[k], fileItem(f, &e))
			}
		}
	}
	if s.evidence, err = loadEvidence(ctx, db, titles, items); err != nil {
		return nil, err
	}
	// Failed tries matter only to titles with stored sightings to judge.
	seen := map[int64]bool{}
	var judged []int64
	for k := range s.evidence {
		if !seen[k.titleID] {
			seen[k.titleID] = true
			judged = append(judged, k.titleID)
		}
	}
	if s.failed, err = loadFailedHashes(ctx, db, judged); err != nil {
		return nil, err
	}
	if cutoffs {
		s.cutoffs = w.loadCutoffs(vs)
		if err := w.loadQualityFlags(ctx, db, vs, s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// loadQualityFlags notes which versions have their quality-cutoff flags stored and reads a movie version's flag.
func (w *Web) loadQualityFlags(ctx context.Context, db *sql.DB, vs []upgradeVersion, s *upgradeSet) error {
	s.qualityJudged, s.belowQuality = map[int64]bool{}, map[int64]bool{}
	if w.pol == nil {
		return nil
	}
	var movies []any
	for _, v := range vs {
		if vp := w.pol.Versions[v.key]; s.cutoffs[v.id] != nil && vp != nil && !vp.PreferSeaDex {
			s.qualityJudged[v.id] = true
			if v.kind != "series" {
				movies = append(movies, v.id)
			}
		}
	}
	for chunk := range slices.Chunk(movies, upgradeLoadChunk) {
		err := scanAll(ctx, db, `SELECT id, below_quality_cutoff FROM version WHERE id IN (`+
			store.Placeholders(len(chunk))+`)`, chunk, func(r *sql.Rows) error {
			var id int64
			var below bool
			if err := r.Scan(&id, &below); err != nil {
				return err
			}
			s.belowQuality[id] = below
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// explain builds a version's UpgradeInfo; episode > 0 explains that one episode of a series version.
func (s *upgradeSet) explain(v upgradeVersion, episode int64) *UpgradeInfo {
	u := &UpgradeInfo{State: "none", Cutoff: s.cutoffs[v.id]}
	files := s.files[v.id]
	recs := s.evidence[titleKey{v.titleID, v.key}]
	failed := s.failed[titleKey{v.titleID, v.key}]
	var lastSearch int64
	belowQuality := false
	// A found release wins over a tried one, then the higher score.
	note := func(next *UpgradeNext, at int64) {
		lastSearch = max(lastSearch, at)
		if next != nil && (u.Next == nil || (u.Next.tried && !next.tried) || (u.Next.tried == next.tried && next.Score > u.Next.Score)) {
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
			belowQuality = s.belowQuality[v.id]
			next, at := judgeUpgrade(fileItem(f, nil), recs, failed)
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
			belowQuality = belowQuality || e.belowQuality
			k := qs{q: f.quality}
			if f.score != nil {
				k.s, k.has = *f.score, true
			}
			counts[k]++
			if counts[k] > counts[best] {
				best = k
			}
			next, at := judgeUpgrade(fileItem(f, &e), recs, failed)
			if next.found() {
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
	if u.Next.found() {
		u.State = "upgrade_available"
	}
	if s.qualityJudged[v.id] && u.State != "none" && u.State != "cutoff_met" {
		met := !belowQuality
		u.QualityMet = &met
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
	failed := s.failed[titleKey{v.titleID, v.key}]
	for _, e := range s.episodes[v.id] {
		f, ok := s.files[v.id][e.id]
		if !ok || e.state != "filled" {
			continue
		}
		if next, _ := judgeUpgrade(fileItem(f, &e), recs, failed); next.found() {
			out[e.id] = true
		}
	}
	return out
}

// loadCutoffs finds each version's cutoff from its library's profile. A profile without qualities of its own (one
// taken from an arr) gives none.
func (w *Web) loadCutoffs(vs []upgradeVersion) map[int64]*Cutoff {
	out := map[int64]*Cutoff{}
	if w.pol == nil {
		return out
	}
	for _, v := range vs {
		vp := w.pol.Versions[v.key]
		if vp == nil {
			continue
		}
		pr := w.pol.Profiles[vp.Profile]
		if pr == nil || len(pr.Qualities) == 0 {
			continue
		}
		score := int64(pr.CutoffScore)
		out[v.id] = &Cutoff{Quality: pr.Cutoff, Score: &score, Profile: vp.Profile, From: "policy",
			Upgrades: pr.Upgrades == nil || *pr.Upgrades}
	}
	return out
}

// rowUpgradeRank orders upgrade states for a list row's badge: the most pressing one wins.
var rowUpgradeRank = map[string]int{"cutoff_met": 1, "below_cutoff": 2, "upgrade_available": 3}

// markUpgradeSeen sets upgrade_seen on list rows: on each version below cutoff with a better release seen, and on
// the row when any version has one. A row with a file held (available or partial) also gets upgrade_state, as on the
// title page.
func (w *Web) markUpgradeSeen(ctx context.Context, db *sql.DB, items []TitleRow, files map[int64]map[int64]itemFile) error {
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
	s, err := w.upgradesWith(ctx, db, vs, files, false)
	if err != nil {
		return fmt.Errorf("upgrade evidence: %w", err)
	}
	for _, v := range vs {
		u := s.explain(v, 0)
		p := where[v.id]
		row := &items[p[0]]
		if u.State == "upgrade_available" {
			row.Versions[p[1]].UpgradeSeen = true
			row.UpgradeSeen = true
		}
		if (row.Availability == "available" || row.Availability == "partial") &&
			rowUpgradeRank[u.State] > rowUpgradeRank[row.UpgradeState] {
			row.UpgradeState = u.State
		}
	}
	return nil
}
