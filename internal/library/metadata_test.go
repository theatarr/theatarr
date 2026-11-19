package library

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
)

func TestImportRecordsFileMetadata(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	release := "Amelie.2001.REPACK.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-GRP"
	e.release(release, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, release, release, "[]")
	e.exec(`UPDATE download SET release_data = '{"languages":["French"],"flags":["internal"]}',
		formats = '[{"name":"HDR","score":100}]', size_bytes = 9000 WHERE id = 1`)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	// The file's own languages (its probed English audio, as the arrs' media info augmenter) win over the release's.
	var f decide.ExistingFile
	if err := json.Unmarshal([]byte(e.one(`SELECT file_metadata FROM link WHERE state = 'active'`)), &f); err != nil {
		t.Fatal(err)
	}
	if f.Quality != "Remux-2160p" || f.Revision != 2 || f.SceneName != release || f.ReleaseGroup != "GRP" ||
		!reflect.DeepEqual(f.Languages, []string{"English"}) || f.Size != 5 || f.InfoHash != hashA || f.IndexerFlags == 0 ||
		f.Score == nil || *f.Score != 100 || len(f.Formats) != 1 || f.Formats[0].Name != "HDR" ||
		f.RelativePath != "movie.mkv" {
		t.Fatalf("imported metadata: %+v", f)
	}
	// Later download changes and renamed files cannot change what the decision engine sees on disk.
	e.exec(`UPDATE download SET release_title = 'wrong', release_data = '{}', score = 0, formats = '[]' WHERE id = 1`)
	got, err := decide.LoadExisting(e.ctx, e.st.Reader(), 10)
	if err != nil || got == nil || got.SceneName != f.SceneName || got.Revision != f.Revision ||
		!reflect.DeepEqual(got.Languages, f.Languages) || got.Size != f.Size || got.Score == nil || *got.Score != 100 {
		t.Fatalf("loaded snapshot: %+v, %v", got, err)
	}
}

// A scored import whose release matched no format stores an empty format list, so a score of 0 reads as a real
// score (the Libraries table's linkScoreSQL); a file never scored (an adopted one) stores none.
func TestImportRecordsZeroScore(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	release := "Amelie.2001.2160p.UHD.BluRay.REMUX.HEVC-GRP"
	e.release(release, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, release, release, "[]")
	e.exec(`UPDATE download SET formats = '[]', score = 0 WHERE id = 1`)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT score || ' ' || COALESCE(json_type(file_metadata, '$.Formats'), 'none') FROM link
		WHERE state = 'active'`); got != "0 array" {
		t.Errorf("zero-format import: %s, want 0 array", got)
	}
	b, err := json.Marshal(&decide.ExistingFile{Quality: "Remux-2160p"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["Formats"]; ok {
		t.Errorf("unscored file stores formats: %s", b)
	}
}
