package library

import (
	"path/filepath"
	"testing"
)

func TestCheckNamesUsesSourceSeriesType(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	e.pol.Versions["tv"].Naming.Anime = "{Series Title} - S{season:00}E{episode:00} [{Quality Full}] Anime"
	// The title's type names the files, not the owning arr's.
	e.exec(`UPDATE title SET series_type = 'anime' WHERE id = 2`)
	e.exec(`UPDATE arr_title SET series_type = 'standard' WHERE instance = 'sonarr'`)
	p := filepath.Join(e.shows, "Show", "Season 01", "Show - S01E01 [WEBDL-1080p] Anime.mkv")
	e.arrFile(1, "sonarr", 20, 2, p, "", "WEBDL-1080p", 201)
	nc := e.lib.checkNames(e.ctx, "tv", e.shows)
	if nc.Error != "" || nc.Shaped != 1 || nc.Equal != 1 {
		t.Errorf("source naming: %+v", nc)
	}
}

func TestLoadEpisodesSkipsRemovedExceptExplicit(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	e.exec(`UPDATE episode SET removed_at = 1 WHERE id = 202`)
	for _, tc := range []struct {
		ids  []int64
		want int
	}{{nil, 1}, {[]int64{202}, 1}} {
		eps, err := loadEpisodes(e.ctx, e.st.Reader(), 2, tc.ids)
		if err != nil || len(eps) != tc.want {
			t.Errorf("loadEpisodes(%v): %+v, %v", tc.ids, eps, err)
		}
	}
}
