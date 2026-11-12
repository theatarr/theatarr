package rss

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
)

func TestMixedCachePagesRequestAccounting(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	items := filler(205, 0)
	e.prow.setRSS(items...)
	svc, err := indexer.New(e.pol, e.gw, indexer.Options{Logger: e.log, CacheTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	e.build(nil)
	q := indexer.Query{Type: indexer.QueryRSS, Categories: e.r.rssCategories("prowlarr"), RSSLimit: 100}
	if _, err = svc.Search(e.ctx, "prowlarr", q); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO shadow_rss_cursor (indexer,item_key,updated_at) VALUES ('prowlarr',?,?)`, "h:"+items[150].hash, e.clock().UnixMilli())
	before := len(e.net.requests("prowlarr.test"))
	sum, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	actual := len(e.net.requests("prowlarr.test")) - before
	for _, ix := range sum.Indexers {
		if ix.Indexer == "prowlarr" {
			if ix.Requests != actual {
				t.Fatalf("reported %d requests, sent %d: %+v", ix.Requests, actual, ix)
			}
			return
		}
	}
	t.Fatal("no prowlarr result")
}
