package anime

// Helpers that reproduce, exactly, the Python behaviour the golden outputs depend on: date strings and their sort
// keys, title tokens and similarity, episode range labels, and the str()/repr() forms that end up inside notes.

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SE is a TVDB season and episode. It marshals as [season, episode], like a Python tuple.
type SE [2]int

func (a SE) less(b SE) bool { return a[0] < b[0] || a[0] == b[0] && a[1] < b[1] }

func (a SE) String() string { return fmt.Sprintf("S%02dE%02d", a[0], a[1]) }

// FuzzyDate is AniList's partial date.
type FuzzyDate struct {
	Year  *int `json:"year"`
	Month *int `json:"month"`
	Day   *int `json:"day"`
}

// fuzzy is an AniList fuzzy date as a string: "2021", "2021-04" or "2021-04-06", or nil without a year.
func fuzzy(d FuzzyDate) *string {
	if d.Year == nil || *d.Year == 0 {
		return nil
	}
	s := fmt.Sprintf("%04d", *d.Year)
	if d.Month != nil && *d.Month != 0 {
		s += fmt.Sprintf("-%02d", *d.Month)
		if d.Day != nil && *d.Day != 0 {
			s += fmt.Sprintf("-%02d", *d.Day)
		}
	}
	return &s
}

// sortDate is a date's sort key: partial dates pad with zeros, unknown dates sort last.
func sortDate(s *string) string {
	if s == nil || *s == "" {
		return "9999"
	}
	v := *s
	if len(v) < 10 {
		pad := "-00-00"
		if len(v)-4 >= 0 && len(v)-4 <= len(pad) {
			return v + pad[len(v)-4:]
		}
		return v
	}
	return v
}

// pyDate is a date as a proleptic Gregorian ordinal (Python's date.toordinal()); 0 means none.
type pyDate int

const (
	pyDateMin pyDate = 1       // date.min
	pyDateMax pyDate = 3652059 // date.max
)

var ordinalEpoch = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)

func ordinal(t time.Time) pyDate { return pyDate((t.Unix()-ordinalEpoch.Unix())/86400 + 1) }

func (d pyDate) iso() string {
	return ordinalEpoch.AddDate(0, 0, int(d)-1).Format("2006-01-02")
}

func (d pyDate) String() string {
	if d == 0 {
		return "None"
	}
	return d.iso()
}

// toDate parses a date: "2021" and "2021-04" pad to the first day; anything else must be exactly
// YYYY-MM-DD (Python 3.10's date.fromisoformat).
func toDate(s string) pyDate {
	p := s
	if len(s) < 10 {
		p = s + "-01-01"
		if len(p) > 10 {
			p = p[:10]
		}
	}
	if len(p) != 10 {
		return 0
	}
	for i, c := range p {
		if i == 4 || i == 7 {
			if c != '-' {
				return 0
			}
		} else if c < '0' || c > '9' {
			return 0
		}
	}
	t, err := time.Parse("2006-01-02", p)
	if err != nil || t.Year() < 1 {
		return 0
	}
	return ordinal(t)
}

func toDateP(s *string) pyDate {
	if s == nil {
		return 0
	}
	return toDate(*s)
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func or(d, def pyDate) pyDate {
	if d == 0 {
		return def
	}
	return d
}

var stopWords = setOf("the", "of", "a", "to", "in", "no", "kara", "from", "movie", "season", "part", "and", "ni", "wa",
	"ga", "wo", "de", "hen", "ban", "tv", "ova", "oad", "special", "episode", "2nd", "3rd", "4th", "nd",
	"rd", "th", "datta", "ken", "shitara", "tensei", "hajimeru", "isekai", "seikatsu", "kimetsu",
	"yaiba", "starting", "life", "another", "world", "that", "time", "got", "reincarnated", "as",
	"demon", "slayer", "gekijouban", "made", "abyss", "re", "zero", "rezero", "slime")

func setOf(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

var wordRE = regexp.MustCompile(`[a-z0-9]+`)

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// tokens are a title's lower-case word tokens; with stop, stop words and plain numbers are left out.
func tokens(s string, stop bool) map[string]bool {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "re:zero", "rezero")
	s = strings.ReplaceAll(s, "re:petit", "repetit")
	out := map[string]bool{}
	for _, t := range wordRE.FindAllString(s, -1) {
		if !stop || (!stopWords[t] && !allDigits(t)) {
			out[t] = true
		}
	}
	return out
}

func inter(a, b map[string]bool) int {
	n := 0
	for k := range a {
		if b[k] {
			n++
		}
	}
	return n
}

// titleSim is the best overlap of any name with other, over the smaller token set.
func titleSim(names []string, other string, stop bool) float64 {
	b := tokens(other, stop)
	best := 0.0
	for _, n := range names {
		a := tokens(n, stop)
		if len(a) > 0 && len(b) > 0 {
			best = math.Max(best, float64(inter(a, b))/float64(min(len(a), len(b))))
		}
	}
	return best
}

// jaccard is the best symmetric token overlap between any two titles.
func jaccard(as, bs []string) float64 {
	best := 0.0
	for _, x := range as {
		a := tokens(x, false)
		for _, y := range bs {
			b := tokens(y, false)
			if len(a) > 0 && len(b) > 0 {
				i := inter(a, b)
				best = math.Max(best, float64(i)/float64(len(a)+len(b)-i))
			}
		}
	}
	return best
}

// ranges labels episodes as runs: [(1,1),(1,2),(1,3),(0,5)] -> "S01E01-E03, S00E05".
func ranges(eps []SE) string {
	type run struct{ s, a, b int }
	var out []run
	var cur *run
	for _, x := range eps {
		if cur != nil && x[0] == cur.s && x[1] == cur.b+1 {
			cur.b = x[1]
			continue
		}
		if cur != nil {
			out = append(out, *cur)
		}
		cur = &run{x[0], x[1], x[1]}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	parts := make([]string, len(out))
	for i, r := range out {
		p := fmt.Sprintf("S%02dE%02d", r.s, r.a)
		if r.b != r.a {
			p += fmt.Sprintf("-E%02d", r.b)
		}
		parts[i] = p
	}
	return strings.Join(parts, ", ")
}

var stemSplitRE = newPyRE(false, pyAlt{`[:\[(~\-]`, false, false}, pyAlt{` movie`, false, true}, pyAlt{` the movie`, false, true})

// stem is a title's series stem: "Fate/stay night: Unlimited Blade Works" -> "fate stay night".
func stem(title string) string {
	t := strings.ToLower(title)
	if i := stemSplitRE.find(t); i >= 0 {
		t = t[:i]
	}
	return strings.Join(wordRE.FindAllString(t, -1), " ")
}

func normWords(s string) string {
	return strings.Join(wordRE.FindAllString(strings.ToLower(s), -1), " ")
}

// ---- str() and repr() as Python prints them

func pyStr(s *string) string {
	if s == nil {
		return "None"
	}
	return *s
}

// pyFloat is repr(float).
func pyFloat(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if f == math.Trunc(f) && math.Abs(f) < 1e16 {
		return strconv.FormatFloat(f, 'f', 1, 64)
	}
	if strings.Contains(s, "e") {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return s
}

// round2 is Python's round(x, 2) (correctly rounded from the binary value).
func round2(f float64) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(f, 'f', 2, 64), 64)
	return v
}

func pyIntList(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// pyRepr is repr(str).
func pyRepr(s string) string {
	q := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = `"`
	}
	var b strings.Builder
	b.WriteString(q)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case string(r) == q:
			b.WriteString(`\` + q)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString(q)
	return b.String()
}

// pyQuote is urllib.parse.quote(s) with the default safe="/".
func pyQuote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_.-~/", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func sp(s string) *string { return &s }

func ip(i int) *int { return &i }

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func intv(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func sortedUniqueInts(xs []int) []int {
	m := map[int]bool{}
	out := []int{}
	for _, x := range xs {
		if !m[x] {
			m[x] = true
			out = append(out, x)
		}
	}
	sort.Ints(out)
	return out
}

func sortedFlags(f map[string]bool) []string {
	out := make([]string, 0, len(f))
	for k := range f {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nonEmpty(xs ...*string) []string {
	var out []string
	for _, x := range xs {
		if x != nil && *x != "" {
			out = append(out, *x)
		}
	}
	return out
}
