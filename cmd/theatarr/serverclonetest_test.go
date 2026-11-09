package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/nowplaying"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestRcloneTestSharesTrackerBudget(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"version":"v1.70.0"}`)
	}))
	defer server.Close()
	p := &policy.Policy{Library: &policy.Library{Mount: policy.LibraryMount{RcloneRC: &policy.RcloneRC{URL: server.URL, Budget: "rc"}}}, Budgets: map[string]*policy.Budget{"rc": {Requests: 1, Per: policy.Duration(time.Hour), Burst: 1}}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	live, err := gateway.New(context.Background(), p, gateway.Options{Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	tracker := nowplaying.RC{URL: server.URL, GW: live}
	if _, err := tracker.Version(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	candidate, err := rcloneTestGateway(nil, live, log)(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	test := nowplaying.RC{URL: server.URL, GW: candidate}
	if _, err := test.Version(ctx); err == nil {
		t.Fatal("test bypassed the tracker budget")
	}
	if calls.Load() != 1 {
		t.Fatalf("requests: %d", calls.Load())
	}
}

func TestRcloneTestReachesUnsavedURL(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"version":"v1.70.0"}`)
	}))
	defer server.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// The live gateway has no rclone_rc yet: the owner is testing before saving.
	live, err := gateway.New(context.Background(), &policy.Policy{}, gateway.Options{Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Library: &policy.Library{Mount: policy.LibraryMount{RcloneRC: &policy.RcloneRC{URL: server.URL}}}}
	candidate, err := rcloneTestGateway(nil, live, log)(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	test := nowplaying.RC{URL: server.URL, GW: candidate}
	if v, err := test.Version(context.Background()); err != nil || v != "v1.70.0" {
		t.Fatalf("version %q, err %v", v, err)
	}
}
