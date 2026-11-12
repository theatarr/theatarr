package demo

import (
	"fmt"
	"strings"
	"time"
)

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
	job("shadow.rss", "{}", "failed", s.ago(5*time.Hour), 5, "rss: every indexer is backing off")
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
