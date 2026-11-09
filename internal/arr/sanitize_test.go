package arr

import (
	"encoding/json"
	"strings"
	"testing"
)

// A grabbed record as the arrs return it, with every kind of secret the history can hold.
const dirtyRecord = `{
  "id": 4711,
  "movieId": 12,
  "seriesId": 0,
  "episodeId": 0,
  "sourceTitle": "Some.Movie.2021.2160p.WEB-DL.DDP5.1.HDR.H.265-GRP",
  "languages": [{"id": 1, "name": "English"}],
  "quality": {"quality": {"id": 18, "name": "WEBDL-2160p", "source": "webdl", "resolution": 2160, "modifier": "none", "extra": "x"},
              "revision": {"version": 1, "real": 0, "isRepack": false}},
  "customFormats": [{"id": 3, "name": "HDR", "specifications": [{"value": "https://evil/?apikey=abc"}]}],
  "customFormatScore": 1500,
  "qualityCutoffNotMet": true,
  "date": "2026-10-01T12:00:00Z",
  "downloadId": "ABCDEF0123456789ABCDEF0123456789ABCDEF01",
  "eventType": "grabbed",
  "movie": {"title": "Some Movie", "images": [{"remoteUrl": "https://image.tmdb.org/x.jpg"}]},
  "data": {
    "indexer": "Torrentio (Prowlarr)",
    "nzbInfoUrl": "https://tracker.example/details.php?id=1",
    "releaseGroup": "GRP",
    "age": "3",
    "publishedDate": "2026-09-30T00:00:00Z",
    "downloadClient": "qBittorrent",
    "downloadClientName": "decypharr",
    "size": "12345678901",
    "downloadUrl": "http://prowlarr:9696/1/download?apikey=PROWLARRKEY&link=abc",
    "guid": "https://tracker.example/torrent/123?passkey=PASSKEY123",
    "tmdbId": "603",
    "imdbId": "tt0133093",
    "protocol": "2",
    "customFormatScore": "1500",
    "indexerFlags": "0",
    "ReleaseType": "SeasonPack",
    "IndexerId": "7",
    "TorrentInfoHash": "abcdef0123456789abcdef0123456789abcdef01",
    "releaseHash": "magnet:?xt=urn:btih:abc",
    "releaseGroupLeak": "x"
  }
}`

func TestSanitizeDropsSecrets(t *testing.T) {
	out, _, err := SanitizeHistory(json.RawMessage(dirtyRecord))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, bad := range []string{"://", "apikey", "passkey", "PROWLARRKEY", "PASSKEY123", "tracker.example", "guid",
		"downloadUrl", "nzbInfoUrl", "downloadClient", "magnet", "specifications", "movie\"", "images",
		"qualityCutoffNotMet", "IndexerId", "indexerId", "releaseGroupLeak", "extra", `"age"`} {
		if strings.Contains(s, bad) {
			t.Errorf("sanitised record still has %q: %s", bad, s)
		}
	}
	var rec map[string]any
	if err := json.Unmarshal(out, &rec); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "movieId", "sourceTitle", "languages", "quality", "customFormats",
		"customFormatScore", "date", "downloadId", "eventType", "data"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("lost allowed field %q", k)
		}
	}
	data := rec["data"].(map[string]any)
	want := map[string]string{
		"indexer": "Torrentio (Prowlarr)", "releaseGroup": "GRP", "publishedDate": "2026-09-30T00:00:00Z",
		"size": "12345678901", "tmdbId": "603", "imdbId": "tt0133093", "protocol": "2", "indexerFlags": "0",
		"torrentInfoHash": "abcdef0123456789abcdef0123456789abcdef01", "releaseType": "SeasonPack",
	}
	if len(data) != len(want) {
		t.Errorf("data has %d keys, want %d: %v", len(data), len(want), data)
	}
	for k, v := range want {
		if data[k] != v {
			t.Errorf("data[%s] = %v, want %s", k, data[k], v)
		}
	}
	cf := rec["customFormats"].([]any)[0].(map[string]any)
	if len(cf) != 2 || cf["name"] != "HDR" {
		t.Errorf("customFormats entry %v", cf)
	}
}

func TestSanitizeDropsUnsafeAllowedValues(t *testing.T) {
	// Allowed keys whose values are links or carry keys are dropped too.
	rec := `{"id":1,"sourceTitle":"https://x/?apikey=1","downloadId":"http://dl/passkey=2",
	         "data":{"indexer":"https://idx/api?apikey=K","releaseGroup":"G"}}`
	out, dropped, err := SanitizeHistory(json.RawMessage(rec))
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 3 {
		t.Errorf("dropped %d, want 3", dropped)
	}
	if s := string(out); strings.Contains(s, "://") || strings.Contains(s, "apikey") || strings.Contains(s, "passkey") {
		t.Errorf("unsafe value kept: %s", s)
	}
}

func TestSanitizeRejectsObjectsInScalars(t *testing.T) {
	out, _, err := SanitizeHistory(json.RawMessage(`{"id":1,"sourceTitle":{"url":"https://x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "https") || strings.Contains(string(out), "sourceTitle") {
		t.Errorf("object kept: %s", out)
	}
}
