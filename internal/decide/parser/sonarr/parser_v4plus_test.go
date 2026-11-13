package sonarr

import (
	"fmt"
	"strings"
	"testing"
)

// TestV4Plus covers every row of the v4plus change table (parser_v4plus.go) with a corpus
// title, and each v5 regression class it must not show. want is the parse as "S<season>E<episodes> | languages |
// group | quality"; v4 is what plain v4 gives, to show the change.
func TestV4Plus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, title, want, v4 string
	}{
		// Taken from v5.
		{"anime multi-episode range (91c4ae17c)",
			"[PMR] Re ZERO Starting Life in Another World S03E01-12 (BD Remux 1080p AVC FLAC E-AC-3) [Dual Audio]",
			"S3E1,2,3,4,5,6,7,8,9,10,11,12 | Unknown | PMR | Bluray-1080p Remux",
			"S3E1 | Unknown | PMR | Bluray-1080p Remux"},
		{"anime range with a space (91c4ae17c)",
			"[PMR] Re ZERO Starting Life in Another World S03 E01-05 (BD Remux 1080p AVC FLAC E-AC-3) [Dual Audio]",
			"S3E1,2,3,4,5 | Unknown | PMR | Bluray-1080p Remux", ""},
		{"Ger Eng DL (9e5ebdc62)",
			"The Expanse 2015 S06E06 Ger Eng DL EAC3 2160p DV HDR AMZN WEB H265-ZeroTwo mkv",
			"S6E6 | German,English | ZeroTwo | WEBDL-2160p", "S6E6 | English | ZeroTwo | WEBDL-2160p"},
		{"ENG-GER (9e5ebdc62)", "Dark.S02E02.1080p.NF.WEB-DL.ENG-GER.DDP5.1.x264-NTb",
			"S2E2 | English,German | NTb | WEBDL-1080p", ""},
		{"case-sensitive DE (9e5ebdc62) and EN (ours)", "The.Mandalorian.S01E08.2160p.WEB-DL.DE-EN-TR.DDP5.1.Atmos.HDR10.H.265-TURG",
			"S1E8 | German,English | TURG | WEBDL-2160p", "S1E8 | Unknown | TURG | WEBDL-2160p"},
		{"Sonarr's own [JA+EN] (ours)",
			"Mushoku Tensei - Jobless Reincarnation (2021) - S02E24 - Succession [Bluray-1080p Remux][8bit][AVC][FLAC 2.0][JA+EN]-RUDY",
			"S2E24 | Japanese,English | RUDY | Bluray-1080p Remux", "S2E24 | Unknown | RUDY | Bluray-1080p Remux"},
		{"mojibake ger is not German (ours)",
			`[1/6] - "Only Murders in the Building - S01E06 - ProtÃÂ©ger et servir 2160p.DV.HDR.x265-Amen.mkv"`,
			"S1E6 | Unknown | 1/6 | HDTV-2160p", ""},
		{"language list (3e2e8e938, 9e5ebdc62, dacd46962)",
			"The.Last.of.Us.S01E07.Eng.Fre.Ger.Ita.Spa.Cze.Jpn.2160p.BluRay.Remux.DV.HDR.HEVC.Atmos-SGF",
			"S1E7 | English,French,German,Italian,Spanish,Japanese | SGF | Bluray-2160p Remux", ""},
		{"JPN (dacd46962)", "ReZero.kara.Hajimeru.Isekai.Seikatsu.S04E12.1080p.AMZN.WEB-DL.JPN.DDP2.0.H.264-ToonsHub",
			"S4E12 | Japanese | ToonsHub | WEBDL-1080p", "S4E12 | Unknown | ToonsHub | WEBDL-1080p"},
		{"Eng-Subs is a subtitle (224e74605)", "[Judas] Dororo (Season 1 S1)   [BD 1080p][HEVC x265 10bit][Eng-Subs]",
			"S1 | Unknown | Judas | Bluray-1080p", "S1 | English | Judas | Bluray-1080p"},
		{"WEB EAC3 is WEB-DL (6421bb621)", "Silo.S03E02.MULTi.VFF.2160p.HDR.WEB.EAC3.Atmos.5.1.H265-MRD",
			"S3E2 | French | MRD | WEBDL-2160p", "S3E2 | French | MRD | HDTV-2160p"},
		{"Vialle (5eb18fe27)", "1923 - S02E02 - The Rapist is Winter[WEBDL-1080p.AC3.x264.Vialle]",
			"S2E2 | Unknown | Vialle | WEBDL-1080p", ""},
		{"TAoE (b00229e53)",
			"Dune Prophecy (2024) S01E02 Two Wolves (2160p HDR10 DV Hybrid MAX Webrip x265 10bit EAC3 5.1 Atmos - Goki)[TAoE]",
			"S1E2 | Unknown | TAoE | WEBRip-2160p", ""},
		{"QxR, not the encoder (b00229e53)",
			"Planet Earth II (2016) Season 1 S01 (2160p BluRay x265 HEVC 10bit HDR AAC 5.1 Silence) [QxR]",
			"S1 | Unknown | QxR | Bluray-2160p", "S1 | Unknown | Silence | Bluray-2160p"},
		{"10-bit is not a group (350dea10d)",
			"Is It Wrong to Try to Pick Up Girls in a Dungeon - S03E10 (BDRip 1920x1080 x265 10-bit AAC) [Dual Audio]",
			"S3E10 | Unknown |  | Bluray-1080p", "S3E10 | Unknown | bit | Bluray-1080p"},

		// The v5 regression classes, kept as v4 (no v4plus change).
		{"E12.2020 is not S20E20 (7b0db46c2 left out)", "Cosmos.Possible.Worlds.E12.2020.DSNP.1080p.WEB-DL.DDP5.1.H.264-FLUX",
			"S1E12 | Unknown | FLUX | WEBDL-1080p", "S1E12 | Unknown | FLUX | WEBDL-1080p"},
		{"E13 with a year (7b0db46c2 left out)",
			"Gushing.over.Magical.Girls.E13.Ich.bewundere.zweifelsohne.Magical.Girls.German.2024.ANiME.DL.1080p.BluRay.x264-STARS",
			"S1E13 | German,Original | STARS | Bluray-1080p", "S1E13 | German,Original | STARS | Bluray-1080p"},
		{"E9 ... S01 (7b0db46c2 left out)",
			"[ToonsHub] Solo Leveling E9 You've Been Hiding Your Skills 1080p B-Global WEB-DL x264 (Multi-Subs) S01",
			"S1E9 | Unknown | ToonsHub | WEBDL-1080p", "S1E9 | Unknown | ToonsHub | WEBDL-1080p"},
		{"romaji ka is not Georgian (309b55fe3 left out)",
			"[SubsPlus+] Is It Wrong to Try to Pick Up Girls in a Dungeon  - S05E02 (CR WEB 1080p AVC EAC3) | Dungeon ni Deai wo Motomeru no wa Machigatteiru Darou ka | DanMachi",
			"S5E2 | Unknown | SubsPlus+ | WEBDL-1080p", ""},
		{"ITA ENG SUBS keeps both audio languages (ours)", "Halo.S02E03 04.WebDL.2160p.Hevc.HDR.E AC3 AC3.ITA.ENG.SUBS.K-Z",
			"S2E3 | Italian,English | Z | WEBDL-2160p", "S2E3 | Italian,English | Z | WEBDL-2160p"},
		{"Ita Eng Spa SubS keeps the list (ours)", "Your Friends and Neighbors S02e08 [720p Ita Eng Spa SubS] byMe7alh [MIRCrew]",
			"S2E8 | Italian,English,Spanish | MIRCrew | HDTV-720p", ""},
		{"Japan is not Japanese (ours: \\b)",
			"Anthony.Bourdain.No.Reservations.2005.S08E06.Japan.Cook.It.Raw.1080p.HMAX.WEB-DL.DDP2.0.H.264-FUZEER",
			"S8E6 | Unknown | FUZEER | WEBDL-1080p", "S8E6 | Unknown | FUZEER | WEBDL-1080p"},
		{"Uzbekistan is not Uzbek (99fc61e63 left out)",
			"Anthony.Bourdain.No.Reservations.2005.S01E08.Uzbekistan.960p.HMAX.WEB-DL.DDP2.0.H.264-FUZEER",
			"S1E8 | Unknown | FUZEER | WEBDL-720p", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := summarizeV4Plus(ParseTitle(c.title)); got != c.want {
				t.Errorf("v4plus %q\n got %s\nwant %s", c.title, got, c.want)
			}
			if c.v4 != "" {
				if got := summarizeV4Plus(v4d.parseTitle(c.title)); got != c.v4 {
					t.Errorf("v4 %q\n got %s\nwant %s", c.title, got, c.v4)
				}
			}
		})
	}
}

func summarizeV4Plus(p *ParsedEpisodeInfo) string {
	if p == nil {
		return "nil"
	}
	var eps, langs []string
	for _, e := range p.EpisodeNumbers {
		eps = append(eps, fmt.Sprint(e))
	}
	for _, l := range p.Languages {
		langs = append(langs, l.Name)
	}
	se := fmt.Sprintf("S%d", p.SeasonNumber)
	if len(eps) > 0 {
		se += "E" + strings.Join(eps, ",")
	}
	group := ""
	if p.ReleaseGroup != nil {
		group = *p.ReleaseGroup
	}
	return fmt.Sprintf("%s | %s | %s | %s", se, strings.Join(langs, ","), group, p.Quality.Quality.Name)
}

// TestV4PlusSubtitleLanguages covers the SUBS rule on its own: a single language before SUBS is a subtitle, a list
// keeps v4's reading.
func TestV4PlusSubtitleLanguages(t *testing.T) {
	t.Parallel()
	for title, want := range map[string]string{
		"Series.S01E01.1080p.WEB-DL.ENG.SUBS-GRP":      "Unknown",
		"Series.S01E01.1080p.WEB-DL.ITA.ENG.SUBS-GRP":  "Italian,English",
		"Series.S01E01.1080p.WEB-DL.ITA.Eng-Sub-GRP":   "Italian,English",
		"Series.S01E01.1080p.WEB-DL.JPN.x264.ENG.SUBS": "Japanese",
		"Series.S01E01.1080p.WEB-DL.ITA.ENG-GRP":       "Italian,English",
		"Series.S01E01.1080p.WEB-DL.POR.SUBS-GRP":      "Unknown",
		"[Erai-raws] Akuyaku Reijou Level 99 - 01 [1080p][Multiple Subtitle] [ENG][POR-BR][SPA-LA][SPA][ARA][FRE][GER][ITA][RUS]": "Unknown",
		"[Erai-raws].Akuyaku.Reijou.Level.99.-.06.[1080p][Multiple.Subtitle].[ENG][POR-BR][SPA-LA][SPA][ARA][FRE][GER][ITA][RUS]": "Unknown",
	} {
		var got []string
		for _, l := range ParseLanguages(title) {
			got = append(got, l.Name)
		}
		if g := strings.Join(got, ","); g != want {
			t.Errorf("%s: %s, want %s", title, g, want)
		}
	}
}

// TestExportedParserIsV4Plus checks that the package's plain functions are V4Plus, not the v4 base: the three titles
// where the two differ most in practice.
func TestExportedParserIsV4Plus(t *testing.T) {
	t.Parallel()
	p := ParseTitle("[PMR] Re ZERO Starting Life in Another World S03E01-12 (BD Remux 1080p AVC FLAC E-AC-3) [Dual Audio]")
	if p == nil || len(p.EpisodeNumbers) != 12 {
		t.Errorf("S03E01-12: %v, want 12 episodes", p)
	}
	if q := ParseQuality("Silo.S03E02.MULTi.VFF.2160p.HDR.WEB.EAC3.Atmos.5.1.H265-MRD").Quality; q != QualityWEBDL2160p {
		t.Errorf("WEB EAC3: %v, want WEBDL-2160p", q)
	}
	var langs []string
	for _, l := range ParseLanguages("The Expanse 2015 S06E06 Ger Eng DL EAC3 2160p DV HDR AMZN WEB H265-ZeroTwo mkv") {
		langs = append(langs, l.Name)
	}
	if g := strings.Join(langs, ","); g != "German,English" {
		t.Errorf("Ger Eng DL: %s, want German,English", g)
	}
}
