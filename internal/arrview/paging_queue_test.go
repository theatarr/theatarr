package arrview

import (
	"strconv"
	"testing"
	"time"
)

func TestSeasonPackQueueIDs(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedDownloads()
	var p struct {
		TotalRecords int
		Records      []queueResource
	}
	e.call("GET", "/sonarr/api/v3/queue?includeEpisode=true", nil, 200, &p)
	if p.TotalRecords != 2 || len(p.Records) != 2 {
		t.Fatalf("pack: %+v", p)
	}
	ids := map[int64]int64{}
	for _, item := range p.Records {
		if item.EpisodeID == nil || item.Episode == nil || item.ID < 1<<52 || item.ID >= 1<<53 {
			t.Fatalf("item: %+v", item)
		}
		ids[*item.EpisodeID] = item.ID
		if item.DownloadID != p.Records[0].DownloadID {
			t.Fatal("download id differs within pack")
		}
	}
	if ids[101] == ids[102] {
		t.Fatal("shared queue id")
	}
	e.start() // ids survive a restart, changed episode order and embedding choices
	e.exec(`UPDATE download SET episodes='[102,101]' WHERE id=3`)
	for page := 1; page <= 2; page++ {
		p.Records = nil
		e.call("GET", "/sonarr/api/v3/queue?pageSize=1&page="+strconv.Itoa(page), nil, 200, &p)
		if p.TotalRecords != 2 || len(p.Records) != 1 {
			t.Fatalf("page: %+v", p)
		}
		it := p.Records[0]
		if it.ID != ids[*it.EpisodeID] || *it.EpisodeID != int64(100+page) || it.Episode != nil {
			t.Fatalf("unstable id/page: %+v", it)
		}
	}
	var details []queueResource
	e.call("GET", "/sonarr/api/v3/queue/details?episodeIds=102", nil, 200, &details)
	if len(details) != 1 || details[0].ID != ids[102] {
		t.Fatalf("details: %+v", details)
	}
	// The same episode in another download must have a different id too.
	e.exec(`INSERT INTO download(id,title_id,version_id,version_key,info_hash,release_title,indexer,episodes,state,created_at,updated_at)
 VALUES(9,10,10,'tv-2160p','other','Other pack','dmm','[101,102]','queued',?,?)`, ms(testNow), ms(testNow))
	p.Records = nil
	e.call("GET", "/sonarr/api/v3/queue", nil, 200, &p)
	seen := map[int64]bool{}
	for _, item := range p.Records {
		if seen[item.ID] {
			t.Fatal("duplicate queue id")
		}
		seen[item.ID] = true
	}
	if len(seen) != 4 || p.TotalRecords != 4 {
		t.Fatalf("two packs: %+v", p)
	}
}

func TestDeleteAnySeasonPackMember(t *testing.T) {
	t.Parallel()
	for _, pending := range []bool{false, true} {
		for _, bulk := range []bool{false, true} {
			for _, episode := range []int64{101, 102} {
				name := strconv.FormatBool(pending) + "/" + strconv.FormatBool(bulk) + "/" + strconv.FormatInt(episode, 10)
				t.Run(name, func(t *testing.T) {
					e := newEnv(t)
					source := int64(3)
					if pending {
						source = -1
						e.exec(`INSERT INTO upgrade_hold(id,title_id,version_id,version_key,episodes,info_hash,release_title,reason,candidate,created_at,seen_at)
      VALUES(1,10,10,'tv-2160p','101,102','pending','Pack','upgrade','{}',?,?)`, ms(testNow), ms(testNow))
					} else {
						e.seedDownloads()
					}
					id := queueEpisodeID(source, episode)
					path := "/sonarr/api/v3/queue/" + strconv.FormatInt(id, 10)
					var body any
					if bulk {
						path = "/sonarr/api/v3/queue/bulk"
						body = map[string]any{"ids": []int64{id, queueEpisodeID(source, 203-episode)}}
					}
					e.call("DELETE", path+"?blocklist=true&skipRedownload=true", body, 200, nil)
					var p struct {
						TotalRecords int
						Records      []queueResource
					}
					e.call("GET", "/sonarr/api/v3/queue", nil, 200, &p)
					if p.TotalRecords != 0 || len(p.Records) != 0 {
						t.Fatalf("pack remains: %+v", p)
					}
					if pending {
						if e.str(`SELECT state FROM upgrade_hold WHERE id=1`) != "declined" {
							t.Fatal("pending pack remains")
						}
						if e.one(`SELECT COUNT(*) FROM download`) != 0 {
							t.Fatal("pending deletion created download")
						}
					} else if e.str(`SELECT state FROM download WHERE id=3`) != "failed" {
						t.Fatal("download not removed")
					}
					if e.one(`SELECT COUNT(*) FROM router_blocklist WHERE version_key='tv-2160p'`) != 1 || e.one(`SELECT COUNT(*) FROM arrview_command`) != 0 {
						t.Fatal("blocklist or skipRedownload differs")
					}
				})
			}
		}
	}
}

func TestSeasonPackBulkRedownloadOnce(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedDownloads()
	e.call("DELETE", "/sonarr/api/v3/queue/bulk?blocklist=true", map[string]any{"ids": []int64{queueEpisodeID(3, 101), queueEpisodeID(3, 102)}}, 200, nil)
	if e.one(`SELECT COUNT(*) FROM arrview_command WHERE name='SeriesSearch'`) != 1 {
		t.Fatal("pack searched more than once")
	}
	e.call("DELETE", "/v/anime-tv/sonarr/api/v3/queue/"+strconv.FormatInt(queueEpisodeID(3, 101), 10), nil, 404, nil)
}

func TestQueueSortMetadataWithoutEmbedding(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedDownloads()
	e.exec(`UPDATE download SET state='queued' WHERE id=1`)
	e.exec(`UPDATE download SET title_id=3,version_id=4,version_key='anime-movie' WHERE id=4`)
	var p struct {
		TotalRecords int
		Records      []queueResource
	}
	for _, tc := range []struct {
		key, dir string
		id       int64
	}{
		{"movies.sortTitle", "descending", 4}, {"movies.sortTitle", "ascending", 1},
		{"year", "descending", 4}, {"quality", "descending", 1}, {"size", "descending", 1},
		{"languages", "ascending", 1}, {"timeleft", "ascending", 1},
	} {
		p.Records = nil
		e.call("GET", "/radarr/api/v3/queue?pageSize=1&sortKey="+tc.key+"&sortDirection="+tc.dir, nil, 200, &p)
		if len(p.Records) != 1 || p.Records[0].ID != tc.id || p.Records[0].Movie != nil {
			t.Fatalf("sort %s %s: %+v", tc.key, tc.dir, p)
		}
	}
}

func TestQueueSQLSortsEstimates(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedDownloads()
	e.exec(`UPDATE download SET state='downloading',provider='rd',provider_torrent_id='T4',progress=80 WHERE id=4`)
	e.exec(`INSERT INTO download_progress(download_id,sampled_at,provider,torrent_id,progress) VALUES
 (1,?,'rd','T1',10),(1,?,'rd','T1',40),(4,?,'rd','T4',20),(4,?,'rd','T4',80)`, ms(testNow.Add(-time.Minute)), ms(testNow), ms(testNow.Add(-time.Minute)), ms(testNow))
	for _, key := range []string{"timeleft", "estimatedCompletionTime"} {
		for _, tc := range []struct {
			dir   string
			first int64
		}{{"ascending", 4}, {"descending", 1}} {
			var p struct {
				TotalRecords int
				Records      []queueResource
			}
			e.call("GET", "/radarr/api/v3/queue?pageSize=1&sortKey="+key+"&sortDirection="+tc.dir, nil, 200, &p)
			if p.TotalRecords != 2 || len(p.Records) != 1 || p.Records[0].ID != tc.first {
				t.Fatalf("%s %s: %+v", key, tc.dir, p)
			}
		}
	}
}
