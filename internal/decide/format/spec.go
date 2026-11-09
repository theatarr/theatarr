package format

// Port of Radarr src/NzbDrone.Core/CustomFormats/Specifications/*.cs @ a96bf7c and
// Sonarr src/NzbDrone.Core/CustomFormats/Specifications/*.cs @ cab419a.

import (
	"fmt"
	"math"

	"github.com/dlclark/regexp2"

	"github.com/theatarr/theatarr/internal/netregex"
)

// Spec is one condition of a format (the arr's ICustomFormatSpecification): its name, the negate and required
// flags shared by every type, and the type-specific Condition.
type Spec struct {
	Name      string
	Negate    bool
	Required  bool
	Condition Condition
}

// Condition is a specification type. Implementation returns the arr class name ("ReleaseTitleSpecification"),
// which is also the grouping key: conditions of one implementation form a group.
type Condition interface {
	Implementation() string
	// satisfied is the arr's IsSatisfiedByWithoutNegate. A non-nil error (a regex that timed out) means the
	// condition could not be evaluated.
	satisfied(in *Input) (bool, error)
}

// ownNegate is implemented by conditions that override the arr's IsSatisfiedBy instead of negating the plain result.
type ownNegate interface {
	satisfiedNegated(in *Input) (bool, error)
}

// IsSatisfiedBy is CustomFormatSpecificationBase.IsSatisfiedBy: the condition with its own negate applied. A
// condition that errors is not satisfied, negated or not, and the error is returned for logging.
func (s *Spec) IsSatisfiedBy(in *Input) (bool, error) {
	if s.Negate {
		if n, ok := s.Condition.(ownNegate); ok {
			m, err := n.satisfiedNegated(in)
			if err != nil {
				return false, err
			}
			return m, nil
		}
	}
	m, err := s.Condition.satisfied(in)
	if err != nil {
		return false, fmt.Errorf("condition %q: %w", s.Name, err)
	}
	if s.Negate {
		m = !m
	}
	return m, nil
}

// regexCondition is RegexSpecificationBase: a pattern compiled with RegexOptions.IgnoreCase. A null or blank value
// leaves the regex unset, and an unset regex or a null string never matches.
type regexCondition struct {
	Pattern string
	re      *regexp2.Regexp
}

func newRegex(pattern string) (regexCondition, error) {
	r := regexCondition{Pattern: pattern}
	if isBlank(pattern) {
		return r, nil
	}
	re, err := netregex.Compile(pattern, netregex.IgnoreCase)
	if err != nil {
		return r, fmt.Errorf("regex %q: %w", pattern, err)
	}
	r.re = re
	return r, nil
}

func (r regexCondition) match(s *string) (bool, error) {
	if s == nil || r.re == nil {
		return false, nil
	}
	return r.re.MatchString(*s)
}

// isBlank is .NET string.IsNullOrWhiteSpace for a non-null string.
func isBlank(s string) bool {
	for _, c := range s {
		switch c {
		case ' ', '\t', '\n', '\v', '\f', '\r', 0x85, 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000:
		default:
			if c >= 0x2000 && c <= 0x200A {
				continue
			}
			return false
		}
	}
	return true
}

// ReleaseTitle matches the release title (Radarr: the simplified title) or the file name.
type ReleaseTitle struct{ regexCondition }

// NewReleaseTitle compiles a Release Title condition.
func NewReleaseTitle(pattern string) (*ReleaseTitle, error) {
	r, err := newRegex(pattern)
	return &ReleaseTitle{r}, err
}

func (*ReleaseTitle) Implementation() string { return "ReleaseTitleSpecification" }
func (c *ReleaseTitle) satisfied(in *Input) (bool, error) {
	m, err := c.match(in.ReleaseTitle)
	if m || err != nil {
		return m, err
	}
	return c.match(in.Filename)
}

// Edition matches Radarr's parsed edition.
type Edition struct{ regexCondition }

// NewEdition compiles an Edition condition (Radarr).
func NewEdition(pattern string) (*Edition, error) {
	r, err := newRegex(pattern)
	return &Edition{r}, err
}

func (*Edition) Implementation() string              { return "EditionSpecification" }
func (c *Edition) satisfied(in *Input) (bool, error) { return c.match(in.Edition) }

// ReleaseGroup matches the parsed release group.
type ReleaseGroup struct{ regexCondition }

// NewReleaseGroup compiles a Release Group condition.
func NewReleaseGroup(pattern string) (*ReleaseGroup, error) {
	r, err := newRegex(pattern)
	return &ReleaseGroup{r}, err
}

func (*ReleaseGroup) Implementation() string              { return "ReleaseGroupSpecification" }
func (c *ReleaseGroup) satisfied(in *Input) (bool, error) { return c.match(in.ReleaseGroup) }

// Language matches a language id; Original (-2) is the mapped movie's or series' original language when known.
// With ExceptLanguage it matches when any language other than that one is present.
type Language struct {
	Value          int
	ExceptLanguage bool
}

func (*Language) Implementation() string { return "LanguageSpecification" }

func (c *Language) compared(in *Input) int {
	// input.MovieInfo != null is always true for us: an Input is built from a parse.
	if c.Value == LanguageOriginal && in.Media != nil && in.Media.OriginalLanguage != LanguageUnknown {
		return in.Media.OriginalLanguage
	}
	return c.Value
}

func (c *Language) satisfied(in *Input) (bool, error) {
	if in.Languages == nil {
		return false, nil
	}
	want := c.compared(in)
	for _, l := range in.Languages {
		if c.ExceptLanguage && l != want || !c.ExceptLanguage && l == want {
			return true, nil
		}
	}
	return false, nil
}

// satisfiedNegated is LanguageSpecification.IsSatisfiedByWithNegate: `!languages?.Contains(x) ?? false`, so null
// languages stay false when negated.
func (c *Language) satisfiedNegated(in *Input) (bool, error) {
	if in.Languages == nil {
		return false, nil
	}
	m, _ := c.satisfied(in)
	return !m, nil
}

// IndexerFlag matches when every bit of Value is set (Enum.HasFlag; a zero Value always matches).
type IndexerFlag struct{ Value int }

func (*IndexerFlag) Implementation() string { return "IndexerFlagSpecification" }
func (c *IndexerFlag) satisfied(in *Input) (bool, error) {
	return in.IndexerFlags&c.Value == c.Value, nil
}

// QualityModifier matches Radarr's quality modifier.
type QualityModifier struct{ Value int }

func (*QualityModifier) Implementation() string { return "QualityModifierSpecification" }
func (c *QualityModifier) satisfied(in *Input) (bool, error) {
	return in.Modifier == c.Value, nil
}

// Resolution matches the quality's resolution.
type Resolution struct{ Value int }

func (*Resolution) Implementation() string { return "ResolutionSpecification" }
func (c *Resolution) satisfied(in *Input) (bool, error) {
	return in.Resolution == c.Value, nil
}

// Source matches the quality's source, in the input's flavour.
type Source struct{ Value int }

func (*Source) Implementation() string { return "SourceSpecification" }
func (c *Source) satisfied(in *Input) (bool, error) {
	return in.Source == c.Value, nil
}

// Size matches Min < size <= Max, both in GiB.
type Size struct{ Min, Max float64 }

func (*Size) Implementation() string { return "SizeSpecification" }
func (c *Size) satisfied(in *Input) (bool, error) {
	return in.Size > gigabytes(c.Min) && in.Size <= gigabytes(c.Max), nil
}

// gigabytes is NumberExtensions.Gigabytes(double): Convert.ToInt64(g * 1024^3), which rounds half to even. .NET
// throws on overflow; we saturate.
func gigabytes(g float64) int64 {
	v := math.RoundToEven(g * 1024 * 1024 * 1024)
	if v >= math.MaxInt64 {
		return math.MaxInt64
	}
	if v <= math.MinInt64 {
		return math.MinInt64
	}
	return int64(v)
}

// Year is Radarr's Year condition: Min <= year <= Max, where year is the parsed year, or the movie's when the
// parse has none. No year at all never matches.
type Year struct{ Min, Max int }

func (*Year) Implementation() string { return "YearSpecification" }
func (c *Year) satisfied(in *Input) (bool, error) {
	var year int
	switch {
	case in.Year > 0:
		year = in.Year
	case in.Media != nil:
		year = in.Media.Year
	default:
		return false, nil
	}
	return year >= c.Min && year <= c.Max, nil
}

// ReleaseType is Sonarr's Release Type condition.
type ReleaseType struct{ Value int }

func (*ReleaseType) Implementation() string { return "ReleaseTypeSpecification" }
func (c *ReleaseType) satisfied(in *Input) (bool, error) {
	return in.ReleaseType == c.Value, nil
}
