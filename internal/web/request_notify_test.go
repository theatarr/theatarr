package web

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

func TestRequestNotifications(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			e := newAddEnv(t, func(p *policy.Policy) {
				p.Notify = &policy.Notify{Enabled: enabled, Targets: map[string]*policy.NotifyTarget{
					"requests": {URL: "http://example.invalid/hook", Native: true},
				}}
			})
			ctx := context.Background()
			n, err := notify.New(notify.Config{Store: e.st, Policy: e.pol, Logger: e.web.log})
			if err != nil {
				t.Fatal(err)
			}
			var events []notify.Kind
			e.web.onRequest = func(ev RequestEvent) {
				if ev.Notification == nil {
					return
				}
				raw, _ := json.Marshal(ev)
				if strings.Contains(string(raw), "requesters") || strings.Contains(string(raw), "ann") {
					t.Errorf("private notification in SSE: %s", raw)
				}
				events = append(events, ev.Notification.Kind)
				n.Emit(ctx, *ev.Notification)
			}
			_, ann := e.user("ann", auth.RoleUser)
			_, bob := e.user("bob", auth.RoleUser)
			_, mgr := e.user("mgr", auth.RoleManager)
			var cr CreateResult
			e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 105, "name": "Epsilon"}, 201, &cr)
			id := cr.Request.ID
			path := fmt.Sprintf("%s/requests/%d", APIBase, id)
			e.callAs(bob, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 105}, 200, nil)
			var ap ApproveResult
			e.callAs(mgr, "POST", path+"/approve", nil, 200, &ap)
			tid := *ap.Request.TitleID
			now := store.Millis(addNow)
			e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer,
				state, created_at, updated_at) SELECT title_id, id, policy_key, 'failure', 'release', 'x',
				'failed', ?, ? FROM version WHERE title_id = ? AND policy_key = 'movie-web'`, now, now, tid)
			if err := e.web.syncRequests(ctx); err != nil {
				t.Fatal(err)
			}
			e.exec(`UPDATE version SET state = 'filled' WHERE title_id = ?`, tid)
			if err := e.web.syncRequests(ctx); err != nil {
				t.Fatal(err)
			}
			if err := e.web.syncRequests(ctx); err != nil {
				t.Fatal(err)
			}
			e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 106, "name": "Decline"}, 201, &cr)
			e.callAs(mgr, "POST", fmt.Sprintf("%s/requests/%d/decline", APIBase, cr.Request.ID), map[string]string{"reason": "No thanks"}, 200, nil)
			// Managers auto-approve: do not send a pending alert for the transient persisted state.
			e.callAs(mgr, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 107, "name": "Automatic"}, 201, nil)
			// Series progress must notify even when status stays approved, and only for requested episodes.
			e.exec(`UPDATE version SET monitored = 0 WHERE id = 10`)
			e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"title_id": 10, "seasons": []int{1}}, 201, &cr)
			e.callAs(mgr, "POST", fmt.Sprintf("%s/requests/%d/approve", APIBase, cr.Request.ID), nil, 200, nil)
			e.exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path)
				VALUES (999, 10, 10, 'sonarr', 999, '/episodes')`)
			e.exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (999, 103)`)
			if err := e.web.syncRequests(ctx); err != nil {
				t.Fatal(err)
			}
			if slices.Contains(events, notify.RequestPartial) {
				t.Fatal("unrequested season notified")
			}
			e.exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (999, 101)`)
			if err := e.web.syncRequests(ctx); err != nil {
				t.Fatal(err)
			}
			// The persistent claim, not process memory, prevents duplicates after restart.
			restarted, err := New(Config{Store: e.st, Auth: e.au, Policy: e.pol, Logger: e.web.log,
				Now: e.web.now, OnRequest: e.web.onRequest})
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.syncRequests(ctx); err != nil {
				t.Fatal(err)
			}
			e.exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (999, 102)`)
			e.exec(`UPDATE episode SET air_date = '2020-01-01' WHERE id IN (101, 102)`)
			e.exec(`UPDATE episode_version_state SET state = 'filled' WHERE episode_id IN (101, 102)`)
			e.exec(`UPDATE version SET state = 'filled' WHERE id = 10`)
			if err := restarted.syncRequests(ctx); err != nil {
				t.Fatal(err)
			}
			want := []notify.Kind{notify.RequestPending, notify.RequestPending, notify.RequestApproved, notify.RequestFailed,
				notify.RequestAvailable, notify.RequestPending, notify.RequestDeclined, notify.RequestAutoApproved,
				notify.RequestPending, notify.RequestApproved, notify.RequestPartial, notify.RequestAvailable}
			if !slices.Equal(events, want) {
				t.Fatalf("events: %v, want %v", events, want)
			}
			rows, err := e.st.Reader().Query(`SELECT event, payload, state, last_error FROM notify_delivery ORDER BY id`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			i := 0
			for rows.Next() {
				var event, raw, state, why string
				if err := rows.Scan(&event, &raw, &state, &why); err != nil {
					t.Fatal(err)
				}
				if i >= len(want) || event != "theatarr."+string(want[i]) {
					t.Fatalf("delivery %d: %s", i, event)
				}
				if enabled && state != "pending" || !enabled && (state != "dropped" || why != "notify.enabled is false") {
					t.Errorf("state %s: %s", state, why)
				}
				var b struct {
					Notification string `json:"notification_type"`
					Subject      string `json:"subject"`
					Message      string `json:"message"`
					Request      struct {
						ID       int64                   `json:"request_id"`
						Users    []notify.RequestUser    `json:"requesters"`
						Versions []notify.RequestVersion `json:"versions"`
						Seasons  []int                   `json:"seasons"`
					} `json:"request"`
				}
				if err := json.Unmarshal([]byte(raw), &b); err != nil {
					t.Fatal(err)
				}
				if b.Request.ID == 0 || len(b.Request.Users) == 0 || len(b.Request.Versions) == 0 || b.Request.Versions[0].Label == "" || b.Subject == "" || b.Notification == "" {
					t.Fatalf("payload: %s", raw)
				}
				if want[i] == notify.RequestApproved && b.Subject == "Epsilon" && len(b.Request.Users) != 2 {
					t.Errorf("joined requesters missing: %s", raw)
				}
				if want[i] == notify.RequestDeclined && b.Message != "No thanks" {
					t.Errorf("decline reason: %s", raw)
				}
				if want[i] == notify.RequestPartial && (!slices.Equal(b.Request.Seasons, []int{1}) || b.Notification != "MEDIA_AVAILABLE") {
					t.Errorf("partial: %s", raw)
				}
				i++
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if i != len(want) {
				t.Fatalf("deliveries: %d, want %d", i, len(want))
			}
		})
	}
}

// Request ready on a Discord target: "<Title> (year) is ready in Plex, requested by <name>", mentioning the requester
// whose account has a Discord id and nobody else; the id never reaches the SSE event or the native payload.
func TestRequestReadyDiscord(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
		p.Notify = &policy.Notify{Enabled: true, Targets: map[string]*policy.NotifyTarget{
			"chan":     {URL: "https://discord.com/api/webhooks/1/token", Format: policy.NotifyFormatDiscord},
			"requests": {URL: "http://example.invalid/hook", Native: true},
		}}
	})
	ctx := context.Background()
	n, err := notify.New(notify.Config{Store: e.st, Policy: e.pol, Logger: e.web.log})
	if err != nil {
		t.Fatal(err)
	}
	e.web.onRequest = func(ev RequestEvent) {
		raw, _ := json.Marshal(ev)
		if strings.Contains(string(raw), "123456789012345678") {
			t.Errorf("discord id in SSE: %s", raw)
		}
		if ev.Notification != nil {
			n.Emit(ctx, *ev.Notification)
		}
	}
	annID, ann := e.user("ann", auth.RoleUser)
	_, bob := e.user("bob", auth.RoleUser)
	_, mgr := e.user("mgr", auth.RoleManager)
	e.exec(`UPDATE web_user SET discord_id = '123456789012345678', display_name = 'Ann' WHERE id = ?`, annID)
	var cr CreateResult
	e.callAs(ann, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 105, "name": "Epsilon"}, 201, &cr)
	e.callAs(bob, "POST", APIBase+"/requests", map[string]any{"kind": "movie", "tmdb_id": 105}, 200, nil)
	var ap ApproveResult
	e.callAs(mgr, "POST", fmt.Sprintf("%s/requests/%d/approve", APIBase, cr.Request.ID), nil, 200, &ap)
	e.exec(`UPDATE version SET state = 'filled' WHERE title_id = ?`, *ap.Request.TitleID)
	if err := e.web.syncRequests(ctx); err != nil {
		t.Fatal(err)
	}
	var name string
	var year int
	if err := e.st.Reader().QueryRow(`SELECT name, COALESCE(year, 0) FROM title WHERE id = ?`, *ap.Request.TitleID).
		Scan(&name, &year); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.Reader().Query(`SELECT target, payload FROM notify_delivery ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var discord []string
	for rows.Next() {
		var target, raw string
		if err := rows.Scan(&target, &raw); err != nil {
			t.Fatal(err)
		}
		if target == "policy:chan" {
			discord = append(discord, raw)
		} else if strings.Contains(raw, "123456789012345678") {
			t.Errorf("discord id in %s: %s", target, raw)
		}
	}
	if len(discord) != 1 {
		t.Fatalf("discord messages %v (only ready)", discord)
	}
	var m struct {
		Content  string `json:"content"`
		Mentions struct {
			Parse []string `json:"parse"`
			Users []string `json:"users"`
		} `json:"allowed_mentions"`
	}
	if err := json.Unmarshal([]byte(discord[0]), &m); err != nil {
		t.Fatal(err)
	}
	title := name
	if year > 0 {
		title = fmt.Sprintf("%s (%d)", name, year)
	}
	if want := "**" + title + "** is ready in Plex, requested by Ann (<@123456789012345678>) and bob"; m.Content != want {
		t.Errorf("content %q, want %q", m.Content, want)
	}
	if len(m.Mentions.Parse) != 0 || !slices.Equal(m.Mentions.Users, []string{"123456789012345678"}) {
		t.Errorf("mentions %+v", m.Mentions)
	}
}
