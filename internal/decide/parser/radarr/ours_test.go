package radarr

import (
	"strings"
	"testing"
)

// TestWebTokens: ATVP before WEB and EAC3 or a separated DDP 5.1 after WEB read as WEB-DL (ours, Sonarr v5's source
// rule); DD5.1, DDP5.1 and an explicit WEBRip keep their reading.
func TestWebTokens(t *testing.T) {
	t.Parallel()
	for title, want := range map[string]string{
		"Movie.2024.2160p.HDR.WEB.EAC3-GRP":            "WEBDL-2160p",
		"Movie.2024.2160p.ATVP.WEB.DDP.5.1.DV.HDR-GRP": "WEBDL-2160p",
		"Movie.2024.2160p.ATVP.WEB.DV.HDR-GRP":         "WEBDL-2160p",
		"Movie.2024.2160p.HDR.WEB.DDP.5.1-GRP":         "WEBDL-2160p",
		"Movie 2024 2160p HDR WEB DDP 5.1 GRP":         "WEBDL-2160p",
		"Movie.2024.2160p.HDR.WEB.DD5.1-GRP":           "WEBDL-2160p",
		"Movie.2024.2160p.HDR.WEB.DDP5.1-GRP":          "WEBDL-2160p",
		"Movie.2024.2160p.ATVP.WEBRip.DDP.5.1-GRP":     "WEBRip-2160p",
		"Movie.2024.2160p.HDR.WEBRip.EAC3-GRP":         "WEBRip-2160p",
	} {
		if q := ParseQuality(title).Quality.Name; q != want {
			t.Errorf("%s: %s, want %s", title, q, want)
		}
	}
}

// TestJapaneseAndSubtitleLanguages: JPN, (JA) and upper-case JA are Japanese; a language right before SUBS or
// after "Multiple Subtitle" is a subtitle, not audio (ours, as the series parser reads it); a dual-audio name keeps
// both languages, and KA stays Georgian (Radarr's fixtures).
func TestJapaneseAndSubtitleLanguages(t *testing.T) {
	t.Parallel()
	for title, want := range map[string]string{
		"Akira.1988.2160p.UHD.BluRay.REMUX.JPN.ENG.SUBS-GRP":                  "Japanese",
		"Akira (1988) [JA] 1080p BluRay x264-GRP":                             "Japanese",
		"Akira (1988) (JA) 1080p BluRay x264-GRP":                             "Japanese",
		"Akira.1988.1080p.BluRay.ENG.SUBS-GRP":                                "Unknown",
		"Akira.1988.1080p.BluRay.x264.[Multiple Subtitle].[ENG][POR-BR][FRE]": "Unknown",
		"Akira.1988.1080p.BluRay.JPN.ENG.x264-GRP":                            "Japanese,English",
		"Akira (1988) [JA+EN] 1080p BluRay x264-GRP":                          "Japanese,English",
		"Movie.Title.2019.1080p.KA.WEB-DL-GRP":                                "Georgian",
	} {
		p := ParseMovieTitle(title, false)
		if p == nil {
			t.Errorf("%s: no parse", title)
			continue
		}
		var got []string
		for _, l := range p.Languages {
			got = append(got, l.Name)
		}
		if g := strings.Join(got, ","); g != want {
			t.Errorf("%s: %s, want %s", title, g, want)
		}
	}
}
