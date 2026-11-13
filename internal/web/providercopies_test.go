package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// fakeCopier answers like the router: 409 words while paused, no hash refused.
type fakeCopier struct {
	paused bool
	got    []router.CopyRequest
}

func (f *fakeCopier) QueueCopies(_ context.Context, req router.CopyRequest) ([]router.CopyView, error) {
	f.got = append(f.got, req)
	switch {
	case req.Hash == "":
		return nil, router.ErrCopyNoHash
	case f.paused:
		return nil, router.ErrCopyPaused
	}
	return []router.CopyView{{Provider: "tb", State: router.CopyQueued, UpdatedAt: testNow}}, nil
}

func (f *fakeCopier) TorrentHash(_ context.Context, pk, id string) (string, []string, error) {
	return hashA, []string{"Alpha.2020.2160p.BluRay.REMUX-GRP", "alpha.mkv"}, nil
}

// "Add to other providers": the file's hash is resolved and handed to the router; a file without one is refused
// with the plan's words, Pause answers 409. The copies show on the file object for see_files only.
func TestProviderCopiesAPI(t *testing.T) {
	t.Parallel()
	f := &fakeCopier{}
	e := newEnvWith(t, func(c *Config) {
		c.ProviderCopies = func(context.Context) Copier { return f }
	})
	sess := e.login()
	csrf := e.csrfFor(sess)
	var out CopiesResult
	if code := e.jsonCall("POST", APIBase+"/titles/1/copies", CopyTitleRequest{Version: "movie-remux"}, sess, csrf,
		&out); code != 200 || len(out.Copies) != 1 || len(f.got) != 1 || f.got[0].Hash != hashA ||
		f.got[0].Name != "Alpha.2020.2160p.BluRay.REMUX-GRP" || f.got[0].TitleID != 1 {
		t.Fatalf("movie: %d %+v %+v", code, out, f.got)
	}
	var fail apiError
	if code := e.jsonCall("POST", APIBase+"/titles/1/copies", CopyTitleRequest{Version: "movie-web"}, sess, csrf,
		&fail); code != 422 || fail.Error != router.CopyNoHashWords {
		t.Fatalf("no hash: %d %+v", code, fail)
	}
	f.paused = true
	if code := e.jsonCall("POST", APIBase+"/torrents/copy", CopyTorrentRequest{Provider: "rd", ID: "10"}, sess, csrf,
		&fail); code != 409 || !strings.HasPrefix(fail.Error, "Paused:") {
		t.Fatalf("paused: %d %+v", code, fail)
	}
	if last := f.got[len(f.got)-1]; last.Except != "rd" || last.Hash != hashA {
		t.Fatalf("torrents row: %+v", last)
	}
	// A seeded copy shows on the movie's current file for an admin, never for a user without see_files.
	e.exec(`INSERT INTO provider_copy (info_hash, name, provider, state, message, created_at, updated_at)
		VALUES (?, 'x', 'tb', 'not_cached', 'Not cached on TB', 0, ?)`, hashA, store.Millis(testNow.Add(-time.Hour)))
	if body := e.get(APIBase+"/titles/1", sess).Body.String(); !strings.Contains(body,
		`"copies":[{"provider":"tb","state":"not_cached","message":"Not cached on TB"`) {
		t.Fatalf("admin title page: %s", body)
	}
	uid := e.account("viewer", "user", "active", false)
	us, _ := e.session(uid)
	if rec := e.get(APIBase+"/titles/1", us); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "copies") {
		t.Fatalf("user title page: %d %s", rec.Code, rec.Body.String())
	}
	if code := e.jsonCall("POST", APIBase+"/titles/1/copies", CopyTitleRequest{Version: "movie-remux"}, us,
		e.auth.SessionCSRF(us.Value), nil); code != http.StatusForbidden {
		t.Fatalf("user may copy: %d", code)
	}
}

// J2, J3, K: the action resolves the hash the page shows (an arr file whose torrent hash is its download id), the
// page shows that file's copies, a Torrents row's every name reaches the router, and GET /torrents/copies reads one
// torrent's copies for the Torrents page to poll.
func TestProviderCopiesSameHashAndPoll(t *testing.T) {
	t.Parallel()
	f := &fakeCopier{}
	e := newEnvWith(t, func(c *Config) {
		c.ProviderCopies = func(context.Context) Copier { return f }
	})
	const dl = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	e.exec(`UPDATE version_file SET download_id = ? WHERE version_id = 3`, dl)
	sess := e.login()
	csrf := e.csrfFor(sess)
	if code := e.jsonCall("POST", APIBase+"/titles/2/copies", CopyTitleRequest{Version: "movie-web"}, sess, csrf,
		nil); code != 200 || f.got[0].Hash != strings.ToLower(dl) {
		t.Fatalf("download id hash: %d %+v", code, f.got)
	}
	e.exec(`INSERT INTO provider_copy (info_hash, name, provider, state, created_at, updated_at)
		VALUES (?, 'x', 'tb', 'queued', 0, 0)`, strings.ToLower(dl))
	if body := e.get(APIBase+"/titles/2", sess).Body.String(); !strings.Contains(body, `"copies":[{"provider":"tb","state":"queued"`) {
		t.Fatalf("the page does not show the action's copies: %s", body)
	}
	if code := e.jsonCall("POST", APIBase+"/torrents/copy", CopyTorrentRequest{Provider: "rd", ID: "10"}, sess, csrf,
		nil); code != 200 || len(f.got[1].Names) != 1 || f.got[1].Names[0] != "alpha.mkv" {
		t.Fatalf("names: %d %+v", code, f.got)
	}
	var out CopiesResult
	if code := e.getKey(APIBase+"/torrents/copies?hash="+dl, e.apiKey(), &out); code != 200 || len(out.Copies) != 1 ||
		out.Copies[0].State != "queued" {
		t.Fatalf("poll: %d %+v", code, out)
	}
}
