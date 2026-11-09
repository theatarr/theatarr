package library

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/theatarr/theatarr/internal/library/naming"
)

// Shapes of the current naming formats: a file the arr imported without renaming (or under an older format) has
// another shape, and its name is not the naming's output.
var (
	movieShaped  = regexp.MustCompile(`\{imdb-tt\d+\}|\) - \[`)
	seriesShaped = regexp.MustCompile(` - S\d{2,}E\d{2,}.* \[`)
)

// mediaInfoFromAPI turns the arr's MediaInfoResource (already formatted) into naming.MediaInfo. The API formats the
// video codec with the file's scene name only, where naming used GetSceneOrFileName (at import, the original file
// name), so a codec that fell to the default token is matched again against that name.
func mediaInfoFromAPI(raw, scene, orig, rel string) *naming.MediaInfo {
	if raw == "" || raw == "{}" {
		return nil
	}
	var a struct {
		AudioChannels         float64 `json:"audioChannels"`
		AudioCodec            string  `json:"audioCodec"`
		AudioLanguages        string  `json:"audioLanguages"`
		Subtitles             string  `json:"subtitles"`
		VideoBitDepth         int     `json:"videoBitDepth"`
		VideoCodec            string  `json:"videoCodec"`
		VideoDynamicRange     string  `json:"videoDynamicRange"`
		VideoDynamicRangeType string  `json:"videoDynamicRangeType"`
	}
	if json.Unmarshal([]byte(raw), &a) != nil {
		return nil
	}
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(s, "/")
	}
	mi := &naming.MediaInfo{VideoCodec: a.VideoCodec, VideoBitDepth: a.VideoBitDepth, AudioCodec: a.AudioCodec,
		AudioChannels: a.AudioChannels, AudioLanguages: split(a.AudioLanguages), Subtitles: split(a.Subtitles),
		VideoDynamicRange: a.VideoDynamicRange, VideoDynamicRangeType: a.VideoDynamicRangeType}
	if strings.TrimSpace(scene) == "" {
		at := orig
		if at == "" {
			at = rel
		}
		at = strings.TrimSuffix(path.Base(at), path.Ext(at))
		m := &naming.Model{VideoFormat: map[string]string{"h264": "h264", "h265": "hevc"}[mi.VideoCodec]}
		if m.VideoFormat != "" {
			mi.VideoCodec = naming.FormatVideoCodec(m, at, false)
		}
	}
	return mi
}

// cfFromName reads the custom formats back from an arr file name: the bracket right before "[<Quality Full>]".
func cfFromName(name, qualityFull string) []string {
	i := strings.Index(name, "["+qualityFull+"]")
	if i <= 0 || name[i-1] != ']' {
		return nil
	}
	j := strings.LastIndex(name[:i-1], "[")
	if j < 0 || name[j+1:i-1] == "3D" {
		return nil
	}
	return []string{name[j+1 : i-1]}
}
