package arrview

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestFileAlternativesKeepOwnerWithoutDecodingDiscardedLanguages(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE version_file SET languages = '["French"]', media_info = '{"videoCodec":"h264"}' WHERE id = 1`)
	languages := `[` + strings.Repeat(`"English",`, 999) + `"English"]`
	for i := 0; i < 50; i++ {
		e.exec(`INSERT INTO version_file (version_id,title_id,instance,arr_file_id,path,languages,media_info)
			VALUES (1,1,'alternative',?,?,?,?)`, 1000+i, fmt.Sprintf("/alternative/%d.mkv", i), languages, `{"videoCodec":"discarded"}`)
	}
	v := &View{Flavour: Radarr, Versions: []string{"movie-remux"}}
	load := func() {
		entries, err := e.svc.load(context.Background(), v, []int64{1})
		if err != nil || len(entries) != 1 {
			t.Fatalf("entries: %+v, %v", entries, err)
		}
		_, f := entries[0].file()
		if f == nil || f.id != 1 || len(f.languages) != 1 || f.languages[0] != "French" || string(f.mediaInfo) != `{"videoCodec":"h264"}` {
			t.Fatalf("owning file: %+v", f)
		}
	}
	// On the pinned toolchain this fixture needs about 970 allocations after selection, and about 1,670 when the
	// discarded arrays are decoded too. Allow room for SQL scanning while catching that unnecessary decoding.
	n := testing.AllocsPerRun(1, load)
	t.Logf("allocations: %.0f", n)
	if n > 1300 {
		t.Fatalf("%.0f allocations loading one chosen file; discarded alternatives were decoded", n)
	}
}
