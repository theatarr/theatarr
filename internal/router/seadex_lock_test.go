package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func TestSeaDexLockBlocksOtherIndexerInSearch(t *testing.T) {
	st, err := store.Open(context.Background(), storetest.Seed(t, filepath.Join(t.TempDir(), "db.sqlite")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(st.Path()+".seadex-lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	pub := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" {
			pub <- struct{}{}
			if r.URL.Query().Get("t") == "caps" {
				fmt.Fprint(w, `<caps><limits default="100" max="100"/><searching><movie-search available="yes" supportedParams="tmdbid"/></searching><categories><category id="2000" name="Movies"/></categories></caps>`)
			} else {
				fmt.Fprint(w, `<rss version="2.0"><channel></channel></rss>`)
			}
			return
		}
		fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":0,"totalPages":0,"items":[]}`)
	}))
	defer srv.Close()
	pol := &policy.Policy{Profiles: map[string]*policy.Profile{"web": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}}, Versions: map[string]*policy.VersionPolicy{"web": {Kind: policy.KindMovie, Profile: "web", Indexers: []string{"seadex", "pub"}}}, Indexers: map[string]*policy.Indexer{"seadex": {Kind: "seadex", URL: srv.URL, Privacy: policy.PrivacyPublic}, "pub": {Kind: "torznab", URL: srv.URL + "/api", Privacy: policy.PrivacyPublic}}}
	eng, err := decide.New(pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := indexer.New(pol, &searchGateway{}, indexer.Options{CacheTTL: -1})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetEvidence(indexer.NewEvidence(indexer.EvidenceConfig{Store: st, Policy: pol}))
	svc.SetMetadata(indexer.Metadata{AniList: indexer.AniListFunc(func(context.Context, indexer.Query) ([]int, error) { return []int{1}, nil })})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := SearchRequest(ctx, eng, svc, decide.Title{Kind: policy.KindMovie, Name: "Film", TMDb: 1}, decide.Version{Key: "web", Policy: pol.Versions["web"]}, nil, nil)
		done <- err
	}()
	blocked := false
	select {
	case <-pub:
	case <-time.After(150 * time.Millisecond):
		blocked = true
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pub:
	case <-time.After(time.Second):
		t.Fatal("other indexer never started after releasing lock")
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Fatal("other indexer started only after SeaDex lock was released")
	}
}
