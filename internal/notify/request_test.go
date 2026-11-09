package notify

import (
	"context"
	"testing"
)

func TestRequestPayloadAndTargets(t *testing.T) {
	t.Parallel()
	e := newNEnv(t, true)
	ctx := context.Background()
	r := &Request{ID: 7, Name: "The Show", Kind: "series", TMDbID: 3, TVDbID: 4, Seasons: []int{1, 2},
		Users:    []RequestUser{{ID: 2, Username: "ann", DisplayName: "Ann"}},
		Versions: []RequestVersion{{Key: "tv-web", Label: "TV Web"}}}
	for _, tc := range []struct {
		kind        Kind
		name, event string
	}{
		{RequestPending, "MEDIA_PENDING", "New Series Request"},
		{RequestApproved, "MEDIA_APPROVED", "Series Request Approved"},
		{RequestAutoApproved, "MEDIA_AUTO_APPROVED", "Series Request Automatically Approved"},
		{RequestDeclined, "MEDIA_DECLINED", "Series Request Declined"},
		{RequestPartial, "MEDIA_AVAILABLE", "Series Partially Available"},
		{RequestAvailable, "MEDIA_AVAILABLE", "Series Now Available"},
		{RequestFailed, "MEDIA_FAILED", "Series Request Failed"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			ds, err := e.n.deliveries(ctx, Event{Kind: tc.kind, Request: r})
			if err != nil {
				t.Fatal(err)
			}
			// Requests go only to native policy targets; never through arr payloads or grab-only targets.
			if len(ds) != 1 || ds[0].target != "policy:automation" || ds[0].view != "" {
				t.Fatalf("deliveries: %+v", ds)
			}
			b := ds[0].body.(map[string]any)
			if b["notification_type"] != tc.name || b["event"] != tc.event || b["subject"] != r.Name {
				t.Fatalf("payload: %+v", b)
			}
			media := b["media"].(map[string]any)
			if media["media_type"] != "tv" || media["tmdbId"] != int64(3) || media["tvdbId"] != int64(4) {
				t.Errorf("media: %+v", media)
			}
			req := b["request"].(map[string]any)
			if req["request_id"] != int64(7) || req["requestedBy_username"] != "Ann" {
				t.Errorf("request: %+v", req)
			}
		})
	}
	e.n.set.Targets["automation"].Versions = []string{"movie-web"}
	ds, err := e.n.deliveries(ctx, Event{Kind: RequestPending, Request: r})
	if err != nil || len(ds) != 0 {
		t.Fatalf("excluded version: %+v %v", ds, err)
	}
	e.n.set.Targets["automation"].Versions = []string{"tv-web"}
	e.n.Emit(ctx, Event{Kind: RequestPending, Request: r})
	e.doer.status = 503
	if sent, err := e.n.SendDue(ctx, 10); err != nil || sent != 0 {
		t.Fatalf("retry: %d %v", sent, err)
	}
	var state string
	var attempts int
	if err := e.st.Reader().QueryRow(`SELECT state, attempts FROM notify_delivery`).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || attempts != 1 {
		t.Errorf("retry state: %s %d", state, attempts)
	}
	e.now = e.now.Add(backoff(1))
	e.doer.status = 200
	if sent, err := e.n.SendDue(ctx, 10); err != nil || sent != 1 {
		t.Fatalf("send: %d %v", sent, err)
	}
}

// A label that already names its key (two copies with the same words, "4K · tv-2160p-theatarr") is shown once.
func TestRequestCopiesNameEachKeyOnce(t *testing.T) {
	t.Parallel()
	b := map[string]any{}
	requestPayload(b, Event{Kind: RequestPending, Request: &Request{Kind: "series", Versions: []RequestVersion{
		{Key: "tv-2160p-theatarr", Label: "4K · tv-2160p-theatarr"}, {Key: "tv-web", Label: "TV Web"}, {Key: "anime-tv"}}}})
	extra := b["extra"].([]map[string]string)
	if got, want := extra[1]["value"], "4K · tv-2160p-theatarr, TV Web (tv-web), anime-tv"; got != want {
		t.Fatalf("copies %q, want %q", got, want)
	}
}
