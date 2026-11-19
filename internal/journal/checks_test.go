package journal

import "testing"

// The file audio check reads a BCP-47 tag by its base language, so ja-JP matches Japanese.
func TestAudioCode(t *testing.T) {
	for in, want := range map[string]string{"ja-JP": "jpn", "jpn": "jpn", "ja": "jpn", "en-US": "eng", "nob": "nor",
		"und": "und"} {
		if got := audioCode(in); got != want {
			t.Errorf("audioCode(%q) = %q, want %q", in, got, want)
		}
	}
}
