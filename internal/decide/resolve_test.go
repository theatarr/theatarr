package decide

import (
	"fmt"
	"testing"
)

// TestResolveFile: an imported file maps to the episodes the decision would have mapped it to: franchise entries,
// the decision's stored mapping, alternate cuts, scene numbering, daily air dates, specials by title, and the files
// of a mixed pack.
func TestResolveFile(t *testing.T) {
	t.Parallel()
	scene := show()
	scene.UseSceneNumbering = true
	// TVDB S01E03 is scene S02E01 (the scene splits the first season after two episodes).
	ep(&scene, 101).SceneSeason, ep(&scene, 101).SceneNumber = ip(1), ip(1)
	ep(&scene, 102).SceneSeason, ep(&scene, 102).SceneNumber = ip(1), ip(2)
	ep(&scene, 103).SceneSeason, ep(&scene, 103).SceneNumber = ip(2), ip(1)
	daily := show()
	daily.SeriesType = "daily"
	franchise := animeMapped("auto")
	cases := []struct {
		name string
		t    Title
		in   FileInput
		want string
	}{
		{"canonical", show(), FileInput{Path: "The.Show.S01E02.1080p.WEB-DL-GRP.mkv", OtherVideoFiles: true}, "[102] file"},
		{"multi-episode file", show(), FileInput{Path: "The.Show.S01E01E02.1080p.WEB-DL-GRP.mkv", OtherVideoFiles: true}, "[101 102] file"},
		{"special by title", show(), FileInput{Path: "The.Show.The.Making.Of.1080p.WEB-DL-GRP.mkv", OtherVideoFiles: true}, "[100] special"},
		{"special numbered", show(), FileInput{Path: "Extras/The.Show.S00E01.1080p.WEB-DL-GRP.mkv", OtherVideoFiles: true}, "[100] file"},
		{"scene numbering", scene, FileInput{Path: "The.Show.S02E01.1080p.WEB-DL-GRP.mkv", OtherVideoFiles: true}, "[103] file"},
		{"daily by air date", daily, FileInput{Path: "The.Show.2020.04.08.Guest.1080p.WEB-DL-GRP.mkv"}, "[102] file"},
		{"daily date on a standard series", show(), FileInput{Path: "The.Show.2020.04.08.Guest.1080p.WEB-DL-GRP.mkv"}, "[] "},
		{"release title for a single obfuscated file", show(), FileInput{Path: "abc123.mkv",
			Release: "The.Show.S01E03.1080p.WEB-DL-GRP"}, "[103] release"},
		{"entry-local number", franchise, FileInput{Path: "[Group] Isekai Story 2nd Season - 01 (1080p).mkv",
			OtherVideoFiles: true}, "[313] file+franchise:al:2"},
		{"entry's own numbering", franchise, FileInput{Path: "[Group] Isekai Story - 03 (BD 1080p).mkv",
			OtherVideoFiles: true}, "[303] file+franchise:al:1"},
		{"decision's mapping for a file named by an alias", franchise, FileInput{Path: "[Group] Isekai Monogatari - 01 (1080p).mkv",
			OtherVideoFiles: true, Mapping: &EpisodeMap{How: AnimeMapFranchise, Entry: "al:2"}}, "[313] file+franchise:al:2"},
		{"without the decision's mapping", franchise, FileInput{Path: "[Group] Isekai Monogatari - 01 (1080p).mkv",
			OtherVideoFiles: true}, "[301] file"},
		{"alternate cut named by the release", franchise, FileInput{Path: "[Group] Isekai Story - 01 (BD 1080p).mkv",
			Release: "[Group] Isekai Story Directors Cut (BD 1080p)", OtherVideoFiles: true}, "[301 302] file+alt_cut:Director's Cut"},
		{"alternate cut from the decision", franchise, FileInput{Path: "[Group] Isekai Monogatari - 02 (BD 1080p).mkv",
			OtherVideoFiles: true, Mapping: &EpisodeMap{How: "alt_cut:Director's Cut", Entry: "kitsu:9"}}, "[303] file+alt_cut:Director's Cut"},
		{"unparseable", show(), FileInput{Path: "random.mkv", OtherVideoFiles: true}, "[] "},
	}
	for _, c := range cases {
		got, err := ResolveFile(&c.t, "", c.in)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if s := fmt.Sprintf("%v %s", got.EpisodeIDs, got.How); s != c.want {
			t.Errorf("%s: %s, want %s", c.name, s, c.want)
		}
	}
}
