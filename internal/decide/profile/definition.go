package profile

// Port of Radarr src/NzbDrone.Core/Qualities/{QualityDefinition,QualityDefinitionLimits}.cs and
// Quality.DefaultQualityDefinitions @ a96bf7c; Sonarr's @ cab419a. Size helpers port
// NzbDrone.Common/Extensions/NumberExtensions.cs (Megabytes, SizeSuffix) and NzbDrone.Core/Fluent.cs (Round) from
// both.

import (
	"fmt"
	"math"
	"math/big"
	"sort"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// Definition is a QualityDefinition: the weight and the size limits of one quality, in megabytes per minute of
// runtime. A nil size is unlimited (the API's null).
type Definition struct {
	ID            int // the arr's row id; 0 for the defaults
	Quality       Quality
	Title         string
	GroupName     string
	Weight        int
	MinSize       *float64
	MaxSize       *float64
	PreferredSize *float64
}

// Definitions are a flavour's quality definitions, by quality id.
type Definitions struct {
	Flavour format.Flavour
	byID    map[int]*Definition
}

// Get is QualityDefinitionService.Get. A quality with no definition gets its default one, as
// QualityDefinitionService inserts missing definitions at startup.
func (d *Definitions) Get(q Quality) *Definition {
	if d != nil {
		if def, ok := d.byID[q.ID]; ok {
			return def
		}
	}
	fl := format.Radarr
	if d != nil {
		fl = d.Flavour
	}
	def := *defaultDefinition(fl, q.ID)
	return &def
}

// Set replaces the definition of def.Quality. Weight is taken from the defaults, as QualityDefinitionService.WithWeight
// does whatever the database holds.
func (d *Definitions) Set(def Definition) {
	if d.byID == nil {
		d.byID = map[int]*Definition{}
	}
	def.Weight = defaultDefinition(d.Flavour, def.Quality.ID).Weight
	d.byID[def.Quality.ID] = &def
}

// All returns the definitions ordered by weight, then by the default order (QualityDefinitionService.All).
func (d *Definitions) All() []*Definition {
	out := make([]*Definition, 0, len(d.byID))
	for _, def := range defaultDefs(d.Flavour) {
		out = append(out, d.Get(def.Quality))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Weight < out[j].Weight })
	return out
}

// DefaultDefinitions returns a copy of Quality.DefaultQualityDefinitions for flavour fl.
func DefaultDefinitions(fl format.Flavour) *Definitions {
	d := &Definitions{Flavour: fl, byID: map[int]*Definition{}}
	for _, def := range defaultDefs(fl) {
		c := *def
		d.byID[c.Quality.ID] = &c
	}
	return d
}

func f(v float64) *float64 { return &v }

type defRow struct {
	name           string
	weight         int
	min, max, pref *float64
	group          string
}

// Radarr Quality.DefaultQualityDefinitions, in declaration order.
var radarrDefaultRows = []defRow{
	{"Unknown", 1, f(0), f(100), f(95), ""},
	{"WORKPRINT", 2, f(0), f(100), f(95), ""},
	{"CAM", 3, f(0), f(100), f(95), ""},
	{"TELESYNC", 4, f(0), f(100), f(95), ""},
	{"TELECINE", 5, f(0), f(100), f(95), ""},
	{"REGIONAL", 6, f(0), f(100), f(95), ""},
	{"DVDSCR", 7, f(0), f(100), f(95), ""},
	{"SDTV", 8, f(0), f(100), f(95), ""},
	{"DVD", 9, f(0), f(100), f(95), ""},
	{"DVD-R", 10, f(0), f(100), f(95), ""},
	{"WEBDL-480p", 11, f(0), f(100), f(95), "WEB 480p"},
	{"WEBRip-480p", 11, f(0), f(100), f(95), "WEB 480p"},
	{"Bluray-480p", 12, f(0), f(100), f(95), ""},
	{"Bluray-576p", 13, f(0), f(100), f(95), ""},
	{"HDTV-720p", 14, f(0), f(100), f(95), ""},
	{"WEBDL-720p", 15, f(0), f(100), f(95), "WEB 720p"},
	{"WEBRip-720p", 15, f(0), f(100), f(95), "WEB 720p"},
	{"Bluray-720p", 16, f(0), f(100), f(95), ""},
	{"HDTV-1080p", 17, f(0), f(100), f(95), ""},
	{"WEBDL-1080p", 18, f(0), f(100), f(95), "WEB 1080p"},
	{"WEBRip-1080p", 18, f(0), f(100), f(95), "WEB 1080p"},
	{"Bluray-1080p", 19, f(0), nil, nil, ""},
	{"Remux-1080p", 20, f(0), nil, nil, ""},
	{"HDTV-2160p", 21, f(0), nil, nil, ""},
	{"WEBDL-2160p", 22, f(0), nil, nil, "WEB 2160p"},
	{"WEBRip-2160p", 22, f(0), nil, nil, "WEB 2160p"},
	{"Bluray-2160p", 23, f(0), nil, nil, ""},
	{"Remux-2160p", 24, f(0), nil, nil, ""},
	{"BR-DISK", 25, f(0), nil, nil, ""},
	{"Raw-HD", 26, f(0), nil, nil, ""},
}

// Sonarr Quality.DefaultQualityDefinitions, in declaration order.
var sonarrDefaultRows = []defRow{
	{"Unknown", 1, f(1), f(199.9), f(95), ""},
	{"SDTV", 2, f(2), f(100), f(95), ""},
	{"WEBRip-480p", 3, f(2), f(100), f(95), "WEB 480p"},
	{"WEBDL-480p", 3, f(2), f(100), f(95), "WEB 480p"},
	{"DVD", 4, f(2), f(100), f(95), ""},
	{"Bluray-480p", 5, f(2), f(100), f(95), ""},
	{"Bluray-576p", 6, f(2), f(100), f(95), ""},
	{"HDTV-720p", 7, f(3), f(125), f(95), ""},
	{"HDTV-1080p", 8, f(4), f(125), f(95), ""},
	{"Raw-HD", 9, f(4), nil, f(95), ""},
	{"WEBRip-720p", 10, f(3), f(130), f(95), "WEB 720p"},
	{"WEBDL-720p", 10, f(3), f(130), f(95), "WEB 720p"},
	{"Bluray-720p", 11, f(4), f(130), f(95), ""},
	{"WEBRip-1080p", 12, f(4), f(130), f(95), "WEB 1080p"},
	{"WEBDL-1080p", 12, f(4), f(130), f(95), "WEB 1080p"},
	{"Bluray-1080p", 13, f(4), f(155), f(95), ""},
	{"Bluray-1080p Remux", 14, f(35), nil, f(95), ""},
	{"HDTV-2160p", 15, f(35), f(199.9), f(95), ""},
	{"WEBRip-2160p", 16, f(35), nil, f(95), "WEB 2160p"},
	{"WEBDL-2160p", 16, f(35), nil, f(95), "WEB 2160p"},
	{"Bluray-2160p", 17, f(35), nil, f(95), ""},
	{"Bluray-2160p Remux", 18, f(35), nil, f(95), ""},
}

var defaultDefsByFlavour = map[format.Flavour][]*Definition{
	format.Radarr: buildDefaults(format.Radarr, radarrDefaultRows),
	format.Sonarr: buildDefaults(format.Sonarr, sonarrDefaultRows),
}

func buildDefaults(fl format.Flavour, rows []defRow) []*Definition {
	out := make([]*Definition, len(rows))
	for i, r := range rows {
		q := MustQuality(fl, r.name)
		out[i] = &Definition{Quality: q, Title: q.Name, GroupName: r.group, Weight: r.weight,
			MinSize: r.min, MaxSize: r.max, PreferredSize: r.pref}
	}
	if len(out) != len(Qualities(fl)) {
		panic(fmt.Sprintf("profile: %s default definitions cover %d of %d qualities", fl, len(out), len(Qualities(fl))))
	}
	return out
}

func defaultDefs(fl format.Flavour) []*Definition { return defaultDefsByFlavour[fl] }

func defaultDefinition(fl format.Flavour, id int) *Definition {
	for _, d := range defaultDefs(fl) {
		if d.Quality.ID == id {
			return d
		}
	}
	panic(fmt.Sprintf("profile: no %s default definition for quality %d", fl, id))
}

// Weight is the quality's weight from the default definitions (QualityDefinitionService.WithWeight).
func Weight(fl format.Flavour, q Quality) int { return defaultDefinition(fl, q.ID).Weight }

// Megabytes is NumberExtensions.Megabytes(double): Convert.ToInt64(mb * 1024 * 1024), which rounds half to even.
func Megabytes(mb float64) int64 { return int64(math.RoundToEven(mb * 1024 * 1024)) }

var sizeSuffixes = []string{"B", "KB", "MB", "GB", "TB", "PB", "EB", "ZB", "YB"}

// SizeSuffix is NumberExtensions.SizeSuffix: "1.5 GB", with one decimal and a thousands separator ({0:n1}).
// Radarr and Sonarr differ only on long.MinValue, which a size never is.
func SizeSuffix(bytes int64) string {
	if bytes == 0 {
		return "0 B"
	}
	if bytes < 0 {
		if bytes == math.MinInt64 {
			return "-" + SizeSuffix(math.MaxInt64)
		}
		return "-" + SizeSuffix(-bytes)
	}
	mag := int(math.Log(float64(bytes)) / math.Log(1024))
	// bytes / 1024^mag exactly, then {0:n1}: one decimal, midpoints away from zero (decimal formatting).
	r := new(big.Rat).SetFrac(big.NewInt(bytes), new(big.Int).Lsh(big.NewInt(1), uint(10*mag)))
	r.Mul(r, big.NewRat(10, 1))
	r.Add(r, big.NewRat(1, 2))
	tenths := new(big.Int).Quo(r.Num(), r.Denom()) // floor(x*10 + 0.5), x >= 0
	whole := new(big.Int).Quo(tenths, big.NewInt(10)).Int64()
	frac := new(big.Int).Rem(tenths, big.NewInt(10)).Int64()
	return fmt.Sprintf("%s.%d %s", groupThousands(whole), frac, sizeSuffixes[mag])
}

func groupThousands(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// roundTo is Fluent.Round(number, level). Radarr rounds negative numbers toward zero (Math.Ceiling) and positive
// ones down; Sonarr always takes Math.Floor.
func roundTo(fl format.Flavour, n, level int64) int64 {
	q := n / level // truncates toward zero
	if n%level != 0 && n < 0 && fl == format.Sonarr {
		q-- // floor
	}
	return q * level
}
