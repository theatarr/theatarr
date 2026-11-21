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

// TestJapaneseAndSubtitleLanguages: JPN, (JA) and upper-case JA are Japanese; a language right before the word
// SUBS/SUB or after "Multiple Subtitle", or SUB joined before a language name, is a subtitle, not audio (ours); a
// joined SUBFRENCH does not make the language before it a subtitle; a dual-audio name keeps both languages, and KA
// stays Georgian (Radarr's fixtures).
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
		"Akira.1988.1080p.BluRay.JPN.English.Subs-GRP":                        "Japanese",
		"Akira.1988.1080p.BluRay.JPN.[Multiple Subtitle].[EN][DE]-GRP":        "Japanese",
		"Akira (1988) 1080p BluRay [English Subs] x264-GRP":                   "Unknown",
		"Akira.1988.1080p.BluRay.ENG.SUBFRENCH-GRP":                           "English",
		"Akira.1988.1080p.BluRay.ITA.ENG.SUBS-GRP":                            "Italian",
		// Every tag form the audio readings accept, before every marker form.
		"Akira.1988.1080p.BluRay.ENG.PT-BR.SUBS-GRP":    "English",
		"Akira.1988.1080p.BluRay.pt-BR.SUBS-GRP":        "Unknown",
		"Akira.1988.1080p.BluRay.ENG.FRENCH.SUBBED-GRP": "English",
		"Akira.1988.1080p.BluRay.JPN.GER.DUB.SUBS-GRP":  "Japanese",
		"Akira.1988.1080p.BluRay.JPN.PL.DUB.Subs-GRP":   "Japanese",
		"Akira.1988.1080p.BluRay.JPN.DUB-PL.Subs-GRP":   "Japanese",
		"Akira.1988.1080p.BluRay.JPN.LEK.PL.Subs-GRP":   "Japanese",
		"Akira.1988.1080p.BluRay.ENG.(JA).Subs-GRP":     "English",
		// A bracketed tag before a marker outside it is left alone on purpose: it reads as on main.
		"Akira (1988) [French Audio] SUBS 1080p x264-GRP":       "French",
		"Akira (1988) [ENG.DTS.5.1].Subs 1080p x264-GRP":        "English",
		"Akira (1988) 1080p [JPN][CHS].SUBS x264-GRP":           "Japanese,Chinese", // main: Chinese; JPN is read now
		"Akira.1988.1080p.BluRay.JPN.2xUkr.Subs-GRP":            "Japanese",
		"Akira.1988.1080p.BluRay.JPN.EN.SUBS-GRP":               "Japanese",
		"Akira.1988.1080p.BluRay.JPN.Castellano.Subs-GRP":       "Japanese",
		"Akira.1988.1080p.BluRay.JPN.Español.Subs-GRP":          "Japanese",
		"Akira.1988.1080p.BluRay.ENG.VF2.SUBS-GRP":              "English",
		"Akira.1988.1080p.BluRay.JPN.ENG-Sub.x264-GRP":          "Japanese",
		"Akira.1988.1080p.BluRay.JPN.ENG_Subtitle-GRP":          "Japanese",
		"Akira.1988.1080p.BluRay.JPN.English Subtitles-GRP":     "Japanese",
		"Akira.1988.1080p.BluRay.JPN.Eng.Subbed-GRP":            "Japanese",
		"Akira.1988.1080p.BluRay.JPN.ENG.SUBFRENCH-GRP":         "Japanese,English",
		"Akira.1988.1080p.BluRay.JPN.English.Subs (Forced)-GRP": "Japanese", // the marker stays for the rule above
		// An explicit "Sub (…)" list is subtitles: live download 327 reads Ukrainian, Original (Eng) and English, not
		// the Russian of its subtitle list; with the only Eng in that list there is no English.
		"Достать ножи / Knives Out [2019 WEB-DL 2160p HDR10 HDR10+ Dolby Vision] [Hybrid] Dub + 2x MVO + 2x DVO + AVO + Ukr + Kaz + Original (Eng) + Sub (Rus Eng)": "Ukrainian,Original,English",
		"Достать ножи / Knives Out [2019 WEB-DL 2160p] Dub + Ukr + Sub (Rus Eng)":                                                                                   "Ukrainian",
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

// TestReleaseGroupIDTag: a trailing id tag ("[tt…]", "{imdb-tt…}", "-tt…") is not a group (ours; Radarr reads a
// bracketed IMDb id as the group); a real group before it wins, with none the group is empty.
func TestReleaseGroupIDTag(t *testing.T) {
	t.Parallel()
	for title, want := range map[string]string{
		"Knives.Out.2019.2160p.WEB-DL.H265-GRP [tt8946378].mkv":  "GRP",
		"Knives.Out.2019.2160p.WEB-DL.H265 [tt8946378].mkv":      "",
		"Knives.Out.2019.2160p.WEB-DL.H265-GRP {imdb-tt8946378}": "GRP",
		"Knives.Out.2019.2160p.WEB-DL.H265-GRP {tmdb-546554}":    "GRP",
		"Knives.Out.2019.2160p.WEB-DL.H265-tt8946378.mkv":        "",
		"Knives.Out.2019.2160p.WEB-DL.H265-GRP.mkv":              "GRP",
	} {
		got := ""
		if g := ParseReleaseGroup(title); g != nil {
			got = *g
		}
		if got != want {
			t.Errorf("%s: group %q, want %q", title, got, want)
		}
	}
}
