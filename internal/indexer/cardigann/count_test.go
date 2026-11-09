package cardigann

import "testing"

// Sites that write thousands with dots ("1.234 seeders") are read as thousands, as Prowlarr does; a decimal
// ("5.0") still reads as its whole part.
func TestParseCountDotThousands(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int64{"1.234": 1234, "12.345.678": 12345678, "1.234 seeders": 1234,
		"5.0": 5, "1.5": 1, "1,234": 1234, "1,234.5": 1234, "-1.234": -1234} {
		if got := parseCount(in); got != want {
			t.Errorf("parseCount(%q) = %d, want %d", in, got, want)
		}
	}
}
