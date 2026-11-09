package policy

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Title art and metadata (docs/design/artwork.md): the meta.art job fills title_meta from TMDb
// (metadata.sources.tmdb, its budget) on metadata.art's schedule, and the image proxy (/img/...) fetches TMDb images
// from metadata.sources.tmdb-images (its own budget) into the disk cache ui.images.

// TMDbImagesSource is the metadata source key of TMDb's image host (https://image.tmdb.org/t/p).
const TMDbImagesSource = "tmdb-images"

// Art schedules the meta.art job (metadata.art).
type Art struct {
	// PerRun caps the titles one run fetches. Default 200.
	PerRun int `yaml:"per_run"`
	// Interval is the time between scheduled runs. Default 6h, minimum 10m.
	Interval Duration `yaml:"interval"`
	// Prewarm fetches each filled title's poster at this size into the image cache, so grids load at once. Default
	// w342; "off" turns it off.
	Prewarm string `yaml:"prewarm"`
}

// UI holds settings of the web UI's server side.
type UI struct {
	// Images configures the image cache on disk.
	Images *ImagesConfig `yaml:"images"`
}

// ImagesConfig is the image proxy's disk cache (ui.images).
type ImagesConfig struct {
	// Dir is the cache directory, absolute. Default: images next to the database (/data/images in the container).
	Dir string `yaml:"dir"`
	// MaxBytes caps the cache; the least recently used files go first. Default 2GiB, minimum 16MiB.
	MaxBytes ByteSize `yaml:"max_bytes"`
	// Original allows the "original" size (full-resolution files, often several MB). Default false.
	Original bool `yaml:"original"`
}

// Art and image defaults and limits.
const (
	DefaultArtPerRun     = 200
	MaxArtPerRun         = 5000
	DefaultArtInterval   = 6 * time.Hour
	MinArtInterval       = 10 * time.Minute
	DefaultArtPrewarm    = "w342"
	DefaultImageMaxBytes = 2 << 30
	MinImageMaxBytes     = 16 << 20
)

// ImageSizes are the TMDb sizes the proxy serves, smallest first; "original" only with ui.images.original.
var ImageSizes = []string{"w92", "w185", "w342", "w500", "w780", "w1280"}

// ArtSettings is metadata.art with defaults.
type ArtSettings struct {
	PerRun   int
	Interval time.Duration
	// Prewarm is the poster size to pre-fetch, "" for none.
	Prewarm string
}

// ArtSettings returns metadata.art with its defaults.
func (p *Policy) ArtSettings() ArtSettings {
	s := ArtSettings{PerRun: DefaultArtPerRun, Interval: DefaultArtInterval, Prewarm: DefaultArtPrewarm}
	if p.Metadata == nil || p.Metadata.Art == nil {
		return s
	}
	a := p.Metadata.Art
	if a.PerRun > 0 {
		s.PerRun = a.PerRun
	}
	if a.Interval > 0 {
		s.Interval = a.Interval.D()
	}
	switch a.Prewarm {
	case "":
	case "off":
		s.Prewarm = ""
	default:
		s.Prewarm = a.Prewarm
	}
	return s
}

// ImageSettings is ui.images with defaults.
type ImageSettings struct {
	Dir      string
	MaxBytes int64
	Original bool
}

// ImageSettings returns ui.images with its defaults; dataDir is where the database lives.
func (p *Policy) ImageSettings(dataDir string) ImageSettings {
	s := ImageSettings{Dir: filepath.Join(dataDir, "images"), MaxBytes: DefaultImageMaxBytes}
	if p.UI == nil || p.UI.Images == nil {
		return s
	}
	im := p.UI.Images
	if im.Dir != "" {
		s.Dir = im.Dir
	}
	if im.MaxBytes > 0 {
		s.MaxBytes = int64(im.MaxBytes)
	}
	s.Original = im.Original
	return s
}

// ValidImageSize reports whether size is one the proxy serves under these settings.
func (s ImageSettings) ValidImageSize(size string) bool {
	if size == "original" {
		return s.Original
	}
	for _, v := range ImageSizes {
		if v == size {
			return true
		}
	}
	return false
}

func (p *Policy) validateArt(e *Errors) {
	var sources map[string]*MetadataSource
	if p.Metadata != nil {
		sources = p.Metadata.Sources
	}
	if s := sources[TMDbImagesSource]; s != nil && s.APIKeyEnv != "" {
		e.add(fmt.Sprintf("metadata.sources[%s].api_key_env", TMDbImagesSource),
			"TMDb images need no key; nothing secret goes to the image host")
	}
	if p.Metadata != nil && p.Metadata.Art != nil {
		a := p.Metadata.Art
		if sources["tmdb"] == nil {
			e.add("metadata.art", "needs metadata.sources.tmdb")
		}
		if a.PerRun < 0 || a.PerRun > MaxArtPerRun {
			e.add("metadata.art.per_run", "must be 1 to %d", MaxArtPerRun)
		}
		if a.Interval != 0 && a.Interval.D() < MinArtInterval {
			e.add("metadata.art.interval", "%v is under the %v minimum", a.Interval.D(), MinArtInterval)
		}
		if a.Prewarm != "" && a.Prewarm != "off" && !(ImageSettings{}).ValidImageSize(a.Prewarm) {
			e.add("metadata.art.prewarm", "%q: want off or one of %s", a.Prewarm, strings.Join(ImageSizes, ", "))
		}
	}
	if p.UI != nil && p.UI.Images != nil {
		im := p.UI.Images
		if sources[TMDbImagesSource] == nil {
			e.add("ui.images", "needs metadata.sources.%s (the image host, through the gateway)", TMDbImagesSource)
		}
		if im.Dir != "" && !filepath.IsAbs(im.Dir) {
			e.add("ui.images.dir", "%q is not an absolute path", im.Dir)
		} else if im.Dir != "" {
			// The cache removes files under its dir (least recently used first): never a library's.
			if what := p.dirOverlap(im.Dir); what != "" {
				e.add("ui.images.dir", "%q overlaps %s; the image cache removes files under its dir", im.Dir, what)
			}
		}
		if im.MaxBytes != 0 && im.MaxBytes < MinImageMaxBytes {
			e.add("ui.images.max_bytes", "%d is under the %d (16MiB) minimum", int64(im.MaxBytes), MinImageMaxBytes)
		}
	}
}

// ByteSize is a size in bytes, written as a number or with a unit: 2GiB, 512MiB, 500MB, 64KiB.
type ByteSize int64

var byteUnits = []struct {
	suffix string
	mult   int64
}{
	{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
	{"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12}, {"B", 1},
}

// ParseByteSize parses a size such as 2GiB or 1048576.
func ParseByteSize(s string) (ByteSize, error) {
	s = strings.TrimSpace(s)
	mult := int64(1)
	num := s
	for _, u := range byteUnits {
		if strings.HasSuffix(s, u.suffix) {
			num, mult = strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), u.mult
			break
		}
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("%q is not a size (e.g. 2GiB, 512MiB, 1048576)", s)
	}
	v := f * float64(mult)
	if v > 1<<62 {
		return 0, fmt.Errorf("%q is too large", s)
	}
	return ByteSize(v), nil
}

func (b *ByteSize) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := ParseByteSize(s)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*b = v
	return nil
}

func (b ByteSize) MarshalYAML() (any, error) { return int64(b), nil }
