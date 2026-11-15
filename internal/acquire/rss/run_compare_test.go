package rss

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Two runs over a mixed feed (episodes and a pack of one series by name, scene name and TVDB-less titles, films by
// IMDb id and by name, unknown items, a repeated item), the second with the first's items seen and new ones added:
// what each run decides, routes and remembers is the golden file, as the per-item loads made it.
func TestRSSRunsGolden(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.seed(t)
	e.seedShow(t)
	e.prow.setRSS(
		item{title: "Show.Alt.S01E03.2160p.WEB-DL-GRP", hash: hShowE03, size: 8e9, pub: t0, tv: true},
		item{title: "The.Show.S01E02.2160p.WEB-DL-GRP", hash: "9999999999999999999999999999999999999999", size: 8e9, pub: t0.Add(-time.Minute), tv: true},
		item{title: showPack, hash: hShowPack, size: 24e9, pub: t0.Add(-2 * time.Minute), tv: true},
		item{title: "Unknown.Series.S02E01.1080p.WEB-DL-X", hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", size: 2e9, pub: t0.Add(-3 * time.Minute), tv: true},
		item{title: "Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos-FRAME", hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", size: 70e9, pub: t0.Add(-4 * time.Minute), imdb: dune.imdb},
		item{title: "Wonka.2023.1080p.WEB-DL.DDP5.1.H.264-FLUX", hash: "cccccccccccccccccccccccccccccccccccccccc", size: 6e9, pub: t0.Add(-5 * time.Minute)},
		item{title: "Some.Other.Film.2021.1080p.WEB-DL-X", hash: "dddddddddddddddddddddddddddddddddddddddd", size: 5e9, pub: t0.Add(-6 * time.Minute)},
		item{title: "Wonka.2023.1080p.WEB-DL.DDP5.1.H.264-FLUX", hash: "cccccccccccccccccccccccccccccccccccccccc", size: 6e9, pub: t0.Add(-7 * time.Minute)},
	)
	var out strings.Builder
	run := func(label string) {
		sum, err := e.r.RSS(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		sum.TookMS = 0
		b, _ := json.Marshal(sum)
		out.WriteString(label + " summary " + string(b) + "\n")
		for _, c := range e.rec.candidates() {
			out.WriteString(label + " routed " + c + "\n")
		}
		rows, err := e.st.Reader().QueryContext(e.ctx, `SELECT indexer, item_key FROM shadow_rss_seen ORDER BY indexer, item_key`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var ik, k string
			if err := rows.Scan(&ik, &k); err != nil {
				t.Fatal(err)
			}
			out.WriteString(label + " seen " + ik + " " + k + "\n")
		}
		rows.Close()
	}
	run("first")
	e.advance(15 * time.Minute)
	e.prow.setRSS(
		item{title: "Argylle.2024.2160p.WEB-DL.DDP5.1.Atmos.DV.H.265-FLUX", hash: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", size: 20e9, pub: t0.Add(10 * time.Minute), imdb: argylle.imdb},
		item{title: "Show.Alt.S01E03.2160p.WEB-DL-GRP", hash: hShowE03, size: 8e9, pub: t0, tv: true},
		item{title: "Some.Other.Film.2021.1080p.WEB-DL-X", hash: "dddddddddddddddddddddddddddddddddddddddd", size: 5e9, pub: t0.Add(-6 * time.Minute)},
		item{title: "Wonka.2023.1080p.WEB-DL.DDP5.1.H.264-FLUX", hash: "cccccccccccccccccccccccccccccccccccccccc", size: 6e9, pub: t0.Add(-7 * time.Minute)},
	)
	run("second")
	golden := filepath.Join("testdata", "rss_runs.golden")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, []byte(out.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != out.String() {
		t.Fatalf("runs differ from %s:\n%s", golden, out.String())
	}
}
