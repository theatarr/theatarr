package arrview

import (
	"database/sql/driver"
	"fmt"
	"math"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
)

var historyPayloadReads atomic.Int64

func init() {
	sqlite.MustRegisterScalarFunction("theatarr_test_history_payload", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		historyPayloadReads.Add(1)
		return args[0], nil
	})
}

func TestListPagingInputs(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedDownloads()
	for _, endpoint := range []string{"history", "wanted/missing", "wanted/cutoff", "blocklist", "queue"} {
		for _, tc := range []struct {
			query              string
			status, page, size int
		}{
			{"", 200, 1, 10}, {"page=0", 200, 1, 10}, {"page=-1", 200, 1, 10}, {"page=-2147483648", 200, 1, 10},
			{"pageSize=0", 200, 1, 0}, {"pageSize=-1", 400, 0, 0}, {"pageSize=-2147483648", 400, 0, 0},
			{"page=2147483648", 400, 0, 0}, {"pageSize=2147483648", 400, 0, 0},
			{"page=999999999999999999999999999", 400, 0, 0}, {"pageSize=99999999999999999999999999", 400, 0, 0},
			{"page=wat", 400, 0, 0}, {"pageSize=", 400, 0, 0},
			{"page=2147483647&pageSize=2147483647", 200, 2147483647, 2147483647},
		} {
			t.Run(endpoint+"/"+tc.query, func(t *testing.T) {
				var p struct {
					Page, PageSize, TotalRecords int
					Records                      []map[string]any
				}
				if tc.status != 200 {
					e.call("GET", "/radarr/api/v3/"+endpoint+"?"+tc.query, nil, tc.status, nil)
					return
				}
				e.call("GET", "/radarr/api/v3/"+endpoint+"?"+tc.query, nil, tc.status, &p)
				wantPage := tc.page
				if endpoint == "queue" && wantPage > 1 {
					wantPage = 1
				}
				if p.Page != wantPage || p.PageSize != tc.size {
					t.Fatalf("paging %+v", p)
				}
				if tc.size == 0 && len(p.Records) != 0 {
					t.Fatalf("zero page size: %+v", p)
				}
				if endpoint != "queue" && tc.page == 2147483647 && len(p.Records) != 0 {
					t.Fatal("overflow returned rows")
				}
			})
		}
	}
	for _, p := range []pagingResource{{Page: math.MaxInt, PageSize: math.MaxInt}, {Page: 3, PageSize: math.MaxInt}} {
		if p.offset() != math.MaxInt64 {
			t.Fatalf("overflow offset %d", p.offset())
		}
	}
}

func TestHistorySQLFilters(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedDownloads()
	e.exec(`INSERT INTO arr_history(instance,arr_id,event_type,date_at,arr_title_id,title_id,version_id,download_id,source_title,quality,languages,data)
 VALUES('radarr_4k_web',900,'downloadFailed',?,8,2,3,'Other','Beta.Failed','WEBDL-1080p','[]','{}')`, ms(testNow))
	e.exec(`INSERT INTO arr_alias(instance,kind,arr_id,target_id,created_at) VALUES('sonarr_4k_debrid','episode',5001,102,?)`, ms(testNow))
	e.exec(`INSERT INTO arr_history(instance,arr_id,event_type,date_at,arr_title_id,arr_episode_id,title_id,version_id,download_id,source_title,quality,languages,data) VALUES
 ('sonarr_4k_debrid',901,'grabbed',?,10,5001,10,10,'ImportedX','The.Show.Second','WEBDL-1080p','[]','{}'),
 ('sonarr_4k_debrid',902,'downloadFailed',?,10,5001,10,10,'ImportedX','The.Show.Second','WEBDL-1080p','[]','{}')`, ms(testNow.Add(-20*time.Second)), ms(testNow.Add(-10*time.Second)))
	for _, tc := range []struct {
		view, query string
		count       int
	}{
		{"radarr", "", 6}, {"radarr", "eventType=1", 3}, {"radarr", "eventType=1&eventType=4", 4}, {"radarr", "eventType=1,3", 5},
		{"radarr", "eventType=downloadFailed", 1}, {"radarr", "eventType=999", 0},
		{"radarr", "downloadId=abcdef", 2}, {"radarr", "downloadId=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", 1},
		{"radarr", "movieIds=1000001", 5}, {"radarr", "movieIds=1000002", 1}, {"radarr", "movieIds=1000001,1000002", 6},
		{"radarr", "movieIds=7&movieIds=1000002", 6}, {"radarr", "movieIds=999", 0},
		{"radarr", "movieIds=1000002&eventType=1", 0},
		// The local pack grab of 101 and 102 is one record per episode.
		{"sonarr", "", 4}, {"sonarr", "episodeId=101", 1}, {"sonarr", "episodeId=102", 3},
		{"sonarr", "episodeId=102&eventType=4", 1}, {"sonarr", "eventType=1,4", 4}, {"sonarr", "eventType=999", 0},
		{"sonarr", "seriesIds=1000010,999", 4}, {"sonarr", "seriesIds=999", 0}, {"sonarr", "downloadId=importedx", 2},
	} {
		t.Run(tc.view+"/"+tc.query, func(t *testing.T) {
			var p struct {
				TotalRecords int
				Records      []historyResource
			}
			e.call("GET", "/"+tc.view+"/api/v3/history?pageSize=100&"+tc.query, nil, 200, &p)
			if p.TotalRecords != tc.count || len(p.Records) != tc.count {
				t.Fatalf("want %d: %+v", tc.count, p)
			}
			if tc.view == Sonarr && tc.query == "episodeId=102" {
				for _, h := range p.Records {
					if h.EpisodeID == nil || *h.EpisodeID != 102 {
						t.Fatalf("unmapped episode: %+v", h)
					}
				}
			}
		})
	}
	var p struct {
		TotalRecords int
		Records      []historyResource
	}
	e.call("GET", "/radarr/api/v3/history?includeMovie=true&pageSize=100", nil, 200, &p)
	for _, h := range p.Records {
		if h.Movie == nil || h.Movie.ID != *h.MovieID {
			t.Fatalf("movie not embedded: %+v", h)
		}
	}
	p.Records = nil
	e.call("GET", "/sonarr/api/v3/history?includeSeries=true&includeEpisode=true", nil, 200, &p)
	for _, h := range p.Records {
		if h.Series == nil || h.Episode == nil || h.Episode.ID != *h.EpisodeID {
			t.Fatalf("series/episode not embedded: %+v", h)
		}
	}
	p.Records = nil
	e.call("GET", "/sonarr/api/v3/history?includeSeries=false&includeEpisode=false", nil, 200, &p)
	for _, h := range p.Records {
		if h.Series != nil || h.Episode != nil {
			t.Fatalf("unexpected embedding: %+v", h)
		}
	}
	var hs []historyResource
	e.call("GET", "/sonarr/api/v3/history/series?seriesId=1000010&seasonNumber=1&eventType=1,4&includeEpisode=true", nil, 200, &hs)
	if len(hs) != 4 || hs[0].Episode == nil {
		t.Fatalf("series history: %+v", hs)
	}
	e.call("GET", "/sonarr/api/v3/history/series?seriesId=1000010&seasonNumber=0", nil, 200, &hs)
	if len(hs) != 0 {
		t.Fatalf("season filter: %+v", hs)
	}
	e.call("GET", "/radarr/api/v3/history/movie?movieId=1000001&eventType=1&includeMovie=true", nil, 200, &hs)
	if len(hs) != 3 || hs[0].Movie == nil {
		t.Fatalf("movie history: %+v", hs)
	}
	for _, view := range []string{"radarr", "sonarr"} {
		e.call("GET", "/"+view+"/api/v3/history/since?date=bad", nil, 400, nil)
	}
	e.call("GET", "/sonarr/api/v3/history/since?date="+testNow.Add(-15*time.Second).Format(time.RFC3339)+"&includeEpisode=true&includeSeries=true", nil, 200, &hs)
	if len(hs) != 1 || hs[0].EventType != "downloadFailed" || hs[0].Episode == nil || hs[0].Series == nil {
		t.Fatalf("since: %+v", hs)
	}
	e.call("GET", "/sonarr/api/v3/history/since?date="+testNow.Add(-20*time.Second).Format(time.RFC3339), nil, 200, &hs)
	if len(hs) != 2 || !time.Time(hs[0].Date).Before(time.Time(hs[1].Date)) {
		t.Fatalf("since must be inclusive, ascending: %+v", hs)
	}
	e.call("GET", "/radarr/api/v3/history/since?date="+testNow.Format(time.RFC3339)+"&includeMovie=true&eventType=4", nil, 200, &hs)
	if len(hs) != 1 || hs[0].Movie == nil {
		t.Fatalf("radarr since: %+v", hs)
	}
}

// The view counts actual payload reads, so reading and discarding 5k records cannot pass this test.
func TestLargeHistoryReadsOnlyPagePayloads(t *testing.T) {
	e := newEnv(t)
	e.exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<5000)
 INSERT INTO arr_history(instance,arr_id,event_type,date_at,arr_title_id,title_id,version_id,source_title,quality,languages,data)
 SELECT 'radarr_4k_web',1000+x,'grabbed',?+x,7,1,2,'Large release '||x,'WEBDL-1080p','[]','{"message":"payload"}' FROM n`, ms(testNow))
	e.exec(`ALTER TABLE arr_history RENAME TO arr_history_source`)
	e.exec(`CREATE VIEW arr_history AS SELECT rowid,instance,arr_id,event_type,date_at,arr_title_id,arr_episode_id,title_id,version_id,download_id,info_hash,protocol,source_title,quality,custom_format_score,languages,theatarr_test_history_payload(data) AS data FROM arr_history_source`)
	for _, page := range []int{1, 2, 500, 501} {
		historyPayloadReads.Store(0)
		var p struct {
			TotalRecords int
			Records      []historyResource
		}
		e.call("GET", "/radarr/api/v3/history?page="+strconv.Itoa(page)+"&pageSize=10", nil, 200, &p)
		if p.TotalRecords != 5002 {
			t.Fatalf("count %d", p.TotalRecords)
		}
		want := 10
		if page == 501 {
			want = 2
		}
		if len(p.Records) != want || historyPayloadReads.Load() != int64(want) {
			t.Fatalf("page %d: %d records, %d payload reads", page, len(p.Records), historyPayloadReads.Load())
		}
		if page < 501 && p.Records[0].SourceTitle != fmt.Sprintf("Large release %d", 5000-(page-1)*10) {
			t.Fatalf("wrong page: %+v", p.Records[0])
		}
	}
}

func TestHistoryUnknownEventFilter(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.exec(`UPDATE arr_history SET event_type='unknown' WHERE arr_id=40`)
	var p struct {
		TotalRecords int
		Records      []historyResource
	}
	e.call("GET", "/radarr/api/v3/history?eventType=0", nil, 200, &p)
	if p.TotalRecords != 1 || len(p.Records) != 1 || p.Records[0].EventType != "unknown" {
		t.Fatalf("unknown event: %+v", p)
	}
	e.call("GET", "/radarr/api/v3/history?eventType=0,3", nil, 200, &p)
	if p.TotalRecords != 2 || len(p.Records) != 2 {
		t.Fatalf("unknown event in list: %+v", p)
	}
}

func TestHistorySQLQualityAndLanguageSort(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.exec(`INSERT INTO arr_history(instance,arr_id,event_type,date_at,title_id,version_id,source_title,quality,languages,data) VALUES
 ('radarr_4k_web',200,'grabbed',?,2,3,'Quality eighteen','WEBDL-2160p','["Spanish"]','{}'),
 ('radarr_4k_web',201,'grabbed',?,1,2,'Quality three','WEBDL-1080p','["German"]','{}')`, ms(testNow), ms(testNow))
	for _, tc := range []struct {
		key   string
		page  int
		title string
	}{
		{"quality", 1, "Quality eighteen"}, {"quality", 2, "Quality three"},
		{"languages", 3, "Quality eighteen"}, {"languages", 4, "Quality three"},
	} {
		var p struct {
			TotalRecords int
			Records      []historyResource
		}
		e.call("GET", "/radarr/api/v3/history?sortDirection=ascending&pageSize=1&sortKey="+tc.key+"&page="+strconv.Itoa(tc.page), nil, 200, &p)
		if p.TotalRecords != 4 || len(p.Records) != 1 || p.Records[0].SourceTitle != tc.title {
			t.Fatalf("%s page %d: %+v", tc.key, tc.page, p)
		}
	}
}
