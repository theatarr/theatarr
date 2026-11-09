package router

import (
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// The selection on Real-Debrid is every playable file (DMM's isVideo set): videos and the audio beside them, never
// an nfo, text, image, subtitle or executable, samples and trailers only when nothing else is a video; a movie gets
// the same set as a series, and AltFiles is its largest video alone.
func TestPlayable(t *testing.T) {
	t.Parallel()
	files := []provider.File{
		{ID: "1", Path: "/RARBG.com.mp4", Bytes: 10},
		{ID: "2", Path: "/Movie/Movie.2024.mkv", Bytes: 9000},
		{ID: "3", Path: "/Movie/Featurettes/Making.Of.mp4", Bytes: 800},
		{ID: "4", Path: "/Movie/Movie.2024.sample.mkv", Bytes: 50},
		{ID: "5", Path: "/Movie/Trailer/Movie.Trailer.mkv", Bytes: 60},
		{ID: "6", Path: "/Movie/Movie.2024.nfo", Bytes: 1},
		{ID: "7", Path: "/Movie/RARBG.txt", Bytes: 1},
		{ID: "8", Path: "/Movie/poster.jpg", Bytes: 2},
		{ID: "9", Path: "/Movie/Subs/eng.srt", Bytes: 3},
		{ID: "10", Path: "/Movie/setup.exe", Bytes: 4},
		{ID: "11", Path: "/Movie/OST/01.flac", Bytes: 30},
		{ID: "12", Path: "/Movie/disc.iso", Bytes: 99999},
		{ID: "13", Path: "/Movie/VIDEO_TS/VTS_01_1.VOB", Bytes: 999},
		{ID: "", Path: "/Movie/noid.mkv", Bytes: 1},
		{ID: "14", Path: "/Movie/Movie_sample_scene.MKV", Bytes: 70}, // "_" joins words: not a sample to DMM
	}
	want := "2,3,11,14"
	if got := strings.Join(ChooseFiles(policy.KindMovie, files), ","); got != want {
		t.Errorf("movie: %s, want %s", got, want)
	}
	if got := strings.Join(ChooseFiles(policy.KindSeries, files), ","); got != want {
		t.Errorf("series: %s, want %s", got, want)
	}
	if got := strings.Join(AltFiles(policy.KindMovie, files), ","); got != "2" {
		t.Errorf("alternative: %s", got)
	}
	if got := AltFiles(policy.KindSeries, files); got != nil {
		t.Errorf("a series has no alternative: %v", got)
	}
	// One playable file: the alternative would be the same set.
	if got := AltFiles(policy.KindMovie, files[1:2]); got != nil {
		t.Errorf("single file: %v", got)
	}
	// Only samples and trailers: they are what there is.
	if got := strings.Join(PlayableIDs(files[3:5]), ","); got != "4,5" {
		t.Errorf("samples only: %s", got)
	}
	// Audio, an image and an nfo: no video, nothing to select (the caller fails it or leaves it to the person).
	if got := PlayableIDs([]provider.File{files[5], files[7], files[10], files[11]}); got != nil {
		t.Errorf("no video: %v", got)
	}
	if !SameFiles([]string{"1", "2"}, []string{"2", "1"}) || SameFiles([]string{"1"}, []string{"1", "2"}) ||
		SameFiles([]string{"1", "1"}, []string{"1", "2"}) {
		t.Error("SameFiles")
	}
}
