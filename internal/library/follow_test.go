package library

import (
	"testing"
)

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
