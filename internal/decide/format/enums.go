package format

// Enum tables ported from:
//   Radarr src/NzbDrone.Core/Qualities/{QualitySource,Resolution,Modifier}.cs, Indexers/IndexerFlags (ReleaseInfo.cs),
//   Languages/Language.cs @ a96bf7c;
//   Sonarr src/NzbDrone.Core/Qualities/{QualitySource,Resolution}.cs, Parser/Model/{ReleaseInfo,ReleaseType}.cs,
//   Languages/Language.cs @ cab419a.
// The arr enum names are kept; the arr API serialises them camelCased ("webdl", "webRip", "seasonPack"), and
// lookups here ignore case so both spellings resolve.

import (
	"fmt"
	"strconv"
	"strings"
)

// Flavour says which arr a format, profile or input belongs to. Spec values and input enums are read in it: Radarr and
// Sonarr number sources, indexer flags and languages differently.
type Flavour string

const (
	Radarr Flavour = "radarr"
	Sonarr Flavour = "sonarr"
)

// Valid reports whether f is one of the two flavours.
func (f Flavour) Valid() bool { return f == Radarr || f == Sonarr }

// enumTable maps an arr enum's names to values, in declaration order.
type enumTable struct {
	names  []string
	values []int
}

func (t enumTable) value(name string) (int, bool) {
	for i, n := range t.names {
		if strings.EqualFold(n, name) {
			return t.values[i], true
		}
	}
	return 0, false
}

func (t enumTable) name(v int) string {
	for i, x := range t.values {
		if x == v {
			return t.names[i]
		}
	}
	return strconv.Itoa(v)
}

func seq(names ...string) enumTable {
	t := enumTable{names: names}
	for i := range names {
		t.values = append(t.values, i)
	}
	return t
}

var (
	radarrSources = seq("UNKNOWN", "CAM", "TELESYNC", "TELECINE", "WORKPRINT", "DVD", "TV", "WEBDL", "WEBRIP", "BLURAY")
	sonarrSources = seq("Unknown", "Television", "TelevisionRaw", "Web", "WebRip", "DVD", "Bluray", "BlurayRaw")

	// Same values in both arrs.
	resolutions = enumTable{
		names:  []string{"Unknown", "R360p", "R480p", "R540p", "R576p", "R720p", "R1080p", "R2160p"},
		values: []int{0, 360, 480, 540, 576, 720, 1080, 2160},
	}

	radarrModifiers = seq("NONE", "REGIONAL", "SCREENER", "RAWHD", "BRDISK", "REMUX")

	radarrIndexerFlags = enumTable{
		names: []string{"G_Freeleech", "G_Halfleech", "G_DoubleUpload", "PTP_Golden", "PTP_Approved", "G_Internal",
			"AHD_Internal", "G_Scene", "G_Freeleech75", "G_Freeleech25", "AHD_UserRelease", "Nuked"},
		values: []int{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2048},
	}
	sonarrIndexerFlags = enumTable{
		names:  []string{"Freeleech", "Halfleech", "DoubleUpload", "Internal", "Scene", "Freeleech75", "Freeleech25", "Nuked"},
		values: []int{1, 2, 4, 8, 16, 32, 64, 128},
	}

	sonarrReleaseTypes = seq("Unknown", "SingleEpisode", "MultiEpisode", "SeasonPack")
)

// Language ids shared by both arrs.
const (
	LanguageOriginal = -2
	LanguageAny      = -1 // Radarr only
	LanguageUnknown  = 0
)

var radarrLanguages = langTable(`0 Unknown;1 English;2 French;3 Spanish;4 German;5 Italian;6 Danish;7 Dutch;8 Japanese;
9 Icelandic;10 Chinese;11 Russian;12 Polish;13 Vietnamese;14 Swedish;15 Norwegian;16 Finnish;17 Turkish;18 Portuguese;
19 Flemish;20 Greek;21 Korean;22 Hungarian;23 Hebrew;24 Lithuanian;25 Czech;26 Hindi;27 Romanian;28 Thai;29 Bulgarian;
30 Portuguese (Brazil);31 Arabic;32 Ukrainian;33 Persian;34 Bengali;35 Slovak;36 Latvian;37 Spanish (Latino);
38 Catalan;39 Croatian;40 Serbian;41 Bosnian;42 Estonian;43 Tamil;44 Indonesian;45 Telugu;46 Macedonian;47 Slovenian;
48 Malayalam;49 Kannada;50 Albanian;51 Afrikaans;52 Marathi;53 Tagalog;54 Urdu;55 Romansh;56 Mongolian;57 Georgian;
-1 Any;-2 Original`)

var sonarrLanguages = langTable(`0 Unknown;1 English;2 French;3 Spanish;4 German;5 Italian;6 Danish;7 Dutch;8 Japanese;
9 Icelandic;10 Chinese;11 Russian;12 Polish;13 Vietnamese;14 Swedish;15 Norwegian;16 Finnish;17 Turkish;18 Portuguese;
19 Flemish;20 Greek;21 Korean;22 Hungarian;23 Hebrew;24 Lithuanian;25 Czech;26 Arabic;27 Hindi;28 Bulgarian;
29 Malayalam;30 Ukrainian;31 Slovak;32 Thai;33 Portuguese (Brazil);34 Spanish (Latino);35 Romanian;36 Latvian;
37 Persian;38 Catalan;39 Croatian;40 Serbian;41 Bosnian;42 Estonian;43 Tamil;44 Indonesian;45 Macedonian;
46 Slovenian;-2 Original`)

func langTable(s string) enumTable {
	var t enumTable
	for _, e := range strings.Split(strings.ReplaceAll(s, "\n", ""), ";") {
		id, name, _ := strings.Cut(e, " ")
		v, err := strconv.Atoi(id)
		if err != nil {
			panic("format: bad language table entry " + e)
		}
		t.names = append(t.names, name)
		t.values = append(t.values, v)
	}
	return t
}

func (f Flavour) sources() enumTable {
	if f == Sonarr {
		return sonarrSources
	}
	return radarrSources
}

func (f Flavour) indexerFlags() enumTable {
	if f == Sonarr {
		return sonarrIndexerFlags
	}
	return radarrIndexerFlags
}

func (f Flavour) languages() enumTable {
	if f == Sonarr {
		return sonarrLanguages
	}
	return radarrLanguages
}

// SourceValue reads a quality source, as the arr API spells it ("webdl", "webRip") or as a number, in flavour f.
func (f Flavour) SourceValue(s string) (int, error) { return enumValue(f.sources(), "source", s) }

// ModifierValue reads a Radarr quality modifier ("remux", "brdisk").
func ModifierValue(s string) (int, error) { return enumValue(radarrModifiers, "modifier", s) }

// ReleaseTypeValue reads a Sonarr release type ("seasonPack").
func ReleaseTypeValue(s string) (int, error) { return enumValue(sonarrReleaseTypes, "release type", s) }

// LanguageName names a language id in flavour f.
func (f Flavour) LanguageName(id int) string { return f.languages().name(id) }

// LanguageID finds a language by name in flavour f ("Original", "Portuguese (Brazil)").
func (f Flavour) LanguageID(name string) (int, bool) { return f.languages().value(name) }

// IndexerFlagsValue reads indexer flags the way .NET Enum.TryParse(ignoreCase: true) does for the arrs'
// history data: a number, or a comma-separated list of flag names. A string it cannot read yields 0 and false, as
// TryParse leaves the out value at default.
func (f Flavour) IndexerFlagsValue(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if v, err := strconv.Atoi(s); err == nil {
		return v, true
	}
	t := f.indexerFlags()
	total := 0
	for _, part := range strings.Split(s, ",") {
		v, ok := t.value(strings.TrimSpace(part))
		if !ok {
			return 0, false
		}
		total |= v
	}
	return total, true
}

func enumValue(t enumTable, what, s string) (int, error) {
	if v, err := strconv.Atoi(s); err == nil {
		return v, nil
	}
	if v, ok := t.value(s); ok {
		return v, nil
	}
	return 0, fmt.Errorf("unknown %s %q", what, s)
}
