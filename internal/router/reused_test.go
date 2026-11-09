package router

import (
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

func routeReusedRD(t *testing.T) (*env, *fakeDebrid, int64) {
	t.Helper()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, false)
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("a"))
	e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at) VALUES (7, 'rd', 'HELD', 0)`)
	f.rd["HELD"] = &fakeRD{hash: h("a"), status: "waiting_files_selection"}
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || !res.Reused || res.Provider != "rd" || res.TorrentID != "HELD" {
		t.Fatalf("reuse: %+v %v", res, err)
	}
	return e, f, res.DownloadID
}

func TestReusedWaitingFilesSelectionNeverWrites(t *testing.T) {
	t.Parallel()
	e, f, dl := routeReusedRD(t)
	for i := 0; i < 3; i++ {
		sum, err := e.r.Poll(e.ctx)
		if err != nil || sum.Selected != 0 {
			t.Fatalf("poll %d: %+v %v", i+1, sum, err)
		}
	}
	if n := f.count("rd.test POST /rest/1.0/torrents/selectFiles/HELD"); n != 0 {
		t.Errorf("sent %d file selections for a reused torrent", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'added' AND reused = 1
		AND files_selected = 0`, dl); n != 1 {
		t.Fatal("reused torrent did not remain added while awaiting its owner")
	}

	// The provider's owner selects the files; Theatarr observes its download progress normally.
	f.rd["HELD"].status = "downloading"
	sum, err := e.r.Poll(e.ctx)
	if err != nil {
		t.Fatalf("downloading poll: %+v %v", sum, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'downloading' AND reused = 1`, dl); n != 1 {
		t.Fatal("reused torrent did not enter downloading")
	}

	// The provider reports completion; Theatarr marks the reused torrent ready without selecting files.
	f.rd["HELD"].status = "downloaded"
	sum, err = e.r.Poll(e.ctx)
	if err != nil || sum.Ready != 1 {
		t.Fatalf("downloaded poll: %+v %v", sum, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'ready' AND reused = 1`, dl); n != 1 {
		t.Fatal("reused torrent did not become ready")
	}
	if f.count("rd.test POST /rest/1.0/torrents/selectFiles/HELD") != 0 || f.count("rd.test DELETE /rest/1.0/torrents/delete/HELD") != 0 {
		t.Fatal("theatarr wrote to the reused torrent")
	}
}

// A reused torrent whose file list holds no video at all fails at once, blocklisted on our side, instead of waiting
// for the stalled deadline: no selection by its owner can give the library a file to import. Theatarr still never selects
// or deletes anything on the shared torrent.
func TestReusedWaitingWithoutVideoFailsWithoutProviderWrite(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("a"))
	e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at) VALUES (7, 'rd', 'HELD', 0)`)
	e.pr["rd"].set("HELD", provider.Torrent{Hash: h("a"), Status: "downloading", State: provider.StateDownloading})
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || !res.Reused {
		t.Fatalf("reuse: %+v %v", res, err)
	}
	e.pr["rd"].set("HELD", provider.Torrent{Status: rdWaitingFiles, State: provider.StateQueued,
		Files: []provider.File{{ID: "1", Path: "readme.nfo"}}})
	sum, err := e.r.Poll(e.ctx)
	if err != nil || sum.Failed != 1 || sum.Selected != 0 || sum.Stalled != 0 {
		t.Fatalf("poll: %+v %v", sum, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'failed' AND reused = 1 AND error =
		'no video file in the torrent; the reused torrent is shared and was kept on rd'`, res.DownloadID); n != 1 {
		t.Fatal("reused torrent without a video not failed with the reason")
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE download_id = ?`, res.DownloadID); n != 1 {
		t.Fatal("not blocklisted")
	}
	if len(e.ad["rd"].selects) != 0 || len(e.ad["rd"].deletes) != 0 {
		t.Fatal("theatarr wrote to the reused torrent")
	}
}

func TestReusedWaitingFilesSelectionStallsWithoutProviderWrite(t *testing.T) {
	e, f, dl := routeReusedRD(t)
	e.clock = e.clock.Add(e.r.set.StalledAfter)
	sum, err := e.r.Poll(e.ctx)
	if err != nil || sum.Stalled != 1 || sum.Failed != 1 {
		t.Fatalf("poll: %+v %v", sum, err)
	}
	var msg string
	if err := e.st.Reader().QueryRow(`SELECT error FROM download WHERE id = ? AND state = 'failed'`, dl).Scan(&msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "reused torrent is shared and was not deleted") {
		t.Errorf("stalled message %q", msg)
	}
	if f.count("rd.test POST /rest/1.0/torrents/selectFiles/HELD") != 0 || f.count("rd.test DELETE /rest/1.0/torrents/delete/HELD") != 0 {
		t.Fatal("theatarr wrote to the reused torrent after the stalled deadline")
	}
}

func TestReusedTorrentHashMismatchFailsWithoutBlocklisting(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("a"))
	e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at) VALUES (7, 'rd', 'HELD', 0)`)
	// The entry holds the hash when it is reused (the reuse reads it first), and another one by the poll.
	e.pr["rd"].set("HELD", provider.Torrent{Hash: h("a"), Status: "downloading", State: provider.StateDownloading})
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || !res.Reused || res.TorrentID != "HELD" {
		t.Fatalf("reuse: %+v %v", res, err)
	}
	e.pr["rd"].set("HELD", provider.Torrent{Hash: h("b"), Status: "downloading", State: provider.StateDownloading})
	if sum, err := e.r.Poll(e.ctx); err != nil || sum.Failed != 1 {
		t.Fatalf("hash mismatch poll: %+v %v", sum, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'failed' AND error = 'provider returned a different torrent for this ID'`, res.DownloadID); n != 1 {
		t.Fatalf("mismatched reused download state count=%d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Fatalf("mismatched reused torrent was blocklisted: %d", n)
	}
}
