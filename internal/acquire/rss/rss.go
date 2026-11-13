package rss

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// RSSSummary is one shadow.rss run.
type RSSSummary struct {
	Indexers []RSSIndexer `json:"indexers"`
	// Items read, New not seen before, Matched mapped to a title of ours, Decided decisions made.
	Items    int `json:"items"`
	New      int `json:"new"`
	Matched  int `json:"matched"`
	Decided  int `json:"decided"`
	Requests int `json:"requests"`
	// Routed are the router.route jobs enqueued for Live versions (one per title and version, its picks ranked);
	// RouteSkipped the titles skipped because a download for them is in flight.
	Routed       int   `json:"routed,omitempty"`
	RouteSkipped int   `json:"route_skipped,omitempty"`
	TookMS       int64 `json:"took_ms"`
	// Skipped says why the run read nothing (downloads paused).
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`

	active   map[string]*activeGroup // accepted items grouped for routing at the end of the run
	remember bool                    // every candidate for the current item has a durable outcome
}

// RSSIndexer is one feed's part of a run.
type RSSIndexer struct {
	Indexer  string `json:"indexer"`
	Items    int    `json:"items"`
	New      int    `json:"new"`
	Requests int    `json:"requests"`
	Skipped  string `json:"skipped,omitempty"`
	Error    string `json:"error,omitempty"`
	// Gap says catch-up did not reach the previous feed head. At the page cap the cursor moves on at once;
	// older items were not read. A feed ending before the previous head also reports a gap.
	Gap string `json:"gap,omitempty"`
}

const rssCatchupPages = 10
const rssPageSize = 100

// rssIndexers are the torznab feeds, DMM's newly cached list, SeaDex's recent feed and the catalog sources (kind
// cardigann) the Live versions list. DMM's feed is read on a path naming rd or ad (the indexer asks those
// providers only); a path whose only provider is tb has none and is left out. Torrentio and Zilean have no RSS feed
// we read: they answer per title. SeaDex files each recent entry under one of our titles itself (its TVDB or TMDb id and seadexerr's
// season or movie title), so its items map like any other feed's. A catalog source's feed is its definition's search
// without keywords, as Prowlarr's is (one search, no offset: rssPages reads one page of it); skipped are the catalog
// sources that cannot be read, with the reason, for the run summary.
func (r *Runner) rssIndexers(ctx context.Context) (feeds []string, skipped map[string]string) {
	set := map[string]bool{}
	skipped = map[string]string{}
	for k := range r.rssVersions() {
		for _, ik := range r.pol.Versions[k].Indexers {
			ix := r.pol.Indexers[ik]
			if ix == nil || ix.Kind != "torznab" && ix.Kind != "dmm" && ix.Kind != "seadex" && ix.Kind != "cardigann" {
				continue
			}
			if r.idx != nil {
				if why := r.idx.RSSProblem(ctx, ik); why != "" {
					if ix.Kind == "cardigann" {
						skipped[ik] = "no RSS: " + why
					}
					continue
				}
			}
			set[ik] = true
		}
	}
	return sortedKeys(set), skipped
}

// rssCategories are the Newznab categories asked of a feed: movies (2000) and TV (5000) as the Live versions
// that list it need.
func (r *Runner) rssCategories(k string) []int {
	var movie, tv, anime bool
	for v := range r.rssVersions() {
		vp := r.pol.Versions[v]
		if !slices.Contains(vp.Indexers, k) {
			continue
		}
		if vp.Kind == policy.KindSeries {
			tv = true
			anime = anime || vp.Anime != nil && *vp.Anime
		} else {
			movie = true
		}
	}
	var out []int
	if movie {
		out = append(out, 2000)
	}
	if tv {
		out = append(out, 5000)
	}
	if anime {
		out = append(out, 5070)
	}
	return out
}

// isTV is whether an RSS item is in a TV category (5000-5999), else it is decided as a movie.
func isTV(rel indexer.Release) bool {
	for _, c := range rel.Categories {
		if c >= 5000 && c < 6000 {
			return true
		}
	}
	return false
}

func isAnime(rel indexer.Release) bool {
	return slices.Contains(rel.Categories, 5070)
}

// candidate is a version of ours an RSS item may be for: monitored, and wanted or below its cutoff.
type candidate struct {
	title   decide.Title
	version decide.Version
}

// skippedPaused is the run row's Skipped while downloads are paused.
const skippedPaused = "downloads are paused"

// RSS runs shadow.rss once: read the feeds, decide what is new and route what is accepted.
func (r *Runner) RSS(ctx context.Context) (*RSSSummary, error) {
	run, err := r.forOperation(ctx)
	if err != nil {
		return nil, err
	}
	return run.rssJob(run.pin(ctx))
}

func (r *Runner) rssJob(ctx context.Context) (*RSSSummary, error) {
	ctx = r.pin(ctx)
	start := r.now()
	sum := &RSSSummary{}
	if r.paused() {
		// Nothing is read, so no cursor or seen mark moves: the next run after the pause reads the same items. The
		// film index is reloaded all the same (searches use it).
		if _, err := r.refresh(ctx); err != nil {
			r.log.Warn("shadow rss: film index not reloaded", "err", err)
		}
		sum.Skipped = skippedPaused
		r.record(ctx, KindRSS, start, true, sum)
		r.log.Info("shadow rss skipped: downloads are paused")
		return sum, nil
	}
	err := r.rss(ctx, sum)
	if _, perr := r.st.Writer().ExecContext(ctx, `DELETE FROM shadow_rss_seen WHERE seen_at < ?`,
		store.Millis(r.now().Add(-14*24*time.Hour))); perr != nil {
		r.log.Warn("rss prune", "err", perr)
	}
	if err != nil {
		sum.Error = err.Error()
	}
	sum.TookMS = r.now().Sub(start).Milliseconds()
	r.record(ctx, KindRSS, start, err == nil, sum)
	r.m.rssItems.Add(float64(sum.New))
	r.log.Info("shadow rss", "items", sum.Items, "new", sum.New, "matched", sum.Matched, "routed", sum.Routed,
		"requests", sum.Requests, "took_ms", sum.TookMS)
	return sum, err
}

func (r *Runner) rss(ctx context.Context, sum *RSSSummary) error {
	if r.idx == nil {
		return errors.New("no indexer service")
	}
	// Reload the film index: films added since the last run (by hand, a request or a list) are matched now.
	titles, err := r.refresh(ctx)
	if err != nil {
		return err
	}
	cands, err := r.candidates(ctx, titles)
	if err != nil {
		return err
	}
	scands, _, err := r.seriesCandidates(ctx)
	if err != nil {
		return err
	}
	six, err := r.seriesIndex(ctx, scands)
	if err != nil {
		return err
	}
	byClean := map[string][]int64{}
	for id, t := range titles {
		for _, n := range append([]string{t.Name, t.OriginalTitle}, t.Aliases...) {
			if c := radarr.CleanMovieTitle(n); c != "" && !slices.Contains(byClean[c], id) {
				byClean[c] = append(byClean[c], id)
			}
		}
	}
	lib := decide.NewLibrary(titles)
	var failed []string
	// Only immutable rejections are remembered. Temporary rejections and routes are reconsidered next time (enqueuing a route does not prove a grab succeeded).
	var fresh []seenItem
	// cursors are the new feed heads, written with the seen marks.
	var cursors []rssCursor
	inRun := map[seenItem]bool{}
	feeds, skipped := r.rssIndexers(ctx)
	for _, k := range sortedKeys(skipped) {
		sum.Indexers = append(sum.Indexers, RSSIndexer{Indexer: k, Skipped: skipped[k]})
	}
	type readyFeed struct {
		key    string
		info   RSSIndexer
		result *indexer.Result
	}
	// A feed is an indexer, or one of its lists when it has several (a DMM path naming rd and ad reads each
	// provider's own newly cached list): key "<indexer>#<list>", with its own cursor and seen marks.
	var all []string
	for _, ik := range feeds {
		lists := r.idx.RSSFeeds(ik)
		if len(lists) == 0 {
			all = append(all, ik)
		}
		for _, l := range lists {
			all = append(all, ik+"#"+l)
		}
	}
	var ready []readyFeed
	for _, k := range all {
		ik, _, _ := strings.Cut(k, "#")
		ri := RSSIndexer{Indexer: k}
		if why := r.budgetBlocked(ctx, ik); why != "" {
			ri.Skipped = why
			sum.Indexers = append(sum.Indexers, ri)
			continue
		}
		cursor := rssCursor{indexer: k}
		var cursorPub sql.NullInt64
		if err := r.st.Reader().QueryRowContext(ctx, `SELECT item_key, published_at FROM shadow_rss_cursor
			WHERE indexer = ?`, k).Scan(&cursor.key, &cursorPub); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if cursor.key == "" && ik != k {
			// A list's first sync after the split: catch up to the old combined feed head's publish date.
			var key string
			if err := r.st.Reader().QueryRowContext(ctx, `SELECT item_key, published_at FROM shadow_rss_cursor
				WHERE indexer = ?`, ik).Scan(&key, &cursorPub); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if cursorPub.Valid {
			cursor.published = time.UnixMilli(cursorPub.Int64)
		}
		res, end, newest, err := r.rssPages(ctx, k, r.rssCategories(ik), cursor)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			ri.Error = err.Error()
			ri.Requests = indexer.RequestsSpent(err) // a failed answer can still have cost requests
			sum.Requests += ri.Requests
			failed = append(failed, k)
			sum.Indexers = append(sum.Indexers, ri)
			continue
		}
		switch end {
		case rssCapped:
			// More full pages lie between the last page read and the previous head. The cursor moves on at once and
			// the gap is reported, as the arrs do (HttpIndexerBase: "rss sync didn't cover the period"): holding it
			// re-read the newest pages every sync while new items pushed the gap further down, so it was never
			// reached.
			ri.Gap = fmt.Sprintf("catch-up stopped after %d pages before the previous feed head; the cursor moved "+
				"on and older items were not read", rssCatchupPages)
			r.log.Warn("RSS catch-up truncated", "indexer", k, "pages", rssCatchupPages, "gap", ri.Gap)
		case rssFeedEnd:
			if cursor.key != "" {
				ri.Gap = "the previous feed head is no longer in the feed; items between may have been missed"
				r.log.Warn("RSS sync did not reach the previous feed head", "indexer", k, "gap", ri.Gap)
			}
		}
		if newest != nil {
			cursors = append(cursors, *newest)
		}
		ri.Requests = res.Requests // rssPages already counts only fresh pages
		ready = append(ready, readyFeed{k, ri, res})
	}
	// Successful feeds have persisted their complete observations. Pin once for all sources and versions.
	ctx, err = decide.WithStoredSeaDex(ctx, r.st.Reader())
	if err != nil {
		return err
	}
	for _, feed := range ready {
		k, ri, res := feed.key, feed.info, feed.result
		ri.Items = len(res.Releases)
		// One feed page per sync, with a hard decision cap even if an indexer ignores its page size.
		page := res.Releases[:min(len(res.Releases), indexer.DefaultPageCap)]
		ranked := r.withCacheEvidence(ctx, page)
		for i, rel := range page {
			it := seenItem{k, r.seenKey(rel)}
			if inRun[it] {
				continue
			}
			inRun[it] = true
			isNew, err := r.unseen(ctx, it)
			if err != nil {
				return err
			}
			if !isNew {
				continue
			}
			rssDecided := sum.Decided
			sum.remember = true
			ri.New++
			if isTV(rel) {
				matched, err := r.decideSeriesItem(ctx, rel, ranked[i], scands, six, sum)
				if err != nil {
					return err
				}
				if matched {
					sum.Matched++
				}
				if !isAnime(rel) || animeEpisodeItem(ctx, rel) {
					if sum.remember && sum.Decided > rssDecided {
						fresh = append(fresh, it)
					}
					continue
				}
			}
			if err := r.decideItem(ctx, rel, ranked[i], cands, byClean, lib, sum); err != nil {
				return err
			}
			if sum.remember && sum.Decided > rssDecided {
				fresh = append(fresh, it)
			}
		}
		sum.Items += ri.Items
		sum.New += ri.New
		sum.Requests += ri.Requests
		sum.Indexers = append(sum.Indexers, ri)
	}
	r.m.requests.Add(float64(sum.Requests))
	if err := r.routeActive(ctx, sum); err != nil {
		return err
	}
	// The cursors move only now, with the seen marks, once every item read has durable state (a route enqueued, or
	// a decision remembered): an error before this point leaves both, and the next sync reads the same pages.
	if err := r.markSeen(ctx, fresh, cursors); err != nil {
		return err
	}
	if len(failed) > 0 && len(failed) == len(all) {
		return fmt.Errorf("every RSS feed failed: %s", strings.Join(failed, ", "))
	}
	return nil
}

// rssEnd is why rssPages stopped paging.
type rssEnd int

const (
	// rssReached: the page holding the previous feed head was read (or there was no head: one page, as the arrs).
	rssReached rssEnd = iota
	// rssFeedEnd: the feed had no more (a short or empty page, or a page with nothing new: the offset is ignored).
	rssFeedEnd
	// rssCapped: rssCatchupPages full pages were read without the previous head; more lie beyond.
	rssCapped
	// rssBroken: a catch-up page failed; the pages before it are returned and the cursor stays.
	rssBroken
)

// rssCursor is a feed's head as the last sync recorded it: its itemKey and publish date (zero when unknown).
type rssCursor struct {
	indexer, key string
	published    time.Time
}

// rssPages reads the feed from its head, page by page, through the page that holds the previous feed head (cursor) or
// an item published before it (the arrs' test, so a head the indexer removed still ends catch-up), bounded like the arr indexer paging loops (Sonarr Indexers/HttpIndexerBase.cs:142-183; Radarr
// Indexers/HttpIndexerBase.cs:114). Every item of every page read is returned, those below the previous head too:
// the arrs evaluate the whole feed each sync, so an item rejected for a temporary reason is decided again while it
// stays in the feed. The cursor bounds only how far back catch-up pages. newest is the new head; nil when the cursor
// must not move.
func (r *Runner) rssPages(ctx context.Context, key string, cats []int, cursor rssCursor) (*indexer.Result, rssEnd, *rssCursor, error) {
	ik, list, _ := strings.Cut(key, "#") // a feed key: the indexer, or "<indexer>#<list>"
	ctx = gateway.WithPurpose(ctx, gateway.PurposeRSS)
	out := &indexer.Result{}
	var newest *rssCursor
	seen := map[string]bool{}
	eligibleSeen := map[string]bool{}
	for page := 0; ; page++ {
		if page == rssCatchupPages {
			return out, rssCapped, newest, nil
		}
		q := indexer.Query{Type: indexer.QueryRSS, Categories: cats, RSSOffset: page * rssPageSize, RSSLimit: rssPageSize,
			RSSFeed: list}
		res, err := r.idx.Search(ctx, ik, q)
		if err != nil {
			if page > 0 {
				out.Requests += indexer.RequestsSpent(err)
				out.Truncated = true
				out.Notes = append(out.Notes, fmt.Sprintf("RSS catch-up stopped at page %d: %v", page+1, err))
				// No new cursor: the next sync reads the gap again (the arrs keep the last release info on a failure).
				return out, rssBroken, nil, nil
			}
			return nil, rssBroken, nil, err
		}
		if !res.Cached && !res.Shared {
			out.Requests += res.Requests
		}
		out.Cached = out.Cached || res.Cached
		out.Shared = out.Shared || res.Shared
		out.SeaDex = append(out.SeaDex, res.SeaDex...)
		pageItems := res.RSSPage
		if pageItems == nil {
			pageItems = res.Releases
		}
		if len(pageItems) == 0 {
			return out, rssFeedEnd, newest, nil
		}
		if page == 0 {
			newest = &rssCursor{indexer: key, key: itemKey(pageItems[0]), published: pageItems[0].PublishDate}
		}
		// No head, nor a publish date to catch up to (a split list's first sync): one page, as the arrs.
		reached, added := cursor.key == "" && cursor.published.IsZero(), 0
		for _, rel := range pageItems {
			k := itemKey(rel)
			reached = reached || k == cursor.key ||
				!cursor.published.IsZero() && !rel.PublishDate.IsZero() && rel.PublishDate.Before(cursor.published)
			if !seen[k] {
				seen[k] = true
				added++
			}
		}
		for _, rel := range res.Releases {
			k := itemKey(rel)
			if !eligibleSeen[k] {
				out.Releases = append(out.Releases, rel)
				eligibleSeen[k] = true
			}
		}
		switch {
		case reached:
			// Without a cursor the arrs read one page (HttpIndexerBase: no lastReleaseInfo ends the loop).
			return out, rssReached, newest, nil
		case len(pageItems) < rssPageSize || added == 0:
			return out, rssFeedEnd, newest, nil
		}
	}
}

// candidates are, per title, the Live versions that are monitored and wanted or below their cutoff.
func (r *Runner) candidates(ctx context.Context, titles map[int64]decide.Title) (map[int64][]candidate, error) {
	keys := sortedKeys(r.rssVersions())
	if len(keys) == 0 {
		return nil, nil
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	rows, err := r.st.Reader().QueryContext(ctx, `SELECT id, title_id, policy_key FROM version
		WHERE monitored = 1 AND state IN ('wanted', 'filled', 'cutoff_met') AND policy_key IN (?`+strings.Repeat(", ?", len(keys)-1)+`)`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]candidate{}
	for rows.Next() {
		var id, tid int64
		var key string
		if err := rows.Scan(&id, &tid, &key); err != nil {
			return nil, err
		}
		t, ok := titles[tid]
		if !ok {
			continue
		}
		out[tid] = append(out[tid], candidate{title: t, version: decide.Version{ID: id, Key: key, Policy: r.pol.Versions[key]}})
	}
	return out, rows.Err()
}

// itemKey identifies a feed item: its hash, else its title and size.
func itemKey(rel indexer.Release) string {
	if rel.InfoHash != "" {
		return "h:" + rel.InfoHash
	}
	sum := sha1.Sum([]byte(fmt.Sprintf("%s\x00%d", strings.ToLower(rel.Title), rel.Size)))
	return "t:" + hex.EncodeToString(sum[:])
}

// seenKey is the item's itemKey under the policy revision it is decided under: a decision remembered under one
// revision (a profile rejection, say) is decided again once the policy changes.
func (r *Runner) seenKey(rel indexer.Release) string {
	if r.pol.Revision == "" {
		return itemKey(rel)
	}
	return itemKey(rel) + "@" + r.pol.Revision
}

// seenItem is a feed item: its indexer and seenKey.
type seenItem struct{ indexer, key string }

// unseen reports whether the item was not seen (and decided) by an earlier read.
func (r *Runner) unseen(ctx context.Context, it seenItem) (bool, error) {
	var n int
	err := r.st.Writer().QueryRowContext(ctx, `SELECT COUNT(*) FROM shadow_rss_seen WHERE indexer = ? AND item_key = ?`,
		it.indexer, it.key).Scan(&n)
	return n == 0, err
}

// markSeen records the items a read decided and the feeds' new heads (cursors), in one transaction.
func (r *Runner) markSeen(ctx context.Context, items []seenItem, cursors []rssCursor) error {
	if len(items) == 0 && len(cursors) == 0 {
		return nil
	}
	now := store.Millis(r.now())
	return r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, it := range items {
			if _, err := tx.ExecContext(ctx, `INSERT INTO shadow_rss_seen (indexer, item_key, seen_at) VALUES (?, ?, ?)
				ON CONFLICT DO NOTHING`, it.indexer, it.key, now); err != nil {
				return err
			}
		}
		for _, c := range cursors {
			var pub any
			if !c.published.IsZero() {
				pub = store.Millis(c.published)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO shadow_rss_cursor (indexer, item_key, updated_at, published_at)
				VALUES (?, ?, ?, ?) ON CONFLICT(indexer) DO UPDATE SET item_key = excluded.item_key,
				updated_at = excluded.updated_at, published_at = excluded.published_at`, c.indexer, c.key, now, pub); err != nil {
				return err
			}
		}
		return nil
	})
}

// decideItem maps an item to our titles (the indexer's ids, else the parsed clean title) and decides it for every
// candidate version; accepted decisions are collected for routing. ranked is rel with the cache evidence the decision ranks by;
// picks and routes carry rel, so the route orders its provider accounts by its own reads.
func (r *Runner) decideItem(ctx context.Context, rel, ranked indexer.Release, cands map[int64][]candidate,
	byClean map[string][]int64, lib decide.Library, sum *RSSSummary) error {
	ctx = decide.WithParseMemo(ctx)
	// Anime shares a category with films; episode/batch syntax remains authoritative even when no series matches.
	if isTV(rel) && (!isAnime(rel) || animeEpisodeItem(ctx, rel)) {
		return nil
	}
	var ids []int64
	if rel.IMDb != "" {
		if t := lib.ByIMDb(rel.IMDb); t != nil {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) == 0 && rel.TMDb > 0 {
		if t := lib.ByTMDb(rel.TMDb); t != nil {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) == 0 {
		p := decide.ParseMovieRelease(ctx, rel.Title)
		if p != nil {
			for _, n := range p.MovieTitles {
				for _, id := range byClean[radarr.CleanMovieTitle(n)] {
					tt := cands[id]
					// Match the parsed year before deciding identity. A yearless name must identify just one library movie.
					fits := p.Year < 1800
					if !fits {
						// Dated releases need a matching primary or secondary year among the candidates.
						for _, c := range tt {
							fits = fits || c.title.Year == p.Year || c.title.SecondaryYear == p.Year
						}
					}
					if fits && !slices.Contains(ids, id) {
						ids = append(ids, id)
					}
				}
			}
		}
		if len(ids) != 1 {
			return nil
		}
	}
	matched := false
	for _, id := range ids {
		for _, c := range cands[id] {
			matched = true
			available, err := availability.Movie(ctx, r.st.Reader(), r.pol, c.title.ID, c.version.ID, c.version.Key, r.now())
			if err != nil {
				return err
			}
			if !available {
				sum.remember = false
				continue
			}
			existing, err := decide.LoadExisting(ctx, r.st.Reader(), c.version.ID)
			if err != nil {
				return err
			}
			d, err := r.eng.Decide(ctx, decide.Release{Release: ranked}, c.title, c.version, existing)
			if err != nil {
				return err
			}
			r.recordCeiling(ctx, c.title.ID, c.version.Key, "movie", "rss", []*decide.Decision{d}, map[*decide.Decision]indexer.Release{d: rel})
			sum.Decided++
			if !d.Accepted || d.MatchType == decide.MatchNone {
				sum.remember = sum.remember && permanentRSS(d)
				continue
			}
			sum.remember = false
			sum.collect(c, rel, d)
		}
	}
	if matched {
		sum.Matched++
	}
	return nil
}

// Arr rejection types describe the decision, not whether a later sync can change it. Use a narrow list of
// release/profile facts; unknown titles, missing episodes, availability and existing-file checks can change.
func permanentRSS(d *decide.Decision) bool {
	pending := false
	for _, rejection := range d.Rejections {
		if rejection.Reason == "SeaDexQualityPending" {
			pending = true
		}
	}
	for _, rejection := range d.Rejections {
		switch rejection.Reason {
		case "QualityNotWanted", "CustomFormatMinimumScore":
			if !pending {
				return true
			}
		case "WantedLanguage", "IndexerNotInVersion", "Blocklisted":
			return true
		}
	}
	return false
}

var rssEpisodeRE = regexp.MustCompile(`(?i)\b(?:S\d+[ ._-]*E\d+|\d+x\d+)\b`)

var rssBatchRE = regexp.MustCompile(`(?i)\b(?:batch|complete\s+(?:series|season))\b`)

// animeEpisodeItem distinguishes series releases within TV/Anime from standalone anime movies.
func animeEpisodeItem(ctx context.Context, rel indexer.Release) bool {
	if rel.TVDB > 0 || rssBatchRE.MatchString(rel.Title) {
		return true
	}
	p := decide.ParseSeriesRelease(ctx, rel.Title)
	// Sonarr also reads a bare film year as compact season/episode numbering (1988 -> S19E88).
	// Keep the movie path when the only episode number is precisely that year.
	if movie := decide.ParseMovieRelease(ctx, rel.Title); movie != nil && movie.Year >= 1800 && p != nil &&
		len(p.EpisodeNumbers) == 1 && len(p.AbsoluteEpisodeNumbers) == 0 && !rssEpisodeRE.MatchString(rel.Title) &&
		p.SeasonNumber*100+p.EpisodeNumbers[0] == movie.Year {
		return false
	}
	return p != nil && (p.FullSeason || p.IsMultiSeason || p.IsSeasonExtra || p.Special || p.AirDate != nil ||
		len(p.EpisodeNumbers) > 0 || len(p.AbsoluteEpisodeNumbers) > 0 || len(p.SpecialAbsoluteEpisodeNumbers) > 0)
}
