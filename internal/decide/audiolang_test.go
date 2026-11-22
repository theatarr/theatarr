package decide

import "testing"

// An audio track's tag: two-letter codes (a Matroska LanguageIETF tag's primary subtag) as their three-letter ones,
// ISO 639-2/B codes (fre, ger, chi) as their 639-2/T ones (fra, deu, zho), Norwegian Bokmål and Nynorsk as
// Norwegian, a region cut (ja-JP). und, zxx, mul, mis, a private code and a code the table lacks are unknown.
func TestAudioLanguage(t *testing.T) {
	for in, want := range map[string]string{"jpn": "jpn", "ja": "jpn", "ja-JP": "jpn", "en": "eng", "nob": "nor",
		"nb": "nor", "nn": "nor", "nno": "nor", "fre": "fra", "fra": "fra", "FRE": "fra", "ger": "deu", "chi": "zho",
		"zho": "zho", "kor": "kor", "pt-BR": "por", "spa": "spa",
		"und": "", "zxx": "", "mul": "", "mis": "", "qaa": "", "myn": "", "": "", "english": ""} {
		got, ok := AudioLanguage(in)
		if got != want || ok != (want != "") {
			t.Errorf("AudioLanguage(%q) = %q, %v, want %q", in, got, ok, want)
		}
	}
}

// A language held by code or by name reads the same: a title stores "ko" or "Korean" (live holds both).
func TestLanguageCode(t *testing.T) {
	for in, want := range map[string]string{"ko": "kor", "Korean": "kor", "English": "eng", "en": "eng",
		"French": "fra", "fr": "fra", "Norwegian": "nor", "Portuguese (Brazil)": "por", "Spanish (Latino)": "spa",
		"cn": "zho", "Japanese": "jpn", "": "", "Unknown": "", "any": "", "original": "", "Flemish": "", "xx": ""} {
		got, ok := LanguageCode(in)
		if got != want || ok != (want != "") {
			t.Errorf("LanguageCode(%q) = %q, %v, want %q", in, got, ok, want)
		}
	}
	if n := LanguageName("kor"); n != "Korean" {
		t.Errorf("LanguageName(kor) = %q", n)
	}
	if n := LanguageName("nor"); n != "Norwegian" {
		t.Errorf("LanguageName(nor) = %q", n)
	}
}
