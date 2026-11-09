// Port of Radarr src/NzbDrone.Core/Parser/RomanNumerals/{RomanNumeral,RomanNumeralParser,
// ArabicRomanNumeral}.cs @ a96bf7c. ParsingService and MovieService use the mapping to match
// "Rocky 2" against "Rocky II".

package radarr

import (
	"fmt"
	"strconv"
	"strings"
)

var (
	romanThousands = []string{"MMM", "MM", "M"}
	romanHundreds  = []string{"CM", "DCCC", "DCC", "DC", "D", "CD", "CCC", "CC", "C"}
	romanTens      = []string{"XC", "LXXX", "LXX", "LX", "L", "XL", "XXX", "XX", "X"}
	romanUnits     = []string{"IX", "VIII", "VII", "VI", "V", "IV", "III", "II", "I"}
)

// RomanNumeral is Radarr's RomanNumeral: an integer in [1, 3999].
type RomanNumeral struct{ value int }

// NewRomanNumeralFromInt is new RomanNumeral(int). Radarr checks the range only when formatting;
// here it is checked up front.
func NewRomanNumeralFromInt(v int) (RomanNumeral, error) {
	if v > 3999 || v < 1 {
		return RomanNumeral{}, fmt.Errorf("roman numerals must be within 1-3999, got %d", v)
	}
	return RomanNumeral{v}, nil
}

// NewRomanNumeralFromString is new RomanNumeral(string): 0 when the text is not a numeral.
func NewRomanNumeralFromString(s string) (RomanNumeral, error) {
	v, _ := TryParseRomanNumeral(s)
	return RomanNumeral{v}, nil
}

// ToInt is RomanNumeral.ToInt.
func (r RomanNumeral) ToInt() int { return r.value }

// ToRomanNumeral is RomanNumeral.ToRomanNumeral.
func (r RomanNumeral) ToRomanNumeral() string {
	n := r.value
	var b strings.Builder
	if t := n / 1000; t > 0 {
		b.WriteString(romanThousands[3-t])
	}
	n %= 1000
	if h := n / 100; h > 0 {
		b.WriteString(romanHundreds[9-h])
	}
	n %= 100
	if t := n / 10; t > 0 {
		b.WriteString(romanTens[9-t])
	}
	if u := n % 10; u > 0 {
		b.WriteString(romanUnits[9-u])
	}
	return b.String()
}

func (r RomanNumeral) String() string { return r.ToRomanNumeral() }

// TryParseRomanNumeral is RomanNumeral.TryParse.
func TryParseRomanNumeral(text string) (int, bool) {
	if text == "" {
		return 0, false
	}
	text = strings.ToUpper(text)
	value := 0
	step := func(table []string, unit, top int) {
		for i, s := range table {
			if strings.HasPrefix(text, s) {
				value += unit * (top - i)
				text = text[len(s):]
				return
			}
		}
	}
	step(romanThousands, 1000, 3)
	step(romanHundreds, 100, 9)
	step(romanTens, 10, 9)
	// The units are not stripped: Radarr compares the rest's length with the unit's length.
	for i, s := range romanUnits {
		if strings.HasPrefix(text, s) {
			value += 9 - i
			if len(text) > len(s) {
				return 0, false
			}
			return value, true
		}
	}
	if len(text) > 0 {
		return 0, false
	}
	return value, true
}

// ArabicRomanNumeral is Radarr's ArabicRomanNumeral.
type ArabicRomanNumeral struct {
	ArabicNumeral         int
	ArabicNumeralAsString string
	RomanNumeral          string
}

// RomanNumeralLowerCase is ArabicRomanNumeral.RomanNumeralLowerCase.
func (a ArabicRomanNumeral) RomanNumeralLowerCase() string { return strings.ToLower(a.RomanNumeral) }

// ArabicRomanNumeralsMapping is RomanNumeralParser.GetArabicRomanNumeralsMapping() with its
// default size: 1 to 20.
func ArabicRomanNumeralsMapping() []ArabicRomanNumeral {
	out := make([]ArabicRomanNumeral, 0, 20)
	for i := 1; i <= 20; i++ {
		out = append(out, ArabicRomanNumeral{i, strconv.Itoa(i), RomanNumeral{i}.ToRomanNumeral()})
	}
	return out
}
