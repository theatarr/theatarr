package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

const QualitySyncBudget = "settings-quality-sync"

// noRedirectKey marks a request whose redirects are refused (quality sync fetches).
type noRedirectKey struct{}

// QualityArchive uses the probe's destination checks and a separate, shared budget for upstream archives.
func (g *Gateway) QualityArchive(ctx context.Context, base *policy.Policy, archiveURL string) ([]byte, error) {
	res, err := g.qualityGet(ctx, base, archiveURL, 64<<20, "")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("gateway: quality archive request failed")
	}
	return io.ReadAll(res.Body)
}

var qualityCommitRE = regexp.MustCompile(`^[a-f0-9]{40}$`)

// QualityCommit asks GitHub's commits API for a branch's newest commit id (commitURL is
// https://api.github.com/repos/<owner>/<repo>/commits/<branch>). The sha media type makes the answer the bare 40-hex
// id, so the request stays a few bytes. It is charged to the same budget as the archives.
func (g *Gateway) QualityCommit(ctx context.Context, base *policy.Policy, commitURL string) (string, error) {
	res, err := g.qualityGet(ctx, base, commitURL, 4<<10, "application/vnd.github.sha")
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errors.New("gateway: quality commit request failed")
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	sha := strings.ToLower(strings.TrimSpace(string(b)))
	if !qualityCommitRE.MatchString(sha) {
		return "", errors.New("gateway: quality commit answer is not a commit id")
	}
	return sha, nil
}

func (g *Gateway) qualityGet(ctx context.Context, base *policy.Policy, rawURL string, maxBytes int64, accept string) (*http.Response, error) {
	return g.syncGet(ctx, base, QualitySyncBudget, rawURL, maxBytes, accept, &g.runtime.qualityOnce, &g.runtime.qualityBucket)
}

// syncGet fetches from an upstream repository (the quality sync's TRaSH archives, the cardigann definitions) as the
// indexer-class target key: VPN-guarded, through the probe's destination checks, charged to its own budget that
// every runtime generation shares, and following no redirect.
func (g *Gateway) syncGet(ctx context.Context, base *policy.Policy, key, rawURL string, maxBytes int64, accept string,
	once *sync.Once, shared **bucketState) (*http.Response, error) {
	p, err := base.Clone()
	if err != nil {
		return nil, err
	}
	if p.Indexers == nil {
		p.Indexers = map[string]*policy.Indexer{}
	}
	if p.Budgets == nil {
		p.Budgets = map[string]*policy.Budget{}
	}
	p.Indexers[key] = &policy.Indexer{Kind: "torznab", URL: rawURL, Privacy: policy.Privacy("public"), Budget: key}
	p.Budgets[key] = &policy.Budget{Requests: 2, Per: policy.Duration(time.Minute), Burst: 2, Daily: 24}
	target := Target{Class: ClassIndexer, Key: key, MaxBytes: maxBytes}
	pg, err := g.Probe(ctx, p, ProbeOptions{Target: target, Budget: key, Candidate: true})
	if err != nil {
		return nil, err
	}
	once.Do(func() { *shared = &bucketState{tokens: 2, last: g.now()} })
	pg.buckets[key].state = *shared
	// No redirect is followed: the commits API and codeload answer directly, and a hop could otherwise reach any host
	// of the live allowlist copied into the probe (an arr or Plex on the LAN).
	ctx = context.WithValue(ctx, noRedirectKey{}, true)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errors.New("gateway: bad URL")
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	return pg.Do(ctx, target, req)
}
