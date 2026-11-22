package journal

import (
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
)

// The journal names a title's original language stored as a code or a name the same ("ko" is Korean); a release
// label is compared as it is ("Spanish (Latino)"). The audio tag readings moved with the shared mapping
// (decide.TestAudioLanguage).
func TestLanguageNameShared(t *testing.T) {
	for in, want := range map[string]string{"ko": "Korean", "Korean": "Korean", "fr": "French",
		"Flemish": "Flemish", "Spanish (Latino)": "Spanish (Latino)"} {
		if got := languageName(in); got != want {
			t.Errorf("languageName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The file audio warning names languages (never codes), and the wrong-audio refusal is not cut in the short form.
func TestFileAudioWordsAndRefusalWhole(t *testing.T) {
	b := &builder{}
	e := &Entry{}
	e.f.fileAudio = []string{"fre", "fra"}
	ti := &titleInfo{t: decide.Title{OriginalLanguage: "Japanese", NamedOriginalLanguage: "Japanese"}}
	b.checkFile(e, ti)
	if len(e.Flags) != 1 || e.Flags[0].Reason != "the file's audio is French; wanted English or Japanese" {
		t.Errorf("flags %+v", e.Flags)
	}
	// One track of unknown language (und): no verdict, as the import's rule.
	u := &Entry{}
	u.f.fileAudio = []string{"fre", "und"}
	if b.checkFile(u, ti); len(u.Flags) != 0 {
		t.Errorf("an unknown track flagged %+v", u.Flags)
	}
	long := "Import refused: French and Spanish audio only; this library wants English or Japanese. Another release " +
		"with the same audio would be taken."
	if got := cleanText(long, false); got != long {
		t.Errorf("cut: %q", got)
	}
}
