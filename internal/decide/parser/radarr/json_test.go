package radarr

import (
	"encoding/json"
	"testing"
)

// The parsedMovieInfo of a live GET /api/v3/parse (radarr_4k_debrid, Radarr 6.4.4.10685), byte for
// byte: field names, order, enum spelling, and which fields are present.
func TestParsedMovieInfoJSON(t *testing.T) {
	cases := []struct{ title, want string }{
		{
			"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.DV.HDR.HEVC.TrueHD.Atmos.7.1-FGT",
			`{"movieTitles":["Dune Part Two"],"originalTitle":"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.DV.HDR.HEVC.TrueHD.Atmos.7.1-FGT","releaseTitle":"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.DV.HDR.HEVC.TrueHD.Atmos.7.1-FGT","simpleReleaseTitle":"A.Movie.2024.2160p.UHD.BluRay.REMUX.DV.HDR.HEVC.TrueHD.Atmos.7.1-FGT","quality":{"quality":{"id":31,"name":"Remux-2160p","source":"bluray","resolution":2160,"modifier":"remux"},"revision":{"version":1,"real":0,"isRepack":false}},"languages":[{"id":0,"name":"Unknown"}],"releaseGroup":"FGT","releaseHash":"","edition":"","year":2024,"imdbId":"","tmdbId":0,"movieTitle":"Dune Part Two","primaryMovieTitle":"Dune Part Two"}`,
		},
		{
			"Movie.Title.2016.1080p.KORSUB.WEBRip.x264.AAC2.0-RADARR",
			`{"movieTitles":["Movie Title"],"originalTitle":"Movie.Title.2016.1080p.KORSUB.WEBRip.x264.AAC2.0-RADARR","releaseTitle":"Movie.Title.2016.1080p.KORSUB.WEBRip.x264.AAC2.0-RADARR","simpleReleaseTitle":"A.Movie.2016.1080p.KORSUB.WEBRip.x264.AAC2.0-RADARR","quality":{"quality":{"id":15,"name":"WEBRip-1080p","source":"webrip","resolution":1080,"modifier":"none"},"revision":{"version":1,"real":0,"isRepack":false}},"languages":[{"id":0,"name":"Unknown"}],"releaseGroup":"RADARR","releaseHash":"","edition":"","year":2016,"imdbId":"","tmdbId":0,"hardcodedSubs":"KORSUB","movieTitle":"Movie Title","primaryMovieTitle":"Movie Title"}`,
		},
	}
	for _, c := range cases {
		b, err := json.Marshal(ParseMovieTitle(c.title, false))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.title, b, c.want)
		}
	}
}
