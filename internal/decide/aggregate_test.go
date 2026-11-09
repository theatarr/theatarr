package decide

import (
	"reflect"
	"testing"
)

// Per-file import aggregation (Sonarr's and Radarr's import Aggregation): every file of a release is what its own
// name and probe say.
func TestAggregateSeriesFile(t *testing.T) {
	t.Parallel()
	pack := "Show.S01.2160p.BluRay.REMUX.HEVC-PACK"
	for _, tc := range []struct {
		name string
		in   ImportFile
		want FileFacts
	}{
		{"a WEB-DL file in a remux pack", ImportFile{Path: pack + "/Show.S01E01.1080p.WEB-DL.H.264-FILE.mkv", Folder: pack,
			Client: pack, Grabbed: pack, OtherVideoFiles: true, Media: &MediaInfo{Width: 1920, Height: 1080}},
			FileFacts{Quality: "WEBDL-1080p", Revision: 1, ReleaseGroup: "FILE", SceneName: "Show.S01E01.1080p.WEB-DL.H.264-FILE"}},
		{"media info beats the name's resolution", ImportFile{Path: pack + "/Show.S01E01.2160p.WEB-DL.H.265-FILE.mkv",
			Folder: pack, Client: pack, Grabbed: pack, OtherVideoFiles: true, Media: &MediaInfo{Width: 1920, Height: 800}},
			FileFacts{Quality: "WEBDL-1080p", Revision: 1, ReleaseGroup: "FILE", SceneName: "Show.S01E01.2160p.WEB-DL.H.265-FILE"}},
		{"an untagged file takes the folder's", ImportFile{Path: pack + "/01.mkv", Folder: pack, Client: pack, Grabbed: pack,
			OtherVideoFiles: true, Media: &MediaInfo{Width: 3840, Height: 2160, AudioLanguages: []string{"jpn", "eng"}}},
			FileFacts{Quality: "Bluray-2160p Remux", Revision: 1, ReleaseGroup: "PACK", Languages: []string{"Japanese", "English"}}},
		{"a proper single episode", ImportFile{Path: "Show.S01E02.PROPER.1080p.WEB-DL-GRP/abc.mkv",
			Folder: "Show.S01E02.PROPER.1080p.WEB-DL-GRP", Client: "Show.S01E02.PROPER.1080p.WEB-DL-GRP",
			Grabbed: "Show.S01E02.PROPER.1080p.WEB-DL-GRP", Media: &MediaInfo{Width: 1920, Height: 1080}},
			FileFacts{Quality: "WEBDL-1080p", Revision: 2, ReleaseGroup: "GRP", SceneName: "Show.S01E02.PROPER.1080p.WEB-DL-GRP"}},
		{"an anime file's release hash", ImportFile{Path: "[Group] Show (BD 1080p)/[Group] Show - 01 (BD 1080p) [ABCD1234].mkv",
			Folder: "[Group] Show (BD 1080p)", Client: "[Group] Show (BD 1080p)", OtherVideoFiles: true},
			FileFacts{Quality: "Bluray-1080p", Revision: 1, ReleaseGroup: "Group", ReleaseHash: "ABCD1234"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AggregateSeriesFile("", tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
	if _, err := AggregateSeriesFile("v9", ImportFile{Path: "x.mkv"}); err == nil {
		t.Error("an unknown parser dialect was accepted")
	}
}

func TestAggregateMovieFile(t *testing.T) {
	t.Parallel()
	rel := "Movie.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC-GRP"
	for _, tc := range []struct {
		name string
		in   ImportFile
		want FileFacts
	}{
		{"an untagged file of a remux", ImportFile{Path: rel + "/movie.mkv", Folder: rel, Client: rel, Grabbed: rel,
			Media: &MediaInfo{Width: 3840, Height: 2160}},
			FileFacts{Quality: "Remux-2160p", Revision: 1, ReleaseGroup: "GRP", SceneName: rel}},
		{"Radarr's modifier rule: the folder's REMUX wins whatever its confidence", ImportFile{Path: rel + "/Movie.2001.1080p.WEB-DL.H.264-FILE.mkv",
			Folder: rel, Client: rel, Grabbed: rel, OtherVideoFiles: true, Media: &MediaInfo{Width: 1920, Height: 1080}},
			FileFacts{Quality: "Remux-1080p", Revision: 1, ReleaseGroup: "GRP", SceneName: "Movie.2001.1080p.WEB-DL.H.264-FILE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AggregateMovieFile(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}
