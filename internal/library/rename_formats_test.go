package library

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

// onlyHDRWhenRenaming stands in for cmd/theatarr's matchedRenameFormats: only "HDR" has include-when-renaming.
func onlyHDRWhenRenaming(_ context.Context, _ string, names []string) []string {
	var out []string
	for _, n := range names {
		if n == "HDR" {
			out = append(out, n)
		}
	}
	return out
}

const storedFormats = `[{"name":"HDR","score":500},{"name":"Remux Tier 01","score":1900}]`

// A file named by import gets no rename proposal right after import: the rename preview builds {Custom Formats} from
// the stored formats through the same include-when-renaming filter, so a scoring-only format stays out of the name.
func TestRenamePreviewKeepsIncludeWhenRenaming(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie-remux"].Naming.File = "{Movie CleanTitle} {(Release Year)} {[Custom Formats]}{[Quality Full]}"
	e.lib.matched = onlyHDRWhenRenaming
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	e.exec(`UPDATE download SET formats = ? WHERE id = 1`, storedFormats)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 {
		t.Fatalf("links %+v rejected %+v", plan.Links, plan.Rejected)
	}
	if p := plan.Links[0].Path; strings.Contains(p, "Remux Tier 01") || !strings.Contains(p, "[HDR]") {
		t.Fatalf("import named it %s", p)
	}
	p, err := e.lib.PreviewRename(e.ctx, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 0 {
		t.Fatalf("rename right after import proposes %+v, want none", p.Files)
	}
}

// Reassigning episodes names the file with the same include-when-renaming filter as import.
func TestReassignKeepsIncludeWhenRenaming(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["tv"].Naming.File = "{Series Title} - S{season:00}E{episode:00} {[Custom Formats]}"
	e.lib.matched = onlyHDRWhenRenaming
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (203, 2, 1, 3, 'Third', 0, 0)`)
	rel := "Show.S01.2160p.BluRay.REMUX.HEVC-GRP"
	e.release(rel, map[string]float64{"Show.S01E01.2160p.BluRay.REMUX.HEVC-GRP.mkv": 2700,
		"Show.S01E02.2160p.BluRay.REMUX.HEVC-GRP.mkv": 2700})
	e.seriesDownload(2, 2, 20, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", rel, "[201, 202]", nil)
	e.exec(`UPDATE download SET quality = 'Bluray-2160p Remux', formats = ? WHERE id = 2`, storedFormats)
	if _, err := e.lib.Import(e.ctx, 2, false); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES (20, 203, 1, 'wanted', 0)`)
	old := e.one(`SELECT l.path FROM link l JOIN link_episode le ON le.link_id = l.id WHERE le.episode_id = 201`)
	if !strings.HasSuffix(old, "S01E01 [HDR].mkv") {
		t.Fatalf("import named it %s", old)
	}
	r, err := e.lib.ReassignEpisodes(e.ctx, e.linkOf(old), []int64{203}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(r.Path, "S01E03 [HDR].mkv") {
		t.Fatalf("reassign named it %s", r.Path)
	}
}

// An import without formats saved on the download names the file from the formats its metadata keeps (decided with
// the release's size), so a size-based format is in the name and a rename right after import proposes nothing.
func TestRenamePreviewAfterImportWithoutStoredFormats(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	body := `{"name":"Big","specifications":[{"name":"s","implementation":"SizeSpecification","negate":false,
		"required":true,"fields":{"min":10,"max":1000}}]}`
	if err := os.WriteFile(filepath.Join(e.dir, "big.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	e.pol.Dir = e.dir
	e.pol.Formats = map[string]*policy.FormatRef{"Big": {File: "big.json"}}
	for _, p := range e.pol.Profiles {
		p.Formats = map[string]int{"Big": 10}
	}
	engine, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.lib.engine = engine
	e.pol.Versions["movie-remux"].Naming.File = "{Movie CleanTitle} {(Release Year)} {[Custom Formats]}"
	// As cmd/theatarr: the title-alone decision cannot see the size; every matched format is included when renaming.
	e.lib.formats = func(context.Context, string, int64, string) []string { return nil }
	e.lib.matched = func(_ context.Context, _ string, names []string) []string { return names }
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	e.exec(`UPDATE download SET size_bytes = ?, release_data = '{"imdb":"tt0211915"}' WHERE id = 1`, int64(50)<<30)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 || !strings.Contains(plan.Links[0].Path, "[Big]") {
		t.Fatalf("links %+v rejected %+v", plan.Links, plan.Rejected)
	}
	p, err := e.lib.PreviewRename(e.ctx, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 0 {
		t.Fatalf("rename right after import proposes %+v, want none", p.Files)
	}
}
