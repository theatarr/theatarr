package arr

import (
	"encoding/json"
	"fmt"
	"strings"
)

// History records can carry secrets: download URLs with indexer API keys or tracker passkeys, info URLs, and GUIDs
// that are links. SanitizeHistory therefore works from an allowlist: it builds a new record from the fields below
// and drops everything else. As a second line, every kept string is checked by unsafeValue and dropped if it looks
// like a URL or carries a key or passkey parameter.

// historyScalars are the top-level scalar fields kept as is.
var historyScalars = []string{
	"id", "date", "eventType", "sourceTitle", "movieId", "seriesId", "episodeId", "downloadId", "customFormatScore",
}

// historyData are the keys kept from the record's data map, in their canonical spelling. The arrs write them
// PascalCase and serialise them camelCase; matching is case-insensitive.
var historyData = []string{
	"indexer", "releaseGroup", "size", "protocol", "torrentInfoHash", "indexerFlags", "tmdbId", "imdbId", "tvdbId",
	"publishedDate", "releaseType",
}

type sanQuality struct {
	Quality struct {
		ID         int    `json:"id"`
		Name       string `json:"name"`
		Source     string `json:"source,omitempty"`
		Resolution int    `json:"resolution"`
		Modifier   string `json:"modifier,omitempty"`
	} `json:"quality"`
	Revision struct {
		Version  int  `json:"version"`
		Real     int  `json:"real"`
		IsRepack bool `json:"isRepack"`
	} `json:"revision"`
}

type idName struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// SanitizeHistory returns a copy of one raw history record that holds only allowlisted fields. dropped counts kept
// fields whose value was removed because it looked unsafe.
func SanitizeHistory(raw json.RawMessage) (out json.RawMessage, dropped int, err error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, 0, fmt.Errorf("arr: sanitise history: %w", err)
	}
	rec := map[string]any{}
	for _, k := range historyScalars {
		v, ok := in[k]
		if !ok {
			continue
		}
		var s any
		if err := json.Unmarshal(v, &s); err != nil {
			continue
		}
		switch t := s.(type) {
		case string:
			if unsafeValue(t) {
				dropped++
				continue
			}
			rec[k] = t
		case float64, bool, nil:
			rec[k] = json.RawMessage(v)
		default:
			// objects or arrays where a scalar belongs: drop.
			dropped++
		}
	}
	if v, ok := in["quality"]; ok {
		var q sanQuality
		if json.Unmarshal(v, &q) == nil {
			if unsafeValue(q.Quality.Name) || unsafeValue(q.Quality.Source) || unsafeValue(q.Quality.Modifier) {
				dropped++
			} else {
				rec["quality"] = q
			}
		}
	}
	for _, k := range []string{"languages", "customFormats"} {
		v, ok := in[k]
		if !ok {
			continue
		}
		var list []idName
		if json.Unmarshal(v, &list) != nil {
			continue
		}
		clean := make([]idName, 0, len(list))
		for _, l := range list {
			if unsafeValue(l.Name) {
				dropped++
				continue
			}
			clean = append(clean, l)
		}
		rec[k] = clean
	}
	if v, ok := in["data"]; ok {
		var data map[string]any
		if json.Unmarshal(v, &data) == nil {
			keep := map[string]string{}
			for dk, dv := range data {
				canon := canonicalDataKey(dk)
				if canon == "" {
					continue
				}
				s, ok := dv.(string)
				if !ok {
					if dv == nil {
						continue
					}
					s = fmt.Sprint(dv)
				}
				if unsafeValue(s) {
					dropped++
					continue
				}
				keep[canon] = s
			}
			rec["data"] = keep
		}
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return nil, dropped, err
	}
	return b, dropped, nil
}

func canonicalDataKey(k string) string {
	for _, c := range historyData {
		if strings.EqualFold(c, k) {
			return c
		}
	}
	return ""
}

// unsafeValue reports whether a kept string looks like a link or a credential.
func unsafeValue(s string) bool {
	l := strings.ToLower(s)
	for _, bad := range []string{"://", "apikey", "api_key", "passkey", "authkey", "torrent_pass", "token=", "magnet:"} {
		if strings.Contains(l, bad) {
			return true
		}
	}
	return false
}
