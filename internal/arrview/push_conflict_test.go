package arrview

import (
	"strings"
	"testing"
)

func TestPushConflictReachesProvider(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"magnetUrl", "downloadUrl"} {
		t.Run(field, func(t *testing.T) {
			e := newDevEnv(t)
			e.pushClient("autobrr")
			var out []map[string]any
			e.call("POST", "/v/movie-web/radarr/api/v3/release/push", map[string]any{
				"title":    "Alpha.2020.2160p.WEB-DL.H265-GRP",
				"infoHash": strings.Repeat("a", 40), field: "magnet:?xt=urn:btih:" + strings.Repeat("b", 40),
				"protocol": "torrent", "indexerId": 2, "size": 1 << 30, "tmdbId": 101, "publishDate": "2026-10-26T11:59:55Z",
			}, 400, &out)
			e.drain()
			if adds := e.adds(); len(adds) != 0 {
				t.Fatalf("conflicting push accepted and added: approved=%v adds=%v", out[0]["approved"], adds)
			}
		})
	}
}
