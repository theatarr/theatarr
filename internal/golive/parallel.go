package golive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/shadow"
)

// SecParallel is the section for a parallel version (versions[k].follows, docs/parallel.md). It takes the shadow
// section's place: a parallel version runs active beside the arr from the start, so there is no evidence window, and
// the arr that feeds the followed version keeps grabbing (different roots; duplicate grabs are expected).
const SecParallel = "parallel"

// parallel checks what a parallel version needs: the version it follows and the arr feeding it, roots of its own,
// and whether the start by copy (theatarr library adopt) is done.
func (c *checker) parallel(ctx context.Context) {
	p, k, followed := c.o.Policy, c.o.Version, c.v.Follows
	fv := p.Versions[followed]
	if fv == nil {
		c.add(SecParallel, "follows", Fail, "fix versions."+k+".follows", "%s follows %q, which the policy lacks", k, followed)
		return
	}
	var feeds []string
	if p.Import != nil {
		for _, inst := range sortedKeysOf(p.Import.Arr) {
			if a := p.Import.Arr[inst]; a.Version == followed && p.ArrFed(inst) {
				feeds = append(feeds, inst)
			}
		}
	}
	mode := fv.Mode
	if mode == "" {
		mode = policy.ModeShadow
	}
	if len(feeds) == 0 {
		c.add(SecParallel, "follows", Warn, "add the arr that serves "+followed+" to import.arr: a parallel version "+
			"takes its titles and monitoring from that import", "%s follows %s (mode %s), which no import.arr instance "+
			"feeds", k, followed, mode)
	} else {
		c.add(SecParallel, "follows", Pass, "", "%s follows %s (mode %s), fed by %s: same titles and monitoring, "+
			"files of its own", k, followed, mode, strings.Join(feeds, ", "))
	}
	c.add(SecParallel, "arr grabbing", Info, "nothing to stop: the arr keeps its own roots, and duplicate grabs are "+
		"expected", "%s keeps grabbing for %s; Theatarr grabs for %s on its own", list(feeds, 3), followed, k)
	c.followAgreement(ctx, feeds, followed)

	roots, err := p.CheckRootSeparation(k)
	var paths []string
	for _, root := range roots {
		paths = append(paths, fmt.Sprintf("%s (resolved %s)", root.Path, root.Resolved))
	}
	if err != nil {
		c.add(SecParallel, "roots", Fail, "give each version physically separate roots and trash", "%v", err)
	} else if len(paths) == 0 {
		c.add(SecParallel, "roots", Fail, "set library.versions."+k+".roots", "%s has no roots", k)
	} else {
		c.add(SecParallel, "roots", Pass, "", "%s writes only under %s, apart from every other version's roots",
			k, strings.Join(paths, ", "))
	}
	c.adopted(ctx, followed)
	c.adoptFresh(ctx, followed, feeds)
}

// adoptFresh compares K's last adopt or refresh with F's newest arr import that Theatarr has read. Once K grabs,
// refresh stops and nothing adopts new arr files, so a file the arr imported after K's last adopt stays missing in
// K until RSS or a manual search finds it (by design: no catch-up search). Adopt and refresh
// therefore run right before grabbing; this check shows whether that is still due, as of Theatarr's last arr import.
func (c *checker) adoptFresh(ctx context.Context, followed string, feeds []string) {
	const check = "adopt and refresh"
	if c.o.DB == nil {
		c.add(SecParallel, check, Warn, noDB, "not checked: no database")
		return
	}
	k := c.o.Version
	fix := "right before grabbing: refresh Theatarr's arr data (arr.import), then theatarr library adopt -version " + k +
		" and theatarr library adopt -version " + k + " -refresh; anything still missing after that is left to RSS or a " +
		"manual search"
	var last, newest, synced sql.NullInt64
	err := c.o.DB.QueryRowContext(ctx, `SELECT MAX(at) FROM library_action WHERE version_key = ? AND mode = 'applied'
		AND (detail LIKE 'adopted from %' OR detail LIKE 'refresh:%')`, k).Scan(&last)
	if err == nil {
		err = c.o.DB.QueryRowContext(ctx, `SELECT MAX(h.date_at) FROM arr_history h JOIN version v ON v.id = h.version_id
			WHERE v.policy_key = ? AND h.event_type = 'downloadFolderImported'`, followed).Scan(&newest)
	}
	if err == nil && len(feeds) > 0 {
		q := `SELECT MAX(finished_at) FROM import_run WHERE instance IN (?` + strings.Repeat(",?", len(feeds)-1) + `)`
		args := make([]any, len(feeds))
		for i, f := range feeds {
			args[i] = f
		}
		err = c.o.DB.QueryRowContext(ctx, q, args...).Scan(&synced)
	}
	if err != nil {
		c.add(SecParallel, check, Warn, fix, "not readable: %v", err)
		return
	}
	stamp := func(n sql.NullInt64) string {
		if !n.Valid {
			return "never"
		}
		return time.UnixMilli(n.Int64).UTC().Format("2006-01-02 15:04 MST")
	}
	asOf := "as of Theatarr's last import of " + list(feeds, 3) + " (" + stamp(synced) + ")"
	if !newest.Valid {
		c.add(SecParallel, check, Info, fix, "no arr imports of %s recorded %s; %s's last adopt or refresh: %s",
			followed, asOf, k, stamp(last))
		return
	}
	if last.Valid && last.Int64 >= newest.Int64 {
		c.add(SecParallel, check, Pass, "run it again "+fix, "%s's last adopt or refresh (%s) is after %s's newest arr "+
			"import (%s), %s", k, stamp(last), followed, stamp(newest), asOf)
		return
	}
	var since int
	_ = c.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM arr_history h JOIN version v ON v.id = h.version_id
		WHERE v.policy_key = ? AND h.event_type = 'downloadFolderImported' AND h.date_at > ?`, followed, last.Int64).Scan(&since)
	c.add(SecParallel, check, Warn, fix, "%d arr imports of %s since %s's last adopt or refresh (%s; newest import %s), %s",
		since, followed, k, stamp(last), stamp(newest), asOf)
}

func (c *checker) followAgreement(ctx context.Context, feeds []string, followed string) {
	if c.o.DB == nil {
		c.add(SecParallel, "comparison", Warn, noDB, "not checked: no database")
		return
	}
	if len(feeds) == 0 {
		c.add(SecParallel, "comparison", Warn, "import the followed version from its arr", "no arr history to compare")
		return
	}
	var arrGrabs, agrees int
	for _, inst := range feeds {
		r, err := shadow.CompareFollow(ctx, c.o.DB, inst, followed, c.o.Version, c.o.Now.Add(-time.Duration(c.o.Days)*24*time.Hour))
		if err != nil {
			c.add(SecParallel, "comparison", Warn, "check the database schema", "not readable: %v", err)
			return
		}
		arrGrabs += r.Counts["both_same_hash"] + r.Counts["both_different_hash"] + r.Counts["arr_only"]
		agrees += r.Counts["both_same_hash"]
	}
	if arrGrabs == 0 {
		c.add(SecParallel, "comparison", Warn, "wait for arr and Theatarr grabs", "no arr grabs in the comparison window")
		return
	}
	ratio := float64(agrees) / float64(arrGrabs)
	msg := fmt.Sprintf("%.1f%% (%d of %d arr grabs match by hash; bar %.0f%%", ratio*100, agrees, arrGrabs, c.o.Bar*100)
	switch {
	case ratio < c.o.Bar:
		c.add(SecParallel, "comparison", Warn, "review theatarr parallel compare results", "%s); WARN below agreement bar", msg)
	case arrGrabs < c.o.MinGrabs:
		c.add(SecParallel, "comparison", Warn, fmt.Sprintf("wait for at least %d grabs (-min-grabs)", c.o.MinGrabs), "%s; too few grabs to judge", msg)
	default:
		c.add(SecParallel, "comparison", Pass, "", "%s)", msg)
	}
}

// adopted checks every episode of each arr file against active links with existing mount targets.
func (c *checker) adopted(ctx context.Context, followed string) {
	if c.o.DB == nil {
		c.add(SecParallel, "adopt", Warn, noDB, "not checked: no database")
		return
	}
	files, missing, episodes, partial, samples, err := c.adoptionCoverage(ctx, followed)
	switch {
	case errors.Is(err, library.ErrMountDown):
		c.add(SecParallel, "adopt", Warn, "restore the mount and run the check again", "coverage unknown: %v", err)
	case err != nil:
		c.add(SecParallel, "adopt", Fail, "check the database schema and target access", "not readable: %v", err)
	case missing == 0:
		c.add(SecParallel, "adopt", Pass, "", "every one of the %d arr files of %s is fully covered by active links with existing targets in %s",
			files, followed, c.o.Version)
	default:
		c.add(SecParallel, "adopt", Warn, "theatarr library adopt -version "+c.o.Version+" (or -queue it); review skipped packs",
			"%d of %d arr files of %s are not fully covered in %s: %d missing episodes, %d partial packs; sample: %s",
			missing, files, followed, c.o.Version, episodes, partial, list(samples, 10))
	}
}

func (c *checker) adoptionCoverage(ctx context.Context, followed string) (files, missing, episodes, partial int, samples []string, err error) {
	rows, err := c.o.DB.QueryContext(ctx, `SELECT vf.id, vf.path, COALESCE(fe.episode_id, 0),
		COALESCE(e.season, 0), COALESCE(e.number, 0), COALESCE(l.target, '')
		FROM version_file vf JOIN version v ON v.id = vf.version_id
		JOIN version_source vs ON vs.version_id = vf.version_id AND vs.instance = vf.instance
		LEFT JOIN version_file_episode fe ON fe.version_file_id = vf.id LEFT JOIN episode e ON e.id = fe.episode_id
		LEFT JOIN link l ON l.title_id = vf.title_id AND l.state = 'active'
			AND EXISTS (SELECT 1 FROM link_version lv JOIN version k ON k.id = lv.version_id
				WHERE lv.link_id = l.id AND k.policy_key = ?)
			AND (fe.episode_id IS NULL OR EXISTS (SELECT 1 FROM link_episode le
				WHERE le.link_id = l.id AND le.episode_id = fe.episode_id))
		WHERE v.policy_key = ? ORDER BY vf.id, fe.episode_id, l.id`, c.o.Version, followed)
	if err != nil {
		return
	}
	type candidate struct {
		file, episode  int64
		path, target   string
		season, number int
	}
	var candidates []candidate
	for rows.Next() {
		var v candidate
		if err = rows.Scan(&v.file, &v.path, &v.episode, &v.season, &v.number, &v.target); err != nil {
			rows.Close()
			return
		}
		candidates = append(candidates, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(candidates) == 0 {
		return
	}
	s := c.o.Policy.LibrarySettings()
	mount := &library.Mount{Path: s.MountPath, LinkPath: s.LinkPath, AllDir: s.AllDir, StatTimeout: s.StatTimeout}
	if s.MountPath == "" {
		err = fmt.Errorf("%w: library.mount.path is not set", library.ErrMountDown)
		return
	}
	if err = mount.Healthy(ctx); err != nil {
		err = fmt.Errorf("%w: %v", library.ErrMountDown, err)
		return
	}
	targets := map[string]bool{}
	covered := map[int64]map[int64]bool{}
	for _, v := range candidates {
		if covered[v.file] == nil {
			covered[v.file] = map[int64]bool{}
		}
		exists, checked := targets[v.target]
		if !checked {
			if read, ok := mount.FromLink(v.target); v.target != "" && ok {
				var st fs.FileInfo
				st, err = mount.Stat(ctx, read)
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					err = fmt.Errorf("%w: %v", library.ErrMountDown, err)
					return
				}
				exists = err == nil && st.Mode().IsRegular()
			}
			targets[v.target] = exists
		}
		covered[v.file][v.episode] = covered[v.file][v.episode] || exists
	}
	files = len(covered)
	seenFiles, seenEpisodes := map[int64]bool{}, map[int64]bool{}
	for _, v := range candidates {
		if !seenFiles[v.file] {
			seenFiles[v.file] = true
			n := 0
			for _, ok := range covered[v.file] {
				if !ok {
					n++
				}
			}
			if n > 0 {
				missing++
				if n < len(covered[v.file]) {
					partial++
					if len(samples) < 10 {
						samples = append(samples, "partial pack: "+v.path)
					}
				}
			}
		}
		if !covered[v.file][v.episode] && (v.episode == 0 || !seenEpisodes[v.episode]) {
			seenEpisodes[v.episode] = true
			if v.episode != 0 {
				episodes++
			}
			if len(samples) < 10 {
				label := v.path
				if v.episode != 0 {
					label += fmt.Sprintf(": S%02dE%02d (episode %d)", v.season, v.number, v.episode)
				}
				samples = append(samples, label)
			}
		}
	}
	// A mount lost during the scan must not turn target ENOENTs into missing coverage.
	if err = mount.Healthy(ctx); err != nil {
		err = fmt.Errorf("%w: %v", library.ErrMountDown, err)
	}
	return
}

// followerHandover replaces the handover for a parallel version: the arr is not handed over, and only a test
// client is pointed at the version's own view.
func (c *checker) followerHandover() {
	k := c.o.Version
	flavour := "radarr"
	if c.v.Kind == policy.KindSeries {
		flavour = "sonarr"
	}
	c.add(SecHandover, "owner", Pass, "", "parallel version: %s's arr keeps running as it is; nothing to stop or "+
		"repoint (stopping %s: set it to shadow or remove it)", c.v.Follows, k)
	c.add(SecHandover, "client test", Info, "household clients stay on the arr; the default views do not serve "+
		"parallel versions unless listed", "a test Seerr or phone app can use %s/v/%s/%s with a Theatarr key",
		c.o.PublicURL, k, flavour)
}

func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
