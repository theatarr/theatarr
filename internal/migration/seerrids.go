package migration

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// theatarr migrate seerr-ids (docs/design/arrview.md "Seerr's stored ids"): Seerr keeps, per media item, the arr server
// and that arr's id of the title. Once Seerr points at Theatarr's views those ids reach the views, which answer ids
// below arrview.IDBase through their alias_from instances. The check reads Seerr's media (GET only) and asks the
// view each id would reach which title it names.

// Seerr id check results.
const (
	SeerrIDOK           = "ok"           // the view answers the id with the item's title
	SeerrIDWrongTitle   = "wrong_title"  // the view answers the id with another title
	SeerrIDUnresolved   = "unresolved"   // the view answers 404
	SeerrIDUnverifiable = "unverifiable" // a series Seerr has no TVDB id for, answered without a TMDb id
)

// ViewTitle is what a view answered for one id: nil when it answered 404.
type ViewTitle struct {
	Title  string `json:"title"`
	TmdbID int64  `json:"tmdbId"`
	TvdbID int64  `json:"tvdbId"`
}

// ViewResolver asks the view at path for the movie (radarr) or series (sonarr) with id.
type ViewResolver func(ctx context.Context, flavour, path string, id int64) (*ViewTitle, error)

// SeerrIDReport is the check's answer.
type SeerrIDReport struct {
	// Views are the view paths the check used, by flavour.
	Views   map[string]string `json:"views"`
	Summary SeerrIDSummary    `json:"summary"`
	Items   []SeerrIDItem     `json:"items"`
}

// SeerrIDSummary counts the checked ids.
type SeerrIDSummary struct {
	Media        int `json:"media"`
	Checked      int `json:"checked"`
	OK           int `json:"ok"`
	WrongTitle   int `json:"wrong_title"`
	Unresolved   int `json:"unresolved"`
	Unverifiable int `json:"unverifiable"`
	// NoServer counts ids whose server id names no server in Seerr's settings.
	NoServer int `json:"no_server"`
}

// SeerrIDItem is one stored id of one Seerr media item.
type SeerrIDItem struct {
	MediaID   int64  `json:"media_id"`
	MediaType string `json:"media_type"`
	TmdbID    int64  `json:"tmdb_id"`
	TvdbID    int64  `json:"tvdb_id,omitempty"`
	// Field is externalServiceId or externalServiceId4k.
	Field string `json:"field"`
	ID    int64  `json:"id"`
	// ServerID is the item's serviceId (or serviceId4k); Server its name in Seerr, empty when NoServer.
	ServerID  *int64 `json:"server_id,omitempty"`
	Server    string `json:"server,omitempty"`
	Is4K      bool   `json:"is_4k"`
	IsDefault bool   `json:"is_default"`
	NoServer  bool   `json:"no_server,omitempty"`
	View      string `json:"view"`
	Result    string `json:"result"`
	// ViewTitle is what the view answered for ID, when it answered.
	ViewTitle *ViewTitle `json:"view_title,omitempty"`
}

// seerrMedia is the part of Seerr's Media entity the check reads (server/entity/Media.ts).
type seerrMedia struct {
	ID                  int64  `json:"id"`
	MediaType           string `json:"mediaType"`
	TmdbID              int64  `json:"tmdbId"`
	TvdbID              *int64 `json:"tvdbId"`
	ServiceID           *int64 `json:"serviceId"`
	ServiceID4K         *int64 `json:"serviceId4k"`
	ExternalServiceID   *int64 `json:"externalServiceId"`
	ExternalServiceID4K *int64 `json:"externalServiceId4k"`
}

// seerrMediaPage is the take used to page GET media.
const seerrMediaPage = 100

// CheckSeerrIDs reads Seerr's servers and media through g and resolves every stored arr id through the view of its
// flavour (views: radarr and sonarr to a view path). Seerr's availability sync sends an item's id to every server of
// the item's kind and 4K class whatever its serviceId says, so the id's flavour, not its server, picks the view.
func CheckSeerrIDs(ctx context.Context, g Getter, views map[string]string, resolve ViewResolver) (*SeerrIDReport, error) {
	servers := map[string]map[int64]SeerrServer{}
	for _, fl := range []string{"radarr", "sonarr"} {
		var list []struct {
			ID        int64  `json:"id"`
			Name      string `json:"name"`
			Is4K      bool   `json:"is4k"`
			IsDefault bool   `json:"isDefault"`
		}
		if err := g.GetJSON(ctx, "settings/"+fl, nil, &list); err != nil {
			return nil, err
		}
		servers[fl] = map[int64]SeerrServer{}
		for _, s := range list {
			servers[fl][s.ID] = SeerrServer{ID: int(s.ID), Name: s.Name, Is4K: s.Is4K, IsDefault: s.IsDefault}
		}
	}
	rep := &SeerrIDReport{Views: views, Items: []SeerrIDItem{}}
	for skip := 0; ; {
		var page struct {
			PageInfo struct {
				Results int `json:"results"`
			} `json:"pageInfo"`
			Results []seerrMedia `json:"results"`
		}
		q := url.Values{"take": {strconv.Itoa(seerrMediaPage)}, "skip": {strconv.Itoa(skip)}}
		if err := g.GetJSON(ctx, "media", q, &page); err != nil {
			return nil, err
		}
		for _, m := range page.Results {
			rep.Summary.Media++
			if err := rep.check(ctx, m, servers, resolve); err != nil {
				return nil, err
			}
		}
		skip += len(page.Results)
		if len(page.Results) == 0 || skip >= page.PageInfo.Results {
			break
		}
	}
	return rep, nil
}

func (rep *SeerrIDReport) check(ctx context.Context, m seerrMedia, servers map[string]map[int64]SeerrServer,
	resolve ViewResolver) error {
	fl := "radarr"
	if m.MediaType == "tv" {
		fl = "sonarr"
	} else if m.MediaType != "movie" {
		return nil
	}
	for _, f := range []struct {
		field   string
		id, srv *int64
		is4k    bool
	}{{"externalServiceId", m.ExternalServiceID, m.ServiceID, false},
		{"externalServiceId4k", m.ExternalServiceID4K, m.ServiceID4K, true}} {
		if f.id == nil || *f.id <= 0 {
			continue
		}
		it := SeerrIDItem{MediaID: m.ID, MediaType: m.MediaType, TmdbID: m.TmdbID, Field: f.field, ID: *f.id,
			ServerID: f.srv, Is4K: f.is4k, View: rep.Views[fl]}
		if m.TvdbID != nil {
			it.TvdbID = *m.TvdbID
		}
		if f.srv != nil {
			if s, ok := servers[fl][*f.srv]; ok {
				it.Server, it.IsDefault = s.Name, s.IsDefault
			} else {
				it.NoServer = true
			}
		} else {
			it.NoServer = true
		}
		vt, err := resolve(ctx, fl, it.View, it.ID)
		if err != nil {
			return fmt.Errorf("view %s, %s id %d: %w", it.View, fl, it.ID, err)
		}
		it.ViewTitle = vt
		it.Result = seerrIDResult(fl, it, vt)
		rep.Summary.Checked++
		switch it.Result {
		case SeerrIDOK:
			rep.Summary.OK++
		case SeerrIDWrongTitle:
			rep.Summary.WrongTitle++
		case SeerrIDUnresolved:
			rep.Summary.Unresolved++
		default:
			rep.Summary.Unverifiable++
		}
		if it.NoServer {
			rep.Summary.NoServer++
		}
		rep.Items = append(rep.Items, it)
	}
	return nil
}

// seerrIDResult compares as Seerr's availability sync does: movies by TMDb id, series by TVDB id. A series Seerr has
// no TVDB id for is compared by TMDb id when the view gives one (Seerr itself skips the check then).
func seerrIDResult(flavour string, it SeerrIDItem, vt *ViewTitle) string {
	switch {
	case vt == nil:
		return SeerrIDUnresolved
	case flavour == "radarr":
		return sameID(vt.TmdbID, it.TmdbID)
	case it.TvdbID > 0:
		return sameID(vt.TvdbID, it.TvdbID)
	case vt.TmdbID > 0 && it.TmdbID > 0:
		return sameID(vt.TmdbID, it.TmdbID)
	}
	return SeerrIDUnverifiable
}

func sameID(a, b int64) string {
	if a == b {
		return SeerrIDOK
	}
	return SeerrIDWrongTitle
}
