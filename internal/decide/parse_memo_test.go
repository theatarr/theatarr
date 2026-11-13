package decide

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
)

func TestReleaseParseMemoCopies(t *testing.T) {
	t.Parallel()
	ctx := WithParseMemo(context.Background())
	movie := "Dune.Part.Two.2024.FRENCH.1080p.WEB-DL-OLD"
	want := radarr.ParseMovieTitle(movie, false)
	p := ParseMovieRelease(ctx, movie)
	p.MovieTitles[0] = "Changed"
	p.Languages[0] = radarr.LanguageEnglish
	*p.ReleaseGroup = "Manual"
	p.Quality.Revision.Version++
	if got := ParseMovieRelease(ctx, movie); !reflect.DeepEqual(got, want) {
		t.Fatalf("movie parse was shared: %+v, want %+v", got, want)
	}
	title := "The.Show.S01E01E02.1080p.WEB-DL-GRP"
	wantSeries := sonarr.ParseTitle(title)
	ps := ParseSeriesRelease(ctx, title)
	ps.EpisodeNumbers[0] = 99
	ps.SeriesTitleInfo.Title = "Changed"
	*ps.ReleaseGroup = "Manual"
	ps.Quality.Revision.Version++
	if got := ParseSeriesRelease(ctx, title); !reflect.DeepEqual(got, wantSeries) {
		t.Fatalf("series parse was shared: %+v, want %+v", got, wantSeries)
	}
}

func TestReleaseParseMemoAvoidsRepeatedParserWork(t *testing.T) {
	ctx := WithParseMemo(context.Background())
	title := "The.Show.S01E01E02.1080p.WEB-DL-GRP"
	ParseSeriesRelease(ctx, title)
	if n := testing.AllocsPerRun(10, func() { ParseSeriesRelease(ctx, title) }); n > 30 {
		t.Fatalf("%.0f allocations for a repeated parse; expected only an independent copy", n)
	}
}

func TestReleaseParseMemoManualOverridesAndScope(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	ctx := WithParseMemo(context.Background())
	if WithParseMemo(ctx) != ctx || releaseParseMemo(WithSearch(ctx)) != releaseParseMemo(ctx) {
		t.Fatal("nested search lost its memo")
	}
	if releaseParseMemo(WithParseMemo(context.Background())) == releaseParseMemo(ctx) {
		t.Fatal("independent searches share a memo")
	}
	r := rel("Dune.Part.Two.2024.1080p.WEB-DL-OLD", "public", 20*gb)
	group := "MANUAL"
	r.Manual = &ManualOverrides{ReleaseGroup: &group}
	if d, err := e.Decide(ctx, r, dune, Version{Key: "movie-remux"}, nil); err != nil || d.ReleaseGroup != group {
		t.Fatalf("manual decision: %+v, %v", d, err)
	}
	r.Manual = nil
	if d, err := e.Decide(ctx, r, dune, Version{Key: "movie-remux"}, nil); err != nil || d.ReleaseGroup != "OLD" {
		t.Fatalf("manual group leaked into another decision: %+v, %v", d, err)
	}
	for range 2 {
		if ParseMovieRelease(ctx, "123") != nil || ParseSeriesRelease(ctx, "123") != nil {
			t.Fatal("unparseable release unexpectedly parsed")
		}
	}
	if p, ok := releaseParseMemo(ctx).movies["123"]; !ok || p != nil {
		t.Fatal("nil movie parse was not memoized")
	}
	if p, ok := releaseParseMemo(ctx).series["123"]; !ok || p != nil {
		t.Fatal("nil series parse was not memoized")
	}
}

func TestReleaseParseMemoDecisionsMatchUncached(t *testing.T) {
	t.Parallel()
	e := seriesEngine(t)
	ctx := WithParseMemo(context.Background())
	v := tvVersion(e, "tv")
	for _, release := range []string{
		"The.Show.S01E01E02.1080p.WEB-DL-GRP", "Show.Alt.Name.S01.2160p.BluRay.REMUX-GRP",
		"The Show - Special Name (1080p)", "123",
	} {
		for _, search := range []*SeriesSearch{nil, {Season: 1, Episode: 1}, {Season: 1}} {
			r := rel(release, "public", 20*gb)
			want, err := e.DecideSearch(context.Background(), r, show(), v, search)
			if err != nil {
				t.Fatal(err)
			}
			got, err := e.DecideSearch(ctx, r, show(), v, search)
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, _ := json.Marshal(want)
			gotJSON, _ := json.Marshal(got)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("%s: cached %s, uncached %s", release, gotJSON, wantJSON)
			}
		}
	}
}

var movieParseResult *radarr.ParsedMovieInfo
var seriesParseResult *sonarr.ParsedEpisodeInfo

// BenchmarkSearchParse models 300 releases, three versions and two criteria per version. Read the release
// fixture from a scratch SQLite database, then time only parsing; every iteration starts a fresh search memo.
func BenchmarkSearchParse(b *testing.B) {
	dir, err := os.MkdirTemp("/tmp", "cx-opt6-parse-")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { os.RemoveAll(dir) })
	db, err := sql.Open("sqlite", filepath.Join(dir, "releases.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE release (movie TEXT, series TEXT)`); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		if _, err := db.Exec(`INSERT INTO release VALUES (?,?)`, fmt.Sprintf("Film.%03d.2024.1080p.WEB-DL-GRP", i),
			fmt.Sprintf("The.Show.S01E%03d.1080p.WEB-DL-GRP", i+1)); err != nil {
			b.Fatal(err)
		}
	}
	var movies, series []string
	rows, err := db.Query(`SELECT movie, series FROM release`)
	if err != nil {
		b.Fatal(err)
	}
	for rows.Next() {
		var movie, episode string
		if err := rows.Scan(&movie, &episode); err != nil {
			b.Fatal(err)
		}
		movies, series = append(movies, movie), append(series, episode)
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
	}
	rows.Close()
	for _, kind := range []string{"movie", "series"} {
		b.Run(kind, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ctx := WithParseMemo(context.Background())
				for range 6 {
					for i := range movies {
						if kind == "movie" {
							movieParseResult = ParseMovieRelease(ctx, movies[i])
						} else {
							seriesParseResult = ParseSeriesRelease(ctx, series[i])
						}
					}
				}
			}
			b.ReportMetric(1800, "decisions/search")
		})
	}
}
