package arrview

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The queue lists the router's downloads in hand for the view's versions (migration 00014).
func TestQueueFromDownloads(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	hash := strings.Repeat("a", 40)
	created := ms(testNow)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, size_bytes, episodes, provider, provider_torrent_id, progress, state, error, created_at, updated_at)
		VALUES
		(1, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB.H265-GRP', 'dmm', 'WEBDL-2160p', 1700, 10000, '[]', 'rd',
		 'T1', 40, 'downloading', NULL, ?, ?),
		(2, 3, 4, 'anime-movie', ?, 'Gamma.2027.1080p.BluRay-GRP', 'seadex', 'Bluray-1080p', 0, 5000, '[]', 'tb',
		 'T2', 100, 'ready', NULL, ?, ?),
		(3, 1, 1, 'movie-remux', ?, 'Alpha.Old', 'dmm', 'Remux-2160p', 0, 1, '[]', NULL, NULL, 0, 'failed',
		 'miss on every provider', ?, ?),
		(4, 1, 1, 'movie-remux', ?, 'Alpha.2020.2160p.BluRay.REMUX-GRP', 'dmm', 'Remux-2160p', 0, 60000, '[]',
		 NULL, NULL, 0, 'queued', 'waiting for a provider until 2026-10-26T12:05:00Z', ?, ?),
		(5, 10, 10, 'tv-2160p', ?, 'The.Show.S01.2160p.WEB-DL-GRP', 'dmm', 'WEBDL-2160p', 0, 2000, '[101, 102]',
		 'ad', 'A1', 50, 'downloading', NULL, ?, ?),
		(6, 1, 2, 'movie-web', ?, 'Alpha.Done', 'dmm', 'WEBDL-2160p', 0, 1, '[]', 'rd', 'T3', 100, 'imported',
		 NULL, ?, ?)`,
		hash, created, created, strings.Repeat("b", 40), created, created, strings.Repeat("c", 40), created, created,
		strings.Repeat("d", 40), created, created, strings.Repeat("e", 40), created, created,
		strings.Repeat("f", 40), created, created)
	e.exec(`INSERT INTO library_import (download_id, state, last_error, created_at, updated_at)
		VALUES (2, 'rejected', 'not an upgrade for the existing files', ?, ?)`, created, created)

	var q struct {
		TotalRecords int
		Records      []map[string]any
	}
	e.call("GET", "/radarr/api/v3/queue", nil, 200, &q)
	if q.TotalRecords != 3 {
		t.Fatalf("radarr queue: %d records, want 3 (downloading, ready, queued): %v", q.TotalRecords, q.Records)
	}
	r := q.Records[1]
	if r["id"].(float64) != 1 || r["movieId"].(float64) != IDBase+1 || r["status"] != "downloading" ||
		r["trackedDownloadState"] != "downloading" || r["trackedDownloadStatus"] != "ok" ||
		r["downloadId"] != strings.ToUpper(hash) || r["protocol"] != "torrent" || r["sizeleft"].(float64) != 6000 ||
		r["size"].(float64) != 10000 || r["downloadClient"] != "theatarr router (rd)" || r["indexer"] != "dmm" {
		t.Errorf("downloading item %v", r)
	}
	if q.Records[0]["status"] != "completed" || q.Records[0]["trackedDownloadState"] != "importBlocked" ||
		q.Records[0]["trackedDownloadStatus"] != "warning" || q.Records[0]["sizeleft"].(float64) != 0 {
		t.Errorf("ready item %v", q.Records[0])
	}
	if msgs, _ := q.Records[0]["statusMessages"].([]any); len(msgs) != 1 || !strings.Contains(msgs[0].(map[string]any)["messages"].([]any)[0].(string), "not an upgrade") {
		t.Errorf("held ready messages %v", q.Records[0]["statusMessages"])
	}
	if q.Records[2]["status"] != "queued" || len(q.Records[2]["statusMessages"].([]any)) != 1 {
		t.Errorf("queued item %v", q.Records[2])
	}
	// The key set: QueueResource's properties (Radarr), less movie (includeMovie) and the optional ones.
	wantKeys(t, "queue item", r, fields(`id movieId languages quality customFormats customFormatScore size title
		added status trackedDownloadStatus trackedDownloadState statusMessages downloadId protocol downloadClient
		downloadClientHasPostImportCategory indexer sizeleft`), "estimatedCompletionTime", "errorMessage",
		"outputPath", "timeleft", "movie")
	// Per-version view: only its own version.
	e.call("GET", "/v/movie-web/radarr/api/v3/queue", nil, 200, &q)
	if q.TotalRecords != 1 || q.Records[0]["id"].(float64) != 1 {
		t.Errorf("per-version queue %v", q.Records)
	}
	// movieIds by view id.
	e.call("GET", "/radarr/api/v3/queue?movieIds="+strconv.Itoa(IDBase+1), nil, 200, &q)
	if q.TotalRecords != 2 {
		t.Errorf("by id: %v", q.Records)
	}
	e.call("GET", "/radarr/api/v3/queue?movieIds=999", nil, 200, &q)
	if q.TotalRecords != 0 {
		t.Errorf("unknown movie: %v", q.Records)
	}
	e.call("GET", "/radarr/api/v3/queue?sortDirection=descending", nil, 200, &q)
	if q.Records[0]["id"].(float64) != 1 {
		t.Errorf("descending %v", q.Records[0])
	}

	var status map[string]any
	e.call("GET", "/radarr/api/v3/queue/status", nil, 200, &status)
	if status["totalCount"].(float64) != 3 || status["count"].(float64) != 3 {
		t.Errorf("status %v", status)
	}
	var details []map[string]any
	e.call("GET", "/radarr/api/v3/queue/details?movieId="+strconv.Itoa(IDBase+3), nil, 200, &details)
	if len(details) != 1 || details[0]["id"].(float64) != 2 {
		t.Errorf("details %v", details)
	}

	// Sonarr: one item per episode, with the episode when asked (Seerr's includeEpisode).
	q.Records = nil
	e.call("GET", "/sonarr/api/v3/queue?includeEpisode=true", nil, 200, &q)
	if q.TotalRecords != 2 {
		t.Fatalf("sonarr queue %v", q.Records)
	}
	s := q.Records[0]
	ep, _ := s["episode"].(map[string]any)
	if s["seriesId"].(float64) != IDBase+10 || s["episodeId"].(float64) != 101 || s["seasonNumber"].(float64) != 1 ||
		ep == nil || ep["episodeNumber"].(float64) != 1 || s["episodeHasFile"] != true {
		t.Errorf("sonarr item %v", s)
	}
	if _, has := s["movieId"]; has {
		t.Error("movieId on a Sonarr item")
	}
	details = nil
	e.call("GET", "/sonarr/api/v3/queue/details?episodeIds=102", nil, 200, &details)
	if len(details) != 1 || details[0]["episodeId"].(float64) != 102 || details[0]["episode"] != nil {
		t.Errorf("sonarr details %v", details)
	}
}

func TestQueueSortKeys(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := ms(testNow)
	e.exec(`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,quality,score,size_bytes,episodes,progress,state,created_at,updated_at) VALUES
		(21,1,2,'movie-web','a','Zulu','z','WEBDL-2160p',0,100,'[]',10,'downloading',?,?),
		(22,1,2,'movie-web','b','Alpha','a','WEBDL-2160p',0,100,'[]',90,'downloading',?,?)`, now, now, now, now)
	var q struct{ Records []map[string]any }
	e.call("GET", "/radarr/api/v3/queue?sortKey=title&sortDirection=ascending&pageSize=50", nil, 200, &q)
	if len(q.Records) < 2 || q.Records[0]["title"] != "Alpha" {
		t.Fatalf("title sort: %v", q.Records)
	}
	e.call("GET", "/radarr/api/v3/queue?sortKey=progress&sortDirection=descending&pageSize=50", nil, 200, &q)
	if len(q.Records) < 2 || q.Records[0]["id"].(float64) != 22 {
		t.Fatalf("progress sort: %v", q.Records)
	}
}

func TestQueueCompletionEstimates(t *testing.T) {
	t.Parallel()
	for _, view := range []string{"radarr", "sonarr"} {
		for _, tc := range []struct {
			name, state string
			progress    float64
			samples     []float64
			age         time.Duration
			want        string
			eta         time.Duration
		}{
			{name: "estimated", state: "downloading", progress: 40, samples: []float64{20, 40}, want: "00:03:00", eta: 3 * time.Minute},
			{name: "estimated days", state: "downloading", progress: 0.03125, samples: []float64{0, 0.03125}, want: "2.05:19:00", eta: 3199 * time.Minute},
			{name: "ready awaiting link", state: "ready", progress: 100, want: "00:00:00"},
			{name: "importing", state: "importing", progress: 100, want: "00:00:00"},
			{name: "unknown", state: "downloading", progress: 40},
			{name: "one sample", state: "downloading", progress: 40, samples: []float64{40}},
			{name: "flat", state: "downloading", progress: 40, samples: []float64{40, 40}},
			{name: "stale", state: "downloading", progress: 40, samples: []float64{20, 40}, age: 6 * time.Minute},
			{name: "queued", state: "queued", progress: 40, samples: []float64{20, 40}},
		} {
			t.Run(view+"/"+tc.name, func(t *testing.T) {
				e := newEnv(t)
				titleID, versionID, version, episodes := 1, 2, "movie-web", "[]"
				if view == "sonarr" {
					titleID, versionID, version, episodes = 10, 10, "tv-2160p", "[101,102]"
				}
				e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
					provider, provider_torrent_id, progress, state, episodes, created_at, updated_at)
					VALUES (1, ?, ?, ?, ?, 'Release', 'dmm', 'ad', 'A1', ?, ?, ?, ?, ?)`,
					titleID, versionID, version, strings.Repeat("a", 40), tc.progress, tc.state, episodes, ms(e.now), ms(e.now))
				for i, p := range tc.samples {
					e.exec(`INSERT INTO download_progress (download_id, sampled_at, provider, torrent_id, progress)
						VALUES (1, ?, 'ad', 'A1', ?)`, ms(e.now.Add(-tc.age-time.Duration(len(tc.samples)-1-i)*time.Minute)), p)
				}
				var queue struct{ Records []map[string]any }
				e.call("GET", "/"+view+"/api/v3/queue?includeEpisode=true", nil, 200, &queue)
				var details []map[string]any
				e.call("GET", "/"+view+"/api/v3/queue/details?includeEpisode=true", nil, 200, &details)
				count := 1
				if view == "sonarr" {
					count = 2
				}
				if len(queue.Records) != count || len(details) != count {
					t.Fatalf("queue: %+v; details: %+v", queue, details)
				}
				for _, item := range append(queue.Records, details...) {
					if tc.want == "" {
						if _, ok := item["timeleft"]; ok {
							t.Errorf("unknown timeleft: %+v", item)
						}
						if _, ok := item["estimatedCompletionTime"]; ok {
							t.Errorf("unknown completion: %+v", item)
						}
						continue
					}
					if item["timeleft"] != tc.want {
						t.Errorf("timeleft %v, want %s", item["timeleft"], tc.want)
					}
					stamp, _ := item["estimatedCompletionTime"].(string)
					completion, err := time.Parse(time.RFC3339Nano, stamp)
					want := e.now.Add(tc.eta)
					if err != nil || !completion.Equal(want) {
						t.Errorf("completion %q: %v, want %s", stamp, err, want)
					}
				}
			})
		}
	}
}

func TestQueueHoldsAndWaitingStates(t *testing.T) {
	t.Parallel()
	for _, view := range []string{"radarr", "sonarr"} {
		for _, tc := range []struct {
			name, state, importState, reason, severity, tracked string
			wait, added                                         time.Duration
		}{
			{name: "mount waiting", state: "ready", importState: "waiting_mount", reason: "release not on the mount yet", severity: "ok", tracked: "importPending", wait: time.Minute},
			{name: "mount timeout", state: "ready", importState: "waiting_mount", reason: "release not on the mount yet", severity: "warning", tracked: "importPending", wait: 10 * time.Minute},
			{name: "title hold", state: "ready", importState: "waiting_mount", reason: "episode title hold: waiting for an episode title", severity: "warning", tracked: "importBlocked", wait: time.Minute},
			{name: "review", state: "ready", importState: "rejected", reason: "no video files in release", severity: "warning", tracked: "importBlocked"},
			{name: "zurg", state: "ready", importState: "rejected", reason: "zurg does not confirm the files are this download's", severity: "warning", tracked: "importBlocked"},
			{name: "not upgrade", state: "ready", importState: "rejected", reason: "not an upgrade for the existing files", severity: "warning", tracked: "importBlocked"},
			{name: "unmatched", state: "ready", importState: "rejected", reason: "no file matched the download's episodes", severity: "warning", tracked: "importBlocked"},
			{name: "sample", state: "failed", importState: "rejected", reason: "no file passed the sample guard", severity: "warning", tracked: "importBlocked"},
			{name: "mapping review", state: "ready", importState: "dry_run", reason: "mapping_not_reviewed: entry", severity: "warning", tracked: "importBlocked"},
			{name: "library gate", state: "ready", importState: "dry_run", reason: "library.enabled is false", severity: "warning", tracked: "importBlocked"},
			{name: "partial", state: "importing", importState: "partial", reason: "link failed; retry scheduled", severity: "warning", tracked: "importPending"},
			{name: "failed import", state: "failed", importState: "failed", reason: "could not create library link", severity: "error", tracked: "failedPending"},
			{name: "provider queued", state: "queued", reason: "waiting for a provider", severity: "warning", tracked: "downloading"},
			{name: "provider pending", state: "added", severity: "ok", tracked: "downloading", added: time.Minute},
			{name: "provider stalled", state: "added", severity: "warning", tracked: "downloading", added: time.Hour},
			{name: "provider failed stalled", state: "failed", reason: "stalled: not ready after the add", severity: "error", tracked: "failedPending"},
			{name: "ready", state: "ready", severity: "ok", tracked: "importPending"},
			{name: "importing", state: "importing", severity: "ok", tracked: "importing"},
		} {
			t.Run(view+"/"+tc.name, func(t *testing.T) {
				e := newEnv(t)
				tid, vid, key, eps := 1, 2, "movie-web", "[]"
				if view == "sonarr" {
					tid, vid, key, eps = 10, 10, "tv-2160p", "[101,102]"
				}
				errMsg := ""
				if tc.importState == "" {
					errMsg = tc.reason
				} else if tc.state == "failed" {
					errMsg = "library: " + tc.reason
				}
				e.exec(`INSERT INTO download(id,title_id,version_id,version_key,info_hash,release_title,indexer,episodes,state,error,created_at,updated_at,added_at) VALUES(1,?,?,?,'hash','Release','import',?,?,?,?,?,?)`, tid, vid, key, eps, tc.state, errMsg, ms(e.now), ms(e.now), ms(e.now.Add(-tc.added)))
				if tc.importState != "" {
					e.exec(`INSERT INTO library_import(download_id,state,last_error,waiting_since,created_at,updated_at) VALUES(1,?,?,?,?,?)`, tc.importState, tc.reason, ms(e.now.Add(-tc.wait)), ms(e.now), ms(e.now))
				}
				var queue struct{ Records []queueResource }
				e.call("GET", "/"+view+"/api/v3/queue", nil, 200, &queue)
				var details []queueResource
				e.call("GET", "/"+view+"/api/v3/queue/details", nil, 200, &details)
				want := 1
				if view == "sonarr" {
					want = 2
				}
				if len(queue.Records) != want || len(details) != want {
					t.Fatalf("queue %+v details %+v", queue, details)
				}
				for _, item := range append(queue.Records, details...) {
					if item.TrackedDownloadStatus != tc.severity || item.TrackedDownloadState != tc.tracked || len(item.StatusMessages) == 0 {
						t.Fatalf("item %+v", item)
					}
					b, _ := json.Marshal(item.StatusMessages)
					if tc.reason != "" && !strings.Contains(string(b), tc.reason) {
						t.Fatalf("reason absent: %s", b)
					}
				}
				var status queueStatusResource
				e.call("GET", "/"+view+"/api/v3/queue/status", nil, 200, &status)
				if status.Count != want || status.Warnings != (tc.severity == "warning") || status.Errors != (tc.severity == "error") {
					t.Fatalf("status %+v", status)
				}
				if tc.state == "failed" {
					e.call("DELETE", "/"+view+"/api/v3/queue/1?removeFromClient=false&blocklist=false", nil, 200, nil)
					e.call("GET", "/"+view+"/api/v3/queue", nil, 200, &queue)
					if len(queue.Records) != 0 || !strings.Contains(e.str(`SELECT error FROM download WHERE id = 1`), tc.reason) {
						t.Fatalf("failed item not dismissed or reason lost: %+v", queue)
					}
				}
			})
		}
	}
}
