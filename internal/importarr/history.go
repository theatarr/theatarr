package importarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// historyPageSize is the page size for GET history. The arrs compute custom formats per record, so pages stay
// moderate.
const historyPageSize = 250

// historyRecord is the shape of a Radarr or Sonarr history record (the fields the importer reads).
type historyRecord struct {
	Movie struct {
		TmdbID int `json:"tmdbId"`
	} `json:"movie"`
	Series struct {
		TvdbID int `json:"tvdbId"`
	} `json:"series"`
	ID                int             `json:"id"`
	MovieID           int             `json:"movieId"`
	SeriesID          int             `json:"seriesId"`
	EpisodeID         int             `json:"episodeId"`
	SourceTitle       string          `json:"sourceTitle"`
	Languages         []idName        `json:"languages"`
	Quality           qualityModel    `json:"quality"`
	CustomFormatScore *int            `json:"customFormatScore"`
	Date              time.Time       `json:"date"`
	DownloadID        string          `json:"downloadId"`
	EventType         json.RawMessage `json:"eventType"`
	Data              map[string]any  `json:"data"`
}

type historyPage struct {
	Page         int             `json:"page"`
	PageSize     int             `json:"pageSize"`
	TotalRecords int             `json:"totalRecords"`
	Records      []historyRecord `json:"records"`
}

type idName struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type qualityModel struct {
	Quality struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"quality"`
	Revision struct {
		Version int `json:"version"`
		Real    int `json:"real"`
	} `json:"revision"`
}

// fetchHistory pages through history newest first and stops at the first page whose records are all known, so a
// re-run reads one page of overlap plus whatever is new. known reports whether a history id is already stored.
func fetchHistory(ctx context.Context, g Getter, known func(id int) bool) ([]historyRecord, int, error) {
	var out []historyRecord
	seen := map[int]bool{}
	total := 0
	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("page", strconv.Itoa(page))
		q.Set("pageSize", strconv.Itoa(historyPageSize))
		q.Set("sortKey", "date")
		q.Set("sortDirection", "descending")
		var hp historyPage
		if err := g.GetJSON(ctx, "history", q, &hp); err != nil {
			return nil, 0, err
		}
		total = hp.TotalRecords
		allKnown := true
		for _, r := range hp.Records {
			if seen[r.ID] {
				continue // a record that moved across a page boundary while paging
			}
			seen[r.ID] = true
			if !known(r.ID) {
				allKnown = false
				out = append(out, r)
			}
		}
		if len(hp.Records) == 0 || allKnown || page*historyPageSize >= hp.TotalRecords {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, total, nil
}

var (
	hexHashRE = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
)

// eventTypeName reads the arr's eventType, which the API writes as a camelCase string (older builds: a number).
func eventTypeName(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		// Radarr MovieHistoryEventType; Sonarr's numbers differ, but every current build sends strings.
		switch n {
		case 1:
			return EventGrabbed
		case 3:
			return EventImported
		case 4:
			return EventFailed
		case 6:
			return EventFileDeleted
		}
		return "event" + strconv.Itoa(n)
	}
	return ""
}

// toEvent turns a raw record into a sanitised HistoryEvent.
func toEvent(r historyRecord) HistoryEvent {
	ev := HistoryEvent{
		ArrID:             r.ID,
		EventType:         eventTypeName(r.EventType),
		Date:              r.Date.UTC(),
		ArrTitleID:        r.MovieID,
		ArrEpisodeID:      r.EpisodeID,
		SourceTitle:       r.SourceTitle,
		Quality:           r.Quality.Quality.Name,
		CustomFormatScore: r.CustomFormatScore,
		Languages:         languageNames(r.Languages),
		Data:              sanitizeData(r.Data),
	}
	if r.SeriesID != 0 {
		ev.ArrTitleID = r.SeriesID
	}
	if !unsafeValue(r.DownloadID) {
		ev.DownloadID = r.DownloadID
	}
	if unsafeValue(ev.SourceTitle) {
		ev.SourceTitle = ""
	}
	switch ev.Data["protocol"] {
	case "1", "usenet":
		ev.Protocol = "usenet"
	case "2", "torrent":
		ev.Protocol = "torrent"
	}
	if ev.Protocol != "usenet" {
		switch {
		case hexHashRE.MatchString(ev.Data["torrentInfoHash"]):
			ev.InfoHash = strings.ToLower(ev.Data["torrentInfoHash"])
		case hexHashRE.MatchString(ev.DownloadID):
			// qBittorrent-type clients (decypharr too) use the info hash as the download id.
			ev.InfoHash = strings.ToLower(ev.DownloadID)
		}
		if ev.InfoHash != "" {
			ev.Protocol = "torrent"
		}
	}
	return ev
}

func languageNames(l []idName) []string {
	out := make([]string, 0, len(l))
	for _, x := range l {
		if x.Name != "" {
			out = append(out, x.Name)
		}
	}
	return out
}

// historyDataKeys are the data keys kept, in canonical spelling. Everything else is dropped: download URLs, info
// URLs and GUIDs can carry indexer keys or tracker passkeys.
var historyDataKeys = []string{
	"indexer", "releaseGroup", "size", "protocol", "torrentInfoHash", "indexerFlags", "tmdbId", "imdbId", "tvdbId",
	"publishedDate", "customFormatScore", "downloadClient", "downloadClientName", "fileId", "importedPath",
	"droppedPath", "reason", "message", "movieMatchType", "releaseSource", "path", "relativePath", "sourcePath",
	"sourceRelativePath", "releaseType", "seriesMatchType", "releaseHash", "tvRageId",
}

func sanitizeData(in map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		canon := ""
		for _, c := range historyDataKeys {
			if strings.EqualFold(c, k) {
				canon = c
				break
			}
		}
		if canon == "" || v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			s = fmt.Sprint(v)
		}
		if s == "" || unsafeValue(s) {
			continue
		}
		out[canon] = s
	}
	return out
}

// unsafeValue reports whether a string looks like a link or a credential.
func unsafeValue(s string) bool {
	l := strings.ToLower(s)
	for _, bad := range []string{"://", "apikey", "api_key", "passkey", "authkey", "torrent_pass", "token=", "magnet:"} {
		if strings.Contains(l, bad) {
			return true
		}
	}
	return false
}
