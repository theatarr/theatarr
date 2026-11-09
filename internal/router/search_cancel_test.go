package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestCancelDoesNotLaunchQueuedSeaDexQueries(t *testing.T) {
	var calls atomic.Int32
	first, second, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			close(first)
			<-release
		} else if n == 2 {
			close(second)
		}
		fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":0,"totalPages":0,"items":[]}`)
	}))
	defer srv.Close()
	pol := &policy.Policy{Profiles: map[string]*policy.Profile{"web": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}},
		Versions: map[string]*policy.VersionPolicy{"web": {Kind: policy.KindMovie, Profile: "web", Indexers: []string{"sd"}}},
		Indexers: map[string]*policy.Indexer{"sd": {Kind: "seadex", URL: srv.URL, Privacy: policy.PrivacyPublic}}}
	eng, err := decide.New(pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := indexer.New(pol, &searchGateway{}, indexer.Options{CacheTTL: -1, SearchTimeout: time.Second * 3})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetMetadata(indexer.Metadata{AniList: indexer.AniListFunc(func(context.Context, indexer.Query) ([]int, error) { return []int{1}, nil })})
	a := decide.Title{ID: 1, Kind: policy.KindMovie, Name: "First", TMDb: 1}
	b := decide.Title{ID: 2, Kind: policy.KindMovie, Name: "Second", TMDb: 2}
	v := decide.Version{Key: "web", Policy: pol.Versions["web"]}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := SearchRequests(ctx, eng, svc, a, []SearchTarget{{ID: "a", Version: v, Title: &a}, {ID: "b", Version: v, Title: &b}})
		done <- err
	}()
	select {
	case <-first:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("first query never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("search caller did not return")
	}
	var launched bool
	select {
	case <-second:
		launched = true
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if launched {
		t.Fatal("second distinct query started upstream after the caller cancelled")
	}
}

type vpnRefusingGateway struct {
	searchGateway
	first <-chan struct{}
}

func (g *vpnRefusingGateway) Do(ctx context.Context, target gateway.Target, req *http.Request) (*http.Response, error) {
	if target.Key == "vpn" {
		<-g.first
		return nil, &gateway.Refused{Reason: gateway.ReasonVPN}
	}
	return g.searchGateway.Do(ctx, target, req)
}
func TestVPNRefusalCancelsQueuedSiblingQueries(t *testing.T) {
	t.Parallel()
	first, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(first)
			<-release
		}
		fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":0,"totalPages":0,"items":[]}`)
	}))
	defer srv.Close()
	defer close(release)
	pol := &policy.Policy{Profiles: map[string]*policy.Profile{"p": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}}, Versions: map[string]*policy.VersionPolicy{"v": {Kind: policy.KindMovie, Profile: "p", Indexers: []string{"sd", "vpn"}}}, Indexers: map[string]*policy.Indexer{"sd": {Kind: "seadex", URL: srv.URL}, "vpn": {Kind: "seadex", URL: srv.URL}}}
	eng, err := decide.New(pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := indexer.New(pol, &vpnRefusingGateway{first: first}, indexer.Options{CacheTTL: -1, SearchTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetMetadata(indexer.Metadata{AniList: indexer.AniListFunc(func(context.Context, indexer.Query) ([]int, error) { return []int{1}, nil })})
	a := decide.Title{ID: 1, Kind: policy.KindMovie, Name: "First", TMDb: 1}
	b := a
	b.ID = 2
	b.TMDb = 2
	v := decide.Version{Key: "v", Policy: pol.Versions["v"]}
	_, err = SearchRequests(context.Background(), eng, svc, a, []SearchTarget{{ID: "a", Version: v, Title: &a}, {ID: "b", Version: v, Title: &b}})
	if !errors.Is(err, gateway.ErrVPNRequired) {
		t.Fatalf("want VPN refusal: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("started %d sibling queries; want 1", got)
	}
}
