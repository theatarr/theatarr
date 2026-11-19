package journal

import "testing"

// The file audio check reads two-letter codes (a Matroska LanguageIETF tag's primary subtag) as their three-letter
// ones, ISO 639-2/B codes (fre, ger, chi) as their 639-2/T ones (fra, deu, zho) as the expected codes are, and
// Norwegian Bokmål and Nynorsk as Norwegian.
func TestAudioCode(t *testing.T) {
	for in, want := range map[string]string{"jpn": "jpn", "ja": "jpn", "en": "eng", "nob": "nor", "nb": "nor",
		"nn": "nor", "nno": "nor", "und": "und",
		"fre": "fra", "fra": "fra", "ger": "deu", "chi": "zho", "zho": "zho", "zxx": "zxx", "mul": "mul"} {
		if got := audioCode(in); got != want {
			t.Errorf("audioCode(%q) = %q, want %q", in, got, want)
		}
	}
}
