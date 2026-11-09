package ceiling

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
)

// Judge requires successful, complete disc-carrying searches of this version and season for absence.
// Positive-only sources supply tiers, never search coverage. Overrides pin the tier without hiding contrary evidence.
func Judge(evidence []Evidence, searches []Search, discOnly bool, override *Override, now time.Time) Info {
	out := Info{Tier: "unknown", Status: "unknown", CheckedAt: now, NextCheckAt: now.Add(24 * time.Hour),
		Evidence: evidence, Searches: searches}
	if out.Evidence == nil {
		out.Evidence = []Evidence{}
	}
	if out.Searches == nil {
		out.Searches = []Search{}
	}
	var since time.Time
	for _, e := range evidence {
		if e.Kind == "tmdb" && (e.FetchedAt == nil || !e.FetchedAt.After(now.AddDate(0, -6, 0))) {
			continue
		}
		if discOnly && (e.Tier == "web_1080" || e.Tier == "web_2160" || e.Tier == "tv") {
			continue
		}
		if rank(e.Tier) > rank(out.Tier) {
			out.Tier, since = e.Tier, e.FirstSeen
		} else if e.Tier == out.Tier && e.FirstSeen.Before(since) {
			since = e.FirstSeen
		}
	}
	if out.Tier != "unknown" {
		out.Status = "likely"
		var first, last time.Time
		count := 0
		dates := map[int64]bool{}
		for _, s := range searches {
			if s.At.Before(since) || s.At.After(now) || dates[s.At.UnixMilli()] {
				continue
			}
			success := false
			for _, c := range s.Indexers {
				if c.Kind == "torznab" && c.Complete {
					success = true
				}
			}
			if !success {
				continue
			}
			// A newer higher source starts a new absence window; old searches cannot certify it.
			dates[s.At.UnixMilli()] = true
			count++
			if first.IsZero() || s.At.Before(first) {
				first = s.At
			}
			if s.At.After(last) {
				last = s.At
			}
		}
		if count >= 3 && last.Sub(first) >= 90*24*time.Hour {
			out.Status = "known"
		}
	}
	if override != nil && (override.Until == nil || override.Until.After(now)) {
		out.Override = override
		out.Tier = override.Tier
		out.Status = "override"
		if override.Until != nil && override.Until.Before(out.NextCheckAt) {
			out.NextCheckAt = *override.Until
		}
	}
	for _, e := range evidence {
		if e.Kind == "tmdb" && e.FetchedAt != nil {
			expires := e.FetchedAt.AddDate(0, 6, 0)
			if expires.After(now) && expires.Before(out.NextCheckAt) {
				out.NextCheckAt = expires
			}
		}
	}
	out.Label = Label(out.Tier)
	return out
}

// EffectiveCutoff caps by the profile's own order, treating a group as one item only when all its qualities fit.
func EffectiveCutoff(p *profile.Profile, tier string) (int, string) {
	if p == nil {
		return 0, ""
	}
	cutoff := p.Cutoff
	name := func(id int) string {
		for _, it := range p.Items {
			if it.ID == id && it.ID > 0 {
				return it.Name
			}
			for _, q := range it.Qualities() {
				if q.ID == id {
					return q.Name
				}
			}
		}
		return ""
	}
	if tier == "unknown" || tier == "uhd_bd" {
		return cutoff, name(cutoff)
	}
	for i := len(p.Items) - 1; i >= 0; i-- {
		it := &p.Items[i]
		if !it.Allowed {
			continue
		}
		qs := it.Qualities()
		fits := len(qs) > 0
		for _, q := range qs {
			t := Tier(q.Name)
			if t == "unknown" || rank(t) > rank(tier) {
				fits = false
			}
		}
		if !fits {
			continue
		}
		id := it.ID
		if it.Quality != nil {
			id = it.Quality.ID
		}
		if p.CompareQualityIDs(id, cutoff, false) < 0 {
			cutoff = id
		}
		return cutoff, name(cutoff)
	}
	return 0, ""
}

// Profile reads only the quality part of the owning arr profile, or of the policy profile.
func Profile(ctx context.Context, db DB, pol *policy.Policy, vid int64, key, kind string) (*profile.Profile, error) {
	fl := format.Radarr
	if kind == "series" {
		fl = format.Sonarr
	}
	var items, cutoff string
	err := db.QueryRowContext(ctx, `SELECT p.quality_items,p.cutoff FROM version_source vs
 JOIN arr_title a ON a.instance=vs.instance AND a.arr_id=vs.arr_id
 JOIN arr_quality_profile p ON p.instance=a.instance AND p.arr_id=a.quality_profile_id WHERE vs.version_id=?`, vid).Scan(&items, &cutoff)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		if pol == nil || pol.Versions[key] == nil {
			return nil, nil
		}
		pr := pol.Profiles[pol.Versions[key].Profile]
		if pr == nil {
			return nil, nil
		}
		if inst, name, ok := policy.SplitFromArr(pr.FromArr); ok {
			err = db.QueryRowContext(ctx, `SELECT quality_items,cutoff FROM arr_quality_profile WHERE instance=? AND name=?`, inst, name).Scan(&items, &cutoff)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
		} else {
			return policyProfile(pol, key, fl)
		}
	}
	return arrProfile(items, cutoff, fl)
}

func policyProfile(pol *policy.Policy, key string, fl format.Flavour) (*profile.Profile, error) {
	if pol == nil || pol.Versions[key] == nil {
		return nil, nil
	}
	name := pol.Versions[key].Profile
	pr := pol.Profiles[name]
	if pr == nil {
		return nil, nil
	}
	var wanted []format.TrashQualityItem
	for _, it := range pr.Qualities {
		wanted = append(wanted, format.TrashQualityItem{Name: it.Name, Items: it.Qualities, Allowed: true})
	}
	return profile.FromItems(fl, name, wanted, pr.Cutoff, true)
}

func arrProfile(items, cutoff string, fl format.Flavour) (*profile.Profile, error) {
	var itemsJSON []struct {
		ID      int
		Name    string
		Allowed bool
		Quality *struct {
			ID   int
			Name string
		}
		Items []struct {
			Quality *struct {
				ID   int
				Name string
			}
			Allowed bool
		}
	}
	if err := json.Unmarshal([]byte(items), &itemsJSON); err != nil {
		return nil, err
	}
	if len(itemsJSON) == 0 {
		return nil, nil
	}
	p := &profile.Profile{}
	for _, j := range itemsJSON {
		it := profile.Item{ID: j.ID, Name: j.Name, Allowed: j.Allowed}
		quality := func(id int, name string) (*profile.Quality, error) {
			q, ok := profile.QualityByID(fl, id)
			if !ok || q.Name != name {
				return nil, fmt.Errorf("ceiling: unknown %s quality %d/%s", fl, id, name)
			}
			return &q, nil
		}
		if j.Quality != nil {
			q, err := quality(j.Quality.ID, j.Quality.Name)
			if err != nil {
				return nil, err
			}
			it.Quality = q
			if strings.EqualFold(q.Name, cutoff) {
				p.Cutoff = q.ID
			}
		} else {
			if strings.EqualFold(j.Name, cutoff) {
				p.Cutoff = j.ID
			}
			for _, child := range j.Items {
				if child.Quality == nil {
					continue
				}
				q, err := quality(child.Quality.ID, child.Quality.Name)
				if err != nil {
					return nil, err
				}
				it.Items = append(it.Items, profile.Item{Quality: q, Allowed: child.Allowed})
			}
		}
		p.Items = append(p.Items, it)
	}
	return p, nil

}

// Compute refreshes a title from local data only; effective cutoffs in this package must never enter the release
// decision engine.
func Compute(ctx context.Context, db DB, pol *policy.Policy, titleID int64, now time.Time) error {
	if err := seedHistory(ctx, db, titleID); err != nil {
		return err
	}
	if err := seedTMDb(ctx, db, titleID, now); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM source_sighting WHERE title_id=? AND source_kind='tmdb'
  AND (fetched_at IS NULL OR fetched_at<=?)`, titleID, now.AddDate(0, -6, 0).UnixMilli()); err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT v.id,v.policy_key,t.kind FROM version v JOIN title t ON t.id=v.title_id WHERE v.title_id=?`, titleID)
	if err != nil {
		return err
	}
	type version struct {
		id        int64
		key, kind string
	}
	var versions []version
	for rows.Next() {
		var v version
		if err := rows.Scan(&v.id, &v.key, &v.kind); err != nil {
			rows.Close()
			return err
		}
		versions = append(versions, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var seasons []int
	if len(versions) > 0 && versions[0].kind == "series" {
		rows, err = db.QueryContext(ctx, `SELECT DISTINCT season FROM episode WHERE title_id=? ORDER BY season`, titleID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var s int
			if err := rows.Scan(&s); err != nil {
				rows.Close()
				return err
			}
			seasons = append(seasons, s)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	} else {
		seasons = []int{-1}
	}
	for _, season := range seasons {
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO source_ceiling(title_id,season) VALUES(?,?)`, titleID, season); err != nil {
			return err
		}
		ev, err := loadSightings(ctx, db, titleID, season)
		if err != nil {
			return err
		}
		ov, err := loadOverride(ctx, db, titleID, season)
		if err != nil {
			return err
		}
		world := Judge(ev, nil, false, ov, now)
		if _, err := db.ExecContext(ctx, `UPDATE source_ceiling SET tier=?,status=?,checked_at=?,next_check_at=? WHERE title_id=? AND season=?`, world.Tier, world.Status, now.UnixMilli(), world.NextCheckAt.UnixMilli(), titleID, season); err != nil {
			return err
		}
		for _, v := range versions {
			searches, err := loadSearches(ctx, db, titleID, season, v.key)
			if err != nil {
				return err
			}
			discOnly := pol != nil && pol.Versions[v.key] != nil && pol.Versions[v.key].Ceiling.DiscOnly
			info := Judge(ev, searches, discOnly, ov, now)
			// The stored ceiling shows the latest searches only; the full history stays in ceiling_search.
			info.Searches = info.Searches[max(len(info.Searches)-maxInfoSearches, 0):]
			p, err := Profile(ctx, db, pol, v.id, v.key, v.kind)
			if err != nil {
				return err
			}
			_, cutoff := EffectiveCutoff(p, info.Tier)
			state, err := fileState(ctx, db, v.id, season, v.kind, &info, p)
			if err != nil {
				return err
			}
			b, err := json.Marshal(info)
			if err != nil {
				return err
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO version_ceiling(version_id,season,effective_cutoff,upgrade_state,ceiling,computed_at)
    VALUES(?,?,?,?,?,?) ON CONFLICT(version_id,season) DO UPDATE SET effective_cutoff=excluded.effective_cutoff,
    upgrade_state=excluded.upgrade_state,ceiling=excluded.ceiling,computed_at=excluded.computed_at`, v.id, season, cutoff, state, string(b), now.UnixMilli()); err != nil {
				return err
			}
		}
	}
	return nil
}

func loadSightings(ctx context.Context, db DB, titleID int64, season int) ([]Evidence, error) {
	rows, err := db.QueryContext(ctx, `SELECT source,source_kind,positive_only,tier,example,quality,count,first_seen_at,last_seen_at,fetched_at
 FROM source_sighting WHERE title_id=? AND season=? ORDER BY tier,source,source_kind`, titleID, season)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Evidence
	for rows.Next() {
		var e Evidence
		var first, last int64
		var fetched sql.NullInt64
		if err := rows.Scan(&e.Source, &e.Kind, &e.PositiveOnly, &e.Tier, &e.Example, &e.Quality, &e.Count, &first, &last, &fetched); err != nil {
			return nil, err
		}
		e.FirstSeen = time.UnixMilli(first).UTC()
		e.LastSeen = time.UnixMilli(last).UTC()
		if fetched.Valid {
			t := time.UnixMilli(fetched.Int64).UTC()
			e.FetchedAt = &t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// maxInfoSearches is how many searches a stored ceiling (version_ceiling.ceiling) lists.
const maxInfoSearches = 10

func loadSearches(ctx context.Context, db DB, titleID int64, season int, key string) ([]Search, error) {
	rows, err := db.QueryContext(ctx, `SELECT s.searched_at,s.source,s.best_tier,i.indexer,i.source_kind,i.complete
 FROM ceiling_search s LEFT JOIN ceiling_search_indexer i ON i.search_id=s.id WHERE s.title_id=? AND s.season=? AND s.version_key=?
 ORDER BY s.searched_at,s.id,i.indexer`, titleID, season, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Search{}
	for rows.Next() {
		var at int64
		var source, tier string
		var ix, kind sql.NullString
		var complete sql.NullBool
		if err := rows.Scan(&at, &source, &tier, &ix, &kind, &complete); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].At.UnixMilli() != at || out[len(out)-1].Source != source {
			out = append(out, Search{At: time.UnixMilli(at).UTC(), Source: source, BestTier: tier, Indexers: []Coverage{}})
		}
		if ix.Valid {
			s := &out[len(out)-1]
			s.Indexers = append(s.Indexers, Coverage{Indexer: ix.String, Kind: kind.String, Season: season, Complete: complete.Bool})
		}
	}
	return out, rows.Err()
}
func loadOverride(ctx context.Context, db DB, titleID int64, season int) (*Override, error) {
	var tier sql.NullString
	var note string
	var by, at, until sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT override_tier,override_note,override_by,override_at,override_until FROM source_ceiling WHERE title_id=? AND season=?`, titleID, season).Scan(&tier, &note, &by, &at, &until)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !tier.Valid {
		return nil, nil
	}
	o := &Override{Tier: tier.String, Note: note, At: time.UnixMilli(at.Int64).UTC()}
	if by.Valid {
		o.By = &by.Int64
	}
	if until.Valid {
		t := time.UnixMilli(until.Int64).UTC()
		o.Until = &t
	}
	return o, nil
}

// seedHistory is idempotent: aggregates come from imported events, not the number of recomputes.
func seedHistory(ctx context.Context, db DB, titleID int64) error {
	rows, err := db.QueryContext(ctx, `SELECT COALESCE(e.season,-1),h.quality,h.source_title,h.date_at,h.instance,t.kind
 FROM arr_history h JOIN title t ON t.id=h.title_id LEFT JOIN arr_alias a ON a.instance=h.instance AND a.kind='episode' AND a.arr_id=h.arr_episode_id
 LEFT JOIN episode e ON e.id=a.target_id WHERE h.title_id=? AND h.event_type IN ('grabbed','downloadFolderImported') ORDER BY h.date_at`, titleID)
	if err != nil {
		return err
	}
	type sight struct {
		season           int
		quality, example string
		at               int64
		source, kind     string
	}
	var ss []sight
	for rows.Next() {
		var s sight
		if err := rows.Scan(&s.season, &s.quality, &s.example, &s.at, &s.source, &s.kind); err != nil {
			rows.Close()
			return err
		}
		ss = append(ss, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, s := range ss {
		if (s.kind == "series" && s.season < 0) || strings.Contains(strings.ToLower(s.example), "seadex") {
			continue
		}
		at := time.UnixMilli(s.at).UTC()
		if err := Sight(ctx, db, titleID, s.season, Evidence{Source: "arr_history:" + s.source, Kind: "arr_history", PositiveOnly: true, Tier: Tier(s.quality), Example: s.example, Quality: s.quality, FirstSeen: at, LastSeen: at}); err != nil {
			return err
		}
	}
	return nil
}

// seedTMDb uses notes already fetched by the ordinary enrichment path. Old rows without a fetch time are ignored.
func seedTMDb(ctx context.Context, db DB, titleID int64, now time.Time) error {
	if _, err := db.ExecContext(ctx, `UPDATE title_release_date SET note='',fetched_at=NULL WHERE title_id=? AND source='tmdb' AND fetched_at<=?`, titleID, now.AddDate(0, -6, 0).UnixMilli()); err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT country,note,fetched_at FROM title_release_date WHERE title_id=? AND source='tmdb' AND type='physical' AND date<=? AND fetched_at>?`, titleID, now.UTC().Format(time.DateOnly), now.AddDate(0, -6, 0).UnixMilli())
	if err != nil {
		return err
	}
	type note struct {
		country, text string
		at            int64
	}
	var notes []note
	for rows.Next() {
		var n note
		if err := rows.Scan(&n.country, &n.text, &n.at); err != nil {
			rows.Close()
			return err
		}
		notes = append(notes, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, n := range notes {
		tier := "unknown"
		text := strings.ToLower(n.text)
		switch {
		case strings.Contains(text, "4k") || strings.Contains(text, "uhd") || strings.Contains(text, "ultra hd"):
			tier = "uhd_bd"
		case strings.Contains(text, "blu-ray") || strings.Contains(text, "blu ray") || strings.Contains(text, "bluray"):
			tier = "bd"
		case strings.Contains(text, "dvd"):
			tier = "dvd"
		}
		fetched := time.UnixMilli(n.at).UTC()
		if err := Sight(ctx, db, titleID, -1, Evidence{Source: "tmdb:" + n.country, Kind: "tmdb", PositiveOnly: true, Tier: tier, Example: n.text, FirstSeen: fetched, LastSeen: fetched, FetchedAt: &fetched}); err != nil {
			return err
		}
	}
	return nil
}
