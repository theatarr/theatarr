package demo

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/journal/snapshot"
)

// registry adds content the library does not use (unreferenced, absent, gone) and the provider sync runs.
func (s *seeder) registry() {
	states := []string{"unreferenced", "unreferenced", "absent", "gone", "unknown"}
	for i, st := range states {
		name := fmt.Sprintf("%s.%d.2160p.WEB-DL.DDP5.1.H.265-%s", dots(s.pick(episodeAdj)+" "+s.pick(episodeNoun)),
			2015+i, s.pick(movieGroups))
		h := s.hash()
		at := s.between(days(300), days(20))
		id := s.exec(`INSERT INTO content (info_hash, name, mount_folder, size_bytes, state, first_seen_at, last_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, h, name, name, gib(9+i), st, ms(at), ms(s.ago(days(0.25))))
		s.exec(`INSERT INTO content_file (content_id, path, size_bytes) VALUES (?, ?, ?)`, id, name+".mkv", gib(9+i))
		pst, absent := "present", any(nil)
		if st == "absent" || st == "gone" {
			pst, absent = "absent", ms(s.ago(days(4)))
		}
		p := []string{"rd", "ad", "tb"}[i%3]
		s.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, folder_name, seen_at, state, status,
			progress, size_bytes, links, added_at, absent_at, duplicates) VALUES (?, ?, ?, ?, ?, ?, 'downloaded', 100, ?, 1, ?, ?, ?)`,
			id, p, s.torrentID(p), name, ms(at), pst, gib(9+i), ms(at), absent, i%2)
	}
	for _, p := range []string{"rd", "ad", "tb"} {
		for k := range 4 {
			start := s.ago(time.Duration(k)*6*time.Hour + 37*time.Minute)
			ok, errText := 1, any(nil)
			if p == "ad" && k == 0 {
				ok, errText = 0, "alldebrid: AUTH_BLOCKED: approve this location in your account"
			}
			var present, absent int
			_ = s.tx.QueryRowContext(s.ctx, `SELECT COUNT(*) FROM content_provider WHERE provider = ? AND state = 'present'`, p).Scan(&present)
			_ = s.tx.QueryRowContext(s.ctx, `SELECT COUNT(*) FROM content_provider WHERE provider = ? AND state = 'absent'`, p).Scan(&absent)
			s.exec(`INSERT INTO provider_sync_run (provider, started_at, finished_at, ok, error, summary) VALUES (?, ?, ?, ?, ?, ?)`,
				p, ms(start), ms(start.Add(time.Duration(2+s.r.IntN(9))*time.Second)), ok, errText, jsonOf(map[string]any{
					"provider": p, "torrents": present + absent + 40 + k, "present": present, "absent": absent,
					"new": k % 3, "requests": 1 + k%2}))
		}
	}
}

// record writes the record of the accounts (Admin › Torrents): every present torrent as of the provider's last good
// sync. It runs after the downloads, which add registry rows too.
func (s *seeder) record() {
	for _, p := range []string{"rd", "ad", "tb"} {
		// The record of the account (Admin › Torrents): its present torrents as of the last good sync.
		s.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash, name, size_bytes, status, state, progress,
			added_at, files, links) SELECT cp.provider, cp.torrent_id, c.info_hash, cp.folder_name, COALESCE(cp.size_bytes, 0),
			'downloaded', 'downloaded', 100, cp.added_at, -1, COALESCE(cp.links, -1) FROM content_provider cp
			JOIN content c ON c.id = cp.content_id WHERE cp.provider = ? AND cp.state = 'present'`, p)
		s.exec(`INSERT INTO provider_torrent_read (provider, read_at, torrents) SELECT ?, MAX(finished_at),
			(SELECT COUNT(*) FROM provider_torrent WHERE provider = ?) FROM provider_sync_run WHERE provider = ? AND ok = 1`,
			p, p, p)
	}
}

// attempt is one provider tried for a download.
type attempt struct {
	provider, outcome, code, message string
	retry                            bool
}

// dl describes one router download.
type dl struct {
	t          *title
	v          *version
	eps        []*episode
	state      string
	progress   float64
	provider   string
	status     string
	err        string
	age        time.Duration // since the grab
	cached     int           // 1 cached, 0 not, -1 unknown
	attempts   []attempt
	imp        string // library_import state
	research   string // the acquire_run research reason after a failure
	blocklist  string // router_blocklist source
	reasonWord string
	reason     string // the acquire_run reason: missing (the default) or cutoff (an upgrade)
}

var (
	rdCached = []attempt{{"rd", "accepted", "add", "added; cached on Real-Debrid", false}}
	adAfter  = []attempt{{"rd", "refused", "infringing_file", "Real-Debrid refuses this file", false},
		{"ad", "accepted", "add", "added; cached on AllDebrid", false}}
	tbAfter = []attempt{{"rd", "skipped", "rd_name_rule", "Real-Debrid refuses this name", false},
		{"ad", "error", "timeout", "no answer in 30s", false}, {"tb", "accepted", "add", "added; TorBox fetches it", false}}
	paced    = []attempt{{"rd", "deferred", "paced", "10 adds in the last minute", true}}
	noneTook = []attempt{{"rd", "refused", "infringing_file", "Real-Debrid refuses this file", false},
		{"ad", "refused", "not_cached", "not cached on AllDebrid and uncached adds are off", false},
		{"tb", "skipped", "known_uncached", "known uncached on TorBox", false}}
)

// downloads writes the router's downloads in every state, each with the search behind it (journal snapshot and
// acquire run), its provider attempts and cache evidence, and the library's side: imports, links and actions.
func (s *seeder) downloads() {
	m := func(name string, vi int) (*title, *version) {
		t := s.byName[name]
		if t == nil || vi >= len(t.versions) {
			return nil, nil
		}
		return t, t.versions[vi]
	}
	var list []dl
	addMovie := func(name string, vi int, d dl) {
		t, v := m(name, vi)
		if t == nil {
			return
		}
		d.t, d.v = t, v
		list = append(list, d)
	}
	// Series downloads are Theatarr's own: they go to Theatarr's copy when the policy has one, else to the first version.
	mine := func(t *title) *version {
		if cp, _ := t.theatarrCopy(); cp != nil {
			return cp
		}
		return t.versions[0]
	}
	addEps := func(name string, d dl, pick func(t *title) []*episode) {
		t, _ := m(name, 0)
		if t == nil {
			return
		}
		d.t, d.v, d.eps = t, mine(t), pick(t)
		if len(d.eps) == 0 {
			return
		}
		list = append(list, d)
	}
	// addCopy adds a download for Theatarr's copy only: nothing without one.
	addCopy := func(name string, d dl, pick func(t *title) []*episode) {
		if t := s.byName[name]; t != nil {
			if cp, _ := t.theatarrCopy(); cp != nil {
				addEps(name, d, pick)
			}
		}
	}
	wantedIn := func(season int) func(t *title) []*episode {
		return func(t *title) []*episode {
			var out []*episode
			for _, e := range t.episodes {
				if e.aired(s.now) && s.episodeState(t, mine(t), e) == "wanted" && (season == 0 || e.season == season) {
					out = append(out, e)
				}
			}
			return out
		}
	}
	firstWanted := func(t *title) []*episode {
		w := wantedIn(0)(t)
		if len(w) > 0 {
			return w[:1]
		}
		return nil
	}
	latest := func(t *title) []*episode {
		if e := s.latestAired(t); e != nil {
			return []*episode{e}
		}
		return nil
	}

	// In hand: one per state.
	addMovie("The Vellichor Tapes", 1, dl{state: "queued", age: 3 * time.Minute, cached: -1})
	addMovie("Cinderhook", 0, dl{state: "queued", age: 9 * time.Minute, cached: 1, attempts: paced,
		err: "waiting: rd paced (10 adds a minute)"})
	addMovie("Undertow Signal", 1, dl{state: "adding", age: 40 * time.Second, provider: "rd", cached: 1})
	addEps("Lantern Street Station", dl{state: "added", age: 6 * time.Minute, provider: "ad", status: "queued",
		cached: 0, attempts: adAfter}, wantedIn(2))
	addMovie("Glasswing Protocol", 0, dl{state: "downloading", progress: 37.4, age: 25 * time.Minute, provider: "rd",
		status: "downloading", cached: 0, attempts: rdCached})
	addMovie("Corvid Hour", 0, dl{state: "downloading", progress: 64.2, age: 31 * time.Hour, provider: "tb",
		status: "downloading", cached: 0, attempts: tbAfter})
	addEps("Ferrous Bay", dl{state: "downloading", progress: 5.8, age: 12 * time.Minute, provider: "rd",
		status: "downloading", cached: 0, attempts: rdCached}, wantedIn(3))
	addMovie("Ninefold Lantern", 0, dl{state: "ready", age: 8 * time.Hour, provider: "rd", status: "downloaded",
		cached: 1, attempts: rdCached, imp: "waiting_mount"})
	addMovie("Kestrel Nine", 0, dl{state: "ready", age: 50 * time.Minute, provider: "ad", status: "downloaded",
		cached: 1, attempts: adAfter, imp: "dry_run"})
	addEps("Yumegatari Express", dl{state: "importing", age: 15 * time.Minute, provider: "rd", status: "downloaded",
		cached: 1, attempts: rdCached, imp: "waiting_mount"}, firstWanted)
	addEps("Shirokane Blade Academy", dl{state: "queued", age: 2 * time.Minute, cached: -1}, firstWanted)
	// A release named for the wrong year: the journal flags it.
	addMovie("Hollowmark", 0, dl{state: "downloading", progress: 12.0, age: 40 * time.Minute, provider: "rd",
		status: "downloading", cached: 0, attempts: rdCached, reasonWord: "wrongyear"})
	// The library views' last two stages: an upgrade being linked, and a finished download the library refused.
	addMovie("Tidewater Requiem", 0, dl{state: "importing", age: 3 * time.Minute, provider: "rd", status: "downloaded",
		cached: 1, attempts: rdCached})
	addMovie("The Unbound Cartographer", 0, dl{state: "ready", age: 35 * time.Minute, provider: "rd",
		status: "downloaded", cached: 1, attempts: rdCached, imp: "rejected"})

	// Done: imported, with links (Theatarr's own files: the anime films and the newest episode of each airing series).
	for _, name := range []string{"Kumoyuki no Hanabira", "Tsukimori Station", "Aokaze Requiem: The Movie"} {
		addMovie(name, 0, dl{state: "imported", age: days(float64(3 + len(name)%40)), provider: "rd",
			status: "downloaded", cached: 1, attempts: rdCached, imp: "linked"})
	}
	for _, name := range theatarrLatest {
		t := s.byName[name]
		if t == nil {
			continue
		}
		e := s.latestAired(t)
		if e == nil {
			continue
		}
		addEps(name, dl{state: "imported", age: s.now.Sub(e.air) - 2*time.Hour, provider: "rd", status: "downloaded",
			cached: 1, attempts: rdCached, imp: "linked"}, latest)
	}

	// Theatarr's copies of the series (copyPlans): their own gaps in flight, an upgrade, a filled gap, failures.
	addCopy("The Harrowgate Files", dl{state: "queued", age: 4 * time.Minute, cached: -1}, wantedIn(3))
	addCopy("Kiraboshi Mechanics", dl{state: "downloading", progress: 72.5, age: 18 * time.Minute, provider: "rd",
		status: "downloading", cached: 0, attempts: rdCached}, firstWanted)
	addCopy("The Pellbrook Inquiries", dl{state: "downloading", progress: 23.0, age: 9 * time.Minute, provider: "ad",
		status: "downloading", cached: 0, attempts: adAfter, reason: "cutoff"}, func(t *title) []*episode {
		for _, e := range t.episodes {
			if e.season == 1 && e.number == 2 && s.episodeState(t, mine(t), e) == "filled" {
				return []*episode{e}
			}
		}
		return nil
	})
	addCopy("Kindling Hall", dl{state: "imported", age: days(6), provider: "rd", status: "downloaded", cached: 1,
		attempts: rdCached, imp: "linked"}, func(t *title) []*episode {
		var out []*episode
		for _, e := range t.episodes {
			if e.season > 0 && e.aired(s.now) && s.arrEpisodeState(t, e) == "wanted" {
				out = append(out, e)
			}
		}
		return out
	})
	addCopy("Brindlewood Bakers", dl{state: "failed", age: days(1), provider: "ad", status: "magnet_conversion",
		cached: 0, attempts: adAfter, err: "stalled: not ready 10m0s after the add (provider status magnet_conversion)",
		blocklist: "stalled", research: "stalled"}, wantedIn(2))
	addCopy("The Quiet Ledger", dl{state: "failed", age: 7 * time.Hour, cached: 0, attempts: noneTook,
		err: "no provider took the release: rd infringing_file, ad not_cached, tb known_uncached"}, firstWanted)

	// Done: failed, in each way.
	addMovie("Quartz and Quarrel", 1, dl{state: "failed", age: 5 * time.Hour, cached: 0, attempts: noneTook,
		err: "no provider took the release: rd infringing_file, ad not_cached, tb known_uncached"})
	for i, ago := range []float64{9, 4, 0.3} {
		d := dl{state: "failed", age: days(ago), provider: "ad", status: "magnet_conversion", cached: 0,
			attempts: adAfter, err: "stalled: not ready 10m0s after the add (provider status magnet_conversion)",
			blocklist: "stalled", research: "stalled"}
		if i == 1 {
			d = dl{state: "failed", age: days(ago), provider: "rd", status: "error", cached: 0, attempts: rdCached,
				err: "provider error: torrent status error (dead magnet)", blocklist: "provider", research: "failed"}
		}
		addMovie("Bramblecourt", 0, d)
	}
	addMovie("Hakuren Garden", 0, dl{state: "failed", age: days(1.5), provider: "rd", status: "downloading",
		progress: 41, cached: 0, attempts: rdCached, err: "removed from the queue by admin"})
	addMovie("Fennick's Last Engine", 0, dl{state: "failed", age: days(2), provider: "rd", status: "downloaded",
		cached: 1, attempts: rdCached, imp: "rejected", blocklist: "library", research: "rejected",
		err: "library: rejected by the sample guard: the file runs 4 min; the movie runs 128", reasonWord: "extras"})

	marked := map[string]int64{}
	for _, d := range list {
		id := s.download(d)
		if d.reasonWord != "" {
			marked[d.reasonWord] = id
		}
	}
	s.deadLinks()
	s.verdicts(marked["wrongyear"])
	s.libraryExtras()
}

// download writes one download with everything around it and returns its id.
func (s *seeder) download(d dl) int64 {
	t, v := d.t, d.v
	created := s.now.Add(-d.age)
	q := quality(v, false)
	var rel *release
	if t.kind == "movie" {
		rel = s.movieRelease(t, q)
		switch d.reasonWord {
		case "wrongyear":
			rel.name = strings.Replace(rel.name, fmt.Sprint(t.year), fmt.Sprint(t.year-4), 1)
		case "extras":
			rel.name = strings.Replace(rel.name, ".2160p.", ".Extras.2160p.", 1)
		}
		rel.files[0].name = rel.name + ".mkv"
	} else {
		rel = s.seriesRelease(t, q, d.eps, len(d.eps) > 1)
	}
	rel.at = created
	// The search behind it: an acquire run and its decided candidates.
	target, reason := "movie", "missing"
	if d.reason != "" {
		reason = d.reason
	}
	if t.kind == "series" {
		target = map[bool]string{true: "season", false: "episode"}[len(d.eps) > 1]
	}
	s.runID++
	runID := s.runID
	results := 6 + s.r.IntN(30)
	s.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, reason, season, episodes,
		state, results, accepted, message, created_at, updated_at) VALUES (?, 'wanted', ?, ?, ?, ?, ?, ?, ?, 'routed', ?, ?,
		?, ?, ?)`, runID, t.id, v.id, v.key, target, reason, seasonOf(d.eps), jsonOf(epIDs(d.eps)), results, 3,
		fmt.Sprintf("routed %s", rel.name), ms(created.Add(-20*time.Second)), ms(created))
	s.snapshot(t, v, rel, target, runID, results, created.Add(-15*time.Second))
	source := fmt.Sprintf("acquire:%d", runID)

	var provider, tid any
	if d.provider != "" {
		provider, tid = d.provider, s.torrentID(d.provider)
	}
	var cached any
	if d.cached >= 0 {
		cached = d.cached
	}
	var added, ready, imported any
	if d.provider != "" && d.state != "adding" {
		added = ms(created.Add(20 * time.Second))
	}
	progress := d.progress
	switch d.state {
	case "ready", "importing", "imported":
		progress = 100
		ready = ms(created.Add(2 * time.Minute))
	case "failed":
		if d.imp == "rejected" {
			progress, ready = 100, ms(created.Add(2*time.Minute))
		}
	}
	if d.state == "imported" {
		imported = ms(created.Add(5 * time.Minute))
	}
	updated := created.Add(time.Duration(min(d.age, 10*time.Minute)) / 2)
	switch d.state {
	case "downloading":
		updated = s.ago(30 * time.Second)
	case "adding":
		// The router fails a download left in adding for 10 minutes (a crash between the add and its record). The
		// demo's stays in adding: its last update sits ahead of the anchor.
		updated = s.now.AddDate(1, 0, 0)
	}
	var errText any
	if d.err != "" {
		errText = d.err
	}
	name := ""
	if d.provider != "" {
		name = rel.name
	}
	id := s.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, size_bytes, episodes, provider, provider_torrent_id, provider_status, name, progress, cached,
		files_selected, reused, state, error, source, created_at, updated_at, added_at, ready_at, imported_at, polled_at,
		formats, own_add) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.id, v.id, v.key, rel.hash, rel.name, rel.indexer, rel.quality, rel.score, rel.size, jsonOf(epIDs(d.eps)),
		provider, tid, d.status, name, progress, cached, b01(d.provider != ""), d.state, errText, source, ms(created),
		ms(updated), added, ready, imported, ms(s.ago(time.Minute)), jsonOf(formatsFor(rel)), b01(d.provider != ""))
	for i, a := range d.attempts {
		var retry any
		if a.retry {
			retry = ms(s.now.Add(40 * time.Second))
		}
		s.exec(`INSERT INTO download_attempt (download_id, provider, outcome, code, message, retry_at, attempted_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, id, a.provider, a.outcome, a.code, a.message, retry,
			ms(created.Add(time.Duration(i)*2*time.Second)))
	}
	// Cache evidence for the hash.
	for _, p := range []string{"rd", "ad", "tb"} {
		state, src := "uncached", "feed:dmm-"+p
		if d.cached == 1 && p == "rd" || d.cached == 1 && p == d.provider {
			state = "cached"
		}
		if p == "tb" {
			src = "torbox:checkcached"
		}
		if d.cached == -1 && p != "rd" {
			continue
		}
		s.exec(`INSERT OR IGNORE INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?)`, rel.hash, p, state, src, ms(created.Add(-time.Minute)), ms(created.Add(23*time.Hour)))
	}
	// The library's side.
	if d.imp != "" {
		var waiting, lastErr any
		attempts := 1
		if d.imp == "waiting_mount" {
			waiting = ms(created.Add(2 * time.Minute))
			attempts = 4
		}
		if d.imp == "rejected" {
			lastErr = "sample guard: the file runs 4 min; the movie runs 128"
		}
		s.exec(`INSERT INTO library_import (download_id, state, mount_folder, waiting_since, attempts, last_error, plan,
			created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, d.imp, rel.name, waiting, attempts, lastErr,
			jsonOf(map[string]any{"version": v.key, "files": len(rel.files)}), ms(created.Add(2*time.Minute)),
			ms(created.Add(3*time.Minute)))
	}
	switch {
	case d.state == "imported":
		s.link(t, v, rel, id, created.Add(5*time.Minute), "active")
		s.exec(`INSERT INTO content_version (content_id, version_id, release_title, source, grabbed_at, imported_at)
			VALUES (?, ?, ?, 'router', ?, ?) ON CONFLICT DO NOTHING`, s.content[rel.hash], v.id, rel.name, ms(created),
			ms(created.Add(5*time.Minute)))
		s.exec(`INSERT INTO notify_event (kind, ref, created_at) VALUES ('download', ?, ?)`, id, ms(created.Add(5*time.Minute)))
		if t.kind == "movie" && !s.historyShown {
			s.historyShown = true
			s.historyExtras(t, v, rel, id, created.Add(5*time.Minute))
		}
	case d.imp == "rejected":
		s.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, path, detail) VALUES (?, 'reject',
			'applied', ?, ?, ?, ?)`, ms(created.Add(3*time.Minute)), v.key, id, rel.files[0].name,
			"sample guard: the file runs 4 min; the movie runs 128")
	case d.imp == "dry_run":
		s.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, path, target, detail) VALUES (?, 'link',
			'dry_run', ?, ?, ?, ?, 'the library is in dry-run mode')`, ms(created.Add(3*time.Minute)), v.key, id,
			s.libraryPath(t, v, rel, rel.files[0]), s.target(rel, rel.files[0]))
	}
	if d.blocklist != "" {
		s.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, download_id,
			created_at, researched_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, v.key, rel.hash, t.id, rel.name,
			firstClause(d.err), d.blocklist, id, ms(created.Add(11*time.Minute)), ms(created.Add(12*time.Minute)))
	}
	if d.research != "" {
		s.runID++
		s.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, reason, state, results,
			accepted, message, research, created_at, updated_at) VALUES (?, 'wanted', ?, ?, ?, ?, 'missing', 'nothing', ?, 0,
			?, ?, ?, ?)`, s.runID, t.id, v.id, v.key, target, 4+s.r.IntN(9), "no accepted release left after the blocklist",
			d.research, ms(created.Add(12*time.Minute)), ms(created.Add(13*time.Minute)))
	}
	return id
}

func firstClause(s string) string {
	if i := strings.Index(s, " ("); i > 0 {
		return s[:i]
	}
	return s
}

func seasonOf(eps []*episode) any {
	if len(eps) == 0 {
		return nil
	}
	return eps[0].season
}

func epIDs(eps []*episode) []int64 {
	out := []int64{}
	for _, e := range eps {
		out = append(out, e.id)
	}
	return out
}

func formatsFor(rel *release) []decide.FormatScore {
	switch {
	case strings.Contains(rel.name, "REMUX"):
		return []decide.FormatScore{{Name: "Remux Tier 01", Score: rel.score - 600}, {Name: "TrueHD ATMOS", Score: 300},
			{Name: "DV HDR10", Score: 300}}
	case strings.Contains(rel.name, "WEB"):
		return []decide.FormatScore{{Name: "WEB Tier 01", Score: rel.score - 400}, {Name: "DD+ ATMOS", Score: 200},
			{Name: "DV HDR10", Score: 200}}
	}
	return []decide.FormatScore{{Name: "BD Release Group", Score: rel.score}}
}

// snapshot records the search behind a download: the chosen release, runners-up and rejected results.
func (s *seeder) snapshot(t *title, v *version, rel *release, target string, runID int64, results int, at time.Time) {
	if s.err != nil {
		return
	}
	mk := func(name, q string, score int, accepted bool, reasons ...string) *decide.Decision {
		d := &decide.Decision{Release: name, Version: v.key, Accepted: accepted, Quality: q, Score: score, Parsed: true,
			Formats: []decide.FormatScore{{Name: strings.SplitN(q, "-", 2)[0] + " Tier", Score: score}}}
		for _, r := range reasons {
			d.Rejections = append(d.Rejections, decide.Rejection{Reason: r, Message: rejectionText(r)})
		}
		return d
	}
	chosen := mk(rel.name, rel.quality, rel.score, true)
	chosen.Formats = formatsFor(rel)
	alt1 := strings.Replace(rel.name, "-"+rel.group, "-"+s.pick(movieGroups), 1)
	alt2 := strings.Replace(strings.Replace(rel.name, "2160p", "1080p", 1), "-"+rel.group, "-"+s.pick(movieGroups), 1)
	ranked := []*decide.Decision{
		chosen,
		mk(alt1, rel.quality, rel.score-150-s.r.IntN(200), true),
		mk(alt1+".REPACK", rel.quality, rel.score-400, true),
		mk(alt2, "WEBDL-1080p", 900, false, "QualityNotWanted"),
		mk(strings.Replace(rel.name, rel.group, "CAMRIP", 1), "CAM", 0, false, "QualityNotWanted", "CustomFormatMinimumScore"),
		mk(strings.Replace(alt1, "2160p", "2160p.HDR10Plus", 1), rel.quality, rel.score-900, false, "CustomFormatMinimumScore"),
	}
	// The rest of the run's results, low-resolution copies the profile rejects: the snapshot's result count and the
	// run's are the same number.
	for i := len(ranked); i < results; i++ {
		ranked = append(ranked, mk(strings.Replace(alt2, "1080p", "720p", 1)+fmt.Sprintf(".v%d", i), "WEBDL-720p", 0, false,
			"QualityNotWanted"))
	}
	rels := map[*decide.Decision]indexer.Release{}
	for i, d := range ranked {
		h := rel.hash
		switch {
		case i >= 6:
			h = fmt.Sprintf("%s%04x", rel.hash[:len(rel.hash)-4], i) // no draws here, so later seeded data is unchanged
		case i > 0:
			h = s.hash()
		}
		seeders, size := 5+i, rel.size/3 // the padded 720p copies
		if i < 6 {
			seeders, size = 5+s.r.IntN(200), rel.size-int64(i)<<28
		}
		rels[d] = indexer.Release{Title: d.Release, Indexer: rel.indexer, InfoHash: h, Size: size, Seeders: seeders}
	}
	if err := snapshot.Record(s.ctx, s.tx, snapshot.Search{Source: fmt.Sprintf("acquire:%d", runID), RunID: runID,
		TitleID: t.id, Version: v.key, Target: target, Ranked: ranked, Releases: rels,
		Notes: []string{fmt.Sprintf("%d results from %d indexers", len(ranked), 3)}, CreatedAt: at}); err != nil && s.err == nil {
		s.err = err
	}
}

// upgradesFound records, for two movies whose file is below its cutoff, a later search that found a better release:
// they read "Upgrade found" and fill Home's row of that name; the others below cutoff read "Best quality known".
// No draws here, so the rest of the seeded data is unchanged.
func (s *seeder) upgradesFound() {
	for i, name := range []string{"Saltglass Meridian", "Tidewater Requiem"} {
		t := s.byName[name]
		if s.err != nil || t == nil || len(t.versions) == 0 || t.versions[0].state != "filled" {
			continue
		}
		v := t.versions[0]
		rel := fmt.Sprintf("%s.%d.2160p.UHD.BluRay.REMUX.DV.HDR.TrueHD.7.1.Atmos-%s", strings.ReplaceAll(t.name, " ", "."), t.year,
			movieGroups[i%len(movieGroups)])
		d := &decide.Decision{Release: rel, Version: v.key, Accepted: true, Upgrade: true, Quality: "Remux-2160p", Score: 3200,
			Parsed: true, Formats: []decide.FormatScore{{Name: "Remux Tier", Score: 3200}}}
		if err := snapshot.Record(s.ctx, s.tx, snapshot.Search{Source: "wanted", TitleID: t.id, Version: v.key, Target: "movie",
			Ranked: []*decide.Decision{d}, Releases: map[*decide.Decision]indexer.Release{d: {Title: rel, Indexer: "prowlarr-public",
				InfoHash: fmt.Sprintf("%040x", 0xbe77e4+i), Size: 58 << 30, Seeders: 40 + i}},
			Notes: []string{"1 results from 3 indexers"}, CreatedAt: s.now.Add(-time.Duration(3+i) * time.Hour)}); err != nil {
			s.err = err
		}
	}
}

func rejectionText(r string) string {
	return map[string]string{
		"QualityNotWanted":         "the quality is not wanted by the profile",
		"CustomFormatMinimumScore": "the format score is below the profile's minimum",
		"NotAnUpgrade":             "the existing file is as good or better",
		"Language":                 "the language is not wanted",
	}[r]
}

// target is where a link of file f of rel points: the mount, as Plex sees it.
func (s *seeder) target(rel *release, f relFile) string {
	base, all := "/mnt/zurg", "__all__"
	if l := s.pol.Library; l != nil {
		if l.Mount.LinkPath != "" {
			base = l.Mount.LinkPath
		} else if l.Mount.Path != "" {
			base = l.Mount.Path
		}
		if l.Mount.AllDir != "" {
			all = l.Mount.AllDir
		}
	}
	return path.Join(base, all, rel.name, f.name)
}

// link writes the library links of an imported release and their actions.
func (s *seeder) link(t *title, v *version, rel *release, downloadID int64, at time.Time, state string) {
	rel.at = at.Add(-5 * time.Minute)
	cid := s.contentFor(rel, "rd")
	for _, f := range rel.files {
		var cf int64
		_ = s.tx.QueryRowContext(s.ctx, `SELECT id FROM content_file WHERE content_id = ? AND path = ?`, cid, f.name).Scan(&cf)
		p := s.libraryPath(t, v, rel, f)
		var dlID any
		if downloadID > 0 {
			dlID = downloadID
		}
		lid := s.exec(`INSERT INTO link (path, target, kind, content_file_id, title_id, download_id, release_title, quality,
			score, score_breakdown, adopted, state, last_checked_at, created_at, updated_at)
			VALUES (?, ?, 'symlink', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, p, s.target(rel, f), cf, t.id, dlID, rel.name,
			rel.quality, rel.score, jsonOf(formatScores(formatsFor(rel))), b01(downloadID == 0), state,
			ms(s.ago(days(0.3))), ms(at), ms(at))
		s.exec(`INSERT INTO link_version (link_id, version_id) VALUES (?, ?)`, lid, v.id)
		if f.ep != nil {
			s.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (?, ?)`, lid, f.ep.id)
		}
		var dlAny any
		if downloadID > 0 {
			dlAny = downloadID
		}
		s.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, link_id, path, target, detail)
			VALUES (?, 'link', 'applied', ?, ?, ?, ?, ?, ?)`, ms(at), v.key, dlAny, lid, p, s.target(rel, f),
			"linked "+f.name)
	}
}

func formatScores(fs []decide.FormatScore) map[string]int {
	out := map[string]int{}
	for _, f := range fs {
		out[f.Name] = f.Score
	}
	return out
}

// deadLinks writes a few links whose target left the mount: the journal flags them and the dangling report lists
// them.
func (s *seeder) deadLinks() {
	for _, name := range []string{"Lowlight Choir", "Mossgrave", "Velvet Arithmetic"} {
		t := s.byName[name]
		if t == nil || len(t.versions) == 0 {
			continue
		}
		v := t.versions[0]
		var rel *release
		if t.kind == "movie" {
			rel = s.movieRelease(t, quality(v, false))
		} else {
			var e *episode
			for _, x := range t.episodes {
				if x.season == 1 && x.number == 3 {
					e = x
				}
			}
			if e == nil {
				continue
			}
			rel = s.seriesRelease(t, quality(v, false), []*episode{e}, false)
		}
		at := s.between(days(200), days(60))
		rel.at = at
		// Its own path, beside the arr's file (an older copy adopted from disk).
		rel.files[0].name = "old." + rel.files[0].name
		s.link(t, v, rel, 0, at, "dead")
		s.exec(`UPDATE content SET state = 'gone' WHERE info_hash = ?`, rel.hash)
		s.exec(`UPDATE content_provider SET state = 'absent', absent_at = ? WHERE content_id = ?`, ms(s.ago(days(3))),
			s.content[rel.hash])
		var lid int64
		_ = s.tx.QueryRowContext(s.ctx, `SELECT MAX(id) FROM link`).Scan(&lid)
		var p, tg string
		_ = s.tx.QueryRowContext(s.ctx, `SELECT path, target FROM link WHERE id = ?`, lid).Scan(&p, &tg)
		s.exec(`UPDATE link SET path = ? WHERE id = ?`, strings.TrimSuffix(p, ".mkv")+".old.mkv", lid)
		s.exec(`INSERT INTO library_action (at, kind, mode, version_key, link_id, path, target, detail) VALUES (?, 'dead',
			'applied', ?, ?, ?, ?, 'the target is gone and no provider folder holds the file')`, ms(s.ago(days(2))), v.key,
			lid, strings.TrimSuffix(p, ".mkv")+".old.mkv", tg)
		s.exec(`INSERT INTO library_dangling (path, target, root, version_key, folder, first_seen_at, last_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, strings.TrimSuffix(p, ".mkv")+".old.mkv", tg, v.vp.Library, v.key, rel.name,
			ms(s.ago(days(3))), ms(s.ago(days(0.2))))
	}
	s.exec(`INSERT INTO library_dangling_scan (at, roots, links, dangling, error, took_ms) VALUES (?, ?, ?, 3, NULL, 1840),
		(?, ?, ?, 3, NULL, 1712)`, ms(s.ago(days(0.2))), jsonOf(s.roots()), 900+s.r.IntN(200), ms(s.ago(days(0.45))),
		jsonOf(s.roots()), 900+s.r.IntN(200))
}

func (s *seeder) roots() []string {
	var out []string
	for _, k := range sortedKeys(s.pol.Versions) {
		out = append(out, s.pol.Versions[k].Library)
	}
	return out
}

// libraryExtras: a relink, a Plex scan, a mount-wait alert and a replaced link in the trash.
func (s *seeder) libraryExtras() {
	t := s.byName["Tinker and Thorne"]
	if t != nil && len(t.versions) > 0 {
		v := t.versions[0]
		p := path.Join(v.vp.Library, s.folder(t), "Season 01", safePath(t.name)+" - S01E04 - relinked.mkv")
		s.exec(`INSERT INTO library_action (at, kind, mode, version_key, path, target, detail) VALUES (?, 'relink',
			'applied', ?, ?, ?, 'repointed to the folder the provider renamed')`, ms(s.ago(days(5))), v.key, p,
			path.Join("/mnt/debrid/__all__", dots(t.name)+".S01.2160p.WEB-DL", "e04.mkv"))
		s.exec(`INSERT INTO link_trash (version_key, path, trash_path, target, reason, moved_at) VALUES (?, ?, ?, ?,
			'replaced by an upgrade', ?)`, v.key, p, "/media/.trash/"+dots(t.name)+"-S01E04.mkv",
			"/mnt/debrid/__all__/"+dots(t.name)+".S01E04.1080p.WEB-DL/e04.mkv", ms(s.ago(days(5))))
	}
	s.exec(`INSERT INTO library_action (at, kind, mode, version_key, detail) VALUES (?, 'scan', 'dry_run', 'tv-2160p',
		'Plex partial scan of one folder (dry run: the library is off)'), (?, 'alert', 'applied', 'movie-remux',
		'waiting for the mount past 6m0s')`, ms(s.ago(days(0.1))), ms(s.ago(7*time.Hour)))
}

// verdicts records two verdicts: "wrong" on the grab named for the wrong year (its hash blocklisted for the version,
// a re-search queued) and "ok" on a dead link that was removed on purpose.
func (s *seeder) verdicts(wrongYear int64) {
	if wrongYear > 0 {
		var hash, rel, key string
		var titleID int64
		_ = s.tx.QueryRowContext(s.ctx, `SELECT info_hash, release_title, version_key, title_id FROM download WHERE id = ?`,
			wrongYear).Scan(&hash, &rel, &key, &titleID)
		at := s.ago(20 * time.Minute)
		job := s.exec(`INSERT INTO job (kind, payload, state, run_at, attempts, created_at, updated_at, finished_at)
			VALUES ('acquire.research', ?, 'done', ?, 1, ?, ?, ?)`, jsonOf(map[string]any{"title_id": titleID, "version": key}),
			ms(at), ms(at), ms(at), ms(at.Add(30*time.Second)))
		s.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, download_id,
			created_at, researched_at) VALUES (?, ?, ?, ?, 'verdict: wrong (the release is a different year)', 'journal', ?, ?, ?)`,
			key, hash, titleID, rel, wrongYear, ms(at), ms(at.Add(30*time.Second)))
		s.exec(`INSERT INTO journal_verdict (entry_id, verdict, reason, acked, title_id, version_key, info_hash, action,
			action_note, research_job, by, created_at) VALUES (?, 'wrong', 'the release is the 2019 short of the same name',
			'["year_mismatch"]', ?, ?, ?, 'blocklisted', 'blocklisted for the version; re-search queued', ?, ?, ?)`,
			fmt.Sprintf("dl:%d", wrongYear), titleID, key, hash, job, Username, ms(at))
	}
	var actionID, titleID int64
	var key string
	_ = s.tx.QueryRowContext(s.ctx, `SELECT a.id, l.title_id, a.version_key FROM library_action a JOIN link l ON l.id = a.link_id
		WHERE a.kind = 'dead' ORDER BY a.id LIMIT 1`).Scan(&actionID, &titleID, &key)
	if actionID > 0 {
		s.exec(`INSERT INTO journal_verdict (entry_id, verdict, reason, acked, title_id, version_key, action, by, created_at)
			VALUES (?, 'ok', 'the old copy was removed on purpose; the current file is fine', '["dead_link"]', ?, ?,
			'recorded', ?, ?)`, fmt.Sprintf("lib:%d", actionID), titleID, key, Username, ms(s.ago(days(1))))
	}
}

// historyExtras gives one imported movie the rest of History's events: the file its import replaced ("Deleted
// (upgrade)", with the old release as recorded) and the release's copies to the other providers, one added and one
// not cached (provider_copy and its provider_copy_event).
func (s *seeder) historyExtras(t *title, v *version, rel *release, id int64, at time.Time) {
	old := fmt.Sprintf("%s.%d.1080p.WEB-DL.DDP5.1.H.264-OLD", strings.ReplaceAll(t.name, " ", "."), t.year)
	s.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, path, detail, release_title, quality,
		score) VALUES (?, 'replace', 'applied', ?, ?, ?, 'moved aside to the trash', ?, 'WEBDL-1080p', 400)`,
		ms(at.Add(time.Second)), v.key, id, path.Join(v.vp.Library, s.folder(t), old+".mkv"), old)
	for i, c := range []struct{ pk, state, msg string }{{"tb", "added", "Added to TB"}, {"ad", "not_cached",
		"Not cached on AD; our add was deleted"}} {
		at := at.Add(time.Duration(10+i) * time.Minute)
		s.exec(`INSERT INTO provider_copy (info_hash, name, title_id, provider, state, message, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, rel.hash, rel.name, t.id, c.pk, c.state, c.msg, ms(at), ms(at))
		s.exec(`INSERT INTO provider_copy_event (at, info_hash, name, title_id, provider, state, message, quality, score)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, ms(at), rel.hash, rel.name, t.id, c.pk, c.state, c.msg, rel.quality, rel.score)
	}
}
