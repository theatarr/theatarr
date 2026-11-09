package parsertest

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeCaller answers "parse" with a fixed episode-like object, "nothing" with null, and nothing else.
type fakeCaller struct{}

var fakeResult = map[string]any{
	"seriesTitle":    "Mad Men",
	"seasonNumber":   5.0,
	"episodeNumbers": []any{1.0},
	"releaseGroup":   "ORENJI",
	"languages":      []any{map[string]any{"id": 0.0, "name": "Unknown"}},
	"quality":        map[string]any{"quality": map[string]any{"name": "HDTV-720p"}},
}

func (fakeCaller) Call(name, input string, _ map[string]any) (any, error) {
	switch name {
	case "parse":
		return fakeResult, nil
	case "nothing":
		return nil, nil
	case "echo":
		return input, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrNotApplicable, name)
}

func (fakeCaller) Compare(name string, v any) (any, error) {
	if name != "lower" {
		return nil, fmt.Errorf("%w: compare %s", ErrNotApplicable, name)
	}
	s, ok := v.(string)
	if !ok {
		return v, nil
	}
	return strings.ToLower(s), nil
}

func str(s string) *string { return &s }

// TestRejectsWrongAnswers makes sure the matcher fails wrong answers, so a 100% rate means something.
func TestRejectsWrongAnswers(t *testing.T) {
	good := []Case{
		{Expect: map[string]any{"seriesTitle": "Mad Men", "episodeNumbers.0": 1.0, "episodeNumbers.#": 1.0, "airDate": nil}},
		{Expect: map[string]any{"languages.#.name": []any{"Unknown"}, "quality.quality.name": "HDTV-720p"}},
		{Expect: map[string]any{"episodeNumbers": []int{1}}}, // typed Go values compare as JSON
		{Contains: map[string][]any{"languages.#.name": {"Unknown"}, "episodeNumbers": {1.0}}},
		{Not: map[string]any{"": nil, "seasonNumber": 4.0}},
		{Compare: "lower", Expect: map[string]any{"seriesTitle": "MAD MEN"}},
	}
	bad := []Case{
		{Expect: map[string]any{"seriesTitle": "Mad Men 2"}},
		{Expect: map[string]any{"seriesTitle": "mad men"}},
		{Expect: map[string]any{"episodeNumbers": []any{}}},
		{Expect: map[string]any{"episodeNumbers.#": 2.0}},
		{Expect: map[string]any{"episodeNumbers.1": 2.0}},
		{Expect: map[string]any{"releaseGroup": nil}},
		{Expect: map[string]any{"missing.field": 0.0}},
		{Expect: map[string]any{"seasonNumber": "5"}},
		{Contains: map[string][]any{"languages.#.name": {"English"}}},
		{Contains: map[string][]any{"seriesTitle": {"Mad Men"}}}, // not a list
		{Not: map[string]any{"seasonNumber": 5.0}},
		{Not: map[string]any{"airDate": nil}},
		{Compare: "lower", Expect: map[string]any{"seriesTitle": "Mad Man"}},
	}
	for i, c := range good {
		c.Call, c.Input = "parse", str("x")
		if err := RunCase(fakeCaller{}, c); err != nil {
			t.Errorf("good %d: %v", i, err)
		}
	}
	for i, c := range bad {
		c.Call, c.Input = "parse", str("x")
		if err := RunCase(fakeCaller{}, c); err == nil || errors.Is(err, ErrNotApplicable) {
			t.Errorf("bad %d: got %v, want a failure", i, err)
		}
	}

	// Null results: expect "" null passes, anything else fails.
	if err := RunCase(fakeCaller{}, Case{Call: "nothing", Input: str("x"), Expect: map[string]any{"": nil}}); err != nil {
		t.Errorf("null: %v", err)
	}
	if RunCase(fakeCaller{}, Case{Call: "nothing", Input: str("x"), Expect: map[string]any{"seriesTitle": "Mad Men"}}) == nil {
		t.Error("null result met a field expectation")
	}

	// Every input must pass.
	c := Case{Call: "echo", Inputs: []string{"a", "a", "b"}, Expect: map[string]any{"": "a"}}
	if RunCase(fakeCaller{}, c) == nil {
		t.Error("inputs: a failing input passed")
	}
	if RunCase(fakeCaller{}, Case{Call: "echo", Expect: map[string]any{"": ""}}) == nil {
		t.Error("a case without input passed")
	}
}

func TestOutcomesAndTables(t *testing.T) {
	files := []File{
		{Fixture: "A", Cases: []Case{
			{ID: "a0", Call: "parse", Input: str("x"), Expect: map[string]any{"seasonNumber": 5.0}},
			{ID: "a1", Call: "parse", Input: str("x"), Expect: map[string]any{"seasonNumber": 6.0}},
			{ID: "a2", Call: "other", Input: str("x"), Expect: map[string]any{"": nil}},
		}},
		{Fixture: "B", Cases: []Case{
			{ID: "b0", Call: "parse", Input: str("x"), Compare: "upper", Expect: map[string]any{"seriesTitle": "x"}},
			{ID: "b1", Call: "echo", Input: str("x"), Expect: map[string]any{"": "x"}},
		}},
	}
	r := Run(fakeCaller{}, files)
	want := []Outcome{Pass, Fail, NotApplicable, NotApplicable, Pass}
	for i, res := range r.Results {
		if res.Outcome != want[i] {
			t.Errorf("%s: %v, want %v (%v)", res.Case.ID, res.Outcome, want[i], res.Err)
		}
	}
	fx := r.ByFixture()
	if got := fmt.Sprint(fx); got != "[{A 3 1 1 1} {B 2 1 0 1} {total 5 2 1 2}]" {
		t.Errorf("by fixture: %s", got)
	}
	calls := r.ByCall()
	if got := fmt.Sprint(calls); got != "[{echo 1 1 0 0} {other 1 0 0 1} {parse 3 1 1 1} {total 5 2 1 2}]" {
		t.Errorf("by call: %s", got)
	}
	if tot := fx[len(fx)-1]; tot.Applicable() != 3 || tot.ApplicableRate() < 66.6 || tot.ApplicableRate() > 66.7 {
		t.Errorf("applicable: %d %.2f", tot.Applicable(), tot.ApplicableRate())
	}

	// Verify flags the unmarked failure and the n/a cases, and a known failure that passes.
	files[0].Cases[0].KnownFailure = "marked"
	files[0].Cases[1].KnownFailure = "marked"
	rec := &recorder{}
	Run(fakeCaller{}, files).Verify(rec)
	if len(rec.errs) != 3 || !strings.Contains(rec.errs[0], "a0") || !strings.Contains(rec.errs[1], "a2") ||
		!strings.Contains(rec.errs[2], "b0") {
		t.Errorf("verify: %q", rec.errs)
	}
}

type recorder struct {
	testing.TB
	errs []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}
