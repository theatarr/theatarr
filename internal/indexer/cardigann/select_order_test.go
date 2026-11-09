package cardigann

import (
	"slices"
	"testing"
)

// Prowlarr orders select keys with .NET OrderBy (culture-aware unless the process runs with invariant
// globalization): "added" < "Name" < "seeders" < "Size". Go sort.Strings is ordinal: "Name" < "Size" < "added" < "seeders".
func TestProwlarrSelectOrderMixedCase(t *testing.T) {
	t.Parallel()
	s := Setting{Type: "select", Options: []SettingOption{{Value: "added"}, {Value: "Name"}, {Value: "seeders"}, {Value: "Size"}}}
	// Prowlarr (culture order) stores index 0 for "added".
	if got := settingValue(s, float64(0)); got != "added" {
		t.Fatalf("index 0 maps to %q, Prowlarr culture order gives %q", got, "added")
	}
}

// Options.OrderBy(x => x.Key) uses CurrentCulture and is stable. These ASCII orders also match the en-US ICU
// collator used by .NET on Linux: punctuation has weight, digits are lexical (not natural), lower case wins ties.
func TestProwlarrSelectCultureOrder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"case", []string{"added", "Added", "name", "Name", "seeders", "Size"}},
		{"digits", []string{"1", "10", "2", "a10", "a2"}},
		{"punctuation", []string{"_Size", "-Size", "!Size", ".Size", "+Size", "1", "a_b", "a-b", "a.b", "a+b", "ab", "Size"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Setting{Type: "select"}
			for i := len(tc.want) - 1; i >= 0; i-- {
				s.Options = append(s.Options, SettingOption{Value: tc.want[i]})
			}
			original := slices.Clone(s.Options)
			for i, want := range tc.want {
				if got := settingValue(s, float64(i)); got != want {
					t.Errorf("index %d maps to %q, want %q", i, got, want)
				}
			}
			if !slices.Equal(s.Options, original) {
				t.Fatal("definition's option order changed")
			}
		})
	}
	// Canonically equivalent keys collate equally; preserve YAML order just as OrderBy does.
	s := Setting{Type: "select", Options: []SettingOption{{Value: "cafe\u0301"}, {Value: "caf\u00e9"}}}
	for i, option := range s.Options {
		if got := settingValue(s, float64(i)); got != option.Value {
			t.Errorf("stable tie %d maps to %q, want %q", i, got, option.Value)
		}
	}
}
