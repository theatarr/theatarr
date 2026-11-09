package arrview

// The arr API resources the views write. Field names and null handling follow the arrs' System.Text.Json settings
// (src/NzbDrone.Common/Serializer/System.Text.Json/STJson.cs @ a96bf7c and cab419a: camelCase names, enums as
// camelCase strings, DefaultIgnoreCondition WhenWritingNull). So a C# reference or nullable type is `omitempty` (or a
// pointer) here, a C# value type is always written, and RestResource.Id is left out when 0
// (src/Radarr.Http/REST/RestResource.cs: JsonIgnoreCondition.WhenWritingDefault).

import (
	"encoding/json"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/decide/profile"
)

// arrTime is a DateTime as STJUtcConverter writes it: UTC, whole seconds, "Z".
type arrTime time.Time

func (t arrTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).UTC().Format("2006-01-02T15:04:05Z"))
}

func (t *arrTime) UnmarshalJSON(b []byte) error {
	var tt time.Time
	if err := json.Unmarshal(b, &tt); err != nil {
		return nil // clients echo what we sent; a value we cannot read is ignored, never an error
	}
	*t = arrTime(tt)
	return nil
}

func at(t time.Time) arrTime { return arrTime(t) }

func atPtr(t time.Time) *arrTime {
	if t.IsZero() {
		return nil
	}
	a := arrTime(t)
	return &a
}

// minTime is DateTime.MinValue, which the arrs write for an unset non-nullable date.
var minTime = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)

// language is Language (LanguageResource shape): {id, name}.
type language struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func languagesFrom(names []string) []language {
	out := []language{}
	for _, n := range names {
		if l, ok := radarr.LanguageByName(n); ok {
			out = append(out, language{ID: l.ID, Name: l.Name})
		} else {
			out = append(out, language{ID: 0, Name: n})
		}
	}
	return out
}

// quality is Quality: Radarr writes source and modifier, Sonarr source only.
type quality struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Source     string `json:"source"`
	Resolution int    `json:"resolution"`
	Modifier   string `json:"modifier,omitempty"`
}

type revision struct {
	Version  int  `json:"version"`
	Real     int  `json:"real"`
	IsRepack bool `json:"isRepack"`
}

// qualityModel is QualityModel.
type qualityModel struct {
	Quality  quality  `json:"quality"`
	Revision revision `json:"revision"`
}

func qualityFor(flavour, name string) quality {
	fl := format.Radarr
	if flavour == Sonarr {
		fl = format.Sonarr
	}
	q, ok := profile.QualityByName(fl, name)
	if !ok {
		q, _ = profile.QualityByID(fl, 0)
	}
	out := quality{ID: q.ID, Name: q.Name, Resolution: q.Resolution}
	if flavour == Sonarr {
		out.Source = sonarr.QualitySource(q.Source).String()
	} else {
		out.Source = radarr.QualitySource(q.Source).String()
		out.Modifier = radarr.Modifier(q.Modifier).String()
	}
	return out
}

func qualityModelFor(flavour, name string, rev int) qualityModel {
	if rev <= 0 {
		rev = 1
	}
	return qualityModel{Quality: qualityFor(flavour, name), Revision: revision{Version: rev}}
}

// pagingResource is PagingResource<T> (src/Radarr.Http/PagingResource.cs). Page and PageSize default to 1 and 10.
type pagingResource struct {
	Page          int    `json:"page"`
	PageSize      int    `json:"pageSize"`
	SortKey       string `json:"sortKey,omitempty"`
	SortDirection string `json:"sortDirection"`
	TotalRecords  int    `json:"totalRecords"`
	Records       any    `json:"records"`
}

func (r *request) paging(defKey, defDir string) pagingResource {
	p := pagingResource{Page: r.queryInt("page", 1), PageSize: r.queryInt("pageSize", 10), SortKey: defKey,
		SortDirection: defDir}
	if p.Page < 1 {
		p.Page = 1
	}
	if p.PageSize < 0 {
		p.PageSize = 10
	}
	if d, ok := r.query("sortDirection"); ok && (d == "ascending" || d == "descending") {
		p.SortDirection = d
	}
	return p
}

// window returns the slice bounds of the page over n records.
func (p pagingResource) window(n int) (int, int) {
	// Divide before multiplying: a huge page or pageSize must not overflow into a negative bound.
	skip := max(p.Page-1, 0)
	if p.PageSize <= 0 || skip > n/p.PageSize {
		return n, n
	}
	lo := skip * p.PageSize
	if n-lo <= p.PageSize {
		return lo, n
	}
	return lo, lo + p.PageSize
}

// tagResource is TagResource.
type tagResource struct {
	ID    int64  `json:"id,omitempty"`
	Label string `json:"label"`
}

// mediaInfoResource is MediaInfoResource (src/Radarr.Api.V3/MovieFiles/MediaInfoResource.cs). The import stores the
// arr's own object, so it is passed through as read.
type mediaInfoResource = json.RawMessage

// movieFileResource is MovieFileResource (src/Radarr.Api.V3/MovieFiles/MovieFileResource.cs @ a96bf7c).
type movieFileResource struct {
	ID                  int64             `json:"id,omitempty"`
	MovieID             int64             `json:"movieId"`
	RelativePath        string            `json:"relativePath,omitempty"`
	Path                string            `json:"path,omitempty"`
	Size                int64             `json:"size"`
	DateAdded           arrTime           `json:"dateAdded"`
	SceneName           string            `json:"sceneName,omitempty"`
	ReleaseGroup        string            `json:"releaseGroup,omitempty"`
	Edition             string            `json:"edition,omitempty"`
	Languages           []language        `json:"languages"`
	Quality             qualityModel      `json:"quality"`
	CustomFormats       []any             `json:"customFormats,omitempty"`
	CustomFormatScore   *int              `json:"customFormatScore,omitempty"`
	IndexerFlags        *int              `json:"indexerFlags,omitempty"`
	MediaInfo           mediaInfoResource `json:"mediaInfo,omitempty"`
	OriginalFilePath    string            `json:"originalFilePath,omitempty"`
	QualityCutoffNotMet bool              `json:"qualityCutoffNotMet"`
}

// rootFolderResource is RootFolderResource (src/Radarr.Api.V3/RootFolders/RootFolderResource.cs).
type rootFolderResource struct {
	ID              int64            `json:"id,omitempty"`
	Path            string           `json:"path"`
	Accessible      bool             `json:"accessible"`
	FreeSpace       *int64           `json:"freeSpace,omitempty"`
	UnmappedFolders []unmappedFolder `json:"unmappedFolders"`
}

type unmappedFolder struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	RelativePath string `json:"relativePath"`
}

// healthResource is HealthResource (src/Radarr.Api.V3/Health/HealthResource.cs); type is HealthCheckResult
// (ok, notice, warning, error). WikiUrl is a string the arrs always write (HttpUri.FullUri); ours have no wiki page,
// so it is "".
type healthResource struct {
	ID      int64  `json:"id,omitempty"`
	Source  string `json:"source"`
	Type    string `json:"type"`
	Message string `json:"message"`
	WikiURL string `json:"wikiUrl"`
}

// diskSpaceResource is DiskSpaceResource (src/Radarr.Api.V3/DiskSpace/DiskSpaceResource.cs).
type diskSpaceResource struct {
	ID         int64  `json:"id,omitempty"`
	Path       string `json:"path"`
	Label      string `json:"label,omitempty"`
	FreeSpace  int64  `json:"freeSpace"`
	TotalSpace int64  `json:"totalSpace"`
}
