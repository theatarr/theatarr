package sonarr

// Port of Sonarr src/NzbDrone.Core/Qualities/{Quality,QualitySource,QualityModel,Revision,QualityDetectionSource,
// QualityFinder}.cs and src/NzbDrone.Core/MediaFiles/MediaFileExtensions.cs @ cab419a.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// QualitySource is Sonarr's QualitySource enum. It marshals as Sonarr's API does: the camelCase member name.
type QualitySource int

const (
	SourceUnknown QualitySource = iota
	SourceTelevision
	SourceTelevisionRaw
	SourceWeb
	SourceWebRip
	SourceDVD
	SourceBluray
	SourceBlurayRaw
)

var qualitySourceNames = [...]string{"unknown", "television", "televisionRaw", "web", "webRip", "dvd", "bluray", "blurayRaw"}

func (s QualitySource) String() string {
	if int(s) < len(qualitySourceNames) {
		return qualitySourceNames[s]
	}
	return fmt.Sprint(int(s))
}

func (s QualitySource) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

func (s *QualitySource) UnmarshalJSON(b []byte) error {
	return unmarshalEnum(b, qualitySourceNames[:], (*int)(s))
}

// Quality is one of Sonarr's fixed qualities.
type Quality struct {
	ID         int           `json:"id"`
	Name       string        `json:"name"`
	Source     QualitySource `json:"source"`
	Resolution int           `json:"resolution"`
}

func (q Quality) String() string { return q.Name }

// The qualities, with Sonarr's ids.
var (
	QualityUnknown          = Quality{0, "Unknown", SourceUnknown, 0}
	QualitySDTV             = Quality{1, "SDTV", SourceTelevision, 480}
	QualityDVD              = Quality{2, "DVD", SourceDVD, 480}
	QualityWEBDL1080p       = Quality{3, "WEBDL-1080p", SourceWeb, 1080}
	QualityHDTV720p         = Quality{4, "HDTV-720p", SourceTelevision, 720}
	QualityWEBDL720p        = Quality{5, "WEBDL-720p", SourceWeb, 720}
	QualityBluray720p       = Quality{6, "Bluray-720p", SourceBluray, 720}
	QualityBluray1080p      = Quality{7, "Bluray-1080p", SourceBluray, 1080}
	QualityWEBDL480p        = Quality{8, "WEBDL-480p", SourceWeb, 480}
	QualityHDTV1080p        = Quality{9, "HDTV-1080p", SourceTelevision, 1080}
	QualityRAWHD            = Quality{10, "Raw-HD", SourceTelevisionRaw, 1080}
	QualityWEBRip480p       = Quality{12, "WEBRip-480p", SourceWebRip, 480}
	QualityBluray480p       = Quality{13, "Bluray-480p", SourceBluray, 480}
	QualityWEBRip720p       = Quality{14, "WEBRip-720p", SourceWebRip, 720}
	QualityWEBRip1080p      = Quality{15, "WEBRip-1080p", SourceWebRip, 1080}
	QualityHDTV2160p        = Quality{16, "HDTV-2160p", SourceTelevision, 2160}
	QualityWEBRip2160p      = Quality{17, "WEBRip-2160p", SourceWebRip, 2160}
	QualityWEBDL2160p       = Quality{18, "WEBDL-2160p", SourceWeb, 2160}
	QualityBluray2160p      = Quality{19, "Bluray-2160p", SourceBluray, 2160}
	QualityBluray1080pRemux = Quality{20, "Bluray-1080p Remux", SourceBlurayRaw, 1080}
	QualityBluray2160pRemux = Quality{21, "Bluray-2160p Remux", SourceBlurayRaw, 2160}
	QualityBluray576p       = Quality{22, "Bluray-576p", SourceBluray, 576}
)

// AllQualities is Quality.All, in Sonarr's order.
var AllQualities = []Quality{
	QualityUnknown, QualitySDTV, QualityDVD, QualityWEBRip480p, QualityWEBDL480p, QualityBluray480p,
	QualityBluray576p, QualityHDTV720p, QualityWEBRip720p, QualityWEBDL720p, QualityBluray720p,
	QualityBluray1080p, QualityHDTV1080p, QualityWEBRip1080p, QualityWEBDL1080p, QualityRAWHD,
	QualityHDTV2160p, QualityWEBRip2160p, QualityWEBDL2160p, QualityBluray2160p, QualityBluray1080pRemux,
	QualityBluray2160pRemux,
}

// QualityWeights are the Weight values of Quality.DefaultQualityDefinitions, used by QualityModel comparison.
var QualityWeights = map[int]int{
	0: 1, 1: 2, 12: 3, 8: 3, 2: 4, 13: 5, 22: 6, 4: 7, 9: 8, 10: 9, 14: 10, 5: 10, 6: 11, 15: 12, 3: 12,
	7: 13, 20: 14, 16: 15, 17: 16, 18: 16, 19: 17, 21: 18,
}

// FindBySourceAndResolution is QualityFinder.FindBySourceAndResolution.
func FindBySourceAndResolution(source QualitySource, resolution int) Quality {
	var found []Quality
	for _, q := range AllQualities {
		if q.Source == source && q.Resolution == resolution {
			found = append(found, q)
		}
	}
	if len(found) == 1 {
		return found[0]
	}
	// SingleOrDefault throws on more than one; Quality.All has no such pair.
	if resolution < 720 {
		switch source {
		case SourceTelevision:
			return QualitySDTV
		case SourceWeb:
			return QualityWEBDL480p
		case SourceWebRip:
			return QualityWEBRip480p
		}
	}
	var sameRes []Quality
	for _, q := range AllQualities {
		if q.Resolution == resolution {
			sameRes = append(sameRes, q)
		}
	}
	// OrderBy(q => q.Source) is a stable sort.
	for i := 1; i < len(sameRes); i++ {
		for j := i; j > 0 && sameRes[j].Source < sameRes[j-1].Source; j-- {
			sameRes[j], sameRes[j-1] = sameRes[j-1], sameRes[j]
		}
	}
	for _, q := range sameRes {
		if q.Source >= source {
			return q
		}
	}
	return QualityUnknown
}

// Revision is Sonarr's Revision.
type Revision struct {
	Version  int  `json:"version"`
	Real     int  `json:"real"`
	IsRepack bool `json:"isRepack"`
}

// QualityDetectionSource is Sonarr's QualityDetectionSource enum.
type QualityDetectionSource int

const (
	DetectionUnknown QualityDetectionSource = iota
	DetectionName
	DetectionExtension
	DetectionMediaInfo
)

var detectionSourceNames = [...]string{"unknown", "name", "extension", "mediaInfo"}

func (s QualityDetectionSource) String() string {
	if int(s) < len(detectionSourceNames) {
		return detectionSourceNames[s]
	}
	return fmt.Sprint(int(s))
}

func (s QualityDetectionSource) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

func (s *QualityDetectionSource) UnmarshalJSON(b []byte) error {
	return unmarshalEnum(b, detectionSourceNames[:], (*int)(s))
}

// QualityModel is Sonarr's QualityModel. The detection sources are [JsonIgnore] in Sonarr and so are here.
type QualityModel struct {
	Quality  Quality  `json:"quality"`
	Revision Revision `json:"revision"`

	SourceDetectionSource     QualityDetectionSource `json:"-"`
	ResolutionDetectionSource QualityDetectionSource `json:"-"`
	RevisionDetectionSource   QualityDetectionSource `json:"-"`
}

// NewQualityModel is new QualityModel(): Unknown, revision v1.
func NewQualityModel() QualityModel {
	return QualityModel{Quality: QualityUnknown, Revision: Revision{Version: 1}}
}

func (m QualityModel) String() string {
	s := fmt.Sprintf("%s v%d", m.Quality.Name, m.Revision.Version)
	if m.Revision.Real > 0 {
		s += fmt.Sprintf(" Real:%d", m.Revision.Real)
	}
	return s
}

// mediaFileExtensions is MediaFileExtensions.FileExtensions (case-insensitive keys).
var mediaFileExtensions = map[string]Quality{
	".webm": QualityUnknown,

	".m4v": QualitySDTV, ".3gp": QualitySDTV, ".nsv": QualitySDTV, ".ty": QualitySDTV, ".strm": QualitySDTV,
	".rm": QualitySDTV, ".rmvb": QualitySDTV, ".m3u": QualitySDTV, ".ifo": QualitySDTV, ".mov": QualitySDTV,
	".qt": QualitySDTV, ".divx": QualitySDTV, ".xvid": QualitySDTV, ".bivx": QualitySDTV, ".nrg": QualitySDTV,
	".pva": QualitySDTV, ".wmv": QualitySDTV, ".asf": QualitySDTV, ".asx": QualitySDTV, ".ogm": QualitySDTV,
	".ogv": QualitySDTV, ".m2v": QualitySDTV, ".avi": QualitySDTV, ".bin": QualitySDTV, ".dat": QualitySDTV,
	".dvr-ms": QualitySDTV, ".mpg": QualitySDTV, ".mpeg": QualitySDTV, ".mp4": QualitySDTV, ".avc": QualitySDTV,
	".vp3": QualitySDTV, ".svq3": QualitySDTV, ".nuv": QualitySDTV, ".viv": QualitySDTV, ".dv": QualitySDTV,
	".fli": QualitySDTV, ".flv": QualitySDTV, ".wpl": QualitySDTV,

	".img": QualityDVD, ".iso": QualityDVD, ".vob": QualityDVD,

	".mkv": QualityHDTV720p, ".ts": QualityHDTV720p, ".wtv": QualityHDTV720p,

	".m2ts": QualityBluray720p,
}

// qualityForExtension is MediaFileExtensions.GetQualityForExtension.
func qualityForExtension(ext string) Quality {
	if q, ok := mediaFileExtensions[strings.ToLower(ext)]; ok {
		return q
	}
	return QualityUnknown
}

// isMediaFileExtension is MediaFileExtensions.Extensions.Contains.
func isMediaFileExtension(ext string) bool {
	_, ok := mediaFileExtensions[strings.ToLower(ext)]
	return ok
}

func unmarshalEnum(b []byte, names []string, out *int) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return json.Unmarshal(b, out)
	}
	for i, n := range names {
		if strings.EqualFold(n, s) {
			*out = i
			return nil
		}
	}
	return fmt.Errorf("unknown enum value %q", s)
}
