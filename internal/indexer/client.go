package indexer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

// Gateway is what the adapters need from internal/gateway (*gateway.Gateway implements it).
type Gateway interface {
	Do(ctx context.Context, t gateway.Target, req *http.Request) (*http.Response, error)
}

// Searcher is one release source.
type Searcher interface {
	// Key is the policy key; Kind the policy kind.
	Key() string
	Kind() string
	// Supports reports whether the source can answer the query (e.g. Torrentio needs an IMDb id).
	Supports(q Query) bool
	// Search runs the query upstream. The cache and single-flight are in Service, not here.
	Search(ctx context.Context, q Query) (*Result, error)
}

// Result is what one search returned.
type Result struct {
	Releases []Release
	// RSSPage preserves upstream cursor fields before any filtering (adapter dedupe, identity conflicts, eligibility):
	// one entry per upstream item, so RSS catch-up paging depends only on what the feed sent. Never used for decisions.
	RSSPage []Release
	// SeaDex contains complete per-entry recommendation snapshots, before query filters.
	SeaDex []SeaDexSnapshot
	// Requests is how many upstream requests the search made.
	Requests int
	// Truncated: paging stopped early (page_cap reached, or the budget ran out after the first page).
	Truncated bool
	// Capped: Truncated by our own page_cap (or page limit) alone; every request answered up to it. Such an answer
	// is not a failure (the search counts as searched), though it is still not a complete coverage.
	Capped bool
	// failed: a request or a page failed (Truncated for that reason too).
	failed bool
	// Cached: served from the result cache. Shared: joined a search another caller had in flight.
	Cached bool
	Shared bool
	// Notes say what the adapter did that a reader of the output should know (merged, dropped, truncated why).
	Notes []string
	// unrelated is how many rows of id requests Query.Related dropped.
	unrelated int
}

// dropUnrelated removes the rows from index from on that q.Related refuses, counting them.
func dropUnrelated(q Query, res *Result, from int) {
	if q.Related == nil {
		return
	}
	kept := res.Releases[:from]
	for _, r := range res.Releases[from:] {
		if q.Related(r) {
			kept = append(kept, r)
		} else {
			res.unrelated++
		}
	}
	clear(res.Releases[len(kept):])
	res.Releases = kept
}

// rssCursorFields keeps only the fields an RSS cursor reads (Result.RSSPage).
func rssCursorFields(r Release) Release {
	return Release{InfoHash: r.InfoHash, Title: r.Title, Size: r.Size, PublishDate: r.PublishDate}
}

func (r *Result) clone() *Result {
	c := *r
	c.Releases = append([]Release(nil), r.Releases...)
	c.RSSPage = append([]Release(nil), r.RSSPage...)
	c.SeaDex = append([]SeaDexSnapshot(nil), r.SeaDex...)
	for i := range c.SeaDex {
		c.SeaDex[i].Targets = append([]AniListTarget(nil), r.SeaDex[i].Targets...)
		c.SeaDex[i].Torrents = append([]SeaDexStanding(nil), r.SeaDex[i].Torrents...)
		for j := range c.SeaDex[i].Torrents {
			t := &c.SeaDex[i].Torrents[j]
			if t.Facts != nil {
				f := *t.Facts
				f.Tags = append([]string(nil), f.Tags...)
				f.Files = append(f.Files[:0:0], f.Files...)
				t.Facts = &f
			}
		}
	}
	c.Notes = append([]string(nil), r.Notes...)
	return &c
}

// HTTPError is a non-2xx answer. It never carries the URL (it may hold a key).
type HTTPError struct {
	Indexer string
	Status  int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("indexer %s: upstream answered HTTP %d %s", e.Indexer, e.Status, http.StatusText(e.Status))
}

// ErrUnsupported means the source cannot answer this kind of query.
var ErrUnsupported = errors.New("indexer: query not supported by this source")

// base holds what every adapter shares.
type base struct {
	key         string
	kind        string
	ix          *policy.Indexer
	gw          Gateway
	apiKeyValue string
	hasAPIKey   bool
}

func (b *base) Key() string  { return b.key }
func (b *base) Kind() string { return b.kind }

func (b *base) target() gateway.Target {
	return gateway.Target{Class: gateway.ClassIndexer, Key: b.key}
}

// apiKey reads the indexer's key from its env var. Empty when none is configured.
func (b *base) apiKey() (string, error) {
	if b.hasAPIKey {
		if b.apiKeyValue == "" {
			return "", fmt.Errorf("indexer %s: configured credential is not set", b.key)
		}
		return b.apiKeyValue, nil
	}
	if b.ix.APIKeyEnv == "" {
		return "", nil
	}
	v := os.Getenv(b.ix.APIKeyEnv)
	if v == "" {
		return "", fmt.Errorf("indexer %s: env %s is not set", b.key, b.ix.APIKeyEnv)
	}
	return v, nil
}

// get sends one GET through the gateway and returns the body of a 2xx answer.
func (b *base) get(ctx context.Context, rawURL, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("indexer %s: bad URL", b.key) // the URL may hold a key: don't echo it
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := b.gw.Do(ctx, b.target(), req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, &HTTPError{Indexer: b.key, Status: resp.StatusCode}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("indexer %s: reading the answer: %w", b.key, err)
	}
	return body, nil
}

// stamp fills the fields every release from this source shares.
func (b *base) stamp(ctx context.Context, r *Release) {
	r.Indexer, r.Kind = b.key, b.kind
	// r.Privacy is a label: the stricter of the item's own (Prowlarr's <prowlarrindexer type>) and the policy's.
	if r.Privacy == "" || !r.Privacy.IsPrivate() {
		r.Privacy = b.ix.Privacy
	}
	if r.InfoHash == "" && r.Magnet.IsMagnet() {
		r.InfoHash = HashFromMagnet(r.Magnet.URL())
	}
	if r.InfoHash == "" && r.Link.IsMagnet() {
		r.InfoHash = HashFromMagnet(r.Link.URL())
	}
}
