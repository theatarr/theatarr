package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// An OVA larger than episode 01 cannot take that episode's slot in an entry-local batch.
func TestImportFranchiseBatchKeepsOVAOutOfRegularSlot(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.isekai()
	rel := "[Other] Isekai Monogatari Season 2 (BD 1080p)"
	e.release(rel, map[string]float64{"[Other] Isekai Monogatari - 01 (BD 1080p).mkv": 1440,
		"[Other] Isekai Monogatari - 02 (BD 1080p).mkv":     1440,
		"[Other] Isekai Monogatari - OVA 01 (BD 1080p).mkv": 1800})
	e.videoSize(rel, 1920, 1080)
	ova := filepath.Join(e.mount, "__all__", rel, "[Other] Isekai Monogatari - OVA 01 (BD 1080p).mkv")
	if err := os.WriteFile(ova, []byte(strings.Repeat("v", 64)), 0o644); err != nil {
		t.Fatal(err)
	}
	e.seriesDownload(1, 3, 30, hashA, rel, "[313, 314]", `{"how":"franchise","entry":"al:2"}`)
	plan, err := e.lib.Import(e.ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	got := e.links(plan)
	t.Logf("links:\n%s\nunmatched %v unresolved %v", got, plan.Unmatched, plan.Unresolved)
	if strings.Contains(got, "OVA 01") {
		t.Errorf("the OVA is linked as a regular episode:\n%s", got)
	}
	if !strings.Contains(got, "S02E01 - Episode Bluray-1080p.mkv <- [Other] Isekai Monogatari - 01 (BD 1080p).mkv") {
		t.Errorf("episode 01 not linked as S02E01:\n%s", got)
	}
}

func TestImportAltCutTableGapStaysUnmatched(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.isekai()
	e.exec(`INSERT INTO anime_alt_cut (franchise, name, tvdb_series, episodes) VALUES ('isekai', 'Director''s Cut', 2002, '{"1":[[1,1],[1,2]],"2":[[1,3]]}')`)
	rel := "[Group] Isekai Story Directors Cut (BD 1080p)"
	file := "[Group] Isekai Story Directors Cut - 03 (BD 1080p).mkv"
	e.release(rel, map[string]float64{file: 1440})
	e.videoSize(rel, 1920, 1080)
	e.seriesDownload(1, 3, 30, hashA, rel, "[301,302,303]", `{"how":"alt_cut:Director's Cut"}`)
	plan, err := e.lib.Import(e.ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 0 || len(plan.Unmatched) != 1 || plan.Unresolved[file] == "" {
		t.Fatalf("cut gap fell through to canonical numbering: links %s unmatched %v unresolved %v", e.links(plan), plan.Unmatched, plan.Unresolved)
	}
}
