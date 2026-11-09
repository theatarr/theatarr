package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// The last request-count flush comes after the request producers stop: a search that completes during graceful
// shutdown (after the serve context ended) still has its count stored.
func TestRequestFlushAfterProducersStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pol := &policy.Policy{Indexers: map[string]*policy.Indexer{
		"dmm": {Kind: "dmm", URL: "http://dmm.test", Privacy: policy.PrivacyPublic}}}
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	gw, err := gateway.New(ctx, pol, gateway.Options{Store: st, DialContext: dial})
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	stop := startRequestFlush(serveCtx, gw)
	cancel()
	// A handler still running after the signal finishes its search.
	resp, err := gw.Get(gateway.WithPurpose(ctx, gateway.PurposeWanted), gateway.Target{Class: gateway.ClassIndexer,
		Key: "dmm"}, "http://dmm.test/api")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	stop()
	var n int
	if err := st.Reader().QueryRow(`SELECT COALESCE(SUM(count), 0) FROM gateway_indexer_request WHERE indexer = 'dmm'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("stored count %d, want 1", n)
	}
}
