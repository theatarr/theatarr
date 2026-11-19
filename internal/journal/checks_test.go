package journal

import "testing"

// The file audio check reads two-letter codes (a Matroska LanguageIETF tag's primary subtag) as their three-letter
// ones, and Norwegian Bokmål and Nynorsk as Norwegian.
func TestAudioCode(t *testing.T) {
	for in, want := range map[string]string{"jpn": "jpn", "ja": "jpn", "en": "eng", "nob": "nor", "nb": "nor",
		"nn": "nor", "und": "und"} {
		if got := audioCode(in); got != want {
			t.Errorf("audioCode(%q) = %q, want %q", in, got, want)
		}
	}
}
