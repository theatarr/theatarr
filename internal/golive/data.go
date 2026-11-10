package golive

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/shadow"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/uipages"
)

const noDB = "run where the database is: docker exec theatarr theatarr golive check (it reads the database read-only)"

func (c *checker) health(ctx context.Context) {
	if c.o.Health == nil {
		c.add(SecHealth, "checks", Warn, noDB, "not checked: no health results")
	}
	for _, h := range c.o.Health {
		if h.Source == "VpnEgressCheck" {
			continue // egress() reports it, with the running service's view
		}
		st := Pass
		switch h.Type {
		case health.Warning:
			st = Warn
		case health.Error:
			st = Fail
		case health.Notice:
			// An incomplete setup leaves features off: not a pass for going live.
			if h.Source == "SetupCheck" {
				st = Warn
			}
		}
		fix := ""
		if st != Pass {
			fix = healthFix(h.Source)
		}
		c.add(SecHealth, h.Source, st, fix, "%s", h.Message)
	}
	c.egress()
	c.metadata(ctx)
}

func healthFix(source string) string {
	switch source {
	case "MountCheck":
		return "check the debrid mount is mounted and readable in the container (restart the container after a remount); imports pause while it is down"
	case "BackupCheck":
		return "docker exec theatarr theatarr backup now, then theatarr backup list; the nightly set writes the policy archive and manifest"
	case "ProviderSyncCheck":
		return "theatarr providers status; a provider that cannot be listed cannot be added to either"
	case "MetadataRefreshCheck":
		return "check the TMDb/TVDB keys and the meta jobs (" + uipages.Tasks + ")"
	case "JobBacklogCheck", "FailedJobsCheck":
		return uipages.Tasks + ": clear or retry before adding the wanted loop's work"
	case "DiskSpaceCheck":
		return "free space on the data volume"
	case "SetupCheck":
		return "finish " + uipages.Setup + ", then run theatarr preflight"
	}
	return "see docs/design/readiness.md"
}

func (c *checker) egress() {
	guard := c.o.Policy.Egress
	if guard == nil {
		c.add(SecHealth, "VpnEgressCheck", Warn, "", "VPN guard off: no requests require VPN")
	}
	s := c.o.Service
	if s == nil {
		if guard != nil {
			c.add(SecHealth, "VpnEgressCheck", Warn, "run inside the container next to theatarr serve (-service http://127.0.0.1:7700)",
				"not checked: no running service answered /metrics")
		}
		return
	}
	var down []string
	for p, ok := range s.VPNOK {
		if !ok {
			down = append(down, p)
		}
	}
	sort.Strings(down)
	status := Fail
	effect := "requests requiring VPN are refused"
	if guard != nil && len(guard.RequireVPNFor) == 0 {
		status = Warn
		effect = "monitor-only; no requests require VPN"
	} else if guard != nil {
		effect = "requests for " + strings.Join(guard.RequireVPNFor, ", ") + " are refused"
	}
	switch {
	case guard == nil:
	case len(s.VPNOK) == 0:
		c.add(SecHealth, "VpnEgressCheck", status, "docker exec theatarr theatarr egress-check; check the VPN and egress.vpn_ips",
			"the service reports no egress check yet: %s", effect)
	case len(down) > 0:
		c.add(SecHealth, "VpnEgressCheck", status, "docker exec theatarr theatarr egress-check; check the VPN and egress.vpn_ips",
			"VPN egress not confirmed on %s: %s", strings.Join(down, ", "), effect)
	default:
		mode := ""
		if len(guard.RequireVPNFor) == 0 {
			mode = "; monitor-only; no requests require VPN"
		}
		c.add(SecHealth, "VpnEgressCheck", Pass, "", "VPN egress confirmed (%s)%s", plural(len(s.VPNOK), "path", "paths"), mode)
	}
	var off []string
	for _, ik := range c.v.Indexers {
		if slices.Contains(s.Backoffs, "indexer:"+ik) {
			off = append(off, ik)
		}
	}
	if len(off) > 0 {
		c.add(SecHealth, "IndexerStatusCheck", Warn, "wait for the back-off, or check the indexer",
			"backing off now: %s", strings.Join(off, ", "))
	} else {
		c.add(SecHealth, "IndexerStatusCheck", Pass, "", "none of the %d indexers is backing off", len(c.v.Indexers))
	}
}

// metadata: every title of the version has fresh metadata; for series, a successful TVDB/TMDb refresh, since the
// refresh owns the episode list once the import stops (docs/design/tv-metadata.md).
func (c *checker) metadata(ctx context.Context) {
	db := c.o.DB
	if db == nil {
		c.add(SecHealth, "metadata", Warn, noDB, "not checked: no database")
		return
	}
	k := c.o.Version
	if c.v.Kind == policy.KindSeries {
		rows, err := db.QueryContext(ctx, `SELECT t.name, COALESCE(m.error, ''), m.refreshed_at IS NOT NULL
			FROM version v JOIN title t ON t.id = v.title_id LEFT JOIN series_meta m ON m.title_id = t.id
			WHERE v.policy_key = ? AND (m.refreshed_at IS NULL OR m.error <> '') ORDER BY t.name`, k)
		if err != nil {
			c.add(SecHealth, "series metadata", Fail, "check the database schema (theatarr migrate)", "not readable: %v", err)
			return
		}
		var never, failed []string
		for rows.Next() {
			var name, msg string
			var ok bool
			if err := rows.Scan(&name, &msg, &ok); err != nil {
				rows.Close()
				c.add(SecHealth, "series metadata", Fail, "", "not readable: %v", err)
				return
			}
			if !ok {
				never = append(never, name)
			} else {
				failed = append(failed, name)
			}
		}
		rows.Close()
		var total int
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM version WHERE policy_key = ?`, k).Scan(&total)
		if len(never)+len(failed) == 0 {
			c.add(SecHealth, "series metadata", Pass, "", "all %d series have a successful TVDB/TMDb refresh", total)
			return
		}
		var parts []string
		if len(never) > 0 {
			parts = append(parts, fmt.Sprintf("%d never refreshed (%s)", len(never), list(never, 3)))
		}
		if len(failed) > 0 {
			parts = append(parts, fmt.Sprintf("%d whose last refresh failed (%s)", len(failed), list(failed, 3)))
		}
		c.add(SecHealth, "series metadata", Fail, "wait for the hourly sweep (50 a run) or theatarr tvdb refresh TITLE-ID; "+
			"a series that TVDB and TMDb both lack keeps only the import's episodes",
			"of %d series: %s", total, strings.Join(parts, "; "))
		return
	}
	var total, missing, failed int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*),
			COALESCE(SUM(m.title_id IS NULL OR m.refreshed_at IS NULL), 0),
			COALESCE(SUM(m.error <> ''), 0)
		FROM version v LEFT JOIN title_meta m ON m.title_id = v.title_id WHERE v.policy_key = ?`, k).Scan(&total,
		&missing, &failed); err != nil {
		c.add(SecHealth, "movie metadata", Fail, "check the database schema (theatarr migrate)", "not readable: %v", err)
		return
	}
	if missing+failed == 0 {
		c.add(SecHealth, "movie metadata", Pass, "", "all %d movies have TMDb details", total)
		return
	}
	c.add(SecHealth, "movie metadata", Warn, "check the TMDb key and meta.art jobs; runtimes feed the sample guard",
		"of %d movies, %d have no TMDb details and %d failed their last refresh", total, missing, failed)
}

func (c *checker) shadow(ctx context.Context) {
	db := c.o.DB
	if db == nil {
		c.add(SecShadow, "agreement", Warn, noDB, "not checked: no database")
		return
	}
	k := c.o.Version
	since := c.o.Now.Add(-time.Duration(c.o.Days) * 24 * time.Hour)
	rows, err := db.QueryContext(ctx, `SELECT bucket, reason, COUNT(*) FROM shadow_diff WHERE version_key = ? AND at >= ?
		GROUP BY bucket, reason ORDER BY COUNT(*) DESC, bucket, reason`, k, store.Millis(since))
	if err != nil {
		c.add(SecShadow, "agreement", Fail, "check the database schema (theatarr migrate)", "shadow tables not readable: %v", err)
		return
	}
	type rc struct {
		bucket, reason string
		n              int
	}
	var all []rc
	grabs, agree, hand, metadataPending := 0, 0, 0, 0
	for rows.Next() {
		var x rc
		if err := rows.Scan(&x.bucket, &x.reason, &x.n); err != nil {
			rows.Close()
			c.add(SecShadow, "agreement", Fail, "", "not readable: %v", err)
			return
		}
		if shadow.IsMetadataPending(x.reason) {
			metadataPending += x.n
			continue
		}
		all = append(all, x)
		switch {
		case shadow.IsHandGrab(x.reason):
			hand += x.n
		case x.bucket == shadow.BucketRSSOnly && x.reason != shadow.ReasonTheatarrOnly:
		default:
			grabs += x.n
			if x.bucket == shadow.BucketAgree {
				agree += x.n
			}
		}
	}
	rows.Close()
	if metadataPending > 0 {
		c.add(SecShadow, "metadata_pending", Info, "", "%d metadata waits excluded from agreement and minimum sample", metadataPending)
	}
	// How long the runner has been at it: the oldest run it kept (30 days).
	var first sql.NullInt64
	_ = db.QueryRowContext(ctx, `SELECT MIN(started_at) FROM shadow_run`).Scan(&first)
	window := fmt.Sprintf("last %d days", c.o.Days)
	owners := c.owners()
	// A library no arr feeds, with no arr grabs recorded, has nothing to compare: neither a shadow window nor an
	// agreement can be judged, and neither blocks going live. With an arr, or with grabs, both lines are judged.
	unfed := len(owners) == 0 && grabs == 0
	switch {
	case unfed:
		c.add(SecShadow, "window", Info, "", "no arr feeds %s: no shadow window needed", k)
	case !first.Valid:
		c.add(SecShadow, "window", Fail, "let the shadow runner run for the window first", "the shadow runner has not run")
	default:
		ran := c.o.Now.Sub(store.FromMillis(first.Int64))
		days := ran.Hours() / 24
		if days+0.01 < float64(c.o.Days) {
			c.add(SecShadow, "window", Warn, fmt.Sprintf("wait until %s, or pass -days to judge a shorter window",
				store.FromMillis(first.Int64).Add(time.Duration(c.o.Days)*24*time.Hour).Format("2006-01-02")),
				"the shadow runner has run %.1f of the %d days (since %s)", days, c.o.Days,
				store.FromMillis(first.Int64).Format("2006-01-02 15:04 MST"))
		} else {
			c.add(SecShadow, "window", Pass, "", "the shadow runner has run %.0f days", days)
		}
	}
	switch {
	case unfed:
		c.add(SecShadow, "agreement", Info, "", "no arr feeds %s: nothing to compare, skipped", k)
	case grabs == 0:
		c.add(SecShadow, "agreement", Fail, "wait for arr grabs to compare", "no arr grabs of %s in the %s", k, window)
	default:
		ratio := float64(agree) / float64(grabs)
		msg := fmt.Sprintf("%.1f%% (%d of %d arr grabs agree, %s; hand grabs left out: %d); bar %.0f%%",
			math.Floor(ratio*1000)/10, agree, grabs, window, hand, c.o.Bar*100)
		switch {
		case ratio < c.o.Bar:
			c.add(SecShadow, "agreement", Fail, "explain or fix the disagreements below (UI › Shadow diff, theatarr shadow status)",
				"%s", msg)
		case grabs < c.o.MinGrabs:
			c.add(SecShadow, "agreement", Warn, fmt.Sprintf("wait for at least %d grabs (-min-grabs)", c.o.MinGrabs),
				"%s: too few grabs to judge", msg)
		default:
			c.add(SecShadow, "agreement", Pass, "", "%s", msg)
		}
	}
	for _, x := range all {
		if x.bucket == shadow.BucketAgree || x.reason == shadow.ReasonManualAccepted {
			continue
		}
		c.add(SecShadow, "disagreement", Warn, "read each row and record why (policy difference accepted, or tune "+
			"the profile): UI › Shadow diff, filter "+x.bucket,
			"%s / %s: %d in the %s", x.bucket, x.reason, x.n, window)
	}
	var pending int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM shadow_grab WHERE state = 'pending' AND version_key = ?`, k).Scan(&pending)
	if pending > 0 {
		c.add(SecShadow, "pending", Info, "", "%d grabs still wait for a search", pending)
	}
}

func (c *checker) library(ctx context.Context) {
	k := c.o.Version
	lib := c.o.Policy.LibrarySettings()
	var roots []string
	if lv := lib.Versions[k]; lv != nil {
		roots = lv.Roots
	}
	db := c.o.DB
	if db == nil {
		c.add(SecLibrary, "dangling", Warn, noDB, "not checked: no database")
	} else {
		c.dangling(ctx, roots)
		c.wanted(ctx)
		c.plannedOffers(ctx)
	}
	if c.o.Scan == nil {
		c.add(SecLibrary, "scan", Warn, "run inside the container (it walks the roots read-only)",
			"not checked: the version's roots were not walked")
		return
	}
	scans, err := c.o.Scan(ctx, k)
	if err != nil {
		c.add(SecLibrary, "scan", Fail, "check the roots and the mount", "walking the roots failed: %v", err)
		return
	}
	if len(scans) == 0 {
		c.add(SecLibrary, "scan", Warn, "add library.versions."+k, "no roots to walk")
	}
	for _, s := range scans {
		if s.Mount != "" {
			c.add(SecLibrary, "mount", Fail, "fix the zurg mount before the version grabs", "%s: mount: %s", s.Root, s.Mount)
		}
		c.add(SecLibrary, "root", Info, "", "%s: %d folders, %d symlinks (%d into the mount), %d regular files",
			s.Root, s.Dirs, s.Links, s.IntoMnt, s.Files)
		if s.Files > 0 {
			c.add(SecLibrary, "conflicting files", Warn, "move regular files out of the root or accept them: Theatarr only "+
				"makes symlinks, so a link beside a file shows both in Plex",
				"%s holds %d regular files (not symlinks)", s.Root, s.Files)
		}
		if n := s.Names; n != nil {
			switch {
			case n.Error != "":
				c.add(SecLibrary, "names", Warn, "theatarr library naming-sync, then theatarr library verify", "names not compared: %s",
					n.Error)
			case len(n.Differs) > 0:
				ex := make([]string, 0, 2)
				for _, d := range n.Differs[:min(2, len(n.Differs))] {
					ex = append(ex, path.Base(d.Path)+" → "+d.Ours)
				}
				c.add(SecLibrary, "names", Warn, "theatarr library verify -version "+k+"; Theatarr never renames the arr's links, "+
					"but an upgrade gets our name", "%d of %d arr files would be named differently by us (%s)",
					len(n.Differs), n.Shaped, list(ex, 2))
			default:
				c.add(SecLibrary, "names", Pass, "", "our names equal the arr's for all %d renamed files (%s)", n.Shaped,
					n.Naming)
			}
		}
	}
}

func (c *checker) dangling(ctx context.Context, roots []string) {
	db, k := c.o.DB, c.o.Version
	var at sql.NullInt64
	_ = db.QueryRowContext(ctx, `SELECT MAX(at) FROM library_dangling_scan WHERE error IS NULL OR error = ''`).Scan(&at)
	if !at.Valid {
		c.add(SecLibrary, "dangling", Warn, "theatarr library dangling -scan (read-only), or wait for theatarr serve's report",
			"no dangling-link report yet")
		return
	}
	q := `SELECT path FROM library_dangling WHERE version_key = ?`
	args := []any{k}
	for _, r := range roots {
		q += ` OR path LIKE ? ESCAPE '\'`
		args = append(args, likePrefix(r)+"%")
	}
	rows, err := db.QueryContext(ctx, q+` ORDER BY path`, args...)
	if err != nil {
		c.add(SecLibrary, "dangling", Fail, "", "not readable: %v", err)
		return
	}
	var paths []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			paths = append(paths, p)
		}
	}
	rows.Close()
	when := store.FromMillis(at.Int64).Format("2006-01-02 15:04 MST")
	if len(paths) == 0 {
		c.add(SecLibrary, "dangling", Pass, "", "no dangling links under the version's roots (report of %s)", when)
		return
	}
	folders := map[string]bool{}
	for _, p := range paths {
		folders[path.Dir(p)] = true
	}
	// A gone file turns its title wanted; only the wanted loop's missing search looks for it again by itself.
	gone := "gone ones are searched again by the wanted loop"
	if w := c.o.Policy.WantedSettings(); !w.Enabled || !w.Missing {
		gone = "gone ones stay missing (the wanted loop's missing search is off) until RSS or a manual search finds them"
	}
	c.add(SecLibrary, "dangling", Warn, "theatarr library relink -version "+k+" -dry-run shows what the relink would repoint; "+
		"once active the hourly relink fixes renamed ones, "+gone,
		"%d dangling links in %d folders (report of %s), e.g. %s", len(paths), len(folders), when, path.Base(paths[0]))
}

func likePrefix(root string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(strings.TrimRight(root, "/") + "/")
}

// wanted: what the wanted loop will do first, and the arr's links an upgrade will replace.
func (c *checker) wanted(ctx context.Context) {
	db, k := c.o.DB, c.o.Version
	var titles, missing, below, arrFiles, ours, adopted int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*),
			COALESCE(SUM(monitored = 1 AND state = 'wanted'), 0),
			COALESCE(SUM(monitored = 1 AND state = 'filled'), 0)
		FROM version WHERE policy_key = ?`, k).Scan(&titles, &missing, &below); err != nil {
		c.add(SecLibrary, "wanted", Fail, "", "not readable: %v", err)
		return
	}
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM version_file vf JOIN version v ON v.id = vf.version_id
		WHERE v.policy_key = ? AND v.monitored = 1 AND v.state = 'filled'`, k).Scan(&arrFiles)
	// link.adopted is 0 for every row (docs/parallel.md, Refresh): Theatarr's own links are the ones of its downloads;
	// adopt and arr-file imports leave download_id NULL.
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(SUM(l.download_id IS NOT NULL), 0), COALESCE(SUM(l.download_id IS NULL), 0)
		FROM link l WHERE l.state = 'active' AND EXISTS (SELECT 1 FROM link_version lv JOIN version v ON v.id = lv.version_id
			WHERE lv.link_id = l.id AND v.policy_key = ?)`, k).Scan(&ours, &adopted)
	unit := "titles"
	if c.v.Kind == policy.KindSeries {
		// Series items are episodes: aired, monitored, without a file (wanted) or below the cutoff (filled).
		today := c.o.Now.UTC().Format("2006-01-02")
		_ = db.QueryRowContext(ctx, `SELECT
				COALESCE(SUM(s.state = 'wanted' AND e.air_date IS NOT NULL AND e.air_date <= ?), 0),
				COALESCE(SUM(s.state = 'filled'), 0)
			FROM episode_version_state s JOIN version v ON v.id = s.version_id JOIN episode e ON e.id = s.episode_id
			WHERE v.policy_key = ? AND v.monitored = 1 AND s.monitored = 1`, today, k).Scan(&missing, &below)
		unit = "aired episodes"
	}
	w := c.o.Policy.WantedSettings()
	items := 0
	if w.Missing {
		items += missing
	}
	if w.CutoffUnmet != policy.CutoffOff {
		items += below
	}
	hours := 0.0
	if w.PerHour > 0 {
		hours = float64(items) / float64(w.PerHour)
	}
	at := "at most "
	switch w.CutoffUnmet {
	case policy.CutoffScore:
		at = ""
	case policy.CutoffOff:
		at = "none of the "
	}
	switch {
	case !w.Enabled:
		c.add(SecLibrary, "wanted", Info, "", "%d titles; the wanted loop is off (router.wanted.enabled: false): the %d "+
			"missing and %d below-cutoff %s are left to RSS or a manual search", titles, missing, below, unit)
	case !w.Missing:
		c.add(SecLibrary, "wanted", Info, "", "%d titles; the wanted loop's missing search is off (wanted.missing: "+
			"false): the %d missing %s are left to RSS or a manual search; it searches %s%d below-cutoff %s "+
			"(cutoff_unmet: %s): about %.0f h at %d searches an hour (series seasons count once)", titles, missing, unit,
			at, below, unit, w.CutoffUnmet, hours, w.PerHour)
	default:
		c.add(SecLibrary, "wanted", Info, "", "%d titles; the wanted loop starts with %d missing and %s%d below-cutoff %s "+
			"(cutoff_unmet: %s): about %.0f h at %d searches an hour (series seasons count once)", titles, missing, at,
			below, unit, w.CutoffUnmet, hours, w.PerHour)
	}
	if below > 0 {
		c.add(SecLibrary, "arr links replaced", Info, "", "%d titles hold an arr file below the cutoff (%d files): an "+
			"upgrade moves the arr's link to %s (kept %d days) and links ours", below, arrFiles,
			c.o.Policy.LibrarySettings().TrashDir, c.o.Policy.LibrarySettings().TrashKeepDays)
	}
	if ours+adopted > 0 {
		c.add(SecLibrary, "our links", Info, "", "%d active links of %s are Theatarr's own downloads; %d adopted or imported "+
			"from arr files", ours, k, adopted)
	}
}

// plannedOffers reports the offers accepted for the version while its gate was shut (router_offer planned): once it
// grabs, the next offer sweep routes the fresh ones, at most router.OfferPlannedPerPass a pass across all versions,
// and declines those older than router.OfferPlannedExpiry.
func (c *checker) plannedOffers(ctx context.Context) {
	cutoff := store.Millis(c.o.Now.Add(-router.OfferPlannedExpiry))
	var mine, fresh, all int
	if err := c.o.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(version_key = ?), 0),
			COALESCE(SUM(version_key = ? AND updated_at >= ?), 0), COALESCE(SUM(updated_at >= ?), 0)
		FROM router_offer WHERE outcome = 'planned'`, c.o.Version, c.o.Version, cutoff, cutoff).Scan(&mine, &fresh,
		&all); err != nil {
		c.add(SecLibrary, "planned offers", Warn, "", "not readable: %v", err)
		return
	}
	if mine == 0 {
		c.add(SecLibrary, "planned offers", Pass, "", "no offers are waiting for %s to grab", c.o.Version)
		return
	}
	first := min(fresh, router.OfferPlannedPerPass)
	c.add(SecLibrary, "planned offers", Info, "preview them with theatarr router offer -sweep -dry-run against the "+
		"switched policy", "%d offers were planned for %s while it did not grab: the first offer sweep after the flip "+
		"routes up to %d of the %d fresh ones (at most %d a sweep across all versions, %d fresh planned in all) and "+
		"declines %d older than %s", mine, c.o.Version, first, fresh, router.OfferPlannedPerPass, all, mine-fresh,
		router.OfferPlannedExpiry)
}
