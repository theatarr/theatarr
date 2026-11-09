package parsertest

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// Outcome is the result of one case.
type Outcome int

const (
	Pass Outcome = iota
	Fail
	NotApplicable
)

func (o Outcome) String() string {
	switch o {
	case Pass:
		return "pass"
	case Fail:
		return "fail"
	}
	return "n/a"
}

// Result is one case's outcome.
type Result struct {
	Fixture string
	Case    Case
	Outcome Outcome
	Err     error
}

// Report holds the outcome of every case, in file and case order.
type Report struct {
	Results []Result
}

// Run runs every case of every file.
func Run(c Caller, files []File) *Report {
	r := &Report{}
	for _, f := range files {
		for _, tc := range f.Cases {
			err := RunCase(c, tc)
			o := Pass
			switch {
			case errors.Is(err, ErrNotApplicable):
				o = NotApplicable
			case err != nil:
				o = Fail
			}
			r.Results = append(r.Results, Result{Fixture: f.Fixture, Case: tc, Outcome: o, Err: err})
		}
	}
	return r
}

// Verify fails t for every case whose outcome does not match its mark: a case without known_failure must pass,
// and a case with one must fail (so the mark is removed once it passes). It is for a parser that answers every
// call; a not-applicable case is an error here.
func (r *Report) Verify(t testing.TB) {
	t.Helper()
	for _, res := range r.Results {
		c := res.Case
		switch {
		case res.Outcome != Pass && c.KnownFailure == "":
			t.Errorf("%s (line %d): %v", c.ID, c.Line, res.Err)
		case res.Outcome == Pass && c.KnownFailure != "":
			t.Errorf("%s (line %d): passes now; remove its known_failure (%s)", c.ID, c.Line, c.KnownFailure)
		}
	}
}

// Row is one line of a pass-rate table.
type Row struct {
	Key           string
	Total         int
	Pass          int
	Fail          int
	NotApplicable int
}

// Applicable is the number of cases the parser answers.
func (r Row) Applicable() int { return r.Total - r.NotApplicable }

// Rate is the pass rate over all cases, in percent.
func (r Row) Rate() float64 { return Pct(r.Pass, r.Total) }

// ApplicableRate is the pass rate over the applicable cases, in percent.
func (r Row) ApplicableRate() float64 { return Pct(r.Pass, r.Applicable()) }

// Pct is 100*a/b, and 0 when b is 0.
func Pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

// Table groups the results by key (in first-seen order) and appends a total row.
func (r *Report) Table(key func(Result) string) []Row {
	idx := map[string]int{}
	var rows []Row
	var total Row
	total.Key = "total"
	for _, res := range r.Results {
		k := key(res)
		i, ok := idx[k]
		if !ok {
			i = len(rows)
			idx[k] = i
			rows = append(rows, Row{Key: k})
		}
		for _, row := range []*Row{&rows[i], &total} {
			row.Total++
			switch res.Outcome {
			case Pass:
				row.Pass++
			case Fail:
				row.Fail++
			case NotApplicable:
				row.NotApplicable++
			}
		}
	}
	return append(rows, total)
}

// ByFixture is the table per fixture.
func (r *Report) ByFixture() []Row { return r.Table(func(res Result) string { return res.Fixture }) }

// ByCall is the table per call, sorted by call name.
func (r *Report) ByCall() []Row {
	rows := r.Table(func(res Result) string { return res.Case.Call })
	body := rows[:len(rows)-1]
	sort.SliceStable(body, func(i, j int) bool { return body[i].Key < body[j].Key })
	return rows
}

// Format renders rows as a plain text table: cases, passes and the pass rate, plus the not-applicable count and
// the rate over applicable cases when any case is not applicable.
func Format(rows []Row) string {
	withNA := false
	width := 5
	for _, row := range rows {
		withNA = withNA || row.NotApplicable > 0
		width = max(width, len(row.Key))
	}
	var b strings.Builder
	if withNA {
		fmt.Fprintf(&b, "%-*s %5s %5s %8s %5s %5s %8s\n", width, "", "cases", "pass", "all", "n/a", "appl", "appl%")
	} else {
		fmt.Fprintf(&b, "%-*s %5s %5s %8s\n", width, "", "cases", "pass", "rate")
	}
	for _, row := range rows {
		if withNA {
			fmt.Fprintf(&b, "%-*s %5d %5d %7.2f%% %5d %5d %7.2f%%\n", width, row.Key, row.Total, row.Pass, row.Rate(),
				row.NotApplicable, row.Applicable(), row.ApplicableRate())
		} else {
			fmt.Fprintf(&b, "%-*s %5d %5d %7.2f%%\n", width, row.Key, row.Total, row.Pass, row.Rate())
		}
	}
	return b.String()
}

// String renders the per-fixture and per-call tables.
func (r *Report) String() string {
	return "per fixture:\n" + Format(r.ByFixture()) + "\nper call:\n" + Format(r.ByCall())
}
