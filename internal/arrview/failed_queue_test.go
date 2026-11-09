package arrview

import (
	"testing"
	"time"
)

// Regression: a stall the router already replaced (re-searched and imported) stays in the arr queue as an
// error until someone DELETEs it; Homepage's queue count and queue/status errors never clear on their own.
func TestStalledFailureOutlivesReplacement(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	old := ms(e.now.Add(-30 * 24 * time.Hour))
	e.exec(`INSERT INTO download(id,title_id,version_id,version_key,info_hash,release_title,indexer,episodes,state,error,created_at,updated_at,added_at)
		VALUES(1,1,2,'movie-web','h1','Alpha.Stalled','dmm','[]','failed','stalled: not ready 1h after the add (provider status queued)',?,?,?)`, old, old, old)
	e.exec(`INSERT INTO download(id,title_id,version_id,version_key,info_hash,release_title,indexer,episodes,state,created_at,updated_at,added_at)
		VALUES(2,1,2,'movie-web','h2','Alpha.Good','dmm','[]','imported',?,?,?)`, old, ms(e.now), old)
	var q struct{ TotalRecords int }
	e.call("GET", "/radarr/api/v3/queue", nil, 200, &q)
	var status queueStatusResource
	e.call("GET", "/radarr/api/v3/queue/status", nil, 200, &status)
	if q.TotalRecords != 0 || status.Errors {
		t.Fatalf("a month-old stall replaced by an imported download still in the queue: total %d, status %+v", q.TotalRecords, status)
	}
}

func TestFailedQueueExpiryAndReplacementScope(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                 string
		age                  time.Duration
		replacement, version string
		want                 int
	}{
		{"fresh", time.Hour, "", "", 1},
		{"week boundary", 7 * 24 * time.Hour, "", "", 0},
		{"recent imported replacement", time.Hour, "imported", "movie-web", 0},
		{"other version imported", time.Hour, "imported", "movie-remux", 1},
		{"replacement still downloading", time.Hour, "downloading", "movie-web", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			at := ms(e.now.Add(-tc.age))
			e.exec(`INSERT INTO download(id,title_id,version_id,version_key,info_hash,release_title,indexer,state,error,created_at,updated_at)
   VALUES(1,1,2,'movie-web','old','Failed','dmm','failed','stalled: timeout',?,?)`, at, at)
			if tc.replacement != "" {
				e.exec(`INSERT INTO download(id,title_id,version_id,version_key,info_hash,release_title,indexer,state,created_at,updated_at)
    VALUES(2,1,2,?,'new','Replacement','dmm',?,?,?)`, tc.version, tc.replacement, ms(e.now), ms(e.now))
			}
			var page struct{ TotalRecords int }
			e.call("GET", "/radarr/api/v3/queue", nil, 200, &page)
			var details []queueResource
			e.call("GET", "/radarr/api/v3/queue/details", nil, 200, &details)
			if page.TotalRecords != tc.want || len(details) != tc.want {
				t.Fatalf("page=%d details=%d want=%d", page.TotalRecords, len(details), tc.want)
			}
		})
	}
}
