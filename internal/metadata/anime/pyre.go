package anime

import (
	"regexp"
	"unicode"
	"unicode/utf8"
)

// pyRE searches like a Python 3 regex made of alternatives that may start or end with \b. Python's \b is
// Unicode-aware (a Japanese letter is a word character), Go's is ASCII-only; the golden outputs match these
// patterns against native titles too, so the boundaries are checked here with Python's rule.
type pyRE struct {
	alts                  []*regexp.Regexp
	wantBefore, wantAfter []bool
}

type pyAlt struct {
	pattern       string // Go syntax, without \b
	before, after bool
}

func newPyRE(ci bool, alts ...pyAlt) *pyRE {
	r := &pyRE{}
	for _, a := range alts {
		p := `^(?:` + a.pattern + `)`
		if ci {
			p = `(?i)` + p
		}
		r.alts = append(r.alts, regexp.MustCompile(p))
		r.wantBefore = append(r.wantBefore, a.before)
		r.wantAfter = append(r.wantAfter, a.after)
	}
	return r
}

// pyWord is Python's \w for str: alphanumeric (letters and every numeric category) or underscore.
func pyWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

func pyBoundary(s string, i int) bool {
	var before, after bool
	if i > 0 {
		r, _ := utf8.DecodeLastRuneInString(s[:i])
		before = pyWord(r)
	}
	if i < len(s) {
		r, _ := utf8.DecodeRuneInString(s[i:])
		after = pyWord(r)
	}
	return before != after
}

// find returns the leftmost match's start, or -1.
func (r *pyRE) find(s string) int {
	for i := 0; i <= len(s); {
		for k, re := range r.alts {
			if r.wantBefore[k] && !pyBoundary(s, i) {
				continue
			}
			loc := re.FindStringIndex(s[i:])
			if loc == nil {
				continue
			}
			if r.wantAfter[k] && !pyBoundary(s, i+loc[1]) {
				continue
			}
			return i
		}
		if i == len(s) {
			break
		}
		_, n := utf8.DecodeRuneInString(s[i:])
		i += n
	}
	return -1
}

// MatchString is re.search(...) is not None.
func (r *pyRE) MatchString(s string) bool { return r.find(s) >= 0 }
