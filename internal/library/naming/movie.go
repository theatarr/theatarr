// Port of Radarr src/NzbDrone.Core/Organizer/FileNameBuilder.cs @ a96bf7c.

package naming

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/dlclark/regexp2"

	"github.com/theatarr/theatarr/internal/netregex"
)

// radarrTitleRegex is Radarr's TitleRegex. Its two "prefix" groups share one name, so the value is the last capture,
// as in .NET.
var radarrTitleRegex = netregex.MustCompile(`(?<tag>\{(?<prefix>[-{ ._\[(]*)(?:imdb(?:id)?-|edition-))?\{(?<prefix>[-{ ._\[(]*)(?<token>(?:[a-z0-9]+)(?:(?<separator>[- ._]+)(?:[a-z0-9]+))?)(?::(?<customFormat>[ ,a-z0-9|+-]+(?<![- ])))?(?<suffix>[-} ._)\]]*)\}`,
	netregex.IgnoreCase)

// Movie is what Radarr's naming reads from a movie.
type Movie struct {
	Title         string
	OriginalTitle string
	Year          int
	ImdbID        string
	TmdbID        int
	Certification string
	Collection    string
}

// Quality is a file's quality as the naming tokens see it: the definition's title and the revision.
type Quality struct {
	Title   string // the quality definition's title, e.g. "Remux-2160p"
	Version int    // revision version; >1 is a Proper (Sonarr anime: v2, v3, ...)
	Real    int    // revision real; >0 is REAL
}

// MovieFile is what Radarr's naming reads from a movie file.
type MovieFile struct {
	Quality      Quality
	ReleaseGroup string
	Edition      string
	SceneName    string
	RelativePath string
	Path         string
	MediaInfo    *MediaInfo
	// CustomFormats are the names of the file's custom formats that have includeCustomFormatWhenRenaming, in the
	// arr's order.
	CustomFormats []string
}

// SceneOrFileName is MovieFile.GetSceneOrFileName.
func (f *MovieFile) SceneOrFileName() string {
	return sceneOrFileName(f.SceneName, f.RelativePath, f.Path)
}

func sceneOrFileName(scene, rel, p string) string {
	switch {
	case strings.TrimSpace(scene) != "":
		return scene
	case strings.TrimSpace(rel) != "":
		return strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	case strings.TrimSpace(p) != "":
		return strings.TrimSuffix(path.Base(p), path.Ext(p))
	}
	return ""
}

// radarrCleanTitle is Radarr's CleanTitle (Sonarr's plus RemoveDiacritics).
func radarrCleanTitle(title string) string { return removeDiacritics(cleanTitle(title)) }

func addMovieTokens(h handlers, m *Movie) {
	h.set("{Movie Title}", func(t *tokenMatch) string { return truncate(m.Title, t.CustomFormat) })
	h.set("{Movie CleanTitle}", func(t *tokenMatch) string { return truncate(radarrCleanTitle(m.Title), t.CustomFormat) })
	h.set("{Movie TitleThe}", func(t *tokenMatch) string { return truncate(radarrTitleThe(m.Title), t.CustomFormat) })
	h.set("{Movie CleanTitleThe}", func(t *tokenMatch) string {
		return truncate(radarrCleanTitleThe(m.Title), t.CustomFormat)
	})
	h.set("{Movie TitleFirstCharacter}", func(*tokenMatch) string { return titleFirstCharacter(radarrTitleThe(m.Title)) })
	h.set("{Movie OriginalTitle}", func(t *tokenMatch) string { return truncate(m.OriginalTitle, t.CustomFormat) })
	h.set("{Movie CleanOriginalTitle}", func(t *tokenMatch) string {
		return truncate(radarrCleanTitle(m.OriginalTitle), t.CustomFormat)
	})
	h.set("{Movie Certification}", func(*tokenMatch) string { return m.Certification })
	h.set("{Movie Collection}", func(t *tokenMatch) string { return truncate(m.Collection, t.CustomFormat) })
	h.set("{Movie CollectionThe}", func(t *tokenMatch) string { return truncate(radarrTitleThe(m.Collection), t.CustomFormat) })
	h.set("{Movie CleanCollectionThe}", func(t *tokenMatch) string {
		return truncate(radarrCleanTitleThe(m.Collection), t.CustomFormat)
	})
}

func radarrTitleThe(title string) string {
	if strings.TrimSpace(title) == "" {
		return ""
	}
	return titleThe(title)
}

func radarrCleanTitleThe(title string) string {
	if strings.TrimSpace(title) == "" {
		return ""
	}
	return cleanTitleThe(title, radarrCleanTitle)
}

func addReleaseDateTokens(h handlers, year int) {
	if year == 0 {
		h.set("{Release Year}", func(*tokenMatch) string { return "" })
		return
	}
	h.set("{Release Year}", func(*tokenMatch) string { return strconv.Itoa(year) })
}

func addMovieIDTokens(h handlers, m *Movie) {
	h.set("{ImdbId}", func(*tokenMatch) string { return m.ImdbID })
	h.set("{TmdbId}", func(*tokenMatch) string { return strconv.Itoa(m.TmdbID) })
}

func addQualityTokens(h handlers, q Quality, proper string) {
	real := ""
	if q.Real > 0 {
		real = "REAL"
	}
	full := fmt.Sprintf("%s %s %s", q.Title, proper, real)
	h.set("{Quality Full}", func(*tokenMatch) string { return full })
	h.set("{Quality Title}", func(*tokenMatch) string { return q.Title })
	h.set("{Quality Proper}", func(*tokenMatch) string { return proper })
	h.set("{Quality Real}", func(*tokenMatch) string { return real })
}

func addMediaInfoTokens(h handlers, mi *MediaInfo, sonarr bool, radarr3D bool) {
	if mi == nil {
		return
	}
	bitDepth := "8"
	if mi.VideoBitDepth > 0 {
		bitDepth = strconv.Itoa(mi.VideoBitDepth)
	}
	channels := ""
	if mi.AudioChannels > 0 {
		channels = strconv.FormatFloat(mi.AudioChannels, 'f', 1, 64)
	}
	h.set("{MediaInfo Video}", func(*tokenMatch) string { return mi.VideoCodec })
	h.set("{MediaInfo VideoCodec}", func(*tokenMatch) string { return mi.VideoCodec })
	h.set("{MediaInfo VideoBitDepth}", func(*tokenMatch) string { return bitDepth })
	h.set("{MediaInfo Audio}", func(*tokenMatch) string { return mi.AudioCodec })
	h.set("{MediaInfo AudioCodec}", func(*tokenMatch) string { return mi.AudioCodec })
	h.set("{MediaInfo AudioChannels}", func(*tokenMatch) string { return channels })
	h.set("{MediaInfo AudioLanguages}", func(t *tokenMatch) string {
		return languagesToken(mi.AudioLanguages, t.CustomFormat, true, true, sonarr)
	})
	h.set("{MediaInfo AudioLanguagesAll}", func(t *tokenMatch) string {
		return languagesToken(mi.AudioLanguages, t.CustomFormat, false, true, sonarr)
	})
	h.set("{MediaInfo SubtitleLanguages}", func(t *tokenMatch) string {
		return languagesToken(mi.Subtitles, t.CustomFormat, false, true, sonarr)
	})
	h.set("{MediaInfo SubtitleLanguagesAll}", func(t *tokenMatch) string {
		return languagesToken(mi.Subtitles, t.CustomFormat, false, true, sonarr)
	})
	if radarr3D {
		h.set("{MediaInfo 3D}", func(*tokenMatch) string {
			if mi.VideoMultiViewCount > 1 {
				return "3D"
			}
			return ""
		})
	}
	h.set("{MediaInfo Simple}", func(*tokenMatch) string { return mi.VideoCodec + " " + mi.AudioCodec })
	h.set("{MediaInfo Full}", func(t *tokenMatch) string {
		return mi.VideoCodec + " " + mi.AudioCodec + languagesToken(mi.AudioLanguages, t.CustomFormat, true, true, sonarr) +
			" " + languagesToken(mi.Subtitles, t.CustomFormat, false, true, sonarr)
	})
	h.set("{MediaInfo VideoDynamicRange}", func(*tokenMatch) string { return mi.VideoDynamicRange })
	h.set("{MediaInfo VideoDynamicRangeType}", func(*tokenMatch) string { return mi.VideoDynamicRangeType })
}

func addCustomFormatTokens(h handlers, names []string) {
	h.set("{Custom Formats}", func(t *tokenMatch) string { return customFormatsToken(names, t.CustomFormat) })
	h.set("{Custom Format}", func(t *tokenMatch) string {
		if strings.TrimSpace(t.CustomFormat) == "" {
			return ""
		}
		for _, n := range names {
			if n == t.CustomFormat {
				return n
			}
		}
		return ""
	})
}

// radarrReplaceTokens is Radarr's ReplaceTokens.
func radarrReplaceTokens(pattern string, h handlers, c *Config) string {
	return replaceFunc(radarrTitleRegex, pattern, func(m regexp2.Match) string {
		tm := &tokenMatch{Tag: group(&m, "tag"), Prefix: group(&m, "prefix"), Separator: group(&m, "separator"),
			Suffix: group(&m, "suffix"), Token: group(&m, "token"), CustomFormat: group(&m, "customFormat")}
		text := strings.TrimSpace(deref(h.get(tm.Token)(tm)))
		text = applyCase(tm.Token, text)
		if strings.TrimSpace(tm.Separator) != "" {
			text = strings.ReplaceAll(text, " ", tm.Separator)
		}
		text = cleanFileName(text, c)
		if strings.TrimSpace(text) != "" {
			text = tm.Tag + tm.Prefix + text + tm.Suffix
		}
		return text
	})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// splitPattern is pattern.Split('\\', '/', RemoveEmptyEntries).
func splitPattern(p string) []string {
	return strings.FieldsFunc(p, func(r rune) bool { return r == '\\' || r == '/' })
}

// MovieFileName is Radarr's BuildFileName: the file name without extension (a pattern with '/' yields a relative
// path).
func MovieFileName(c *Config, m *Movie, f *MovieFile) (name string, err error) {
	defer catch(&err)
	if !c.Rename {
		return emptyName(movieOriginalTitle(f, false, c), "")
	}
	if strings.TrimSpace(c.StandardMovieFormat) == "" {
		return "", fmt.Errorf("naming: standard movie format cannot be empty")
	}
	pattern := c.StandardMovieFormat
	h := handlers{}
	multiple := len(allMatches(radarrTitleRegex, pattern)) > 1
	addMovieTokens(h, m)
	addReleaseDateTokens(h, m.Year)
	addMovieIDTokens(h, m)
	proper := ""
	if f.Quality.Version > 1 {
		proper = "Proper"
	}
	addQualityTokens(h, f.Quality, proper)
	addMediaInfoTokens(h, f.MediaInfo, false, true)
	h.set("{Original Title}", func(*tokenMatch) string { return movieOriginalTitle(f, multiple, c) })
	h.set("{Original Filename}", func(*tokenMatch) string { return originalFileName(f.RelativePath, f.Path, !multiple) })
	h.set("{Release Group}", func(t *tokenMatch) string {
		if strings.TrimSpace(f.ReleaseGroup) == "" {
			return t.defaultValue("Radarr")
		}
		return truncate(f.ReleaseGroup, t.CustomFormat)
	})
	if strings.TrimSpace(f.Edition) != "" {
		h.set("{Edition Tags}", func(t *tokenMatch) string { return truncate(editionToken(f.Edition), t.CustomFormat) })
	}
	addCustomFormatTokens(h, f.CustomFormats)

	var comps []string
	last := ""
	for _, sp := range splitPattern(pattern) {
		comp := strings.TrimSpace(radarrReplaceTokens(sp, h, c))
		comp = collapseSeparators(comp)
		comp = trimSeparators(comp)
		comp = strings.ReplaceAll(comp, "{ellipsis}", "...")
		comp = replaceReservedDeviceNames(comp)
		last = comp
		if strings.TrimSpace(comp) != "" {
			comps = append(comps, comp)
		}
	}
	// Radarr drops an empty final component and names the file after the folder before it ({Movie Title}/{Edition
	// Tags} without an edition); an empty file name is refused instead.
	if _, err := emptyName(last, ""); err != nil {
		return "", err
	}
	return emptyName(path.Join(comps...), "")
}

// MovieFolder is Radarr's GetMovieFolder.
func MovieFolder(c *Config, m *Movie) (folder string, err error) {
	defer catch(&err)
	h := handlers{}
	addMovieTokens(h, m)
	addReleaseDateTokens(h, m.Year)
	addMovieIDTokens(h, m)
	var comps []string
	for _, sp := range splitPattern(c.MovieFolderFormat) {
		comp := radarrReplaceTokens(sp, h, c)
		comp = cleanFolderName(comp)
		comp = strings.ReplaceAll(comp, "{ellipsis}", "...")
		comp = replaceReservedDeviceNames(comp)
		if strings.TrimSpace(comp) != "" {
			comps = append(comps, comp)
		}
	}
	return path.Join(comps...), nil
}

// movieOriginalTitle is Radarr's GetOriginalTitle, which cleans with NamingConfig.Default.
func movieOriginalTitle(f *MovieFile, multiple bool, _ *Config) string {
	def := DefaultMovieConfig()
	if strings.TrimSpace(f.SceneName) == "" {
		return cleanFileName(originalFileName(f.RelativePath, f.Path, !multiple), &def)
	}
	return cleanFileName(f.SceneName, &def)
}

// originalFileName is GetOriginalFileName: the file name without extension, from the relative path or the path;
// empty unless use is set.
func originalFileName(rel, p string, use bool) string {
	if !use {
		return ""
	}
	if strings.TrimSpace(rel) == "" {
		return strings.TrimSuffix(path.Base(p), path.Ext(p))
	}
	return strings.TrimSuffix(path.Base(rel), path.Ext(rel))
}
