package format

// Port of Radarr src/NzbDrone.Core/CustomFormats/{CustomFormat,CustomFormatCalculationService,
// SpecificationMatchesGroup}.cs and Profiles/Qualities/QualityProfile.cs (CalculateCustomFormatScore) @ a96bf7c;
// the Sonarr files are the same @ cab419a.

import (
	"errors"
	"sort"
)

// Format is a custom format: a name and its conditions.
type Format struct {
	// ID is the arr's id, or the importer's for TRaSH formats. Profiles refer to formats by ID, as the arr does.
	ID                  int
	Name                string
	Flavour             Flavour
	IncludeWhenRenaming bool
	Specs               []*Spec

	// TrashID and TrashScores are set for formats imported from TRaSH JSON.
	TrashID     string
	TrashScores map[string]int
}

// Group is the outcome of one implementation's conditions (SpecificationMatchesGroup).
type Group struct {
	Implementation string
	Specs          []*Spec
	Results        []bool
}

// DidMatch is SpecificationMatchesGroup.DidMatch: no required condition failed and at least one matched.
func (g Group) DidMatch() bool {
	any := false
	for i, ok := range g.Results {
		if g.Specs[i].Required && !ok {
			return false
		}
		any = any || ok
	}
	return any
}

// Evaluation is a format's result for one input, kept so a decision can say why a format matched or not.
type Evaluation struct {
	Format  *Format
	Matched bool
	Groups  []Group
	// Errs lists conditions that could not be evaluated (regex timeouts). Each counted as not satisfied.
	Errs []error
}

// Evaluate runs every condition of f against in. Conditions of the same implementation form a group, and the
// format matches when every group matches. A format with no conditions matches everything (Enumerable.All on an
// empty list), as in the arr.
func (f *Format) Evaluate(in *Input) Evaluation {
	ev := Evaluation{Format: f, Matched: true}
	index := map[string]int{}
	for _, s := range f.Specs {
		impl := s.Condition.Implementation()
		i, ok := index[impl]
		if !ok {
			i = len(ev.Groups)
			index[impl] = i
			ev.Groups = append(ev.Groups, Group{Implementation: impl})
		}
		m, err := s.IsSatisfiedBy(in)
		if err != nil {
			ev.Errs = append(ev.Errs, err)
		}
		ev.Groups[i].Specs = append(ev.Groups[i].Specs, s)
		ev.Groups[i].Results = append(ev.Groups[i].Results, m)
	}
	for _, g := range ev.Groups {
		if !g.DidMatch() {
			ev.Matched = false
		}
	}
	return ev
}

// Matches reports whether f matches in.
func (f *Format) Matches(in *Input) bool { return f.Evaluate(in).Matched }

// Calculate is CustomFormatCalculationService.ParseCustomFormat(input, allCustomFormats): the formats that match,
// ordered by name. The error joins any condition errors; the result is still the arr's rules applied with those
// conditions counted as not satisfied.
func Calculate(formats []*Format, in *Input) ([]*Format, error) {
	var out []*Format
	var errs []error
	for _, f := range formats {
		ev := f.Evaluate(in)
		errs = append(errs, ev.Errs...)
		if ev.Matched {
			out = append(out, f)
		}
	}
	SortByName(out)
	return out, errors.Join(errs...)
}

// SortByName orders formats like the arr's OrderBy(x => x.Name). .NET compares with the current culture; we compare
// ordinally, which only differs for names that need culture rules. The order never changes a score.
func SortByName(fs []*Format) {
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Name < fs[j].Name })
}

// Profile is the format part of a quality profile.
type Profile struct {
	ID                    int
	Name                  string
	Flavour               Flavour
	FormatItems           []FormatItem
	MinFormatScore        int
	CutoffFormatScore     int
	MinUpgradeFormatScore int

	// TrashID and TrashScoreSet are set for profiles imported from TRaSH JSON.
	TrashID       string
	TrashScoreSet string
}

// FormatItem is a profile's score for one format, by format ID.
type FormatItem struct {
	Format int
	Name   string
	Score  int
}

// Score is QualityProfile.CalculateCustomFormatScore: the sum of the scores of the profile's items whose format is
// among the matched ones (compared by id, as CustomFormat.Equals does).
func (p *Profile) Score(matched []*Format) int {
	ids := make(map[int]bool, len(matched))
	for _, f := range matched {
		ids[f.ID] = true
	}
	total := 0
	for _, it := range p.FormatItems {
		if ids[it.Format] {
			total += it.Score
		}
	}
	return total
}
