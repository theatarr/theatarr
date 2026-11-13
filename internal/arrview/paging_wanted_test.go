package arrview

import (
	"strings"
	"testing"
	"time"
)

func TestWantedMissingSQLPagingAndSorts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.exec(`INSERT INTO version(id,title_id,policy_key,monitored,state,created_at,updated_at) VALUES(20,4,'movie-web',1,'wanted',?,?)`, ms(testNow), ms(testNow))
	e.exec(`UPDATE title SET name='Zulu',sort_name='' WHERE id=3`)
	e.exec(`UPDATE title SET name='Äther',sort_name='' WHERE id=4`)
	e.exec(`INSERT INTO title_release_date(title_id,source,country,type,date) VALUES(4,'radarr_4k_web','','digital','2022-01-01'),(3,'radarr_anime','','digital','2027-01-01')`)
	var p struct {
		TotalRecords int
		Records      []movieResource
	}
	for _, tc := range []struct {
		query string
		id    int64
	}{
		{"", IDBase + 3}, {"page=2", IDBase + 4}, {"sortDirection=descending", IDBase + 4},
		{"sortKey=movieMetadata.year", IDBase + 4}, {"sortKey=movieMetadata.digitalRelease", IDBase + 4},
		{"sortKey=unknown", IDBase + 3},
	} {
		e.call("GET", "/radarr/api/v3/wanted/missing?pageSize=1&"+tc.query, nil, 200, &p)
		if p.TotalRecords != 2 || len(p.Records) != 1 || p.Records[0].ID != tc.id {
			t.Fatalf("%s: %+v", tc.query, p)
		}
	}
	e.exec(`INSERT INTO episode(id,title_id,season,number,name,air_date_utc_at,monitored,created_at,updated_at)
 VALUES(104,10,1,4,'Older',?,1,?,?),(105,10,1,5,'Removed',?,1,?,?)`, ms(testNow.Add(-10*365*24*time.Hour)), ms(testNow), ms(testNow), ms(testNow.Add(-11*365*24*time.Hour)), ms(testNow), ms(testNow))
	e.exec(`UPDATE episode SET removed_at=? WHERE id=105`, ms(testNow))
	var eps struct {
		TotalRecords int
		Records      []episodeResource
	}
	for _, tc := range []struct {
		query string
		id    int64
		total int
	}{
		{"", 104, 2}, {"page=2", 102, 2}, {"sortDirection=descending", 102, 2}, {"monitored=false", 100, 1},
	} {
		e.call("GET", "/sonarr/api/v3/wanted/missing?pageSize=1&"+tc.query, nil, 200, &eps)
		if eps.TotalRecords != tc.total || len(eps.Records) != 1 || eps.Records[0].ID != tc.id {
			t.Fatalf("%s: %+v", tc.query, eps)
		}
	}
	// A missing file in a removed view version must not exclude a title or episode.
	e.exec(`INSERT INTO arrview_removed_version(version_id,view,removed_at) VALUES(20,'radarr',?)`, ms(testNow))
	e.call("GET", "/radarr/api/v3/wanted/missing", nil, 200, &p)
	if p.TotalRecords != 1 || len(p.Records) != 1 || p.Records[0].ID != IDBase+3 {
		t.Fatalf("removed version: %+v", p)
	}
}

func TestWantedCutoffSQLPaging(t *testing.T) {
	t.Parallel()
	e := newCutoffEnv(t, "")
	e.cutoff = nil
	e.startDev(devDecider{e: e})
	var p struct {
		TotalRecords int
		Records      []movieResource
	}
	e.call("GET", "/radarr/api/v3/wanted/cutoff?pageSize=1&page=2", nil, 200, &p)
	if p.TotalRecords != 2 || len(p.Records) != 1 || p.Records[0].Title != "Delta" {
		t.Fatalf("cutoff page: %+v", p)
	}
	e.call("GET", "/radarr/api/v3/wanted/cutoff?pageSize=1&sortDirection=descending", nil, 200, &p)
	if p.TotalRecords != 2 || len(p.Records) != 1 || p.Records[0].Title != "Delta" {
		t.Fatalf("cutoff sort: %+v", p)
	}
	var eps struct {
		TotalRecords int
		Records      []episodeResource
	}
	e.call("GET", "/sonarr/api/v3/wanted/cutoff?pageSize=1&page=2", nil, 200, &eps)
	if eps.TotalRecords != 2 || len(eps.Records) != 1 || eps.Records[0].ID != 102 {
		t.Fatalf("episode cutoff page: %+v", eps)
	}
	e.call("GET", "/sonarr/api/v3/wanted/cutoff?pageSize=1&sortDirection=descending", nil, 200, &eps)
	if eps.TotalRecords != 2 || len(eps.Records) != 1 || eps.Records[0].ID != 102 {
		t.Fatalf("episode cutoff sort: %+v", eps)
	}
}

func TestHistoryAndBlocklistSQLTitleSort(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedBlocklist()
	e.exec(`UPDATE title SET name='Äther',sort_name='' WHERE id=1`)
	e.exec(`UPDATE title SET name='Zulu',sort_name='' WHERE id=2`)
	e.exec(`INSERT INTO router_blocklist(version_key,info_hash,title_id,release_title,reason,source,created_at)
 VALUES('movie-web','beta',2,'Zulu','failed','queue',?)`, ms(testNow))
	e.exec(`INSERT INTO download(id,title_id,version_id,version_key,info_hash,release_title,indexer,quality,state,episodes,created_at,updated_at,added_at)
 VALUES(90,2,3,'movie-web',?,'Zulu','dmm','WEBDL-1080p','added','[]',?,?,?)`, strings.Repeat("e", 40), ms(testNow), ms(testNow), ms(testNow))
	var hs struct {
		TotalRecords int
		Records      []historyResource
	}
	// seedBlocklist's failed download adds a second history event; title sort still puts Zulu first.
	e.call("GET", "/radarr/api/v3/history?sortKey=movieMetadata.sortTitle&sortDirection=ascending&pageSize=1", nil, 200, &hs)
	if hs.TotalRecords != 2 || len(hs.Records) != 1 || *hs.Records[0].MovieID != IDBase+2 {
		t.Fatalf("history title sort: %+v", hs)
	}
	var bs struct {
		TotalRecords int
		Records      []blocklistResource
	}
	e.call("GET", "/radarr/api/v3/blocklist?sortKey=movieMetadata.sortTitle&sortDirection=ascending&pageSize=1", nil, 200, &bs)
	if bs.TotalRecords != 3 || len(bs.Records) != 1 || *bs.Records[0].MovieID != IDBase+2 {
		t.Fatalf("blocklist title sort: %+v", bs)
	}
	e.call("GET", "/radarr/api/v3/blocklist?movieIds=1000001,1000002&protocols=1,2&pageSize=1&page=3", nil, 200, &bs)
	if bs.TotalRecords != 3 || len(bs.Records) != 1 {
		t.Fatalf("blocklist filters/page: %+v", bs)
	}
	e.call("GET", "/radarr/api/v3/blocklist?sortKey=indexer&sortDirection=descending&pageSize=1", nil, 200, &bs)
	if bs.TotalRecords != 3 || len(bs.Records) != 1 || bs.Records[0].Indexer != "torrentio" {
		t.Fatalf("blocklist indexer: %+v", bs)
	}
}
