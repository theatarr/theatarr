package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

func TestRequestDetailRoles(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	_, user := e.user("requester", auth.RoleUser)
	_, manager := e.user("manager", auth.RoleManager)
	_, other := e.user("other", auth.RoleUser)
	var created CreateResult
	e.callAs(user, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 105, "name": "Epsilon", "note": "please"}, 201, &created)
	path := fmt.Sprintf("%s/requests/%d", APIBase, created.Request.ID)
	var approved ApproveResult
	e.callAs(manager, "POST", path+"/approve", nil, 200, &approved)
	tid := *approved.Request.TitleID
	now := store.Millis(addNow)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		size_bytes, provider, state, progress, error, created_at, updated_at)
		SELECT title_id, id, policy_key, 'private-hash', 'private-release', 'private-indexer', 'WEBDL-2160p',
		123456789, 'private-provider', 'downloading', 42, '/private/path', ?, ? FROM version
		WHERE title_id = ? AND policy_key = 'movie-web'`, now, now, tid)
	for _, tc := range []struct {
		name, key string
		status    int
	}{{"user", user, 200}, {"manager", manager, 200}, {"admin", e.key, 200}, {"other user's request", other, 404}} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.status != 200 {
				e.callAs(tc.key, "GET", path, nil, tc.status, nil)
				return
			}
			var payload map[string]any
			e.callAs(tc.key, "GET", path, nil, 200, &payload)
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var d RequestDetail
			if err := json.Unmarshal(raw, &d); err != nil {
				t.Fatal(err)
			}
			if len(d.Progress) != 2 || d.Progress[1].Words != "Downloading (42%)" || d.Progress[1].Updated == nil || d.Note != "please" || d.DecidedBy == nil {
				t.Fatalf("detail: %+v", d)
			}
			if d.ApprovedAtCreation {
				t.Error("manager decision is not auto-approval")
			}
			for _, secret := range []string{"private-", "/private/path", "123456789", "WEBDL", `"provider"`, `"size"`, `"quality"`, `"error"`} {
				if strings.Contains(string(raw), secret) {
					t.Errorf("detail contains %q: %s", secret, raw)
				}
			}
		})
	}
}

func TestRequestDetailApprovalAndBranches(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, role, action, words string
		active, router, grabOff   bool
	}{
		{name: "pending shadow", role: auth.RoleUser, words: "Waiting for approval"},
		{name: "auto shadow", role: auth.RoleManager, words: "Automatic downloads are paused"},
		{name: "router paused", role: auth.RoleManager, active: true, words: "Automatic downloads are paused"},
		{name: "version grab off", role: auth.RoleManager, active: true, router: true, grabOff: true, words: "Automatic downloads are paused"},
		{name: "active waiting", role: auth.RoleManager, active: true, router: true, words: "Waiting for a release that matches"},
		{name: "declined", role: auth.RoleUser, action: "decline", words: "Request declined"},
		{name: "canceled", role: auth.RoleUser, action: "cancel", words: "Request canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAddEnv(t, func(p *policy.Policy) {
				p.Router = &policy.Router{Enabled: tc.router}
				if tc.active {
					for _, v := range p.Versions {
						v.Mode, v.Grab = policy.ModeActive, !tc.grabOff
					}
				}
			})
			uid, key := e.user("requester", tc.role)
			var cr CreateResult
			e.callAs(key, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 105, "name": "Epsilon"}, 201, &cr)
			path := fmt.Sprintf("%s/requests/%d", APIBase, cr.Request.ID)
			switch tc.action {
			case "decline":
				e.call("POST", path+"/decline", map[string]any{"reason": "Not this time"}, 200, nil)
			case "cancel":
				e.callAs(key, "DELETE", path, nil, 204, nil)
			}
			var d RequestDetail
			e.callAs(key, "GET", path, nil, 200, &d)
			if d.ApprovedAtCreation != cr.Approved || len(d.Progress) != 2 || d.Progress[0].Words != tc.words {
				t.Fatalf("detail: %+v progress: %+v", d, d.Progress)
			}
			paused := !(tc.active && tc.router && !tc.grabOff)
			if strings.Contains(d.Progress[0].Approval, "automatic downloads are paused") != paused {
				t.Errorf("approval: %s", d.Progress[0].Approval)
			}
			if tc.action != "" && (d.DecidedAt == nil || d.DecidedBy == nil) {
				t.Error("branch lost decision attribution")
			}
			if tc.action == "decline" && d.Reason != "Not this time" {
				t.Error("decline lost note")
			}
			if tc.action == "cancel" && d.DecidedBy.ID != uid {
				t.Error("cancel lost actor")
			}
		})
	}
}

func TestRequestDetailRequestedSeasons(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, nil)
	_, key := e.user("requester", auth.RoleUser)
	e.exec(`UPDATE version SET monitored = 0 WHERE id = 10`)
	var cr CreateResult
	e.callAs(key, "POST", APIBase+"/requests", map[string]any{"title_id": 10, "seasons": []int{1}}, 201, &cr)
	path := fmt.Sprintf("%s/requests/%d", APIBase, cr.Request.ID)
	e.call("POST", path+"/approve", nil, 200, nil)
	now := store.Millis(addNow)
	// Season 2's download must not describe a season 1 request.
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		episodes, created_at, updated_at) SELECT title_id, id, policy_key, 'other', 'Other season', 'x', 'downloading',
		'[103]', ?, ? FROM version WHERE title_id = 10`, now, now)
	var d RequestDetail
	e.callAs(key, "GET", path, nil, 200, &d)
	if len(d.Progress) != 1 || d.Progress[0].Total == nil || *d.Progress[0].Total != 2 || *d.Progress[0].Have != 0 || d.Versions[0].Available || d.Progress[0].Words == "Downloading (0%)" {
		t.Fatalf("season scope: %+v %+v", d.Progress, d.Versions)
	}
	// A partial season is not available; completing the requested season is enough even with season 2 missing.
	e.exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path)
		VALUES (999, 10, 10, 'sonarr', 999, '/private/episode')`)
	e.exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (999, 101)`)
	d = RequestDetail{}
	e.callAs(key, "GET", path, nil, 200, &d)
	if *d.Progress[0].Have != 1 || d.Versions[0].Available {
		t.Fatalf("partial season: %+v", d)
	}
	e.exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (999, 102)`)
	d = RequestDetail{}
	e.callAs(key, "GET", path, nil, 200, &d)
	if *d.Progress[0].Have != 2 || !d.Versions[0].Available || d.Progress[0].Words != "Available" {
		t.Fatalf("requested season available: %+v", d)
	}
}

func TestRequestDetailConcurrentDownloads(t *testing.T) {
	e := newAddEnv(t, nil)
	uid, key := e.user("requester", auth.RoleManager)
	var cr CreateResult
	e.callAs(key, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 105, "name": "Epsilon"}, 201, &cr)
	path := fmt.Sprintf("%s/requests/%d", APIBase, cr.Request.ID)
	now := store.Millis(addNow)
	// Real auto-approval finishes later than creation; it still skips the approval wait after account edits.
	e.exec(`UPDATE request SET decided_at = ? WHERE id = ?`, now+100, cr.Request.ID)
	e.exec(`UPDATE web_user SET role = 'user', auto_approve = 0 WHERE id = ?`, uid)
	for i, state := range []string{"failed", "downloading", "ready"} {
		e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
			progress, created_at, updated_at) SELECT title_id, id, policy_key, ?, 'release', 'x', ?, 30, ?, ?
			FROM version WHERE title_id = ? AND policy_key = 'movie-web'`, fmt.Sprintf("hash%d", i), state, now, now+int64(i), *cr.Request.TitleID)
	}
	var d RequestDetail
	e.callAs(key, "GET", path, nil, 200, &d)
	if !d.ApprovedAtCreation || len(d.Progress) != 3 || d.Progress[1].Words != "Downloading (30%)" || d.Progress[2].Words != "Download complete, waiting to be linked" {
		t.Fatalf("concurrent: %+v %+v", d, d.Progress)
	}
	// Once the acquisitions fail, show the latest failure in Activity's words, without its diagnostic.
	e.exec(`UPDATE download SET state = 'failed', error = 'secret diagnostic'`)
	d = RequestDetail{}
	e.callAs(key, "GET", path, nil, 200, &d)
	if len(d.Progress) != 2 || d.Progress[1].Words != "Download failed" {
		t.Fatalf("failed: %+v", d.Progress)
	}
}
