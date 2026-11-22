package journal

import "testing"

// The journal names a title's original language through the shared mapping the import's wrong-audio rule uses
// (decide.LanguageCode): a code stored for some titles and a name for others read the same. The audio tag
// readings moved with the mapping (decide.TestAudioLanguage).
func TestLanguageNameShared(t *testing.T) {
	for in, want := range map[string]string{"ko": "Korean", "Korean": "Korean", "fr": "French", "jpn": "Japanese",
		"Flemish": "Flemish"} {
		if got := languageName(in); got != want {
			t.Errorf("languageName(%q) = %q, want %q", in, got, want)
		}
	}
}
