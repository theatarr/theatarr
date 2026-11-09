package web

import (
	"context"
	"database/sql"
	"github.com/theatarr/theatarr/internal/acquire"
	"net/http"
	"net/http/httptest"
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
	retries       []int64
	unlinks       []int64
	renameApplied []int64
	reassigned    []ReassignRequest
	refuse        string
	reject        bool
	// retried, when set, is the next RetryDownload's answer.
	retried *RetriedDownload
	// unlinkErr, when set, is the next UnlinkLink's error.
	unlinkErr error
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
	_ string) (*GrabbedRelease, error) {
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
	f.grabs = append(f.grabs, rel)
	return &GrabbedRelease{Decision: d, Overridden: f.reject, Job: 1}, nil
}

func (f *fakeOps) RetryDownload(_ context.Context, id int64, _ string) (*RetriedDownload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.take(); err != nil {
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
	return &UnlinkedLink{TitleID: 2, Path: "/lib/Beta/" + strings.Repeat("x", int(id)), Versions: []string{"movie-web"},
		Trash: "/lib/.trash/x"}, nil
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
	o := &opsEnv{ops: &fakeOps{}, acq: &opsAcquirer{}}
	o.env = newEnvWith(t, func(c *Config) {
		c.Ops = o.ops
		c.Add = AddConfig{Acquirer: o.acq}
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
	o.web.pol.Versions[key].Mode, o.web.pol.Versions[key].Grab = policy.ModeActive, true
	o.t.Cleanup(func() {
		o.web.pol.Router.Enabled = false
		o.web.pol.Versions[key].Mode = policy.ModeShadow
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
		!strings.Contains(p.Items[0].Summary, "unblocked by mgr") || p.Items[0].Release.InfoHash != hash {
		t.Fatalf("journal entries %+v %v", p, err)
	}
}

// TestRemoveCopiesAndTitle: the removal scope names each copy with its links and downloads; a copy an arr still
// owns is refused; removing a copy hides and unmonitors it, ends its downloads in hand in Theatarr's records only, and
// with unlink sends its symlinks to Ops (the gate decides); without unlink nothing on disk changes. Removing every
// copy removes the title.
func TestRemoveCopiesAndTitle(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
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
		len(scope.Copies) != 2 || scope.Copies[0].Key != "movie-remux" || scope.Copies[0].ManagedBy != "radarr_4k_debrid" {
		t.Fatalf("scope: %d %+v", code, scope)
	}
	var apiErr apiError
	if code := o.jsonCall("POST", APIBase+"/titles/1/remove", RemoveRequest{}, o.mc, o.mcsrf, &apiErr); code != 409 ||
		!strings.Contains(apiErr.Error, "radarr_4k_debrid") {
		t.Fatalf("managed: %d %+v", code, apiErr)
	}
	if code := o.jsonCall("GET", APIBase+"/titles/1/removal?versions=tv-2160p", nil, o.mc, "", nil); code != 400 {
		t.Errorf("unknown copy: %d", code)
	}
	// Alpha's web copy alone: not the whole title; its files stay.
	var got RemoveResult
	if code := o.jsonCall("POST", APIBase+"/titles/1/remove", RemoveRequest{Versions: []string{"movie-web"}}, o.mc,
		o.mcsrf, &got); code != 200 || got.Title || len(got.Removed) != 1 || len(o.ops.unlinks) != 0 {
		t.Fatalf("remove copy: %d %+v", code, got)
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
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{Unlink: true}, o.mc, o.mcsrf, &got); code != 200 ||
		!got.Title || got.DownloadsEnded != 1 || len(got.Unlinked) != 1 {
		t.Fatalf("remove title: %d %+v", code, got)
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
	acts := o.actions()
	kinds := []string{}
	for _, a := range acts {
		kinds = append(kinds, a.Action)
	}
	if strings.Join(kinds, " ") != "remove_version remove_title unlink_file" || acts[2].Target != "/lib/Beta/xxxxx" {
		t.Errorf("actions %+v", acts)
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
