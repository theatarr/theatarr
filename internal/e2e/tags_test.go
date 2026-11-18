package e2e

import (
	"slices"
	"testing"

	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/web"
)

// TestTagEditReachesWebhooks: a label put on a title in Theatarr (POST /titles/tags) is on the title in the views
// that hold its copies, so a Connect webhook limited to that tag gets the title's next event; taken off, it does not.
func TestTagEditReachesWebhooks(t *testing.T) {
	t.Parallel()
	e := newEnvironment(t, nil)
	id := e.addMovie(true, "movie")
	tags := func() [][]int64 {
		t.Helper()
		ps, err := e.av.Payloads(e.ctx, notify.Event{Kind: notify.Delete, TitleID: id, Version: "movie"})
		must(t, err)
		if len(ps) == 0 {
			t.Fatal("no payload for the title's event")
		}
		var out [][]int64
		for _, p := range ps {
			out = append(out, p.Tags)
		}
		return out
	}
	var res web.TitleTagsResult
	e.request("POST", web.APIBase+"/titles/tags", web.TitleTagsRequest{TitleIDs: []int64{id}, Add: []string{"kids"}}, &res)
	if res.Changed != 1 || !slices.Equal(res.Tags, []string{"kids"}) {
		t.Fatalf("add: %+v", res)
	}
	tid := int64(e.count(`SELECT id FROM arrview_tag WHERE label = 'kids'`))
	for _, ts := range tags() {
		if !slices.Contains(ts, tid) {
			t.Errorf("payload tags %v, want %d", ts, tid)
		}
	}
	e.request("POST", web.APIBase+"/titles/tags", web.TitleTagsRequest{TitleIDs: []int64{id}, Remove: []string{"kids"}}, &res)
	for _, ts := range tags() {
		if slices.Contains(ts, tid) {
			t.Errorf("after remove: payload tags %v", ts)
		}
	}
}
