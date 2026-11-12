package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
)

// Coverage check (docs/migration.md): does a parallel library hold every title and file its followed library has? It
// reads only. A title (movie) or episode of the followed library's arr files is covered exactly when the library has an
// active link for it whose target exists, imported or adopted. The check lists the titles the followed library has
// from an arr with no row for the library, and each arr file with something not covered, with the answer of a dry run
// of Adopt or (while the library does not grab) Refresh as the reason. It is complete only when nothing is listed; the
// operator decides on each item. The fix is the existing adopt and adopt -refresh, which never replace a file the
// library imported itself.

// CoverageItem is one thing that keeps a coverage check from being complete: a missing title, an arr file with
// something not covered, or a step that could not run. Step is "titles", "adopt" or "refresh" (whose answer gives the
// reason), or "library" (neither had one); Reason is the skip reason (as in AdoptReport.Skipped and
// RefreshReport.Skipped) or one of missing_title, to_link, to_move, not_covered, not_checked (the step could not run:
// Detail says why). Detail carries the underlying error text where there was one, and the library's links for it whose
// target is not there, with the stat error. Episodes are the episodes not covered.
type CoverageItem struct {
	Step     string  `json:"step"`
	Reason   string  `json:"reason"`
	Path     string  `json:"path,omitempty"`
	Title    string  `json:"title,omitempty"`
	Detail   string  `json:"detail,omitempty"`
	Episodes []int64 `json:"episodes,omitempty"`
}

// CoverageReport is the result of a coverage check.
type CoverageReport struct {
	Version string `json:"version"`
	Follows string `json:"follows"`
	// MissingTitles counts the titles of Follows (from an arr: version_source) with no row for Version.
	MissingTitles int `json:"missing_titles"`
	// OwnFilesKept counts the library's own links (not adopted) that cover a title or episode of the followed
	// library's arr files: information only.
	OwnFilesKept int          `json:"own_files_kept"`
	Adopt        *AdoptReport `json:"adopt"`
	// Refresh is nil when the library grabs; RefreshSkipped says why.
	Refresh        *RefreshReport `json:"refresh,omitempty"`
	RefreshSkipped string         `json:"refresh_skipped,omitempty"`
	Complete       bool           `json:"complete"`
	// Items lists everything that keeps the check from being complete, every one; Counts counts them by
	// "<step> <reason>".
	Items  []CoverageItem `json:"items,omitempty"`
	Counts map[string]int `json:"counts,omitempty"`
}

// Coverage checks a parallel library against its followed library. It writes nothing. A step that cannot run (the
// mount down) is listed as not_checked and returned as the error, with the report of what was collected.
func (l *Library) Coverage(ctx context.Context, version string) (*CoverageReport, error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.Coverage(pinned, version)
	}
	vp := l.pol.Versions[version]
	if vp == nil {
		return nil, fmt.Errorf("library: coverage: unknown library %q", version)
	}
	if vp.Follows == "" {
		return nil, fmt.Errorf("library: coverage: %s follows no library (versions.%s.follows)", version, version)
	}
	rep := &CoverageReport{Version: version, Follows: vp.Follows, Counts: map[string]int{}}
	add := func(it CoverageItem) {
		rep.Items = append(rep.Items, it)
		rep.Counts[it.Step+" "+it.Reason]++
	}
	rows, err := l.st.Reader().QueryContext(ctx, `SELECT t.id, t.name, COALESCE(t.year, 0) FROM version f
		JOIN title t ON t.id = f.title_id
		WHERE f.policy_key = ? AND EXISTS (SELECT 1 FROM version_source vs WHERE vs.version_id = f.id)
		AND NOT EXISTS (SELECT 1 FROM version k WHERE k.title_id = f.title_id AND k.policy_key = ?)
		ORDER BY t.name, t.id`, vp.Follows, version)
	if err != nil {
		return nil, err
	}
	var missing []CoverageItem
	for rows.Next() {
		var id int64
		var name string
		var year int
		if err := rows.Scan(&id, &name, &year); err != nil {
			rows.Close()
			return nil, err
		}
		missing = append(missing, CoverageItem{Step: "titles", Reason: "missing_title",
			Title: fmt.Sprintf("%s (%d) [title %d]", name, year, id)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rep.MissingTitles = len(missing)
	for _, it := range missing {
		add(it)
	}

	var errs []error
	notChecked := func(step string, err error) {
		errs = append(errs, err)
		add(CoverageItem{Step: step, Reason: "not_checked", Detail: err.Error()})
	}
	rep.Adopt, err = l.Adopt(ctx, AdoptOptions{Version: version, DryRun: true, Uncapped: true})
	if err != nil {
		notChecked("adopt", err)
	}

	if FollowerGrabs(l.pol, version) {
		rep.RefreshSkipped = "the library grabs: turn Allow new downloads off to check adopted links"
		add(CoverageItem{Step: "refresh", Reason: "not_checked", Detail: rep.RefreshSkipped})
	} else {
		rep.Refresh, err = l.Refresh(ctx, RefreshOptions{Version: version, DryRun: true, Uncapped: true})
		if err != nil {
			notChecked("refresh", err)
		}
	}

	if len(errs) == 0 {
		if err := l.coverUnits(ctx, version, vp, rep, add); err != nil {
			notChecked("library", err)
		}
	}
	rep.Complete = len(rep.Items) == 0
	if len(rep.Counts) == 0 {
		rep.Counts = nil
	}
	return rep, errors.Join(errs...)
}

// coverLink is one active link of the library and whether its target exists.
type coverLink struct {
	path, target, statErr string
	adopted, alive        bool
	episodes              []int64
}

// coverUnits decides coverage from the library's own links: a title (movie) or episode of the followed library's arr
// files is covered exactly when the library has an active link for it whose target exists, imported or adopted.
// Each arr file with something not covered is an item, with adopt's or refresh's answer for it as the reason.
func (l *Library) coverUnits(ctx context.Context, version string, vp *policy.VersionPolicy, rep *CoverageReport,
	add func(CoverageItem)) error {
	rows, err := l.st.Reader().QueryContext(ctx, `SELECT l.title_id, l.path, l.target, l.adopted_from IS NOT NULL,
		COALESCE((SELECT json_group_array(le.episode_id) FROM link_episode le WHERE le.link_id = l.id), '[]')
		FROM link l JOIN link_version lv ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id
		WHERE v.policy_key = ? AND l.state = 'active' ORDER BY l.id`, version)
	if err != nil {
		return err
	}
	links := map[int64][]*coverLink{}
	for rows.Next() {
		var titleID int64
		var eps string
		k := &coverLink{}
		if err := rows.Scan(&titleID, &k.path, &k.target, &k.adopted, &eps); err != nil {
			rows.Close()
			return err
		}
		_ = json.Unmarshal([]byte(eps), &k.episodes)
		links[titleID] = append(links[titleID], k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, ks := range links {
		for _, k := range ks {
			var fi os.FileInfo
			if read, ok := l.adoptTarget(k.target); ok {
				fi, err = l.mount.Stat(ctx, read)
				if errors.Is(err, ErrMountDown) {
					return err
				}
			} else {
				fi, err = os.Stat(k.target)
			}
			switch {
			case err != nil:
				k.statErr = err.Error()
			case !fi.Mode().IsRegular():
				k.statErr = k.target + ": not a regular file"
			default:
				k.alive = true
			}
		}
	}

	// What adopt and refresh said, by the arr's file and by the library's link.
	reasons := map[string]CoverageItem{}
	if a := rep.Adopt; a != nil {
		for _, k := range a.Links {
			reasons[k.From] = CoverageItem{Step: "adopt", Reason: "to_link", Detail: "would link " + k.Path}
		}
		for _, p := range a.Problems {
			reasons[p.Path] = CoverageItem{Step: "adopt", Reason: p.Reason, Detail: p.Detail}
		}
	}
	if r := rep.Refresh; r != nil {
		for _, s := range r.Swaps {
			reasons[s.Old] = CoverageItem{Step: "refresh", Reason: "to_move", Detail: "would move to " + s.Path}
		}
		for _, p := range r.Problems {
			reasons[p.Path] = CoverageItem{Step: "refresh", Reason: p.Reason, Detail: p.Detail}
		}
	}

	files, err := l.adoptFiles(ctx, vp.Follows, version, nil)
	if err != nil {
		return err
	}
	kept := map[*coverLink]bool{}
	for i := range files {
		f := &files[i]
		if f.followerID == 0 {
			continue // a missing title, listed already
		}
		ks := links[f.titleID]
		covers := func(ep int64) *coverLink {
			for _, k := range ks {
				if k.alive && (ep == 0 || slices.Contains(k.episodes, ep)) {
					return k
				}
			}
			return nil
		}
		var units []int64 // episodes; a movie is one unit, 0
		if vp.Kind == policy.KindSeries {
			if units, _, err = adoptEpisodes(ctx, l.st.Reader(), f.id); err != nil {
				return err
			}
		} else {
			units = []int64{0}
		}
		var uncovered []int64
		for _, u := range units {
			if k := covers(u); k != nil {
				if !k.adopted {
					kept[k] = true
				}
			} else {
				uncovered = append(uncovered, u)
			}
		}
		if len(units) > 0 && len(uncovered) == 0 {
			continue
		}
		it := CoverageItem{Step: "library", Reason: "not_covered", Path: f.path,
			Title: fmt.Sprintf("%s (%d)", f.titleName, f.year)}
		if r, ok := reasons[f.path]; ok {
			it.Step, it.Reason, it.Detail = r.Step, r.Reason, r.Detail
		}
		var dead []string
		for _, k := range ks {
			if k.alive || (vp.Kind == policy.KindSeries && !overlaps(k.episodes, uncovered)) {
				continue
			}
			if r, ok := reasons[k.path]; ok && r.Step == "refresh" {
				it.Step, it.Reason, it.Detail = r.Step, r.Reason, r.Detail
			}
			dead = append(dead, fmt.Sprintf("library link %s -> %s: %s", k.path, k.target, k.statErr))
		}
		if len(dead) > 0 {
			it.Detail = strings.TrimPrefix(it.Detail+"; "+strings.Join(dead, "; "), "; ")
		}
		if vp.Kind == policy.KindSeries {
			it.Episodes = uncovered
		}
		add(it)
	}
	rep.OwnFilesKept = len(kept)
	return nil
}
