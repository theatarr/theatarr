package router

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

type detachedVPNGateway struct {
	first  chan struct{}
	resume chan struct{}
	second chan struct{}
}

func (g *detachedVPNGateway) Do(ctx context.Context, target gateway.Target, req *http.Request) (*http.Response, error) {
	if target.Key == "vpn" {
		<-g.first
		return nil, &gateway.Refused{Reason: gateway.ReasonVPN}
	}
	switch req.URL.Query().Get("filter") {
	case "alID=1":
		close(g.first)
		select {
		case <-g.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case "alID=2":
		close(g.second)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"page":1,"perPage":100,"totalItems":0,"totalPages":0,"items":[]}`))}, nil
}
func TestVPNLeavesDetachedSiblingFetching(t *testing.T) {
	g := &detachedVPNGateway{first: make(chan struct{}), resume: make(chan struct{}), second: make(chan struct{})}
	pol := &policy.Policy{Profiles: map[string]*policy.Profile{"p": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}},
		Versions: map[string]*policy.VersionPolicy{"v": {Kind: policy.KindMovie, Profile: "p", Indexers: []string{"sd", "vpn"}}},
		Indexers: map[string]*policy.Indexer{"sd": {Kind: "seadex", URL: "http://example.test"}, "vpn": {Kind: "seadex", URL: "http://example.test"}}}
	eng, err := decide.New(pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := indexer.New(pol, g, indexer.Options{CacheTTL: -1, SearchTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetMetadata(indexer.Metadata{AniList: indexer.AniListFunc(func(context.Context, indexer.Query) ([]int, error) { return []int{1, 2}, nil })})
	title := decide.Title{ID: 1, Kind: policy.KindMovie, Name: "Film", TMDb: 1}
	v := decide.Version{Key: "v", Policy: pol.Versions["v"]}
	done := make(chan error, 1)
	go func() {
		_, err := SearchRequests(context.Background(), eng, svc, title, []SearchTarget{{ID: "a", Version: v, Title: &title}})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, gateway.ErrVPNRequired) {
			t.Fatalf("expected VPN refusal, got %v", err)
		}
	case <-time.After(time.Second):
		close(g.resume)
		t.Fatal("search did not return")
	}
	close(g.resume)
	select {
	case <-g.second:
		t.Fatal("after VPN refusal returned, unshared sibling started second upstream request")
	case <-time.After(300 * time.Millisecond):
	}
}
