package web

import (
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/shadow"
)

func TestShadowLiveReport(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	at := time.Now().Add(-time.Hour).UnixMilli()
	for _, row := range []struct{ bucket, reason, ours, arr string }{
		{"agree", "same_release", "Alpha.2160p.WEB", "Alpha.2160p.WEB"},
		{"arr_grabbed_we_wouldnt", "below_min_score", "", "Alpha.1080p.YTS"},
		{"we_grab_arr_didnt", "theatarr_only", "Alpha.2160p.Remux", ""},
	} {
		if _, err := e.st.Writer().Exec(`INSERT INTO shadow_diff(at,title_id,version_key,bucket,reason,detail,
 our_release,arr_release,compare_instance,compare_key,created_at)
 VALUES(?,1,'movie-web',?,?,'live: recorded Theatarr decision: rejected; below minimum',?,?,'comparison',?,0)`, at, row.bucket, row.reason, row.ours, row.arr, row.reason); err != nil {
			t.Fatal(err)
		}
	}
	key := e.apiKey()
	var sum shadow.Summary
	if code := e.getKey(APIBase+"/shadow/summary", key, &sum); code != 200 || sum.Grabs != 3 || sum.Agreement == nil || *sum.Agreement != 1.0/3 ||
		len(sum.Versions) != 1 || sum.Versions[0].Grabs != 3 {
		t.Fatalf("live summary %d %+v", code, sum)
	}
	var list struct {
		Items []shadow.DiffRow `json:"items"`
		Total int              `json:"total"`
	}
	if code := e.getKey(APIBase+"/shadow/diffs?reason=below_min_score", key, &list); code != 200 || list.Total != 1 {
		t.Fatalf("live diffs %d %+v", code, list)
	}
	id := itoa(list.Items[0].ID)
	var d shadow.DiffDetail
	if code := e.getKey(APIBase+"/shadow/diffs/"+id, key, &d); code != 200 || d.Basis != "live" || d.Instance != "comparison" ||
		d.Grab != nil || d.Pick != nil || d.ArrRelease != "Alpha.1080p.YTS" || !strings.Contains(d.Detail, "recorded Theatarr decision") {
		t.Fatalf("live detail %d %+v", code, d)
	}
}

// The shadow report over a few stored rows: summary, filtered diffs, one diff with both decisions, and candidate formats.
func TestShadowReport(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	at := time.Now().Add(-time.Hour).UnixMilli()
	arr := `{"release":"Alpha.2020.2160p.WEB-DL-FLUX","seeders":-1,"arr":true,"accepted":true,"quality":"WEBDL-2160p","score":1700,"upgrade":false}`
	pick := `{"release":"Alpha.2020.2160p.BluRay.REMUX-GRP","indexer":"prowlarr","seeders":20,"accepted":true,"quality":"Remux-2160p","score":1950,"formats":[{"name":"Remux Tier 01","score":1950}],"upgrade":false}`
	for _, q := range []string{
		`INSERT INTO shadow_grab (id, instance, history_id, title_id, version_key, grabbed_at, release_title, arr_quality,
			arr_score, state, searched, searched_at, result_count, accepted_count, arr_decision, pick, results, created_at, updated_at)
			VALUES (1, 'radarr_4k_debrid', 10, 1, 'movie-remux', ?, 'Alpha.2020.2160p.WEB-DL-FLUX', 'WEBDL-2160p', 1700, 'done', 1, ?, 1, 1, '` + arr + `', '` + pick + `', '[` + pick + `]', 0, 0)`,
		`INSERT INTO shadow_diff (at, title_id, version_key, bucket, reason, detail, searched, grab_id, our_release, our_quality,
			our_score, arr_release, arr_quality, arr_score, created_at) VALUES (?, 1, 'movie-remux', 'we_grab_another',
			'better_quality', 'we would grab the remux', 1, 1, 'Alpha.2020.2160p.BluRay.REMUX-GRP', 'Remux-2160p', 1950,
			'Alpha.2020.2160p.WEB-DL-FLUX', 'WEBDL-2160p', 1700, 0)`,
		`INSERT INTO shadow_diff (at, title_id, version_key, bucket, reason, detail, searched, created_at)
			VALUES (?, 1, 'movie-web', 'agree', 'same_release', 'same', 1, 0)`,
		// An RSS grab our RSS read picked too; our later search found the remux.
		`INSERT INTO shadow_grab (id, instance, history_id, title_id, version_key, grabbed_at, release_title, release_source,
			arr_quality, state, searched, searched_at, arr_decision, pick, created_at, updated_at)
			VALUES (2, 'radarr_4k_debrid', 11, 1, 'movie-remux', ?, 'Alpha.2020.2160p.WEB-DL-FLUX', 'Rss', 'WEBDL-2160p', 'done', 1, ?,
			'` + arr + `', '` + pick + `', 0, 0)`,
		`INSERT INTO shadow_diff (at, title_id, version_key, bucket, reason, detail, searched, grab_id, our_release,
			arr_release, created_at, search_better) VALUES (?, 1, 'movie-remux', 'agree', 'same_rss_pick', 'an RSS grab', 1, 2,
			'Alpha.2020.2160p.WEB-DL-FLUX', 'Alpha.2020.2160p.WEB-DL-FLUX', 0,
			'{"release":"Alpha.2020.2160p.BluRay.REMUX-GRP","indexer":"prowlarr","quality":"Remux-2160p","score":1950}')`,
		// A hand grab we accept: listed, outside the ratio.
		`INSERT INTO shadow_diff (at, title_id, version_key, bucket, reason, detail, searched, created_at)
			VALUES (?, 1, 'movie-web', 'agree', 'manual_grab_accepted', 'the arr grabbed it by hand; we accept it', 0, 0)`,
		// The router's dry run for grab 1.
		`INSERT INTO router_plan (id, title_id, version_key, mode, outcome, release_title, indexer, provider, steps,
			source, created_at) VALUES (7, 1, 'movie-remux', 'shadow', 'would_add', 'Alpha.2020.2160p.BluRay.REMUX-GRP',
			'prowlarr', 'rd', '[{"release":"Alpha.2020.2160p.BluRay.REMUX-GRP","provider":"rd","outcome":"would_add","code":"add","message":"would add the bare magnet"}]',
			'shadow.grab', ?)`,
		`UPDATE shadow_grab SET router_plan_id = 7 WHERE id = 1 AND ? > 0`,
	} {
		args := []any{at}
		if strings.Contains(q, "searched_at") {
			args = append(args, at)
		}
		if _, err := e.st.Writer().Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	key := e.apiKey()
	var sum struct {
		Agreement *float64       `json:"agreement"`
		Grabs     int            `json:"grabs"`
		Buckets   map[string]int `json:"buckets"`
		Bar       float64        `json:"bar"`
		Better    int            `json:"search_found_better"`
		Hand      struct {
			Grabs, Accepted, Rejected int
		} `json:"hand_grabs"`
	}
	if code := e.getKey(APIBase+"/shadow/summary", key, &sum); code != 200 || sum.Grabs != 3 || sum.Agreement == nil ||
		*sum.Agreement != 2.0/3 || sum.Buckets["we_grab_another"] != 1 || sum.Bar != 0.95 || sum.Better != 1 ||
		sum.Hand.Grabs != 1 || sum.Hand.Accepted != 1 || sum.Hand.Rejected != 0 {
		t.Errorf("summary %d %+v", code, sum)
	}
	var list struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if code := e.getKey(APIBase+"/shadow/diffs?bucket=we_grab_another", key, &list); code != 200 || list.Total != 1 ||
		list.Items[0]["reason"] != "better_quality" || list.Items[0]["title"] != "Alpha" {
		t.Errorf("diffs %d %+v", code, list)
	}
	if code := e.getKey(APIBase+"/shadow/diffs?bucket=nope", key, &list); code != 400 {
		t.Errorf("bad bucket: %d", code)
	}
	var better struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if code := e.getKey(APIBase+"/shadow/diffs?search_found_better=1", key, &better); code != 200 || better.Total != 1 ||
		better.Items[0]["basis"] != "rss" || better.Items[0]["reason"] != "same_rss_pick" ||
		better.Items[0]["search_found_better"].(map[string]any)["score"] != 1950.0 {
		t.Errorf("search_found_better diffs %d %+v", code, better)
	}
	if code := e.getKey(APIBase+"/shadow/diffs?search_found_better=maybe", key, &better); code != 400 {
		t.Errorf("bad search_found_better: %d", code)
	}
	id := int(list.Items[0]["id"].(float64))
	if list.Items[0]["router_plan"] != "would add Alpha.2020.2160p.BluRay.REMUX-GRP on rd" {
		t.Errorf("diff row plan %v", list.Items[0]["router_plan"])
	}
	var one struct {
		Grab struct {
			Pick    map[string]any   `json:"pick"`
			Results []map[string]any `json:"results"`
			Plan    map[string]any   `json:"router_plan"`
		} `json:"grab"`
	}
	if code := e.getKey(APIBase+"/shadow/diffs/"+itoa(int64(id)), key, &one); code != 200 || one.Grab.Pick["score"] != 1950.0 ||
		len(one.Grab.Results) != 1 || one.Grab.Plan["summary"] != "would add Alpha.2020.2160p.BluRay.REMUX-GRP on rd" ||
		one.Grab.Plan["mode"] != "shadow" {
		t.Errorf("diff %d %+v", code, one)
	}
	formats := one.Grab.Results[0]["formats"].([]any)
	if len(formats) != 1 || formats[0].(map[string]any)["name"] != "Remux Tier 01" || formats[0].(map[string]any)["score"] != 1950.0 {
		t.Errorf("candidate formats: %v", formats)
	}
	if code := e.getKey(APIBase+"/shadow/diffs/999", key, &one); code != 404 {
		t.Errorf("missing diff: %d", code)
	}
}
