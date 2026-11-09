package policy

import (
	"testing"
	"time"
)

func TestAnimeBuildSettingsDefaultsAndMinimum(t *testing.T) {
	t.Parallel()
	p := &Policy{}
	if s := p.AnimeBuildSettings(); !s.AutoBuild || s.RefreshInterval != DefaultAnimeRefreshInterval {
		t.Fatalf("defaults %+v", s)
	}
	off := false
	p.Metadata = &Metadata{Anime: &AnimeBuild{AutoBuild: &off, RefreshInterval: Duration(48 * time.Hour)}}
	if s := p.AnimeBuildSettings(); s.AutoBuild || s.RefreshInterval != 48*time.Hour {
		t.Fatalf("configured %+v", s)
	}
	p.Metadata.Anime.RefreshInterval = Duration(time.Hour)
	e := &Errors{}
	p.validateAnimeBuild(e)
	if err := e.err(); err == nil {
		t.Fatal("short refresh interval accepted")
	}
	p.Metadata.Anime.RefreshInterval = Duration(MinAnimeRefreshInterval)
	e = &Errors{}
	p.validateAnimeBuild(e)
	if err := e.err(); err != nil {
		t.Fatal(err)
	}
}
