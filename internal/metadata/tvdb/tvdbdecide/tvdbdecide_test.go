package tvdbdecide

import (
	"context"
	"errors"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/policy"
)

type fake struct {
	show   *anime.Show
	series *tvdb.Series
	err    error
	calls  int
}

func (f *fake) Show(context.Context, int) (*anime.Show, error) { f.calls++; return f.show, f.err }
func (f *fake) Series(context.Context, int) (*tvdb.Series, error) {
	f.calls++
	return f.series, f.err
}

func p[T any](v T) *T { return &v }

func TestFill(t *testing.T) {
	ctx := context.Background()
	rt := 24
	f := &fake{
		show: &anime.Show{TVDBID: 1, Episodes: []anime.ShowEpisode{
			{SeasonNumber: 0, EpisodeNumber: 1, Title: p("Special")},
			{SeasonNumber: 1, EpisodeNumber: 1, AbsoluteEpisodeNumber: p(1), Title: p("One"), AirDate: p("2020-01-05"), Runtime: p(24)},
		}},
		series: &tvdb.Series{Name: "テスト", Year: "2020", AverageRuntime: &rt, Aliases: []tvdb.Alias{{Language: "eng", Name: "Test Show"}},
			Translations: &tvdb.Translations{NameTranslations: []tvdb.Translation{{Language: "eng", Name: "Test"}, {Language: "eng", Name: "test"}}}},
	}
	ti := &decide.Title{Kind: policy.KindSeries, TVDB: 1, Monitored: true}
	changed, err := Fill(ctx, f, ti)
	if err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	if len(ti.Episodes) != 2 || ti.Episodes[1] != (decide.Episode{Season: 1, Number: 1, Absolute: 1, Title: "One",
		AirDate: "2020-01-05", Runtime: 24, Monitored: true}) || ti.Episodes[0].Absolute != 0 {
		t.Errorf("episodes %+v", ti.Episodes)
	}
	if ti.Name != "Test" || ti.Year != 2020 || ti.Runtime != 24 {
		t.Errorf("title %+v", ti)
	}
	if got := ti.Aliases; len(got) != 2 || got[0] != "テスト" || got[1] != "Test Show" {
		t.Errorf("aliases %q", got)
	}

	// a title with episodes and names is left alone; movies and titles without a TVDB id too
	f.calls = 0
	for _, x := range []*decide.Title{
		{Kind: policy.KindSeries, TVDB: 1, Name: "A", Year: 2020, Aliases: []string{"B"}, Episodes: []decide.Episode{{Season: 1, Number: 1}}},
		{Kind: policy.KindMovie, TVDB: 1},
		{Kind: policy.KindSeries},
	} {
		if changed, err := Fill(ctx, f, x); changed || err != nil {
			t.Errorf("%+v: %v %v", x, changed, err)
		}
	}
	if f.calls != 0 {
		t.Errorf("%d calls", f.calls)
	}

	// TVDB down: the error comes back and the title is unchanged
	f.err = errors.New("down")
	x := &decide.Title{Kind: policy.KindSeries, TVDB: 1}
	if changed, err := Fill(ctx, f, x); changed || err == nil || x.Episodes != nil {
		t.Errorf("down: %v %v", changed, err)
	}
}
