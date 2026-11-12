package library

import (
	"context"
	"errors"
	"fmt"
)

// Coverage check (docs/migration.md): does a parallel library hold every title and file its followed library has? It
// reads only: the titles the followed library has from an arr with no row for the library, and dry runs of Adopt and
// (while the library does not grab) Refresh. It is complete only when nothing is missing, nothing is left for adopt or
// refresh and nothing was skipped, for any reason; otherwise it lists every item and the operator decides. The fix is
// the existing adopt and adopt -refresh, which never replace a file the library imported itself.

// CoverageItem is one thing that keeps a coverage check from being complete. Step is "titles", "adopt" or "refresh";
// Reason is the skip reason (as in AdoptReport.Skipped and RefreshReport.Skipped) or one of missing_title, to_link,
// to_move, not_checked (the step could not run: Detail says why); Detail carries the underlying error text where there was one.
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
	// OwnFilesKept counts the arr files the library has its own file for (Adopt.HasFile): covered, listed for
	// information only.
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
	if a := rep.Adopt; a != nil {
		rep.OwnFilesKept = a.HasFile
		for _, k := range a.Links {
			add(CoverageItem{Step: "adopt", Reason: "to_link", Path: k.From, Detail: "would link " + k.Path})
		}
		for _, p := range a.Problems {
			add(CoverageItem{Step: "adopt", Reason: p.Reason, Path: p.Path, Detail: p.Detail, Episodes: p.MissingEpisodes})
		}
	}
	if err != nil {
		notChecked("adopt", err)
	}

	if FollowerGrabs(l.pol, version) {
		rep.RefreshSkipped = "the library grabs: turn Allow new downloads off to check adopted links"
		add(CoverageItem{Step: "refresh", Reason: "not_checked", Detail: rep.RefreshSkipped})
	} else {
		rep.Refresh, err = l.Refresh(ctx, RefreshOptions{Version: version, DryRun: true, Uncapped: true})
		if r := rep.Refresh; r != nil {
			for _, s := range r.Swaps {
				add(CoverageItem{Step: "refresh", Reason: "to_move", Path: s.Old, Detail: "would move to " + s.Path})
			}
			for _, p := range r.Problems {
				add(CoverageItem{Step: "refresh", Reason: p.Reason, Path: p.Path, Detail: p.Detail})
			}
		}
		if err != nil {
			notChecked("refresh", err)
		}
	}

	a, r := rep.Adopt, rep.Refresh
	rep.Complete = len(rep.Items) == 0 && a != nil && a.Linked == 0 && len(a.Skipped) == 0 && a.MissingEpisodes == 0 &&
		r != nil && r.Swapped == 0 && len(r.Skipped) == 0 && r.Failed == 0
	if len(rep.Counts) == 0 {
		rep.Counts = nil
	}
	return rep, errors.Join(errs...)
}
