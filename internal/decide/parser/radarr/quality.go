// Port of Radarr src/NzbDrone.Core/Qualities/{Quality,QualitySource,Modifier,Revision,QualityModel,
// QualityDetectionSource,QualityFinder}.cs and MediaFiles/MediaFileExtensions.cs @ a96bf7c.

package radarr

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// QualitySource is Radarr's QualitySource enum. It marshals as the arr API does: the camelCased
// member name ("bluray", "webdl").
type QualitySource int

const (
	SourceUnknown QualitySource = iota
	SourceCAM
	SourceTELESYNC
	SourceTELECINE
	SourceWORKPRINT
	SourceDVD
	SourceTV
	SourceWEBDL
	SourceWEBRIP
	SourceBLURAY
)

var qualitySourceNames = []string{"unknown", "cam", "telesync", "telecine", "workprint", "dvd", "tv", "webdl", "webrip", "bluray"}

func (s QualitySource) String() string { return enumName(qualitySourceNames, int(s)) }

func (s QualitySource) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

func (s *QualitySource) UnmarshalJSON(b []byte) error {
	v, err := enumParse(qualitySourceNames, b)
	*s = QualitySource(v)
	return err
}

// Modifier is Radarr's Modifier enum.
type Modifier int

const (
	ModifierNONE Modifier = iota
	ModifierREGIONAL
	ModifierSCREENER
	ModifierRAWHD
	ModifierBRDISK
	ModifierREMUX
)

var modifierNames = []string{"none", "regional", "screener", "rawhd", "brdisk", "remux"}

func (m Modifier) String() string { return enumName(modifierNames, int(m)) }

func (m Modifier) MarshalJSON() ([]byte, error) { return json.Marshal(m.String()) }

func (m *Modifier) UnmarshalJSON(b []byte) error {
	v, err := enumParse(modifierNames, b)
	*m = Modifier(v)
	return err
}

// QualityDetectionSource is Radarr's QualityDetectionSource enum. Radarr never serialises it
// ([JsonIgnore] on QualityModel); the names here are what System.Text.Json's camelCase policy
// would produce.
type QualityDetectionSource int

const (
	DetectionUnknown QualityDetectionSource = iota
	DetectionName
	DetectionExtension
	DetectionMediaInfo
)

var detectionNames = []string{"unknown", "name", "extension", "mediaInfo"}

func (d QualityDetectionSource) String() string { return enumName(detectionNames, int(d)) }

func (d QualityDetectionSource) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func enumName(names []string, v int) string {
	if v >= 0 && v < len(names) {
		return names[v]
	}
	return fmt.Sprint(v)
}

func enumParse(names []string, b []byte) (int, error) {
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		return n, nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return 0, err
	}
	for i, name := range names {
		if strings.EqualFold(name, s) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("unknown enum value %q", s)
}

// Quality is one of Radarr's fixed qualities. Equality is by Id, as in Radarr.
type Quality struct {
	ID         int           `json:"id"`
	Name       string        `json:"name"`
	Source     QualitySource `json:"source"`
	Resolution int           `json:"resolution"`
	Modifier   Modifier      `json:"modifier"`
}

func (q Quality) String() string { return q.Name }

// The qualities, with Radarr's ids, names, sources, resolutions and modifiers.
var (
	QualityUnknown     = Quality{0, "Unknown", SourceUnknown, 0, ModifierNONE}
	QualityWORKPRINT   = Quality{24, "WORKPRINT", SourceWORKPRINT, 0, ModifierNONE}
	QualityCAM         = Quality{25, "CAM", SourceCAM, 0, ModifierNONE}
	QualityTELESYNC    = Quality{26, "TELESYNC", SourceTELESYNC, 0, ModifierNONE}
	QualityTELECINE    = Quality{27, "TELECINE", SourceTELECINE, 0, ModifierNONE}
	QualityDVDSCR      = Quality{28, "DVDSCR", SourceDVD, 480, ModifierSCREENER}
	QualityREGIONAL    = Quality{29, "REGIONAL", SourceDVD, 480, ModifierREGIONAL}
	QualitySDTV        = Quality{1, "SDTV", SourceTV, 480, ModifierNONE}
	QualityDVD         = Quality{2, "DVD", SourceDVD, 0, ModifierNONE}
	QualityDVDR        = Quality{23, "DVD-R", SourceDVD, 480, ModifierREMUX}
	QualityHDTV720p    = Quality{4, "HDTV-720p", SourceTV, 720, ModifierNONE}
	QualityHDTV1080p   = Quality{9, "HDTV-1080p", SourceTV, 1080, ModifierNONE}
	QualityHDTV2160p   = Quality{16, "HDTV-2160p", SourceTV, 2160, ModifierNONE}
	QualityWEBDL480p   = Quality{8, "WEBDL-480p", SourceWEBDL, 480, ModifierNONE}
	QualityWEBDL720p   = Quality{5, "WEBDL-720p", SourceWEBDL, 720, ModifierNONE}
	QualityWEBDL1080p  = Quality{3, "WEBDL-1080p", SourceWEBDL, 1080, ModifierNONE}
	QualityWEBDL2160p  = Quality{18, "WEBDL-2160p", SourceWEBDL, 2160, ModifierNONE}
	QualityBluray480p  = Quality{20, "Bluray-480p", SourceBLURAY, 480, ModifierNONE}
	QualityBluray576p  = Quality{21, "Bluray-576p", SourceBLURAY, 576, ModifierNONE}
	QualityBluray720p  = Quality{6, "Bluray-720p", SourceBLURAY, 720, ModifierNONE}
	QualityBluray1080p = Quality{7, "Bluray-1080p", SourceBLURAY, 1080, ModifierNONE}
	QualityBluray2160p = Quality{19, "Bluray-2160p", SourceBLURAY, 2160, ModifierNONE}
	QualityRemux1080p  = Quality{30, "Remux-1080p", SourceBLURAY, 1080, ModifierREMUX}
	QualityRemux2160p  = Quality{31, "Remux-2160p", SourceBLURAY, 2160, ModifierREMUX}
	QualityBRDISK      = Quality{22, "BR-DISK", SourceBLURAY, 1080, ModifierBRDISK}
	QualityRAWHD       = Quality{10, "Raw-HD", SourceTV, 1080, ModifierRAWHD}
	QualityWEBRip480p  = Quality{12, "WEBRip-480p", SourceWEBRIP, 480, ModifierNONE}
	QualityWEBRip720p  = Quality{14, "WEBRip-720p", SourceWEBRIP, 720, ModifierNONE}
	QualityWEBRip1080p = Quality{15, "WEBRip-1080p", SourceWEBRIP, 1080, ModifierNONE}
	QualityWEBRip2160p = Quality{17, "WEBRip-2160p", SourceWEBRIP, 2160, ModifierNONE}
)

// AllQualities is Quality.All, in Radarr's order.
var AllQualities = []Quality{
	QualityUnknown, QualityWORKPRINT, QualityCAM, QualityTELESYNC, QualityTELECINE, QualityDVDSCR,
	QualityREGIONAL, QualitySDTV, QualityDVD, QualityDVDR, QualityHDTV720p, QualityHDTV1080p,
	QualityHDTV2160p, QualityWEBDL480p, QualityWEBDL720p, QualityWEBDL1080p, QualityWEBDL2160p,
	QualityWEBRip480p, QualityWEBRip720p, QualityWEBRip1080p, QualityWEBRip2160p, QualityBluray480p,
	QualityBluray576p, QualityBluray720p, QualityBluray1080p, QualityBluray2160p, QualityRemux1080p,
	QualityRemux2160p, QualityBRDISK, QualityRAWHD,
}

// QualityByID is Quality.FindById.
func QualityByID(id int) (Quality, bool) {
	for _, q := range AllQualities {
		if q.ID == id {
			return q, true
		}
	}
	return Quality{}, false
}

// Revision is Radarr's Revision.
type Revision struct {
	Version  int  `json:"version"`
	Real     int  `json:"real"`
	IsRepack bool `json:"isRepack"`
}

// NewRevision is Revision(): version 1.
func NewRevision() Revision { return Revision{Version: 1} }

// QualityModel is Radarr's QualityModel. The detection sources are [JsonIgnore] in Radarr, so they
// are left out of the JSON here too.
type QualityModel struct {
	Quality                   Quality                `json:"quality"`
	Revision                  Revision               `json:"revision"`
	SourceDetectionSource     QualityDetectionSource `json:"-"`
	ResolutionDetectionSource QualityDetectionSource `json:"-"`
	ModifierDetectionSource   QualityDetectionSource `json:"-"`
	RevisionDetectionSource   QualityDetectionSource `json:"-"`
}

func newQualityModel() QualityModel {
	return QualityModel{Quality: QualityUnknown, Revision: NewRevision()}
}

func (m QualityModel) String() string {
	s := fmt.Sprintf("%s v%d", m.Quality.Name, m.Revision.Version)
	if m.Revision.Real > 0 {
		s += fmt.Sprintf(" Real:%d", m.Revision.Real)
	}
	return s
}

// FindBySourceAndResolution is QualityFinder.FindBySourceAndResolution.
func FindBySourceAndResolution(source QualitySource, resolution int, modifier Modifier) Quality {
	var exact []Quality
	for _, q := range AllQualities {
		if q.Source == source && q.Resolution == resolution && q.Modifier == modifier {
			exact = append(exact, q)
		}
	}
	// SingleOrDefault: the table has no duplicates, so at most one.
	if len(exact) == 1 {
		return exact[0]
	}

	var unknownRes []Quality
	for _, q := range AllQualities {
		if q.Source == source && q.Resolution == 0 && q.Modifier == modifier && q.ID != QualityUnknown.ID {
			unknownRes = append(unknownRes, q)
		}
	}
	if len(unknownRes) == 1 {
		return unknownRes[0]
	}
	for _, q := range unknownRes {
		if q.Source >= source {
			return q
		}
	}

	var sameRes []Quality
	for _, q := range AllQualities {
		if q.Modifier == modifier && q.Resolution == resolution {
			sameRes = append(sameRes, q)
		}
	}
	sort.SliceStable(sameRes, func(i, j int) bool { return sameRes[i].Source < sameRes[j].Source })
	for _, q := range sameRes {
		if q.Source >= source {
			return q
		}
	}
	return QualityUnknown
}

// mediaFileExtensions is MediaFileExtensions.FileExtensions (keys compared case-insensitively).
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

	".mkv": QualityWEBDL720p, ".mk3d": QualityWEBDL720p, ".ts": QualitySDTV, ".wtv": QualitySDTV,

	".m2ts": QualityBluray720p,
}

// qualityForExtension is MediaFileExtensions.GetQualityForExtension.
func qualityForExtension(ext string) Quality {
	if q, ok := mediaFileExtensions[strings.ToLower(ext)]; ok {
		return q
	}
	return QualityUnknown
}

func isMediaExtension(ext string) bool {
	_, ok := mediaFileExtensions[strings.ToLower(ext)]
	return ok
}
