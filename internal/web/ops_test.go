package web

import (
	"context"
	"database/sql"
	"github.com/theatarr/theatarr/internal/acquire"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// fakeOps records the recovery actions that need the router or the library; refuse makes the next call refuse.
type fakeOps struct {
	mu            sync.Mutex
	grabs         []indexer.Release
	added         []string // the source of each AddRelease that went through (its release is in grabs)
	retries       []int64
	unlinks       []int64
	renameApplied []int64
	reassigned    []ReassignRequest
	refuse        string
	reject        bool
	// block: the release is on the library's blocklist; a call without unblockBy is refused with the block, one
	// with it is recorded in unblockedBy and goes through.
	block       bool
	unblockedBy []string
	// retried, when set, is the next RetryDownload's answer.
	retried *RetriedDownload
	// unlinkErr, when set, is the next UnlinkLink's error.
	unlinkErr error
	// onUnlinkRemoved runs after each UnlinkRemoved that went through.
	onUnlinkRemoved func(id int64)
	// onUnlink, when set, runs after each UnlinkLink that went through.
	onUnlink func()
}

func (f *fakeOps) PreviewRename(context.Context, int64, *int) (*library.RenamePreview, error) {
	return &library.RenamePreview{Files: []library.RenameFile{{LinkID: 4, Version: "movie-remux", ExistingPath: "/old.mkv", NewPath: "/new.mkv"}}}, nil
}

func (f *fakeOps) ApplyRename(_ context.Context, titleID int64, _ *int) (*library.RenamePreview, error) {
	f.renameApplied = append(f.renameApplied, titleID)
	return &library.RenamePreview{}, nil
}

func (f *fakeOps) ReassignEpisodes(_ context.Context, linkID int64, eps []int64, replace bool) (*library.Reassigned, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.take(); err != nil {
		return nil, err
	}
	f.reassigned = append(f.reassigned, ReassignRequest{EpisodeIDs: eps, Replace: replace})
	return &library.Reassigned{LinkID: linkID, Version: "tv", PreviousPath: "/old.mkv", Path: "/new.mkv",
		Previous: []string{"S01E01"}, Episodes: []string{"S01E03"}, Replaced: []string{"/other.mkv"}}, nil
}

type fakeRefused string

// fakeBlocked is arrview.Refused with its Block: only the blocklist refuses.
type fakeBlocked struct{}

func (fakeBlocked) Error() string { return "On 4K WEB's blocklist: sample" }
func (fakeBlocked) Refused() bool { return true }
func (fakeBlocked) BlockedBy() (string, string, string, bool) {
	return "4K WEB", "stalled", "stalled at the provider", true
}

// unblock answers a call of a blocklisted release: refused without unblockBy, else taken off.
func (f *fakeOps) unblock(unblockBy string) error {
	if !f.block {
		return nil
	}
	if unblockBy == "" {
		return fakeBlocked{}
	}
	f.block = false
	f.unblockedBy = append(f.unblockedBy, unblockBy)
	return nil
}

func (f fakeRefused) Error() string { return string(f) }
func (fakeRefused) Refused() bool   { return true }

func (f *fakeOps) take() error {
	if f.refuse == "" {
		return nil
	}
	err := fakeRefused(f.refuse)
	f.refuse = ""
	return err
}

func (f *fakeOps) GrabRelease(_ context.Context, _ int64, version string, rel indexer.Release, override bool,
	unblockBy, _ string) (*GrabbedRelease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := &decide.Decision{Release: rel.Title, Version: version, Accepted: !f.reject}
	if f.reject {
		d.Rejections = []decide.Rejection{{Reason: "QualityNotWanted", Message: "WEBDL-1080p is not wanted"}}
		if !override {
			return &GrabbedRelease{Decision: d}, fakeRefused("rejected for " + version + ": WEBDL-1080p is not wanted")
		}
	}
	if err := f.take(); err != nil {
		return &GrabbedRelease{Decision: d}, err
	}
	if err := f.unblock(unblockBy); err != nil {
		return nil, err
	}
	f.grabs = append(f.grabs, rel)
	return &GrabbedRelease{Decision: d, Overridden: f.reject, Job: 1}, nil
}

func (f *fakeOps) AddRelease(ctx context.Context, id int64, version string, rel indexer.Release, override bool,
	unblockBy, source string) (*GrabbedRelease, error) {
	g, err := f.GrabRelease(ctx, id, version, rel, override, unblockBy, source)
	if err == nil {
		f.mu.Lock()
		f.added = append(f.added, source)
		f.mu.Unlock()
	}
	return g, err
}

func (f *fakeOps) RetryDownload(_ context.Context, id int64, unblockBy, _ string) (*RetriedDownload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.take(); err != nil {
		return nil, err
	}
	if err := f.unblock(unblockBy); err != nil {
		return nil, err
	}
	f.retries = append(f.retries, id)
	if f.retried != nil {
		return f.retried, nil
	}
	return &RetriedDownload{TitleID: 1, Version: "movie-web", Hash: hashA, Release: "Alpha", What: "routed again", Job: 2}, nil
}

func (f *fakeOps) UnlinkLink(_ context.Context, id int64, _ string) (*UnlinkedLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.take(); err != nil {
		return nil, err
	}
	if err := f.unlinkErr; err != nil {
		f.unlinkErr = nil
		return nil, err
	}
	f.unlinks = append(f.unlinks, id)
	if f.onUnlink != nil {
		f.onUnlink()
	}
	return &UnlinkedLink{TitleID: 2, Path: "/lib/Beta/" + strings.Repeat("x", int(id)), Versions: []string{"movie-web"},
		Trash: "/lib/.trash/x"}, nil
}

// UnlinkRemoved is UnlinkLink for a removal; with onUnlinkRemoved set it runs first (a test marks the row unlinked
// there, as the library does).
func (f *fakeOps) UnlinkRemoved(ctx context.Context, id int64, reason string) (*UnlinkedLink, error) {
	u, err := f.UnlinkLink(ctx, id, reason)
	if err == nil && f.onUnlinkRemoved != nil {
		f.onUnlinkRemoved(id)
	}
	return u, err
}

// opsAcquirer answers live searches with releases that carry hashes (fakeAcquirer's have none).
type opsAcquirer struct{ fakeAcquirer }

func (f *opsAcquirer) Interactive(_ context.Context, _ int64, version string, _ *int, _ []int64) (*router.Searched, error) {
	good := &decide.Decision{Release: "Alpha.2020.2160p.WEB-DL-GRP", Accepted: true, Quality: "WEBDL-2160p", Score: 1500,
		Version: version}
	dup := &decide.Decision{Release: "Alpha.2020.2160p.WEB-DL-GRP", Accepted: true, Quality: "WEBDL-2160p", Score: 1500}
	return &router.Searched{Ranked: []*decide.Decision{good, dup}, Releases: map[*decide.Decision]indexer.Release{
		good: {Title: good.Release, Indexer: "prowlarr-public", InfoHash: strings.Repeat("c", 40),
			Privacy: policy.PrivacyPublic},
		dup: {Title: dup.Release, Indexer: "dmm", InfoHash: strings.Repeat("C", 40), Privacy: policy.PrivacyPublic}}}, nil
}

type opsEnv struct {
	*env
	ops   *fakeOps
	acq   *opsAcquirer
	mc    *http.Cookie
	mcsrf string
	uc    *http.Cookie
	ucsrf string
}

func newOpsEnv(t *testing.T) *opsEnv {
	return newOpsEnvPolicy(t, nil)
}

// newOpsEnvPolicy is newOpsEnv with a chance to change the example policy first (it is validated after).
func newOpsEnvPolicy(t *testing.T, mutate func(*policy.Policy)) *opsEnv {
	o := &opsEnv{ops: &fakeOps{}, acq: &opsAcquirer{}}
	o.env = newEnvWith(t, func(c *Config) {
		c.Ops = o.ops
		c.Add = AddConfig{Acquirer: o.acq}
		if mutate != nil {
			mutate(c.Policy)
			if err := c.Policy.Validate(); err != nil {
				t.Fatal(err)
			}
		}
	})
	mgr := o.account("mgr", auth.RoleManager, auth.StatusActive, false)
	usr := o.account("usr", auth.RoleUser, auth.StatusActive, true)
	o.mc, o.mcsrf = o.session(mgr)
	o.uc, o.ucsrf = o.session(usr)
	return o
}

func (o *opsEnv) actions() []journal.Action {
	o.t.Helper()
	var out []journal.Action
	err := scanAll(context.Background(), o.st.Reader(), `SELECT action, COALESCE(title_id, 0), version_key, info_hash,
		target, detail, by FROM journal_action ORDER BY id`, nil, func(r *sql.Rows) error {
		var a journal.Action
		if err := r.Scan(&a.Action, &a.TitleID, &a.Version, &a.InfoHash, &a.Target, &a.Detail, &a.By); err != nil {
			return err
		}
		out = append(out, a)
		return nil
	})
	if err != nil {
		o.t.Fatal(err)
	}
	return out
}

func (o *opsEnv) activate(key string) {
	o.web.pol.Router.Enabled = true
	o.t.Cleanup(func() {
		o.web.pol.Router.Enabled = false
	})
}

func TestRenameAPIPreviewApply(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	aid := o.account("admin-rename", auth.RoleAdmin, auth.StatusActive, false)
	ac, csrf := o.session(aid)
	var got renameResponse
	if code := o.jsonCall("GET", APIBase+"/titles/1/rename", nil, ac, "", &got); code != http.StatusOK || len(got.Files) != 1 || got.Files[0].NewPath != "/new.mkv" {
		t.Fatalf("preview: %d %+v", code, got)
	}
	if code := o.jsonCall("POST", APIBase+"/titles/1/rename", nil, ac, csrf, &got); code != http.StatusOK || len(o.ops.renameApplied) != 1 || o.ops.renameApplied[0] != 1 {
		t.Fatalf("apply: %d %+v applied=%v", code, got, o.ops.renameApplied)
	}
}

// TestReassignAPI: an admin changes a link's episodes; the body reaches the library, a refusal (a conflict) is 409
// with the reason, and a manager may not.
func TestReassignAPI(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	aid := o.account("admin-reassign", auth.RoleAdmin, auth.StatusActive, false)
	ac, csrf := o.session(aid)
	var got ReassignResult
	body := map[string]any{"episode_ids": []int64{203}, "replace": true}
	if code := o.jsonCall("POST", APIBase+"/links/4/episodes", body, ac, csrf, &got); code != http.StatusOK ||
		got.Path != "/new.mkv" || len(o.ops.reassigned) != 1 || !o.ops.reassigned[0].Replace ||
		got.Message != "The file now holds S01E03; the file it replaced is in the trash" {
		t.Fatalf("reassign: %d %+v %+v", code, got, o.ops.reassigned)
	}
	o.ops.refuse = "S01E03 already has a file in this library (/x.mkv); choose replace to unlink it"
	var e apiError
	if code := o.jsonCall("POST", APIBase+"/links/4/episodes", body, ac, csrf, &e); code != http.StatusConflict ||
		!strings.Contains(e.Error, "choose replace") {
		t.Fatalf("refusal: %d %+v", code, e)
	}
	if code := o.jsonCall("POST", APIBase+"/links/4/episodes", map[string]any{}, ac, csrf, nil); code != http.StatusBadRequest {
		t.Fatalf("no episodes: %d", code)
	}
	if code := o.jsonCall("POST", APIBase+"/links/4/episodes", body, o.mc, o.mcsrf, nil); code != http.StatusForbidden {
		t.Fatalf("manager: %d", code)
	}
	if len(o.ops.reassigned) != 1 {
		t.Fatalf("calls %v", o.ops.reassigned)
	}
}

// TestRecoveryRoutesNeedManage: family accounts get 403 on every recovery action and nothing changes; without Ops
// the actions that need the router or library answer 503.
func TestRecoveryRoutesNeedManage(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	b := APIBase
	for _, call := range [][2]string{{"POST", b + "/titles/1/releases/grab"}, {"GET", b + "/titles/1/removal"},
		{"POST", b + "/titles/1/remove"}, {"POST", b + "/links/1/unlink"}, {"POST", b + "/activity/downloads/1/remove"},
		{"POST", b + "/activity/downloads/1/retry"}, {"POST", b + "/activity/blocked/unblock"}} {
		if code := o.jsonCall(call[0], call[1], map[string]any{}, o.uc, o.ucsrf, nil); code != http.StatusForbidden {
			t.Errorf("user %s %s: %d", call[0], call[1], code)
		}
	}
	if len(o.actions()) != 0 {
		t.Error("a refused call was recorded")
	}
	e := newEnv(t)
	k := e.apiKey()
	r := httptest.NewRequest("POST", b+"/activity/downloads/1/retry", nil)
	r.Header.Set(auth.APIKeyHeader, k)
	if rec := e.do(r); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no ops: %d", rec.Code)
	}
}

// TestGrabFromLiveSearch: a grab takes the release from the panel's last live search (never what the browser
// sends), only for a version a search would run for; a rejected release needs override; each grab is recorded with
// who did it.
func TestGrabFromLiveSearch(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	b := APIBase + "/titles/1"
	hash := strings.Repeat("c", 40)
	var apiErr apiError
	// Shadow: refused, nothing routed.
	if code := o.jsonCall("POST", b+"/releases/grab", GrabRequest{Version: "movie-web", InfoHash: hash}, o.mc, o.mcsrf,
		&apiErr); code != http.StatusConflict || !strings.Contains(apiErr.Error, "Not grabbed") {
		t.Fatalf("shadow grab: %d %+v", code, apiErr)
	}
	o.activate("movie-web")
	// Not searched yet: 404.
	if code := o.jsonCall("POST", b+"/releases/grab", GrabRequest{Version: "movie-web", InfoHash: hash}, o.mc, o.mcsrf,
		nil); code != http.StatusNotFound {
		t.Fatalf("grab before a search: %d", code)
	}
	var rel Releases
	if code := o.jsonCall("GET", b+"/releases?version=movie-web&live=1", nil, o.mc, "", &rel); code != 200 ||
		rel.Source != "live" || !rel.Grab {
		t.Fatalf("live releases: %d %+v", code, rel)
	}
	// The two copies of the hash are one row, shown as the better ranked copy, the one a grab routes.
	if len(rel.Candidates) != 1 || rel.Results != 1 || rel.Candidates[0].Indexer != "prowlarr-public" ||
		!slices.Equal(rel.Candidates[0].Indexers, []string{"prowlarr-public", "dmm"}) {
		t.Errorf("one row per torrent: %+v", rel.Candidates)
	}
	var got GrabResult
	if code := o.jsonCall("POST", b+"/releases/grab", GrabRequest{Version: "movie-web", InfoHash: strings.ToUpper(hash)},
		o.mc, o.mcsrf, &got); code != 200 || !got.Accepted || got.Release != "Alpha.2020.2160p.WEB-DL-GRP" {
		t.Fatalf("grab: %d %+v", code, got)
	}
	if len(o.ops.grabs) != 1 || o.ops.grabs[0].Indexer != "prowlarr-public" || o.ops.grabs[0].Privacy != policy.PrivacyPublic {
		t.Errorf("routed %+v (the better ranked of two results with one hash)", o.ops.grabs)
	}
	// Another version's search does not cover this one.
	if code := o.jsonCall("POST", b+"/releases/grab", GrabRequest{Version: "movie-remux", InfoHash: hash}, o.mc, o.mcsrf,
		nil); code != http.StatusConflict && code != http.StatusNotFound {
		t.Errorf("other version: %d", code)
	}
	// Rejected now: 409 with the rejections, then grabbed with override.
	o.ops.reject = true
	apiErr = apiError{}
	if code := o.jsonCall("POST", b+"/releases/grab", GrabRequest{Version: "movie-web", InfoHash: hash}, o.mc, o.mcsrf,
		&apiErr); code != http.StatusConflict || len(apiErr.Detail) != 1 {
		t.Fatalf("rejected: %d %+v", code, apiErr)
	}
	if code := o.jsonCall("POST", b+"/releases/grab", GrabRequest{Version: "movie-web", InfoHash: hash, Override: true},
		o.mc, o.mcsrf, &got); code != 200 || !got.Overridden || got.Accepted {
		t.Fatalf("override: %d %+v", code, got)
	}
	acts := o.actions()
	if len(acts) != 2 || acts[0].Action != journal.ActGrab || acts[0].By != "mgr" || acts[0].InfoHash != hash ||
		!strings.Contains(acts[1].Detail, "although rejected") {
		t.Errorf("actions %+v", acts)
	}
	if code := o.jsonCall("POST", b+"/releases/grab", map[string]any{"version": "movie-web"}, o.mc, o.mcsrf, nil); code != 400 {
		t.Errorf("no hash: %d", code)
	}
}

// TestManualGrabBlocklisted: a grab, an add by hash and a retry of a release on the library's blocklist answer 409
// with Blocked (the library and the reason in words) and route nothing; sent again with unblock they go through, the
// unblock taken by the person who confirmed.
func TestManualGrabBlocklisted(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	ac, csrf := o.session(o.account("admin-unblock", auth.RoleAdmin, auth.StatusActive, false))
	o.activate("movie-web")
	hash := strings.Repeat("c", 40)
	if code := o.jsonCall("GET", APIBase+"/titles/1/releases?version=movie-web&live=1", nil, o.mc, "", nil); code != 200 {
		t.Fatalf("live search: %d", code)
	}
	add := AddReleaseRequest{Version: "movie-web", InfoHash: strings.Repeat("ab", 20), Name: "Alpha.2020.2160p.WEB-DL-GRP"}
	calls := []struct {
		name, path   string
		body, unblck any
		c            *http.Cookie
	}{
		{"grab", APIBase + "/titles/1/releases/grab", GrabRequest{Version: "movie-web", InfoHash: hash},
			GrabRequest{Version: "movie-web", InfoHash: hash, Unblock: true}, o.mc},
		{"add", APIBase + "/titles/1/releases/add", add, AddReleaseRequest{Version: add.Version, InfoHash: add.InfoHash,
			Name: add.Name, Unblock: true}, ac},
		{"retry", APIBase + "/activity/downloads/40/retry", nil, RetryRequest{Unblock: true}, o.mc},
	}
	for _, c := range calls {
		token := o.mcsrf
		if c.c == ac {
			token = csrf
		}
		o.ops.block = true
		grabs, retries := len(o.ops.grabs), len(o.ops.retries)
		var apiErr apiError
		if code := o.jsonCall("POST", c.path, c.body, c.c, token, &apiErr); code != http.StatusConflict ||
			apiErr.Blocked == nil || apiErr.Blocked.Library != "4K WEB" || apiErr.Blocked.Reason == "" ||
			len(apiErr.Detail) != 0 || !strings.Contains(apiErr.Error, "4K WEB's blocklist") {
			t.Fatalf("%s blocked: %d %+v", c.name, code, apiErr)
		}
		if len(o.ops.grabs) != grabs || len(o.ops.retries) != retries {
			t.Fatalf("%s: a blocked call went through", c.name)
		}
		if code := o.jsonCall("POST", c.path, c.unblck, c.c, token, nil); code != 200 {
			t.Fatalf("%s with unblock: %d", c.name, code)
		}
		if n := len(o.ops.unblockedBy); n == 0 || o.ops.unblockedBy[n-1] == "" || o.ops.block {
			t.Fatalf("%s: not unblocked by the person: %v", c.name, o.ops.unblockedBy)
		}
	}
}

// TestAddReleaseByMagnetOrHash: an admin hands over a magnet or a hash no search found. The hash comes from the
// magnet's btih or the hash field (hex or base32), the name from the body or the magnet's dn; the release goes to
// Ops with no indexer and unknown seeders, as a person's grab; a rejection needs override; a shadow library and a
// router that is off refuse; managers may not; each add is recorded as a manual add.
func TestAddReleaseByMagnetOrHash(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	ac, csrf := o.session(o.account("admin-add", auth.RoleAdmin, auth.StatusActive, false))
	u := APIBase + "/titles/1/releases/add"
	hash := strings.Repeat("ab", 20)
	magnet := "magnet:?xt=urn:btih:" + strings.ToUpper(hash) + "&dn=Alpha.2020.2160p.WEB-DL-GRP&tr=udp%3A%2F%2Ftracker.example%3A1337"
	var apiErr apiError
	// Shadow (and the router off): refused, nothing routed.
	if code := o.jsonCall("POST", u, AddReleaseRequest{Version: "movie-web", Magnet: magnet}, ac, csrf, &apiErr); code != http.StatusConflict ||
		!strings.Contains(apiErr.Error, "Not added") {
		t.Fatalf("shadow add: %d %+v", code, apiErr)
	}
	o.activate("movie-web")
	o.web.pol.Router.Enabled = false
	if code := o.jsonCall("POST", u, AddReleaseRequest{Version: "movie-web", Magnet: magnet}, ac, csrf, nil); code != http.StatusConflict {
		t.Fatalf("add with downloads off: %d", code)
	}
	o.web.pol.Router.Enabled = true
	// Managers may grab what a search found; adding a hash from outside is the admin's.
	if code := o.jsonCall("POST", u, AddReleaseRequest{Version: "movie-web", Magnet: magnet}, o.mc, o.mcsrf, nil); code != http.StatusForbidden {
		t.Fatalf("manager: %d", code)
	}
	if code := o.jsonCall("POST", u, AddReleaseRequest{Version: "movie-web", Magnet: magnet}, o.uc, o.ucsrf, nil); code != http.StatusForbidden {
		t.Fatalf("user: %d", code)
	}
	for name, body := range map[string]AddReleaseRequest{
		"no version":          {Magnet: magnet},
		"neither":             {Version: "movie-web", Name: "Alpha.2020.2160p.WEB-DL-GRP"},
		"both":                {Version: "movie-web", Magnet: magnet, InfoHash: hash},
		"bare hash, no name":  {Version: "movie-web", InfoHash: hash},
		"magnet without name": {Version: "movie-web", Magnet: "magnet:?xt=urn:btih:" + hash},
		"bad hash":            {Version: "movie-web", InfoHash: "nothex", Name: "Alpha.2020.2160p.WEB-DL-GRP"},
		"magnet, no btih":     {Version: "movie-web", Magnet: "magnet:?dn=Alpha.2020.2160p.WEB-DL-GRP"},
		"magnet, two hashes":  {Version: "movie-web", Magnet: magnet + "&xt=urn:btih:" + strings.Repeat("1", 40)},
		// The whole btih value is the hash, and there is exactly one btih.
		"magnet, hash then more":                {Version: "movie-web", Magnet: "magnet:?xt=urn:btih:" + hash + "-bad&dn=Alpha.2020.2160p.WEB-DL-GRP"},
		"magnet, short hash":                    {Version: "movie-web", Magnet: "magnet:?xt=urn:btih:" + hash[:39] + "&dn=Alpha.2020.2160p.WEB-DL-GRP"},
		"magnet, a second btih that is no hash": {Version: "movie-web", Magnet: magnet + "&xt=urn:btih:nothex"},
		"magnet, the same btih twice":           {Version: "movie-web", Magnet: magnet + "&xt=urn:btih:" + hash},
		"hash then more":                        {Version: "movie-web", InfoHash: hash + "-bad", Name: "Alpha.2020.2160p.WEB-DL-GRP"},
	} {
		if code := o.jsonCall("POST", u, body, ac, csrf, nil); code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code := o.jsonCall("POST", APIBase+"/titles/999/releases/add", AddReleaseRequest{Version: "movie-web", Magnet: magnet}, ac, csrf, nil); code != http.StatusNotFound {
		t.Errorf("unknown title: %d", code)
	}
	if len(o.ops.grabs) != 0 || len(o.actions()) != 0 {
		t.Fatalf("a refused add was routed or recorded: %+v", o.ops.grabs)
	}

	var got GrabResult
	if code := o.jsonCall("POST", u, AddReleaseRequest{Version: "movie-web", Magnet: magnet}, ac, csrf, &got); code != 200 ||
		!got.Accepted || got.Release != "Alpha.2020.2160p.WEB-DL-GRP" || got.InfoHash != hash {
		t.Fatalf("magnet add: %d %+v", code, got)
	}
	if len(o.ops.grabs) != 1 || len(o.ops.added) != 1 || o.ops.added[0] != "ui:grab:admin-add" {
		t.Fatalf("routed %+v as %v", o.ops.grabs, o.ops.added)
	}
	if r := o.ops.grabs[0]; r.Indexer != "" || r.InfoHash != hash || r.Seeders != -1 || r.Peers != -1 || r.Size != 0 {
		t.Errorf("release %+v", r)
	}
	// A base32 hash with a name; the name given wins over nothing else.
	b32 := "VOV2XK5LVOV2XK5LVOV2XK5LVOV2XK5L" // 20 bytes of 0xab
	if code := o.jsonCall("POST", u, AddReleaseRequest{Version: "movie-web", InfoHash: b32, Name: " Alpha.2020.2160p.WEB-DL-B32 "}, ac, csrf,
		&got); code != 200 || got.InfoHash != hash || got.Release != "Alpha.2020.2160p.WEB-DL-B32" {
		t.Fatalf("base32 add: %d %+v", code, got)
	}
	// Rejected now: 409 with the rejections, then added with override.
	o.ops.reject = true
	apiErr = apiError{}
	if code := o.jsonCall("POST", u, AddReleaseRequest{Version: "movie-web", Magnet: magnet}, ac, csrf, &apiErr); code != http.StatusConflict ||
		len(apiErr.Detail) != 1 {
		t.Fatalf("rejected: %d %+v", code, apiErr)
	}
	if len(o.ops.grabs) != 2 {
		t.Fatalf("a rejected add was routed: %+v", o.ops.grabs)
	}
	if code := o.jsonCall("POST", u, AddReleaseRequest{Version: "movie-web", Magnet: magnet, Override: true}, ac, csrf, &got); code != 200 ||
		!got.Overridden || got.Accepted || !strings.HasPrefix(got.Message, "Added although rejected") {
		t.Fatalf("override: %d %+v", code, got)
	}
	acts := o.actions()
	if len(acts) != 3 || acts[0].Action != journal.ActGrab || acts[0].By != "admin-add" || acts[0].InfoHash != hash ||
		acts[0].Target != "manual add" || acts[0].Detail != "manual add: routed" ||
		!strings.Contains(acts[2].Detail, "manual add: routed although rejected") {
		t.Errorf("actions %+v", acts)
	}
}

// TestRemoveAndRetryDownload: removal ends Theatarr's record only (state failed), blocklists with blocklist, and searches
// again only with blocklist; retry goes to Ops and a refusal is a 409; every action is in the journal.
func TestRemoveAndRetryDownload(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	now := store.Millis(testNow)
	o.exec(`UPDATE version SET monitored = 1 WHERE id = 2`)
	o.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, created_at, updated_at) VALUES (40, 1, 2, 'movie-web', ?, 'Alpha.WEB', 'dmm', 'rd',
		'TID1', 'downloading', ?, ?)`, strings.Repeat("e", 40), now, now)
	b := APIBase + "/activity/downloads/"
	if code := o.jsonCall("POST", b+"40/remove", RemoveDownloadRequest{Search: true}, o.mc, o.mcsrf, nil); code != 400 {
		t.Errorf("search without blocklist: %d", code)
	}
	o.activate("movie-web")
	var got RemoveDownloadResult
	if code := o.jsonCall("POST", b+"40/remove", RemoveDownloadRequest{Blocklist: true, Search: true}, o.mc, o.mcsrf,
		&got); code != 200 || !got.Ended || !got.Blocklisted || got.Search == nil || !got.Search.Started {
		t.Fatalf("remove: %d %+v", code, got)
	}
	var state, errText, tid string
	if err := o.st.Reader().QueryRow(`SELECT state, error, provider_torrent_id FROM download WHERE id = 40`).Scan(&state,
		&errText, &tid); err != nil || state != "failed" || !strings.Contains(errText, "mgr") || tid != "TID1" {
		t.Errorf("download after removal: %s %q %s %v", state, errText, tid, err)
	}
	if n := o.count(`SELECT COUNT(*) FROM router_blocklist WHERE version_key = 'movie-web' AND source = 'ui'`); n != 1 {
		t.Errorf("blocklist rows %d", n)
	}
	if len(o.acq.submits) != 1 || o.acq.submits[0].Versions[0] != "movie-web" {
		t.Errorf("searches %+v", o.acq.submits)
	}
	if code := o.jsonCall("POST", b+"999/remove", RemoveDownloadRequest{}, o.mc, o.mcsrf, nil); code != 404 {
		t.Errorf("unknown download: %d", code)
	}

	var rr RetryResult
	if code := o.jsonCall("POST", b+"40/retry", nil, o.mc, o.mcsrf, &rr); code != 200 || len(o.ops.retries) != 1 ||
		!strings.Contains(rr.Message, "router") {
		t.Fatalf("retry: %d %+v", code, rr)
	}
	o.ops.refuse = "the release is blocklisted for movie-web: unblock it first"
	var apiErr apiError
	if code := o.jsonCall("POST", b+"40/retry", nil, o.mc, o.mcsrf, &apiErr); code != 409 || !strings.Contains(apiErr.Error, "unblock") {
		t.Errorf("refused retry: %d %+v", code, apiErr)
	}
	o.ops.refuse = "router.enabled is false; no download will start"
	if code := o.jsonCall("POST", b+"40/retry", nil, o.mc, o.mcsrf, &apiErr); code != 409 ||
		apiErr.Error != "router.enabled is false; no download will start" {
		t.Errorf("disabled retry: %d %+v", code, apiErr)
	}
	acts := o.actions()
	if len(acts) != 2 || acts[0].Action != journal.ActRemoveDownload || !strings.Contains(acts[0].Detail, "blocklisted") ||
		acts[1].Action != journal.ActRetryDownload || acts[1].By != "mgr" {
		t.Errorf("actions %+v", acts)
	}
	// An import now the library rejected is not answered as a success.
	o.ops.retried = &RetriedDownload{TitleID: 1, Version: "movie-web", What: "rejected, the release is blocklisted: no file passed the sample guard",
		Outcome: "rejected"}
	if code := o.jsonCall("POST", b+"40/retry", nil, o.mc, o.mcsrf, &rr); code != 200 || rr.Outcome != "rejected" ||
		!strings.HasPrefix(rr.Message, "Not imported") {
		t.Errorf("rejected import now: %d %+v", code, rr)
	}
	if acts := o.actions(); len(acts) != 3 || acts[2].Detail != o.ops.retried.What {
		t.Errorf("rejected import now journal: %+v", acts)
	}
}

// TestUnblock: the blocklist row goes and the action is recorded in the same transaction, listed by the journal
// (kind action); an unknown entry is a 404.
func TestUnblock(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	now := store.Millis(testNow)
	hash := strings.Repeat("f", 40)
	o.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, created_at)
		VALUES ('movie-web', ?, 1, 'Alpha.BAD', 'sample', 'library', ?)`, hash, now)
	var got UnblockResult
	if code := o.jsonCall("POST", APIBase+"/activity/blocked/unblock", UnblockRequest{Version: "movie-web", InfoHash: hash,
		Search: true}, o.mc, o.mcsrf, &got); code != 200 || got.Release != "Alpha.BAD" || got.Search == nil {
		t.Fatalf("unblock: %d %+v", code, got)
	}
	if n := o.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Errorf("blocklist rows %d", n)
	}
	if code := o.jsonCall("POST", APIBase+"/activity/blocked/unblock", UnblockRequest{Version: "movie-web", InfoHash: hash},
		o.mc, o.mcsrf, nil); code != 404 {
		t.Errorf("second unblock: %d", code)
	}
	p, err := o.web.journal.List(context.Background(), journal.Filter{Kinds: []string{journal.KindAction}})
	if err != nil || len(p.Items) != 1 || p.Items[0].Action == nil || p.Items[0].Action.Action != journal.ActUnblock ||
		!strings.HasPrefix(p.Items[0].Summary, "Unblocked ") || !strings.Contains(p.Items[0].Summary, " · by mgr") || p.Items[0].Release.InfoHash != hash {
		t.Fatalf("journal entries %+v %v", p, err)
	}
}

// A blocked release says when its library cannot search now (the reason the title page's search buttons give), and
// the unblock's answer names the library, not its key.
func TestUnblockSearchReasonAndLibraryName(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	now := store.Millis(testNow)
	hash := strings.Repeat("f", 40)
	o.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, created_at)
		VALUES ('movie-web', ?, 1, 'Alpha.BAD', 'sample', 'library', ?)`, hash, now)
	label := o.web.versionLabel("movie-web")
	if label == "movie-web" {
		t.Fatalf("label %q", label)
	}
	blocked := func() ActivityBlocked {
		var page ActivityPage
		if code := o.jsonCall("GET", APIBase+"/activity?state=blocked", nil, o.mc, "", &page); code != 200 || len(page.Blocked) != 1 {
			t.Fatalf("blocked: %d %+v", code, page)
		}
		return page.Blocked[0]
	}
	o.web.pol.Router.Enabled = false
	if b := blocked(); b.SearchReason == "" || b.SearchReason != o.web.searchBlock("movie-web") {
		t.Errorf("paused: search reason %q", b.SearchReason)
	}
	var got UnblockResult
	if code := o.jsonCall("POST", APIBase+"/activity/blocked/unblock", UnblockRequest{Version: "movie-web", InfoHash: hash,
		Search: true}, o.mc, o.mcsrf, &got); code != 200 || strings.Contains(got.Message, "movie-web") ||
		!strings.HasPrefix(got.Message, "Unblocked: the release may be grabbed again for "+label) {
		t.Fatalf("unblock: %d %+v", code, got)
	}
	o.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, created_at)
		VALUES ('movie-web', ?, 1, 'Alpha.BAD', 'sample', 'library', ?)`, hash, now)
	o.web.pol.Router.Enabled = true
	if b := blocked(); b.SearchReason != "" {
		t.Errorf("running: search reason %q", b.SearchReason)
	}
	// A copy the title does not monitor: searchCopy would not search it, so the box is off with that reason.
	o.exec(`UPDATE version SET monitored = 0 WHERE title_id = 1 AND policy_key = 'movie-web'`)
	if b := blocked(); b.SearchReason != "The "+label+" library is not monitored" {
		t.Errorf("not monitored: search reason %q", b.SearchReason)
	}
	// The title removed from that library: the box says so, and so does a search of the copy.
	o.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at)
		SELECT id, 'radarr', 0 FROM version WHERE title_id = 1 AND policy_key = 'movie-web'`)
	if b := blocked(); b.SearchReason != "The title is not in the "+label+" library" {
		t.Errorf("removed: search reason %q", b.SearchReason)
	}
	if s, err := o.web.searchCopy(context.Background(), 1, "movie-web"); err != nil ||
		s.Message != "Not searched: the title is not in the "+label+" library" {
		t.Errorf("search of a removed copy: %+v %v", s, err)
	}
	// A row with no title: the unblock only unblocks.
	o.exec(`UPDATE router_blocklist SET title_id = NULL`)
	if b := blocked(); !strings.HasPrefix(b.SearchReason, "This release has no title in Theatarr") {
		t.Errorf("no title: search reason %q", b.SearchReason)
	}
}

// TestRemoveCopiesAndTitle: the removal scope names each copy with its links and downloads; a copy an arr still owns
// is refused; removing a copy hides and unmonitors it, ends its downloads in hand in Theatarr's records only, and
// with unlink sends its symlinks to Ops (the gate decides); without unlink nothing on disk changes. Removing every
// copy removes the title.
func TestRemoveCopiesAndTitle(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	o.exec(`DELETE FROM link WHERE id = 902`) // the seed's file: this test's links are the files
	now := store.Millis(testNow)
	o.exec(`INSERT INTO link (id, path, target, kind, title_id, state, created_at, updated_at) VALUES
		(5, '/lib/Beta/a.mkv', '/mnt/zurg/a.mkv', 'symlink', 2, 'active', ?, ?),
		(6, '/lib/Beta/b.mkv', '/data/b.mkv', 'hardlink', 2, 'active', ?, ?)`, now, now, now, now)
	o.exec(`INSERT INTO link_version (link_id, version_id) VALUES (5, 3), (6, 3)`)
	o.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (41, 2, 3, 'movie-web', ?, 'Beta.WEB', 'dmm', 'added', ?, ?)`,
		strings.Repeat("1", 40), now, now)

	var scope RemovalScope
	if code := o.jsonCall("GET", APIBase+"/titles/1/removal", nil, o.mc, "", &scope); code != 200 || !scope.Title ||
		len(scope.Copies) != 2 || scope.Copies[0].Key != "movie-remux" {
		t.Fatalf("scope: %d %+v", code, scope)
	}
	webLabel := scope.Copies[1].Label
	if webLabel == "" || webLabel == "movie-web" {
		t.Fatalf("web copy label %q", webLabel)
	}
	if code := o.jsonCall("GET", APIBase+"/titles/1/removal?versions=tv-2160p", nil, o.mc, "", nil); code != 400 {
		t.Errorf("unknown copy: %d", code)
	}
	// Don't add it again from lists is for the whole title only.
	if code := o.jsonCall("POST", APIBase+"/titles/1/remove", RemoveRequest{Versions: []string{"movie-web"}, Exclude: true},
		o.mc, o.mcsrf, nil); code != 400 {
		t.Errorf("exclude with one copy: %d", code)
	}
	// Alpha's web copy alone: not the whole title; its files stay.
	var got RemoveResult
	if code := o.jsonCall("POST", APIBase+"/titles/1/remove", RemoveRequest{Versions: []string{"movie-web"}}, o.mc,
		o.mcsrf, &got); code != 200 || got.Title || len(got.Removed) != 1 || len(o.ops.unlinks) != 0 {
		t.Fatalf("remove copy: %d %+v", code, got)
	}
	// Named as the title page names it; it had no files there, so none are said to stay.
	if got.Message != "Removed Alpha from "+webLabel+"." {
		t.Errorf("message %q", got.Message)
	}
	if n := o.count(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id = 2 AND view = 'ui'`); n != 1 {
		t.Errorf("removed rows %d", n)
	}

	if code := o.jsonCall("GET", APIBase+"/titles/2/removal", nil, o.mc, "", &scope); code != 200 || !scope.Title ||
		len(scope.Copies) != 1 || scope.Copies[0].Links != 1 || scope.Copies[0].Downloads != 1 {
		t.Fatalf("Beta scope: %d %+v", code, scope)
	}
	// Beta's only copy is owned by radarr_4k_web, which the example policy does not import: removable.
	got = RemoveResult{}
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{Unlink: true, Exclude: true}, o.mc, o.mcsrf, &got); code != 200 ||
		!got.Title || got.DownloadsEnded != 1 || len(got.Unlinked) != 1 {
		t.Fatalf("remove title: %d %+v", code, got)
	}
	// Excluded with its name; the exclusions block lists it and Remove takes it off.
	var ex ListExclusions
	if code := o.jsonCall("GET", APIBase+"/lists/exclusions", nil, o.mc, "", &ex); code != 200 || len(ex.Exclusions) != 1 ||
		ex.Exclusions[0].Kind != "movie" || ex.Exclusions[0].Name != "Beta" {
		t.Fatalf("exclusions: %d %+v", code, ex)
	}
	exURL := APIBase + "/lists/exclusions/movie/" + strconv.FormatInt(ex.Exclusions[0].ID, 10)
	if code := o.jsonCall("DELETE", exURL, nil, o.mc, o.mcsrf, nil); code != 204 {
		t.Errorf("delete exclusion: %d", code)
	}
	if code := o.jsonCall("DELETE", exURL, nil, o.mc, o.mcsrf, nil); code != 404 {
		t.Errorf("delete exclusion again: %d", code)
	}
	if len(o.ops.unlinks) != 1 || o.ops.unlinks[0] != 5 {
		t.Errorf("unlinked %v (the hardlink must stay)", o.ops.unlinks)
	}
	var mon int
	var state string
	if err := o.st.Reader().QueryRow(`SELECT v.monitored, d.state FROM version v JOIN download d ON d.version_id = v.id
		WHERE v.id = 3`).Scan(&mon, &state); err != nil || mon != 0 || state != "failed" {
		t.Errorf("after removal: monitored %d download %s %v", mon, state, err)
	}
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{}, o.mc, o.mcsrf, nil); code != 409 {
		t.Errorf("remove again: %d", code)
	}
	o.web.cleanups.Wait()
	acts := o.actions()
	kinds := []string{}
	for _, a := range acts {
		kinds = append(kinds, a.Action)
	}
	if strings.Join(kinds, " ") != "remove_version remove_title unlink_file" || acts[2].Target != "/lib/Beta/a.mkv" {
		t.Errorf("actions %+v", acts)
	}
	// What became of each library's files: Alpha's web copy had none (no file outcome); Beta's symlink went, its
	// hardlink stayed.
	if rm := journal.ParseRemovalDetail(acts[0].Detail); rm.Files != "" || rm.Left != 0 {
		t.Errorf("copy removal %q", acts[0].Detail)
	}
	if rm := journal.ParseRemovalDetail(acts[1].Detail); rm.Files != journal.RemovalFilesPartial || rm.Left != 1 ||
		rm.Reason != "not one of Theatarr's symlinks" {
		t.Errorf("title removal %q", acts[1].Detail)
	}
}

// TestBulkRemovalScope: the selection bar's Remove reads each title's removal scope in one call, in the request's
// order (the per-title scope, unchanged); an unknown library is refused; a title with nothing to remove is skipped with the reason and the rest still answer; managers only.
func TestBulkRemovalScope(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	url := APIBase + "/titles/removal"
	var got BulkRemovalScope
	if code := o.jsonCall("POST", url, BulkRemovalRequest{TitleIDs: []int64{2, 1, 99, 1}}, o.mc, o.mcsrf, &got); code != 200 ||
		len(got.Scopes) != 2 || got.Scopes[1].TitleID != 1 || !got.Scopes[1].Title || len(got.Scopes[1].Copies) != 2 ||
		got.Scopes[0].TitleID != 2 || len(got.Skipped) != 1 || got.Skipped[0] != (BulkRemovalSkip{TitleID: 99, Reason: "no longer exists"}) {
		t.Fatalf("every library: %d %+v", code, got)
	}
	var one RemovalScope
	o.jsonCall("GET", APIBase+"/titles/1/removal", nil, o.mc, "", &one)
	if got.Scopes[1].Name != one.Name || got.Scopes[1].Copies[1] != one.Copies[1] {
		t.Errorf("bulk scope %+v, per title %+v", got.Scopes[1], one)
	}
	// One library: Beta is not in it; Alpha keeps its other library, so it is not the whole title.
	got = BulkRemovalScope{}
	if code := o.jsonCall("POST", url, BulkRemovalRequest{TitleIDs: []int64{1, 2}, Versions: []string{"movie-remux"}}, o.mc, o.mcsrf,
		&got); code != 200 || len(got.Scopes) != 1 || got.Scopes[0].Title || len(got.Scopes[0].Copies) != 1 ||
		got.Skipped[0] != (BulkRemovalSkip{TitleID: 2, Reason: "not in this library"}) {
		t.Fatalf("one library: %d %+v", code, got)
	}
	// A removed title is in no library.
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{}, o.mc, o.mcsrf, nil); code != 200 {
		t.Fatalf("remove Beta: %d", code)
	}
	got = BulkRemovalScope{}
	if code := o.jsonCall("POST", url, BulkRemovalRequest{TitleIDs: []int64{2}}, o.mc, o.mcsrf, &got); code != 200 ||
		len(got.Scopes) != 0 || got.Skipped[0].Reason != "in no library" {
		t.Fatalf("removed title: %d %+v", code, got)
	}
	if code := o.jsonCall("POST", url, BulkRemovalRequest{TitleIDs: []int64{1}, Versions: []string{"no-such"}}, o.mc, o.mcsrf, nil); code != 400 {
		t.Errorf("unknown library: %d", code)
	}
	if code := o.jsonCall("POST", url, BulkRemovalRequest{}, o.mc, o.mcsrf, nil); code != 400 {
		t.Errorf("no titles: %d", code)
	}
	if code := o.jsonCall("POST", url, BulkRemovalRequest{TitleIDs: []int64{1}}, o.uc, o.ucsrf, nil); code != 403 {
		t.Errorf("household account: %d", code)
	}
}

// TestUnlinkRoute: an unlink goes to Ops and is recorded; a refusal (not a symlink, gate) is a 409.
func TestUnlinkRoute(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	var got UnlinkResult
	if code := o.jsonCall("POST", APIBase+"/links/3/unlink", nil, o.mc, o.mcsrf, &got); code != 200 ||
		got.Trash == "" || len(o.ops.unlinks) != 1 {
		t.Fatalf("unlink: %d %+v", code, got)
	}
	o.ops.refuse = "only Theatarr's own symlinks are unlinked; this entry is a hardlink and stays"
	var apiErr apiError
	if code := o.jsonCall("POST", APIBase+"/links/4/unlink", nil, o.mc, o.mcsrf, &apiErr); code != 409 ||
		!strings.Contains(apiErr.Error, "hardlink") {
		t.Errorf("refused: %d %+v", code, apiErr)
	}
	if acts := o.actions(); len(acts) != 1 || acts[0].Action != journal.ActUnlinkFile || acts[0].By != "mgr" {
		t.Errorf("actions %+v", acts)
	}
}

func TestFailedDownloadVisibleAndDismissable(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	now := store.Millis(testNow)
	o.exec(`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,provider,
 provider_torrent_id,state,error,created_at,updated_at) VALUES(70,1,2,'movie-web',?,'Failed release','dmm','rd',
 'TID70','failed','stalled: not ready',?,?)`, strings.Repeat("f", 40), now, now)
	var page ActivityPage
	if code := o.jsonCall("GET", APIBase+"/activity", nil, o.mc, "", &page); code != 200 || len(page.Now) != 1 || page.Now[0].State != "failed" {
		t.Fatalf("failed download invisible: %d %+v", code, page)
	}
	var got RemoveDownloadResult
	if code := o.jsonCall("POST", APIBase+"/activity/downloads/70/remove", RemoveDownloadRequest{}, o.mc, o.mcsrf, &got); code != 200 || !strings.Contains(got.Message, "Dismissed") {
		t.Fatalf("dismiss: %d %+v", code, got)
	}
	page = ActivityPage{}
	o.jsonCall("GET", APIBase+"/activity", nil, o.mc, "", &page)
	if len(page.Now) != 0 {
		t.Fatalf("dismissed download still visible: %+v", page)
	}
	var reason, tid, state string
	var dismissed int64
	if err := o.st.Reader().QueryRow(`SELECT error,provider_torrent_id,state,dismissed_at FROM download WHERE id=70`).Scan(&reason, &tid, &state, &dismissed); err != nil || reason != "stalled: not ready" || tid != "TID70" || state != "failed" || dismissed != now {
		t.Fatalf("lost failure/ownership: %s %s %s %d %v", reason, tid, state, dismissed, err)
	}
	if len(o.actions()) != 1 {
		t.Fatal("dismissal was not audited")
	}
}

func (f *opsAcquirer) WantedList(context.Context, acquire.WantedQuery) (*acquire.WantedList, error) {
	return &acquire.WantedList{Items: []acquire.WantedRow{}, Counts: map[string]int{}}, nil
}

// syncOpsEnv is newOpsEnv with a third movie library, movie-hd (a 4K WEB copy elsewhere), and the libraries of group
// kept in sync; Alpha (1) also has a movie-hd copy (version 4) that shares Alpha's Remux file (link 901) and has a
// dead link of its own (903), which a removal with the files moves too.
func syncOpsEnv(t *testing.T, group ...string) *opsEnv {
	o := newOpsEnvPolicy(t, func(p *policy.Policy) {
		hd := *p.Versions["movie-web"]
		hd.Library = "/media/movies-hd"
		p.Versions["movie-hd"] = &hd
		p.Library.SyncGroups = [][]string{group}
	})
	now := store.Millis(testNow)
	o.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES
		(4, 1, 'movie-hd', 'filled', ?, ?)`, now, now)
	o.exec(`INSERT INTO link (id, path, target, kind, title_id, state, created_at, updated_at) VALUES
		(903, '/hd/Alpha (2020)/Alpha.mkv', '/mnt/zurg/Alpha.WEB/Alpha.mkv', 'symlink', 1, 'dead', ?, ?)`, now, now)
	o.exec(`INSERT INTO link_version (link_id, version_id) VALUES (901, 4), (903, 4)`)
	return o
}

// fresh empties a scope before it is read again (an omitted sync_of would keep the last one).
func fresh(s *RemovalScope) *RemovalScope {
	*s = RemovalScope{}
	return s
}

func copyKeys(s RemovalScope) string {
	var out []string
	for _, c := range s.Copies {
		k := c.Key
		if c.SyncOf != "" {
			k += "<" + c.SyncOf
		}
		out = append(out, k)
	}
	return strings.Join(out, " ")
}

// A removal from one library of a sync group of three takes the group's other copies too, before the scope says
// whether the title goes: a whole-title removal, Exclude allowed although a library was named, the file two of them
// share counted and moved once, and each partner's Removed action naming the library it followed.
func TestRemoveSyncGroupOfThree(t *testing.T) {
	t.Parallel()
	o := syncOpsEnv(t, "movie-remux", "movie-web", "movie-hd")
	var scope RemovalScope
	if code := o.jsonCall("GET", APIBase+"/titles/1/removal?versions=movie-web", nil, o.mc, "", &scope); code != 200 ||
		copyKeys(scope) != "movie-hd<movie-web movie-remux<movie-web movie-web" || !scope.Title {
		t.Fatalf("scope: %d %s %+v", code, copyKeys(scope), scope)
	}
	// Per library, with its dead links (they go too): hd 2 files, remux 1.
	if scope.Copies[0].Links != 2 || scope.Copies[1].Links != 1 {
		t.Errorf("links: %+v", scope)
	}
	var got RemoveResult
	if code := o.jsonCall("POST", APIBase+"/titles/1/remove", RemoveRequest{Versions: []string{"movie-web"}, Unlink: true,
		Exclude: true}, o.mc, o.mcsrf, &got); code != 200 || !got.Title || len(got.Removed) != 3 {
		t.Fatalf("remove: %d %+v", code, got)
	}
	slices.Sort(o.ops.unlinks)
	if !slices.Equal(o.ops.unlinks, []int64{901, 903}) {
		t.Errorf("unlinked %v, want each file once", o.ops.unlinks)
	}
	if n := o.count(`SELECT COUNT(*) FROM movie_exclusion WHERE tmdb_id = 101`); n != 1 {
		t.Errorf("excluded %d", n)
	}
	o.web.cleanups.Wait()
	syncs := map[string]string{}
	for _, a := range o.actions() {
		if a.Action == journal.ActRemoveTitle {
			syncs[a.Version] = journal.ParseRemovalDetail(a.Detail).Sync
		}
	}
	if len(syncs) != 3 || syncs["movie-web"] != "" || syncs["movie-remux"] != "movie-web" || syncs["movie-hd"] != "movie-web" {
		t.Errorf("Removed actions' sync %v", syncs)
	}
}

// In a group of two: the partner goes with the named library, the title stays for a library outside the group (no
// Exclude); both named marks none; a named library with no live copy (the files kept from it) brings no partner and
// its leftover stays its own; a library in no group takes nothing with it; a link a library outside the group uses
// stays.
func TestRemoveSyncGroupOfTwo(t *testing.T) {
	t.Parallel()
	o := syncOpsEnv(t, "movie-web", "movie-hd")
	var scope RemovalScope
	if code := o.jsonCall("GET", APIBase+"/titles/1/removal?versions=movie-hd", nil, o.mc, "", &scope); code != 200 ||
		copyKeys(scope) != "movie-hd movie-web<movie-hd" || scope.Title {
		t.Fatalf("scope: %d %s", code, copyKeys(scope))
	}
	if code := o.jsonCall("GET", APIBase+"/titles/1/removal?versions=movie-hd,movie-web", nil, o.mc, "", fresh(&scope)); code != 200 ||
		copyKeys(scope) != "movie-hd movie-web" {
		t.Fatalf("both named: %d %s", code, copyKeys(scope))
	}
	if code := o.jsonCall("GET", APIBase+"/titles/1/removal?versions=movie-remux", nil, o.mc, "", fresh(&scope)); code != 200 ||
		copyKeys(scope) != "movie-remux" {
		t.Fatalf("no group: %d %s", code, copyKeys(scope))
	}
	if code := o.jsonCall("POST", APIBase+"/titles/1/remove", RemoveRequest{Versions: []string{"movie-hd"}, Exclude: true},
		o.mc, o.mcsrf, nil); code != 400 {
		t.Errorf("exclude with a copy left: %d", code)
	}
	var got RemoveResult
	if code := o.jsonCall("POST", APIBase+"/titles/1/remove", RemoveRequest{Versions: []string{"movie-hd"}, Unlink: true},
		o.mc, o.mcsrf, &got); code != 200 || got.Title || !slices.Equal(got.Removed, []string{"movie-hd", "movie-web"}) {
		t.Fatalf("remove: %d %+v", code, got)
	}
	// 901 is the Remux copy's too: it stays; 903 goes.
	if !slices.Equal(o.ops.unlinks, []int64{903}) || len(got.Kept) != 1 {
		t.Errorf("unlinked %v kept %+v", o.ops.unlinks, got.Kept)
	}
	if n := o.count(`SELECT COUNT(*) FROM arrview_removed_version r JOIN version v ON v.id = r.version_id
		WHERE v.title_id = 1 AND v.policy_key = 'movie-remux'`); n != 0 {
		t.Error("the library outside the group was removed")
	}

	// Beta: movie-web kept its files when movie-hd was removed before (link 904 left); naming movie-hd takes only
	// its leftover, and movie-web stays.
	now := store.Millis(testNow)
	o.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES
		(5, 2, 'movie-hd', 'wanted', ?, ?)`, now, now)
	o.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (5, 'ui', ?)`, now)
	o.exec(`INSERT INTO link (id, path, target, kind, title_id, state, created_at, updated_at) VALUES
		(904, '/hd/Beta (2021)/Beta.mkv', '/mnt/zurg/Beta/Beta.mkv', 'symlink', 2, 'active', ?, ?)`, now, now)
	o.exec(`INSERT INTO link_version (link_id, version_id) VALUES (904, 5)`)
	if code := o.jsonCall("GET", APIBase+"/titles/2/removal?versions=movie-hd", nil, o.mc, "", fresh(&scope)); code != 200 ||
		len(scope.Copies) != 0 || len(scope.Leftover) != 1 || scope.Leftover[0].Links != 1 {
		t.Fatalf("leftover only: %d %+v", code, scope)
	}
	// Removing movie-web with its files leaves the partner's kept files alone (not named).
	if code := o.jsonCall("GET", APIBase+"/titles/2/removal?versions=movie-web", nil, o.mc, "", fresh(&scope)); code != 200 ||
		copyKeys(scope) != "movie-web" || len(scope.Leftover) != 0 || !scope.Title {
		t.Fatalf("Beta web: %d %s %+v", code, copyKeys(scope), scope)
	}
	o.ops.unlinks = nil
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{Versions: []string{"movie-web"}, Unlink: true},
		o.mc, o.mcsrf, nil); code != 200 || slices.Contains(o.ops.unlinks, 904) {
		t.Errorf("Beta web removal: %d, unlinked %v", code, o.ops.unlinks)
	}
}
