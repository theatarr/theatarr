package demo

import (
	"fmt"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/shadow"
)

// outcome is one shadow diff's bucket and reason.
type outcome struct{ bucket, reason, detail string }

// shadowCycle spreads the arr grabs over every bucket, about 80% agreeing (the exit bar of 95% is not met, as on a
// real instance part-way through its shadow period).
var shadowCycle = []outcome{
	{shadow.BucketAgree, shadow.ReasonSameRelease, "we would grab the same release"},
	{shadow.BucketAgree, shadow.ReasonSameRelease, "we would grab the same release"},
	{shadow.BucketAnother, shadow.ReasonBetterQuality, "our search found a better quality"},
	{shadow.BucketAgree, shadow.ReasonSameQualityScore, "another release of the same quality and score"},
	{shadow.BucketAgree, shadow.ReasonSameRelease, "we would grab the same release"},
	{shadow.BucketWouldnt, "quality_not_wanted", "the profile does not want this quality"},
	{shadow.BucketAgree, shadow.ReasonNotSearched, "accepted; not searched, so only the arr's release was decided"},
	{shadow.BucketAgree, shadow.ReasonSameRelease, "we would grab the same release"},
	{shadow.BucketAnother, shadow.ReasonHigherScore, "a release with a higher format score"},
	{shadow.BucketAgree, shadow.ReasonManualAccepted, "the arr grabbed it by hand; we accept it"},
	{shadow.BucketAgree, shadow.ReasonSameRelease, "we would grab the same release"},
	{shadow.BucketWouldnt, shadow.ReasonManualRejected, "the arr grabbed it by hand; we reject it (below the minimum score)"},
	{shadow.BucketAgree, shadow.ReasonSameRSSPick, "an RSS grab our RSS read picked too"},
	{shadow.BucketAgree, shadow.ReasonSameRelease, "we would grab the same release"},
	{shadow.BucketAnother, shadow.ReasonNewerRelease, "a newer release of the same quality"},
	{shadow.BucketAgree, shadow.ReasonSameRelease, "we would grab the same release"},
	{shadow.BucketWouldnt, "below_min_score", "the format score is below the profile's minimum"},
	{shadow.BucketAgree, shadow.ReasonSameRelease, "we would grab the same release"},
}

// shadow writes the shadow runner's rows: re-decided arr grabs with their diffs, RSS picks, router dry runs, the
// cursors and run log.
func (s *seeder) shadow() {
	n := 0
	for _, g := range s.grabs {
		if strings.HasPrefix(g.t.scenario, "ceiling_") {
			continue
		}
		if g.rel.at.Before(s.ago(days(30))) || g.grabID == 0 {
			continue
		}
		o := shadowCycle[n%len(shadowCycle)]
		if o.reason == shadow.ReasonSameRelease && g.t.kind == "series" && n%5 == 0 {
			o = outcome{shadow.BucketAgree, shadow.ReasonParserMode, "a difference the version's Sonarr parser mode explains"}
		}
		n++
		s.shadowGrab(g, o)
	}
	// RSS picks: two the arrs never followed (we'd grab, arr didn't), one still open, one linked.
	var wanted []*title
	for _, t := range s.titles {
		if t.kind == "movie" && (t.scenario == scMissing || t.scenario == scPartial) && len(t.versions) > 0 {
			wanted = append(wanted, t)
		}
	}
	for i, t := range wanted {
		if i >= 4 {
			break
		}
		v := t.versions[0]
		rel := s.movieRelease(t, quality(v, false))
		seen := s.ago(days(float64(2 + i*3)))
		status := []string{"expired", "expired", "open", "expired"}[i]
		dec := shadow.Decided{Release: rel.name, Indexer: rel.indexer, InfoHash: rel.hash, Size: rel.size, Seeders: 40,
			Accepted: true, Quality: rel.quality, Formats: formatsFor(rel), Score: rel.score}
		pick := s.exec(`INSERT INTO shadow_rss_pick (indexer, release_title, info_hash, title_id, version_key, seen_at,
			published_at, quality, score, decision, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, rel.indexer, rel.name,
			rel.hash, t.id, v.key, ms(seen), ms(seen.Add(-time.Hour)), rel.quality, rel.score, jsonOf(dec), status)
		if status == "expired" {
			diff := s.exec(`INSERT INTO shadow_diff (at, title_id, version_key, bucket, reason, detail, searched, rss_pick_id,
				our_release, our_quality, our_score, created_at, kind) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, 'movie')`,
				ms(seen), t.id, v.key, shadow.BucketRSSOnly, shadow.ReasonNoArrGrab,
				"we would grab it from RSS; no arr grab followed within 24h", pick, rel.name, rel.quality, rel.score,
				ms(seen.Add(24*time.Hour)))
			s.exec(`UPDATE shadow_rss_pick SET diff_id = ? WHERE id = ?`, diff, pick)
		}
	}
	for _, name := range s.instances() {
		var last int64
		_ = s.tx.QueryRowContext(s.ctx, `SELECT COALESCE(MAX(arr_id), 0) FROM arr_history WHERE instance = ?`, name).Scan(&last)
		s.exec(`INSERT INTO shadow_cursor (instance, history_id, updated_at) VALUES (?, ?, ?)`, name, last, ms(s.ago(10*time.Minute)))
	}
	for k := range 8 {
		start := s.ago(time.Duration(k)*30*time.Minute + 5*time.Minute)
		s.exec(`INSERT INTO shadow_run (kind, started_at, finished_at, ok, summary) VALUES ('shadow.rss', ?, ?, ?, ?)`,
			ms(start), ms(start.Add(4*time.Second)), b01(k != 3), jsonOf(map[string]any{"items": 180 + k*7, "new": 6 + k,
				"matched": 3, "decided": 3, "picks": k % 2, "requests": 3, "took_ms": 3900 + k*41}))
		if k%2 == 0 {
			s.exec(`INSERT INTO shadow_run (kind, started_at, finished_at, ok, summary) VALUES ('arr.import', ?, ?, 1, ?)`,
				ms(start), ms(start.Add(9*time.Second)), jsonOf(map[string]any{"instances": len(s.instances()), "new_history": k}))
		}
	}
}

// shadowGrab writes one re-decided arr grab and its diff.
func (s *seeder) shadowGrab(g *arrGrab, o outcome) {
	t, v, rel := g.t, g.v, g.rel
	arr := shadow.Decided{Release: rel.name, Indexer: indexerLabel(rel.indexer), Seeders: -1, Arr: true, Accepted: true,
		Quality: rel.quality, Formats: formatsFor(rel), Score: rel.score}
	pick := arr
	pick.Arr, pick.Indexer, pick.InfoHash, pick.Size, pick.Seeders = false, rel.indexer, rel.hash, rel.size, 30+s.r.IntN(300)
	ourRel, ourQ, ourScore := rel.name, rel.quality, rel.score
	switch o.bucket {
	case shadow.BucketAnother:
		better := strings.Replace(rel.name, "-"+rel.group, "-"+s.pick(movieGroups), 1)
		if o.reason == shadow.ReasonBetterQuality {
			better = strings.Replace(better, "WEB-DL", "UHD.BluRay.REMUX", 1)
		}
		pick.Release, pick.Score, pick.InfoHash = better, rel.score+150+s.r.IntN(400), s.hash()
		ourRel, ourScore = better, pick.Score
	case shadow.BucketWouldnt:
		arr.Accepted = false
		reason := "QualityNotWanted"
		if o.reason == "below_min_score" || o.reason == shadow.ReasonManualRejected {
			reason = "CustomFormatMinimumScore"
		}
		arr.Rejections = []decide.Rejection{{Reason: reason, Message: rejectionText(reason)}}
		pick.Release = strings.Replace(rel.name, "-"+rel.group, "-"+s.pick(movieGroups), 1)
		pick.InfoHash = s.hash()
		ourRel = pick.Release
	}
	source := "Rss"
	if o.reason == shadow.ReasonManualAccepted || o.reason == shadow.ReasonManualRejected {
		source = "InteractiveSearch"
	} else if s.r.IntN(3) == 0 {
		source = "Search"
	}
	searched := o.reason != shadow.ReasonNotSearched
	var searchedAt any
	if searched {
		searchedAt = ms(rel.at.Add(2 * time.Minute))
	}
	var pickJSON any = jsonOf(pick)
	results := []shadow.Decided{pick}
	if searched {
		alt := pick
		alt.Release, alt.Score, alt.InfoHash = strings.Replace(pick.Release, "-", ".PROPER-", 1), pick.Score-200, s.hash()
		results = append(results, alt)
	}
	eps, hist := []int64{}, []int64{}
	for i, f := range rel.files {
		if f.ep != nil {
			eps = append(eps, f.ep.id)
		}
		hist = append(hist, g.grabID+int64(i))
	}
	existing := "none"
	if o.reason == shadow.ReasonHigherScore {
		existing = "version_file"
	}
	id := s.exec(`INSERT INTO shadow_grab (instance, history_id, title_id, version_key, grabbed_at, release_title,
		release_source, arr_indexer, arr_quality, arr_score, existing_source, state, searched, searched_at, search_note,
		result_count, accepted_count, found_arr, arr_decision, pick, results, created_at, updated_at, history_ids, episodes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'done', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, g.instance, g.grabID,
		t.id, v.key, ms(rel.at), rel.name, source, indexerLabel(rel.indexer), rel.quality, rel.score, existing,
		b01(searched), searchedAt, map[bool]string{true: "", false: "search off for this grab"}[searched],
		map[bool]int{true: 12 + s.r.IntN(40), false: 1}[searched], len(results), b01(searched), jsonOf(arr), pickJSON,
		jsonOf(results), ms(rel.at.Add(time.Minute)), ms(rel.at.Add(3*time.Minute)), jsonOf(hist), jsonOf(eps))
	var searchBetter any
	if o.reason == shadow.ReasonSameRSSPick {
		searchBetter = jsonOf(map[string]any{"release": strings.Replace(rel.name, "WEB-DL", "UHD.BluRay.REMUX", 1),
			"indexer": rel.indexer, "quality": rel.quality, "score": rel.score + 300})
	}
	s.exec(`INSERT INTO shadow_diff (at, title_id, version_key, bucket, reason, detail, searched, grab_id, our_release,
		our_quality, our_score, arr_release, arr_quality, arr_score, created_at, kind, search_better)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, ms(rel.at), t.id, v.key, o.bucket, o.reason, o.detail,
		b01(searched), id, ourRel, ourQ, ourScore, rel.name, rel.quality, rel.score, ms(rel.at.Add(3*time.Minute)), t.kind,
		searchBetter)
	// The router's dry run for this grab (every version is in shadow mode in the example policy).
	if id%3 != 0 {
		outcome, provider := "would_add", "rd"
		if id%7 == 0 {
			outcome, provider = "deferred", ""
		}
		steps := []map[string]string{{"release": ourRel, "provider": "rd", "outcome": "would_add", "code": "add",
			"message": "would add the bare magnet"}}
		if outcome == "deferred" {
			steps = []map[string]string{{"release": ourRel, "provider": "rd", "outcome": "deferred", "code": "paced",
				"message": "10 adds in the last minute"}}
		}
		plan := s.exec(`INSERT INTO router_plan (title_id, version_key, mode, outcome, release_title, indexer, info_hash,
			provider, steps, source, created_at) VALUES (?, ?, 'shadow', ?, ?, ?, ?, ?, ?, 'shadow.grab', ?)`, t.id, v.key,
			outcome, ourRel, rel.indexer, pick.InfoHash, provider, jsonOf(steps), ms(rel.at.Add(3*time.Minute)))
		s.exec(`UPDATE shadow_grab SET router_plan_id = ? WHERE id = ?`, plan, id)
	}
}

// system writes the operator's tables: jobs in every state, budgets, back-offs, provider pacing, the TVDB sync,
// API keys, the views' profiles and tags, recent view commands and offers.
func (s *seeder) system() {
	job := func(kind, payload, state string, created time.Time, attempts int, lastErr string, extra ...any) int64 {
		var finished, errText any
		if state == "done" || state == "failed" {
			finished = ms(created.Add(time.Duration(2+s.r.IntN(40)) * time.Second))
		}
		if lastErr != "" {
			errText = lastErr
		}
		runAt := created
		if state == "queued" {
			runAt = created.Add(6 * time.Hour)
		}
		id := s.exec(`INSERT INTO job (kind, payload, state, run_at, attempts, max_attempts, last_error, created_at,
			updated_at, finished_at) VALUES (?, ?, ?, ?, ?, 5, ?, ?, ?, ?)`, kind, payload, state, ms(runAt), attempts,
			errText, ms(created), ms(created), finished)
		return id
	}
	// Done: the regular work of a day.
	for k := range 6 {
		at := s.ago(time.Duration(k)*4*time.Hour + 20*time.Minute)
		job("provider.sync", "{}", "done", at, 1, "")
		job("arr.import", "{}", "done", at.Add(3*time.Minute), 1, "")
		job("shadow.rss", "{}", "done", at.Add(7*time.Minute), 1, "")
	}
	for k := range 3 {
		job("meta.art", "{}", "done", s.ago(time.Duration(k)*6*time.Hour+time.Hour), 1, "")
		job("db.nightly", "{}", "done", s.ago(days(float64(k)+0.4)), 1, "")
	}
	for _, g := range s.grabs[max(0, len(s.grabs)-6):] {
		job("shadow.grab", jsonOf(map[string]any{"instance": g.instance, "history_id": g.grabID}), "done", g.rel.at.Add(time.Minute), 1, "")
	}
	for i := int64(1); i <= min(s.runID, 6); i++ {
		job("acquire.search", jsonOf(map[string]any{"run": i}), "done", s.ago(time.Duration(i)*17*time.Minute), 1, "")
	}
	// Failed, resolved by a later success: AllDebrid blocked the location, a later sync of every provider worked.
	job("provider.sync", `{"provider":"ad"}`, "failed", s.ago(26*time.Hour), 5,
		"alldebrid: AUTH_BLOCKED: approve this location in your account")
	job("provider.sync", "{}", "done", s.ago(20*time.Hour), 1, "")
	// Failed and dismissed.
	d := job("meta.art", `{"title_id":7}`, "failed", s.ago(days(3)), 5, "tmdb: 429 Too Many Requests (back-off 10m)")
	s.exec(`UPDATE job SET dismissed_at = ?, dismissed_by = ? WHERE id = ?`, ms(s.ago(days(2.5))), Username, d)
	// Failed and retried; the retry worked.
	f := job("plex.scan", `{"section":19,"path":"/media/movies"}`, "failed", s.ago(days(1.2)), 5, "plex: connection refused")
	r := job("plex.scan", `{"section":19,"path":"/media/movies"}`, "done", s.ago(days(1.1)), 1, "")
	s.exec(`UPDATE job SET retry_of = ? WHERE id = ?`, f, r)
	// Failed, needing attention.
	job("shadow.grab", `{"instance":"sonarr_4k_debrid","history_id":99999}`, "failed", s.ago(5*time.Hour), 5,
		"search: every indexer of the version is backing off")
	job("acquire.research", `{"run":2}`, "failed", s.ago(9*time.Hour), 5, "indexer dmm: 503 Service Unavailable")
	// Queued: the next runs (theatarr serve schedules its own db.nightly).
	job("meta.art", `{"title_id":3}`, "queued", s.now.Add(-5*time.Hour), 0, "")
	job("acquire.wanted", "{}", "queued", s.now.Add(-5*time.Hour), 0, "")

	// Budgets: this hour's and day's counts, as the gateway persists them.
	hour, day := s.now.Truncate(time.Hour), s.now.Truncate(24*time.Hour)
	s.exec(`INSERT INTO gateway_budget_window (budget, period, window_start_at, count) VALUES ('seadex', 'hour', ?, 212),
		('torrentio', 'day', ?, 1840), ('tmdb-images', 'day', ?, 3120)`, ms(hour), ms(day), ms(day))
	s.exec(`INSERT INTO gateway_backoff (subject, until_at, failures, reason, updated_at) VALUES
		('indexer:zilean', ?, 3, '503 from upstream', ?), ('provider:tb', ?, 1, '429 Too Many Requests', ?),
		('budget:seadex', 0, 0, '', ?), ('metadata:anilist', ?, 2, 'Retry-After 90s', ?)`,
		ms(s.now.Add(40*time.Minute)), ms(s.ago(20*time.Minute)), ms(s.now.Add(time.Hour)), ms(s.ago(5*time.Minute)),
		ms(s.ago(days(1))), ms(s.now.Add(90*time.Second)), ms(s.ago(time.Minute)))
	min0 := s.now.Truncate(time.Minute)
	s.exec(`INSERT INTO router_pacing (provider, window_start, window_adds, hour_start, hour_uncached, cooldown_until,
		backoff_until, failures, reason, updated_at) VALUES
		('rd', ?, 4, ?, 2, 0, 0, 0, '', ?), ('ad', ?, 1, ?, 0, 0, 0, 0, '', ?),
		('tb', ?, 0, ?, 7, ?, ?, 2, '429 Too Many Requests', ?)`, ms(min0), ms(hour), ms(s.ago(time.Minute)), ms(min0),
		ms(hour), ms(s.ago(time.Minute)), ms(min0), ms(hour), ms(s.now.Add(4*time.Minute)), ms(s.now.Add(time.Hour)),
		ms(s.ago(time.Minute)))
	s.exec(`INSERT INTO tvdb_sync (id, since_at, run_at, records, invalidated, truncated) VALUES (1, ?, ?, 1840, 3, 0)`,
		ms(s.ago(time.Hour)), ms(s.ago(50*time.Minute)))

	// API keys: their hashes are of random values nobody holds; they only show what the keys page looks like.
	for i, name := range []string{"seerr", "homepage", "phone-app"} {
		var revoked any
		if i == 2 {
			revoked = ms(s.ago(days(9)))
		}
		h := s.hash() + s.hash()
		s.exec(`INSERT INTO api_key (name, prefix, key_hash, created_at, last_used_at, revoked_at) VALUES (?, ?, ?, ?, ?, ?)`,
			name, "tha_"+h[:7], h[:64], ms(s.ago(days(float64(90-i*20)))), ms(s.ago(time.Duration(i+1)*13*time.Minute)), revoked)
	}
	// The views' profiles (ids kept once given) and tags.
	for _, view := range sortedKeys(s.pol.Views) {
		for _, p := range sortedKeys(s.pol.Views[view].Profiles) {
			s.exec(`INSERT INTO arrview_profile (view, name, created_at) VALUES (?, ?, ?)`, view, p, ms(s.ago(days(200))))
		}
	}
	for _, tag := range []string{"family", "kids", "requested"} {
		s.exec(`INSERT INTO arrview_tag (label, created_at) VALUES (?, ?)`, tag, ms(s.ago(days(150))))
	}
	for i, t := range s.titles {
		if i%9 == 0 {
			s.exec(`INSERT INTO arrview_title_tag (version, title_id, tag_id) SELECT policy_key, title_id, ? FROM version
				WHERE title_id = ?`, 1+i%3, t.id)
		}
	}
	for i, name := range []string{"MoviesSearch", "RefreshMovie", "SeriesSearch", "RssSync"} {
		at := s.ago(time.Duration(i+1) * 37 * time.Minute)
		s.exec(`INSERT INTO arrview_command (view, name, body, status, result, message, user_agent, queued_at, started_at,
			ended_at) VALUES (?, ?, ?, 'completed', 'successful', ?, 'Seerr/2', ?, ?, ?)`,
			map[bool]string{true: "sonarr", false: "radarr"}[strings.HasPrefix(name, "Series")], name,
			jsonOf(map[string]any{"name": name}), "every version is in shadow mode; nothing searched", ms(at), ms(at), ms(at.Add(time.Second)))
	}
	// Offers: an imported movie offered to the title's other movie version.
	for i, t := range s.titles {
		if t.kind != "movie" || t.anime || len(t.versions) < 2 || i%11 != 1 {
			continue
		}
		outcome := []string{"declined", "same", "planned"}[i%3]
		s.exec(`INSERT INTO router_offer (version_key, title_id, info_hash, from_version, release_title, outcome, reasons,
			attempts, first_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`, t.versions[1].key, t.id, s.hash(),
			t.versions[0].key, dots(t.name)+fmt.Sprintf(".%d.2160p.UHD.BluRay.REMUX-OSPRYX", t.year), outcome,
			jsonOf([]string{"QualityNotWanted"}), ms(s.ago(days(4))), ms(s.ago(days(4))))
	}
}
