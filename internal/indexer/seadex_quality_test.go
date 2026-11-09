package indexer

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
)

type fixtureTorrentNames map[string][]string

func (f fixtureTorrentNames) TorrentNames(_ context.Context, hash string) ([]string, error) {
	return f[NormalizeHash(hash)], nil
}

func TestSeaDexUnnamedFeaturesUseExactHashInventory(t *testing.T) {
	loadFixtures(t, "seadex_scoring.json")
	n := newNet(t)
	serveSeadex(t, n, &recorder{}, map[string]string{"500": "seadex_scoring.json"})
	h := newHarness(t, n, testPolicy(), false)
	h.svc.SetMetadata(Metadata{Torrents: fixtureTorrentNames{
		"5000000000000000000000000000000000000004": {"[Arid] Show [Dual-Audio][BDRip 1280x720 x264 FLAC]", "Show (BD 720p)"},
	}})
	res := search(t, h, "seadex", Query{Type: QuerySeries, AniList: 500, Title: "Show", Season: 1})
	for _, r := range res.Releases {
		if r.Extras["seadex_id"] == "deb1" {
			if q := sonarr.ParseQuality(r.Title); q.Quality.Name != "Bluray-720p" {
				t.Fatalf("unnamed features did not use the same-hash provider names: %s", r.Title)
			}
			return
		}
	}
	t.Fatal("missing fixture release")
}

func TestSeaDexUsesFeatureQualityInsteadOfInventingRemux(t *testing.T) {
	for _, c := range []struct {
		name, first, second, want string
	}{
		{"720p encode", "[Arid] Show - 01 [BDRip 1280x720 x264 FLAC].mkv", "[Arid] Show - 02 [BDRip 1280x720 x264 FLAC].mkv", "Bluray-720p"},
		{"1080p encode", "Show - 01 (BD 1080p).mkv", "Show - 02 (BD 1080p).mkv", "Bluray-1080p"},
		{"real remux", "Show.S01E01.1080p.BluRay.REMUX.mkv", "Show.S01E02.1080p.BluRay.REMUX.mkv", "Bluray-1080p Remux"},
		{"web", "Show.S01E01.1080p.WEB-DL.mkv", "Show.S01E02.1080p.WEB-DL.mkv", "WEBDL-1080p"},
		{"mixed", "Show - 01 (BD 720p).mkv", "Show - 02 (BD 1080p).mkv", "Unknown"},
		{"unknown", "a.mkv", "b.mkv", "Unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			n := newNet(t)
			body := map[string]any{"items": []any{map[string]any{"alID": 500, "trs": []any{"pick1"}, "expand": map[string]any{"trs": []any{
				map[string]any{"id": "pick1", "infoHash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "tracker": "Nyaa", "isBest": true,
					"files": []any{map[string]any{"name": c.first, "length": 3000000000}, map[string]any{"name": c.second, "length": 3000000000},
						map[string]any{"name": "NCOP.2160p.BluRay.REMUX.mkv", "length": 1000}}},
			}}}}}
			n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(body) }))
			h := newHarness(t, n, testPolicy(), false)
			r := search(t, h, "seadex", Query{Type: QuerySeries, AniList: 500, Title: "Show", Season: 1})
			if len(r.Releases) != 1 {
				t.Fatalf("releases %+v", r)
			}
			got := r.Releases[0]
			p := sonarr.ParseTitle(got.Title)
			if p == nil || p.SeriesTitle != "Show" || !p.FullSeason || p.Quality.Quality.Name != c.want {
				t.Fatalf("title %q parsed %+v, want %s", got.Title, p, c.want)
			}
			if got.Seeders != 1000 || len(preferredOf(r.Releases)) != 1 {
				t.Fatal("truthful quality lost the curator preference")
			}
			// Movie names must carry the same truthful quality, using Radarr's enum names.
			if c.want == "Bluray-720p" {
				p := radarr.ParseQuality(got.Title)
				if p.Quality.Name != "Bluray-720p" {
					t.Fatalf("movie quality %+v", p)
				}
			}
		})
	}
}
