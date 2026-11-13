package decide

import (
	"fmt"
	"testing"
)

// A stored mapping must preserve explicit season/episode numbers and leave specials outside regular entry slots.
func TestFranchiseFallbackSkipsSpecials(t *testing.T) {
	t.Parallel()
	ti := animeMapped("auto")
	ti.Episodes = append(ti.Episodes, Episode{ID: 399, Season: 0, Number: 1, Title: "OVA", Runtime: 24, Monitored: true})
	m := &EpisodeMap{How: AnimeMapFranchise, Entry: "al:2"}
	for _, name := range []string{
		"[Group] Isekai Story 2nd Season - OVA 01 (BD 1080p).mkv",
		"[Group] Isekai Story 2nd Season - Special 01 (BD 1080p).mkv",
		"[Group] Isekai Story 2nd Season - S00E01 (BD 1080p).mkv",
		"[Group] Isekai Monogatari - S01E02 (BD 1080p).mkv",
	} {
		without, err := ResolveFile(&ti, FileInput{Path: name, OtherVideoFiles: true})
		if err != nil {
			t.Fatal(err)
		}
		with, err := ResolveFile(&ti, FileInput{Path: name, OtherVideoFiles: true, Mapping: m})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: without mapping %v %s; with mapping %v %s", name, without.EpisodeIDs, without.How, with.EpisodeIDs, with.How)
		if fmt.Sprint(with.EpisodeIDs) != fmt.Sprint(without.EpisodeIDs) {
			t.Errorf("%s: mapped as S02E01 (the entry's regular episode 1) through the stored mapping (%s)", name, with.How)
		}
	}
}

// TestAltCutFallbackPreservesSpecial: the same through a stored alternate-cut mapping.
func TestAltCutFallbackPreservesSpecial(t *testing.T) {
	t.Parallel()
	ti := animeMapped("auto")
	ti.Episodes = append(ti.Episodes, Episode{ID: 399, Season: 0, Number: 1, Title: "OVA", Runtime: 24, Monitored: true})
	m := &EpisodeMap{How: "alt_cut:Director's Cut", Entry: "kitsu:9"}
	name := "[Group] Isekai Monogatari - S00E01 (BD 1080p).mkv"
	with, err := ResolveFile(&ti, FileInput{Path: name, OtherVideoFiles: true, Mapping: m})
	if err != nil {
		t.Fatal(err)
	}
	if s := fmt.Sprint(with.EpisodeIDs); s != "[399]" {
		t.Errorf("%s: %s %s, want [399] (the special it names)", name, s, with.How)
	}
}

func TestResolveFileAltCutGapIsRefused(t *testing.T) {
	t.Parallel()
	ti := animeMapped("auto")
	for _, in := range []FileInput{
		{Path: "[Group] Isekai Story Directors Cut - 03 (BD 1080p).mkv", OtherVideoFiles: true},
		{Path: "[Group] Isekai Monogatari - 03 (BD 1080p).mkv", OtherVideoFiles: true, Mapping: &EpisodeMap{How: "alt_cut:Director's Cut"}},
		{Path: "[Group] Isekai Monogatari - 01-03 (BD 1080p).mkv", OtherVideoFiles: true, Mapping: &EpisodeMap{How: "alt_cut:Director's Cut"}},
	} {
		got, err := ResolveFile(&ti, in)
		if err != nil || got.Refused == "" || len(got.EpisodeIDs) != 0 {
			t.Errorf("%s: %+v %v", in.Path, got, err)
		}
	}
}
