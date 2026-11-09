// Port of Radarr src/NzbDrone.Core/MediaFiles/MediaInfo/MediaInfoFormatter.cs @ a96bf7c (Sonarr's @ cab419a is the
// same but for the two differences noted on FormatVideoCodec and FormatVideoDynamicRangeType).

package naming

import (
	"regexp"
	"strconv"
	"strings"
)

// HdrFormat is the arrs' HdrFormat.
type HdrFormat int

const (
	HdrNone HdrFormat = iota
	HdrPq10
	Hdr10
	Hdr10Plus
	HdrHlg10
	HdrDolbyVision
	HdrDolbyVisionHdr10
	HdrDolbyVisionSdr
	HdrDolbyVisionHlg
	HdrDolbyVisionHdr10Plus
	// HdrUnknown is Sonarr's UnknownHdr (Radarr has none).
	HdrUnknown
)

// Model is the arrs' MediaInfoModel: what ffprobe reports, before formatting. Format names are ffprobe codec names
// (hevc, h264, truehd, eac3, dts), codec ids are ffprobe codec tags or the arr's markers ("thd+" for TrueHD Atmos,
// "ec+3" for E-AC-3 Atmos), and AudioProfile is ffprobe's profile for DTS ("DTS-HD MA", "DTS:X").
type Model struct {
	ContainerFormat       string
	VideoFormat           string
	VideoCodecID          string
	VideoProfile          string
	VideoBitDepth         int
	VideoMultiViewCount   int
	VideoColourPrimaries  string
	VideoTransferChars    string
	VideoHdrFormat        HdrFormat
	Width, Height         int
	AudioFormat           string
	AudioCodecID          string
	AudioProfile          string
	AudioChannels         int
	AudioChannelPositions string
	AudioStreamCount      int
	RunTimeSeconds        float64
	AudioLanguages        []string
	Subtitles             []string
}

// MediaInfo is a file's media info as the naming tokens use it: formatted the way the arrs' MediaInfoResource
// returns it (videoCodec, audioCodec, audioChannels, videoDynamicRange[Type]), plus the raw language lists.
type MediaInfo struct {
	VideoCodec            string
	VideoBitDepth         int
	VideoMultiViewCount   int
	AudioCodec            string
	AudioChannels         float64
	AudioLanguages        []string
	Subtitles             []string
	VideoDynamicRange     string
	VideoDynamicRangeType string
}

// Format formats m as the naming tokens and the arrs' API see it. sceneName is MovieFile/EpisodeFile
// GetSceneOrFileName(); sonarr selects Sonarr's formatter differences.
func (m *Model) Format(sceneName string, sonarr bool) *MediaInfo {
	if m == nil {
		return nil
	}
	return &MediaInfo{
		VideoCodec:            FormatVideoCodec(m, sceneName, sonarr),
		VideoBitDepth:         m.VideoBitDepth,
		VideoMultiViewCount:   m.VideoMultiViewCount,
		AudioCodec:            FormatAudioCodec(m),
		AudioChannels:         FormatAudioChannels(m),
		AudioLanguages:        m.AudioLanguages,
		Subtitles:             m.Subtitles,
		VideoDynamicRange:     FormatVideoDynamicRange(m),
		VideoDynamicRangeType: FormatVideoDynamicRangeType(m, sonarr),
	}
}

var positionRegex = regexp.MustCompile(`^\d\.\d`)

// FormatAudioChannels is MediaInfoFormatter.FormatAudioChannels.
func FormatAudioChannels(m *Model) float64 {
	if m.AudioChannelPositions != "" {
		if s := positionRegex.FindString(m.AudioChannelPositions); s != "" {
			if v, err := strconv.ParseFloat(s, 64); err == nil && v != 0 {
				return v
			}
		}
	}
	return float64(m.AudioChannels)
}

// FormatAudioCodec is MediaInfoFormatter.FormatAudioCodec. It returns "" where the arr returns null.
func FormatAudioCodec(m *Model) string {
	f, id, profile := m.AudioFormat, m.AudioCodecID, m.AudioProfile
	switch {
	case f == "":
		return ""
	case id == "thd+":
		return "TrueHD Atmos"
	case f == "truehd":
		return "TrueHD"
	case f == "flac":
		return "FLAC"
	case f == "dts":
		switch profile {
		case "DTS:X":
			return "DTS-X"
		case "DTS-HD MA", "DTS-ES", "DTS-HD HRA", "DTS Express", "DTS 96/24":
			return profile
		}
		return "DTS"
	case id == "ec+3":
		return "EAC3 Atmos"
	case f == "eac3":
		return "EAC3"
	case f == "ac3":
		return "AC3"
	case f == "aac":
		if id == "A_AAC/MPEG4/LC/SBR" {
			return "HE-AAC"
		}
		return "AAC"
	case f == "mp3":
		return "MP3"
	case f == "mp2":
		return "MP2"
	case f == "opus":
		return "Opus"
	case strings.HasPrefix(f, "pcm_") || strings.HasPrefix(f, "adpcm_"):
		return "PCM"
	case f == "vorbis":
		return "Vorbis"
	case f == "wmav1" || f == "wmav2" || f == "wmapro":
		return "WMA"
	}
	return f
}

// FormatVideoCodec is MediaInfoFormatter.FormatVideoCodec. Sonarr compares an XviD codec id case-sensitively.
func FormatVideoCodec(m *Model, sceneName string, sonarr bool) string {
	f, id := m.VideoFormat, m.VideoCodecID
	result := strings.TrimSpace(f)
	switch {
	case f == "":
		return result
	case id == "x264":
		return "x264"
	case f == "h264":
		return sceneNameMatch(sceneName, "AVC", "x264", "h264")
	case id == "x265":
		return "x265"
	case f == "hevc":
		return sceneNameMatch(sceneName, "HEVC", "x265", "h265")
	case f == "mpeg2video":
		return "MPEG2"
	case f == "mpeg1video":
		return "MPEG"
	case f == "mpeg4" || strings.Contains(f, "msmpeg4"):
		if id == "XVID" || (!sonarr && strings.ToUpper(id) == "XVID") {
			return "XviD"
		}
		if id == "DIV3" || id == "DX50" || strings.ToUpper(id) == "DIVX" {
			return "DivX"
		}
		return ""
	case f == "vc1":
		return "VC1"
	case f == "av1":
		return "AV1"
	case strings.Contains(f, "vp6"):
		return "VP6"
	case f == "vp7" || f == "vp8" || f == "vp9":
		return strings.ToUpper(f)
	case f == "wmv1" || f == "wmv2" || f == "wmv3":
		return "WMV"
	case f == "qtrle" || f == "rpza" || f == "rv10" || f == "rv20" || f == "rv30" || f == "rv40" || f == "cinepak" ||
		f == "rawvideo" || f == "msvideo1":
		return ""
	}
	return result
}

var videoExtensions = map[string]bool{}

// sceneNameMatch is GetSceneNameMatch: the first token the scene name (extension removed) contains, ignoring case,
// else the last token.
func sceneNameMatch(sceneName string, tokens ...string) string {
	s := strings.ToLower(RemoveFileExtension(sceneName))
	for _, t := range tokens {
		if strings.Contains(s, strings.ToLower(t)) {
			return t
		}
	}
	return tokens[len(tokens)-1]
}

// FormatVideoDynamicRange is MediaInfoFormatter.FormatVideoDynamicRange.
func FormatVideoDynamicRange(m *Model) string {
	if m.VideoHdrFormat != HdrNone {
		return "HDR"
	}
	return ""
}

// FormatVideoDynamicRangeType is MediaInfoFormatter.FormatVideoDynamicRangeType.
func FormatVideoDynamicRangeType(m *Model, sonarr bool) string {
	switch m.VideoHdrFormat {
	case HdrDolbyVision:
		return "DV"
	case HdrDolbyVisionHdr10:
		return "DV HDR10"
	case HdrDolbyVisionHdr10Plus:
		return "DV HDR10Plus"
	case HdrDolbyVisionHlg:
		return "DV HLG"
	case HdrDolbyVisionSdr:
		return "DV SDR"
	case Hdr10:
		return "HDR10"
	case Hdr10Plus:
		return "HDR10Plus"
	case HdrHlg10:
		return "HLG"
	case HdrPq10:
		return "PQ"
	case HdrUnknown:
		if sonarr {
			return "HDR"
		}
	}
	return ""
}

// HdrFormatOf is VideoFileInfoReader.GetHdrFormat: dovi is the Dolby Vision configuration's
// dv_bl_signal_compatibility_id (-1 when there is no Dolby Vision configuration), hdr10Plus whether SMPTE 2094-40
// metadata was seen, mastering whether mastering display or content light level metadata was seen.
func HdrFormatOf(bitDepth int, primaries, transfer string, dovi int, hdr10Plus, mastering bool) HdrFormat {
	if bitDepth < 10 {
		return HdrNone
	}
	if dovi >= 0 {
		switch dovi {
		case 1, 6:
			if hdr10Plus {
				return HdrDolbyVisionHdr10Plus
			}
			return HdrDolbyVisionHdr10
		case 2:
			return HdrDolbyVisionSdr
		case 4:
			return HdrDolbyVisionHlg
		}
		return HdrDolbyVision
	}
	if primaries != "bt2020" || (transfer != "arib-std-b67" && transfer != "smpte2084") {
		return HdrNone
	}
	if transfer == "arib-std-b67" {
		return HdrHlg10
	}
	if hdr10Plus {
		return Hdr10Plus
	}
	if mastering {
		return Hdr10
	}
	return HdrPq10
}

// mediaFileExtensions is the arrs' MediaFileExtensions list (video only), used by RemoveFileExtension.
var mediaFileExtensions = []string{".webm", ".m4v", ".3gp", ".nsv", ".ty", ".strm", ".rm", ".rmvb", ".m3u", ".ifo",
	".mov", ".qt", ".divx", ".xvid", ".bivx", ".nrg", ".pva", ".wmv", ".asf", ".asx", ".ogm", ".ogv", ".m2v", ".avi",
	".bin", ".dat", ".dvr-ms", ".mpg", ".mpeg", ".mp4", ".avc", ".vp3", ".svq3", ".nuv", ".viv", ".dv", ".fli", ".flv",
	".wpl", ".img", ".iso", ".vob", ".mkv", ".mk3d", ".ts", ".wtv", ".m2ts"}

func init() {
	for _, e := range mediaFileExtensions {
		videoExtensions[e] = true
	}
}

// IsVideoExtension reports whether ext (with its dot, any case) is one of the arrs' media file extensions.
func IsVideoExtension(ext string) bool { return videoExtensions[strings.ToLower(ext)] }

// RemoveFileExtension is FileExtensions.RemoveFileExtension / Parser.RemoveFileExtension: strips a known media
// (or .par2/.nzb/.torrent-like) extension only.
func RemoveFileExtension(s string) string {
	i := strings.LastIndexByte(s, '.')
	if i < 0 {
		return s
	}
	if IsVideoExtension(s[i:]) || strings.EqualFold(s[i:], ".par2") || strings.EqualFold(s[i:], ".nzb") {
		return s[:i]
	}
	return s
}
