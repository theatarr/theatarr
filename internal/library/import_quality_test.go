package library

import (
	"github.com/theatarr/theatarr/internal/policy"
	"testing"
)

func TestFirstImportEnforcesAllowedFileQuality(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie-remux"].PreferSeaDex = true
	rel := "Amelie.2001.1080p.WEB-DL.DDP5.1.H.264-GRP"
	e.release(rel, map[string]float64{"Amelie.2001.720p.WEB-DL.H.264-FILE.mkv": 7000})
	e.videoSize(rel, 1280, 720)
	e.download(1, "movie-remux", 1, 10, rel, rel, "[]")
	plan, err := e.lib.Import(e.ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 0 {
		t.Fatalf("first import admitted disallowed file: %+v", plan.Links)
	}
}

func TestNonoverlappingImportEnforcesAllowedFileQuality(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, policy.ModeActive, "")
	e.pol.Versions["tv"].PreferSeaDex = true
	e.heldLink(1, "WEBDL-2160p", "Show.S01E01.2160p.WEB-DL-OLD", 201)
	pack := "Show.S01.1080p.WEB-DL-PACK"
	e.release(pack, map[string]float64{"Show.S01E02.720p.WEB-DL-FILE.mkv": 2700})
	e.videoSize(pack, 1280, 720)
	e.seriesDownload(1, 2, 20, hashA, pack, "[202]", nil)
	plan, err := e.lib.Import(e.ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 0 {
		t.Fatalf("nonoverlapping disallowed file imported: %+v", plan.Links)
	}
}
