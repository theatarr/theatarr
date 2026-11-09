package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// seedHold holds a release for Alpha's movie-remux copy (version 1), as the router would.
func (e *env) seedHold(hash, release, episodes string) int64 {
	e.t.Helper()
	c := router.Candidate{Quality: "Remux-2160p", Score: 3000, QualityRank: 9, UpgradeReason: "better quality"}
	c.Release.Indexer, c.Release.Kind, c.Release.Title, c.Release.InfoHash = "prowlarr-public", "torznab", release, hash
	c.Release.Privacy = policy.PrivacyPublic
	cj, _ := json.Marshal(c)
	now := store.Millis(testNow)
	res, err := e.st.Writer().Exec(`INSERT INTO upgrade_hold (title_id, version_id, version_key, episodes, info_hash,
		release_title, indexer, quality, score, quality_rank, reason, current_quality, current_score, current_at,
		candidate, source, created_at, seen_at) VALUES (1, 1, 'movie-remux', ?, ?, ?, 'prowlarr-public', 'Remux-2160p',
		3000, 9, 'better quality', 'WEBDL-2160p', 1400, 0, ?, 'acquire:1', ?, ?)`, episodes, hash, release, string(cj),
		now, now) // current_at 0: the seeded version_file has no added_at (a newer file would expire the hold)
	if err != nil {
		e.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestUpgradeHoldRoutes: managers list, approve and decline holds, each decision recorded with who and how; the
// approval queues a route of the held release only while the router may write for the version; family accounts
// get 403 on every route and never see a hold.
func TestUpgradeHoldRoutes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	held := e.seedHold(strings.Repeat("c", 40), "Alpha.2020.2160p.UHD.BluRay.REMUX-HELD", "")
	// A second pending row needs another item set; only the key differs here.
	other := e.seedHold(strings.Repeat("d", 40), "Alpha.2020.2160p.UHD.BluRay.REMUX-OTHER", "0")
	mgr := e.account("mgr", auth.RoleManager, auth.StatusActive, false)
	usr := e.account("usr", auth.RoleUser, auth.StatusActive, true)
	mc, mcsrf := e.session(mgr)
	uc, ucsrf := e.session(usr)
	b := APIBase + "/upgrades"

	// Family accounts: 403 on all three, and nothing about the hold in the answer.
	for _, call := range [][2]string{{"GET", b}, {"POST", b + "/1/approve"}, {"POST", b + "/1/decline"}} {
		r := httptest.NewRequest(call[0], call[1], nil)
		r.AddCookie(uc)
		r.Header.Set(auth.CSRFHeader, ucsrf)
		rec := e.do(r)
		if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "REMUX") {
			t.Errorf("user %s %s: %d %s", call[0], call[1], rec.Code, rec.Body.String())
		}
	}

	var list UpgradeHolds
	if code := e.jsonCall("GET", b+"?title=1", nil, mc, "", &list); code != 200 || len(list.Items) != 2 || list.Counts["pending"] != 2 {
		t.Fatalf("list: %d %+v", code, list)
	}
	h := list.Items[0]
	if h.Title.Name != "Alpha" || h.Version != "movie-remux" || h.Current.Quality != "WEBDL-2160p" || h.Current.Score == nil ||
		*h.Current.Score != 1400 || h.Score != 3000 || h.CanApprove || !strings.Contains(h.ApproveReason, "off") {
		t.Errorf("hold %+v", h)
	}
	if code := e.jsonCall("GET", b+"?state=bogus", nil, mc, "", nil); code != 400 {
		t.Errorf("bad state: %d", code)
	}

	// The example policy has the router off: approval is refused and the hold stays pending.
	var apiErr apiError
	if code := e.jsonCall("POST", b+"/"+strconv.FormatInt(held, 10)+"/approve", nil, mc, mcsrf, &apiErr); code != http.StatusConflict ||
		!strings.Contains(apiErr.Error, "router.enabled") {
		t.Fatalf("approve with the router off: %d %+v", code, apiErr)
	}
	e.web.pol.Router.Enabled = true
	e.web.pol.Versions["movie-remux"].Mode, e.web.pol.Versions["movie-remux"].Grab = policy.ModeActive, true
	t.Cleanup(func() {
		e.web.pol.Router.Enabled = false
		e.web.pol.Versions["movie-remux"].Mode = policy.ModeShadow
	})
	var got UpgradeHold
	if code := e.jsonCall("POST", b+"/"+strconv.FormatInt(held, 10)+"/approve", nil, mc, mcsrf, &got); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	if got.State != "approved" || got.DecidedBy == nil || got.DecidedBy.Username != "mgr" || got.DecidedVia != "session" ||
		got.DecidedAt == nil || !got.DecidedAt.Equal(testNow) {
		t.Errorf("approved %+v", got)
	}
	var payload string
	if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE kind = ?`, router.JobRoute).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var req router.Request
	if err := json.Unmarshal([]byte(payload), &req); err != nil || req.Hold != held || len(req.Candidates) != 1 ||
		req.Candidates[0].Release.InfoHash != strings.Repeat("c", 40) || req.Version != "movie-remux" {
		t.Errorf("route job %s (%v)", payload, err)
	}
	if code := e.jsonCall("POST", b+"/"+strconv.FormatInt(held, 10)+"/approve", nil, mc, mcsrf, nil); code != http.StatusConflict {
		t.Errorf("second approve: %d", code)
	}
	if code := e.jsonCall("POST", b+"/999/decline", nil, mc, mcsrf, nil); code != http.StatusNotFound {
		t.Errorf("unknown hold: %d", code)
	}

	// Decline with an API key: recorded as that key.
	key, _, err := e.auth.CreateKey(t.Context(), "k-mgr", mgr)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", b+"/"+strconv.FormatInt(other, 10)+"/decline", strings.NewReader(`{"note":"too big for now"}`))
	r.Header.Set(auth.APIKeyHeader, key)
	rec := e.do(r)
	if rec.Code != 200 {
		t.Fatalf("decline: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.State != "declined" || got.Note != "too big for now" ||
		got.DecidedVia != "key k-mgr" || got.DecidedBy == nil || got.DecidedBy.ID != mgr {
		t.Errorf("declined %+v (%v)", got, err)
	}
	if code := e.jsonCall("GET", b, nil, mc, "", &list); code != 200 || len(list.Items) != 0 {
		t.Errorf("pending after decisions: %d %d", code, len(list.Items))
	}
	if code := e.jsonCall("GET", b+"?state=approved,declined", nil, mc, "", &list); code != 200 || len(list.Items) != 2 {
		t.Errorf("decided: %d %d", code, len(list.Items))
	}
}
