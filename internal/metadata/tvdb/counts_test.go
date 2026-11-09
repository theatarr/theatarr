package tvdb

import (
	"context"
	"testing"
)

func TestEpisodeCounts(t *testing.T) {
	f := newFake(t)
	c, _, _ := newClient(t, f)
	m, err := c.EpisodeCounts(context.Background(), 900001)
	if err != nil || len(m) != 3 || m[0] != 2 || m[1] != 2 || m[2] != 2 {
		t.Errorf("counts %v err %v (both pages read)", m, err)
	}
	if _, err := c.EpisodeCounts(context.Background(), 0); err == nil {
		t.Error("id 0 accepted")
	}
}
