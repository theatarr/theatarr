package library

import (
	"context"
	"fmt"
	"sort"
)

// Coverage check (docs/migration.md): does a parallel library hold every title and file its followed library has? It
// reads only: the titles the followed library has from an arr with no row for the library, and dry runs of Adopt and
// (while the library does not grab) Refresh. The fix is the existing adopt and adopt -refresh, which never replace a
// file the library imported itself.

// Coverage outcomes.
const (
	// CoverageComplete: nothing missing, nothing left for adopt or refresh, no file skipped.
	CoverageComplete = "complete"
	// CoverageExceptions: complete except the listed expected exceptions (files the arr itself cannot give a link
	// to, or packs the library's own files keep), each with its reason.
	CoverageExceptions = "complete_except"
	// CoverageFailed: anything else (missing titles, links still to make or move, naming or path conflicts, a
	// refresh not checked).
	CoverageFailed = "failed"
)

// coverageMissingMax caps the titles listed in a coverage report.
const coverageMissingMax = 50

// CoverageTitle is one title of the followed library the parallel library has no row for.
type CoverageTitle struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Year int    `json:"year,omitempty"`
}

// CoverageIssue is one reason the coverage is not plainly complete: Step is "titles", "adopt" or "refresh", Reason
// the skip reason (as in AdoptReport.Skipped and RefreshReport.Skipped) or what is left, Note what it means.
type CoverageIssue struct {
	Step   string `json:"step"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
	Note   string `json:"note"`
}

// CoverageReport is the result of a coverage check.
type CoverageReport struct {
	Version string `json:"version"`
	Follows string `json:"follows"`
	// MissingTitles counts the titles of Follows (from an arr: version_source) with no row for Version; Missing
	// lists at most 50.
	MissingTitles int             `json:"missing_titles"`
	Missing       []CoverageTitle `json:"missing,omitempty"`
	Adopt         *AdoptReport    `json:"adopt"`
	// Refresh is nil when the library grabs; RefreshSkipped says why.
	Refresh        *RefreshReport `json:"refresh,omitempty"`
	RefreshSkipped string         `json:"refresh_skipped,omitempty"`
	Outcome        string         `json:"outcome"`
	Complete       bool           `json:"complete"` // Outcome is CoverageComplete
	// Exceptions are the expected ones, Failures the rest; both empty when complete.
	Exceptions []CoverageIssue `json:"exceptions,omitempty"`
	Failures   []CoverageIssue `json:"failures,omitempty"`
}

// coverageExpected are the skip reasons a coverage check lists as expected exceptions: the arr's own file cannot be
// linked, or the library's own files are kept. Every other reason fails the check.
var coverageExpected = map[string]string{
	AdoptMissing:         "the arr's file is not in its root",
	AdoptDangling:        "the arr's file points at nothing on the mount",
	AdoptNotLink:         "the arr's file is a regular file; Theatarr links mount content only",
	AdoptOffMount:        "the arr's file points outside the mount",
	AdoptHasFile:         "the library's own files are kept; some episodes of the arr's pack stay uncovered",
	RefreshNoArrFile:     "the arr has no file for it any more; the adopted link stays",
	RefreshSplit:         "the arr holds these episodes in another split; the adopted link stays",
	RefreshTargetMissing: "the arr's current file is not on the mount",
}

// coverageUnexpected explains the skip reasons that fail the check.
var coverageUnexpected = map[string]string{
	AdoptConflict:   "the link's path is taken by something else",
	AdoptNaming:     "the library's naming cannot name the file",
	AdoptNoVersion:  "the title has no row for the library",
	AdoptNoEpisodes: "the arr's file has no episodes recorded",
}

// Coverage checks a parallel library against its followed library. It writes nothing.
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
	rep := &CoverageReport{Version: version, Follows: vp.Follows}
	rows, err := l.st.Reader().QueryContext(ctx, `SELECT t.id, t.name, COALESCE(t.year, 0) FROM version f
		JOIN title t ON t.id = f.title_id
		WHERE f.policy_key = ? AND EXISTS (SELECT 1 FROM version_source vs WHERE vs.version_id = f.id)
		AND NOT EXISTS (SELECT 1 FROM version k WHERE k.title_id = f.title_id AND k.policy_key = ?)
		ORDER BY t.name, t.id`, vp.Follows, version)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t CoverageTitle
		if err := rows.Scan(&t.ID, &t.Name, &t.Year); err != nil {
			rows.Close()
			return nil, err
		}
		rep.MissingTitles++
		if len(rep.Missing) < coverageMissingMax {
			rep.Missing = append(rep.Missing, t)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if rep.Adopt, err = l.Adopt(ctx, AdoptOptions{Version: version, DryRun: true}); err != nil {
		return nil, err
	}
	if FollowerGrabs(l.pol, version) {
		rep.RefreshSkipped = "the library grabs: turn Allow new downloads off to check adopted links"
	} else if rep.Refresh, err = l.Refresh(ctx, RefreshOptions{Version: version, DryRun: true}); err != nil {
		return nil, err
	}
	rep.judge()
	return rep, nil
}

// judge sets the outcome from the counts.
func (r *CoverageReport) judge() {
	fail := func(step, reason string, n int, note string) {
		r.Failures = append(r.Failures, CoverageIssue{Step: step, Reason: reason, Count: n, Note: note})
	}
	skips := func(step string, skipped map[string]int) {
		reasons := make([]string, 0, len(skipped))
		for k := range skipped {
			reasons = append(reasons, k)
		}
		sort.Strings(reasons)
		for _, k := range reasons {
			if skipped[k] == 0 {
				continue
			}
			if note, ok := coverageExpected[k]; ok {
				r.Exceptions = append(r.Exceptions, CoverageIssue{Step: step, Reason: k, Count: skipped[k], Note: note})
				continue
			}
			note := coverageUnexpected[k]
			if note == "" {
				note = "skipped"
			}
			fail(step, k, skipped[k], note)
		}
	}
	if r.MissingTitles > 0 {
		fail("titles", "missing_titles", r.MissingTitles, "titles of "+r.Follows+" without a row for "+r.Version)
	}
	if r.Adopt.Linked > 0 {
		fail("adopt", "to_link", r.Adopt.Linked, "links adopt would still make: run library adopt")
	}
	skips("adopt", r.Adopt.Skipped)
	if r.Refresh == nil {
		fail("refresh", "not_checked", 0, r.RefreshSkipped)
	} else {
		if r.Refresh.Swapped > 0 {
			fail("refresh", "to_move", r.Refresh.Swapped, "adopted links refresh would still move: run library adopt -refresh")
		}
		skips("refresh", r.Refresh.Skipped)
		if r.Refresh.Failed > 0 {
			fail("refresh", "failed", r.Refresh.Failed, "adopted links whose move failed")
		}
	}
	switch {
	case len(r.Failures) > 0:
		r.Outcome = CoverageFailed
	case len(r.Exceptions) > 0:
		r.Outcome = CoverageExceptions
	default:
		r.Outcome = CoverageComplete
	}
	r.Complete = r.Outcome == CoverageComplete
}
