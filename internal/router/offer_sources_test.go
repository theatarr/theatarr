package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// storeSource stores the source the arr history gave for the adopted link 1 (link.adopted_source).
func (e *offerEnv) storeSource(hash, indexer string) {
	e.t.Helper()
	raw, _ := json.Marshal(adoptedSource{From: "radarr /arr/Movie.mkv", Hash: hash, Release: "Movie.2001.2160p.WEB-DL-GRP",
		Indexers: []string{indexer}, Version: "movie-remux"})
	e.exec(`UPDATE link SET adopted_source = ? WHERE id = 1`, string(raw))
}

func (e *offerEnv) sourceFile(kind, hash string) {
	e.t.Helper()
	meta, _ := json.Marshal(map[string]any{"InfoHash": hash, "Size": 1000})
	e.exec(`INSERT INTO link (id, title_id, path, target, release_title, quality, file_metadata,
		created_at, updated_at) VALUES (1, 1, '/theatarr/Movie.mkv', '/mount/__all__/Movie/video.mkv',
		'Movie.2001.2160p.WEB-DL-GRP', 'WEBDL-2160p', ?, 0, 0)`, string(meta))
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 11)`)
	if kind == "adopted" {
		e.exec(`INSERT INTO library_action (at, kind, mode, version_key, link_id, path, target, detail)
			VALUES (0, 'link', 'applied', 'movie-web', 1, '/theatarr/Movie.mkv', '/mount/__all__/Movie/video.mkv',
			'adopted from radarr /arr/Movie.mkv')`)
		e.exec(`UPDATE link SET adopted_from = 'radarr /arr/Movie.mkv' WHERE id = 1`)
	}
}

func TestOfferFileSources(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	hash := strings.Repeat("a", 40)
	e.sourceFile("adopted", strings.ToUpper(hash))
	e.storeSource(hash, "dmm")
	// A second version may accept an active route too.
	e.off.Policy.Versions["movie-active"] = &policy.VersionPolicy{Kind: policy.KindMovie, Mode: policy.ModeActive}
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (15, 1, 'movie-active', 0, 0)`)
	sr, err := e.off.Sweep(e.ctx, true, 0)
	if err != nil || sr.Downloads != 1 || len(sr.Offers) != 2 || sr.Outcomes[OfferPlanned] != 1 || sr.Outcomes[OfferRouted] != 1 {
		t.Fatalf("dry run: %+v, %v", sr, err)
	}
	if len(e.enq.reqs) != 0 || e.outcome("movie-extra", 1, hash) != "" {
		t.Fatal("dry run recorded or queued")
	}
	sr, err = e.off.Sweep(e.ctx, false, 0)
	if err != nil || sr.Downloads != 1 || len(e.enq.reqs) != 2 {
		t.Fatalf("sweep: %+v, routes %+v, %v", sr, e.enq.reqs, err)
	}
	for _, req := range e.enq.reqs {
		if req.Version != "movie-extra" && req.Version != "movie-active" {
			t.Errorf("offered to the source or the arr's library: %+v", req)
		}
		if req.Candidates[0].Release.InfoHash != hash || req.Candidates[0].Release.Size != 1000 {
			t.Errorf("lost file identity: %+v", req)
		}
	}
	if sr, err = e.off.Sweep(e.ctx, false, 0); err != nil || sr.Downloads != 0 || len(sr.Outcomes) != 0 || len(e.enq.reqs) != 2 {
		t.Fatalf("repeated sweep: %+v, %v", sr, err)
	}
}

// The adopted link's hash is its own (content) when it has one, else the stored source's; without either it is
// declined as no_hash.
func TestOfferFileHashDiscovery(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"content", "stored", "no_hash"} {
		t.Run(method, func(t *testing.T) {
			e := newOfferEnv(t)
			hash := strings.Repeat("b", 40)
			e.sourceFile("adopted", "")
			switch method {
			case "content":
				e.storeSource("", "dmm")
				e.exec(`INSERT INTO content (id, info_hash, first_seen_at, last_seen_at) VALUES (1, ?, 0, 0)`, hash)
				e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (1, 1, 'video.mkv', 1000)`)
				e.exec(`UPDATE link SET content_file_id = 1 WHERE id = 1`)
			case "stored":
				e.storeSource(hash, "dmm")
			case "no_hash":
				e.storeSource("", "dmm")
			}
			sr, err := e.off.Sweep(e.ctx, false, 0)
			if err != nil {
				t.Fatal(err)
			}
			if method == "no_hash" {
				var reasons string
				if err := e.st.Reader().QueryRow(`SELECT reasons FROM router_offer WHERE version_key = 'movie-extra' AND info_hash = ''`).Scan(&reasons); err != nil {
					t.Fatal(err)
				}
				if reasons != `["no_hash"]` || sr.Outcomes[OfferDeclined] != 1 || len(e.enq.reqs) != 0 {
					t.Fatalf("hashless source: %+v, reasons %s, routes %+v", sr, reasons, e.enq.reqs)
				}
				if again, err := e.off.Sweep(e.ctx, false, 0); err != nil || len(again.Outcomes) != 0 {
					t.Fatalf("hashless source repeated: %+v, %v", again, err)
				}
			} else if sr.Outcomes[OfferPlanned] != 1 || len(e.enq.reqs) != 1 || e.enq.reqs[0].Candidates[0].Release.InfoHash != hash {
				t.Fatalf("hash discovery: %+v, routes %+v", sr, e.enq.reqs)
			}
		})
	}
}

func TestOfferFileAlreadyHeld(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	hash := strings.Repeat("9", 40)
	e.sourceFile("adopted", strings.ToUpper(hash))
	e.storeSource(hash, "dmm")
	e.download(1, 1, 10, "movie-remux", hash, "Movie.2001.2160p.WEB-DL-GRP", "imported")
	res, err := e.off.Offer(e.ctx, 1, false)
	if err != nil || byVersion(res)["movie-web"].Outcome != OfferSame || len(e.enq.reqs) != 1 {
		t.Fatalf("existing adopted holder: %+v, %v", res, err)
	}
}

func TestOfferFileSeriesPack(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	e.off.Policy.Versions["tv-web"] = &policy.VersionPolicy{Kind: policy.KindSeries, Mode: policy.ModeActive}
	e.exec(`INSERT INTO title (id, kind, name, series_type, created_at, updated_at) VALUES (3, 'series', 'Show', 'standard', 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (30, 3, 'tv', 0, 0), (31, 3, 'tv-web', 0, 0)`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES
		(301, 3, 1, 1, 'One', 0, 0), (302, 3, 1, 2, 'Two', 0, 0)`)
	hash := strings.Repeat("a", 40)
	for i := 1; i <= 2; i++ {
		from := fmt.Sprintf("sonarr /arr/%d.mkv", i)
		raw, _ := json.Marshal(adoptedSource{From: from, Hash: hash, Release: "Show.S01.2160p.WEB-DL",
			Indexers: []string{"dmm"}, Version: "tv"})
		e.exec(`INSERT INTO link (id, title_id, path, target, release_title, file_metadata, adopted_from, adopted_source,
			created_at, updated_at) VALUES (?, 3, ?, ?, 'Show.S01.2160p.WEB-DL', ?, ?, ?, 0, 0)`, i,
			fmt.Sprintf("/theatarr/%d.mkv", i), fmt.Sprintf("/mount/%d.mkv", i), fmt.Sprintf(`{"InfoHash":%q,"Size":1000}`, hash),
			from, string(raw))
		e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (?, 30)`, i)
		e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (?, ?)`, i, 300+i)
	}
	e.off.DecideSeries = func(_ context.Context, rel decide.Release, _ decide.Title, _ decide.Version,
		search *decide.SeriesSearch) (*decide.Decision, error) {
		if search == nil || len(search.Episodes) != 2 || rel.Size != 2000 {
			t.Fatalf("incomplete pack: search %+v, release %+v", search, rel)
		}
		return &decide.Decision{Accepted: true, EpisodeIDs: search.Episodes}, nil
	}
	if sr, err := e.off.Sweep(e.ctx, false, 0); err != nil || sr.Outcomes[OfferRouted] != 1 || len(e.enq.reqs) != 1 {
		t.Fatalf("series file offer: %+v, %v", sr, err)
	}
	if got := e.enq.reqs[0].Candidates[0].Episodes; len(got) != 2 || got[0] != 301 || got[1] != 302 {
		t.Fatalf("pack episodes: %v", got)
	}
}

// An unknown source indexer is declined without asking the target; a private-labelled configured one is decided
// like any other (privacy is a label only).
func TestOfferFilePrivateAndUnknownIndexer(t *testing.T) {
	t.Parallel()
	for _, indexer := range []string{"private", "unknown", ""} {
		t.Run(indexer, func(t *testing.T) {
			e := newOfferEnv(t)
			hash := strings.Repeat("d", 40)
			e.off.Policy.Indexers["private"] = &policy.Indexer{Privacy: policy.PrivacyPrivate}
			e.sourceFile("adopted", hash)
			e.storeSource(hash, indexer)
			sr, err := e.off.Sweep(e.ctx, false, 0)
			if indexer == "private" {
				if err != nil || sr.Outcomes[OfferPlanned] != 1 || len(e.asked) == 0 {
					t.Fatalf("private-labelled source: %+v, routes %+v, %v", sr, e.enq.reqs, err)
				}
				return
			}
			if err != nil || sr.Outcomes[OfferDeclined] != 1 || len(e.enq.reqs) != 0 || len(e.asked) != 0 {
				t.Fatalf("unknown source: %+v, routes %+v, %v", sr, e.enq.reqs, err)
			}
		})
	}
}

func TestOfferGaveUpDailyAndBounded(t *testing.T) {
	e := newOfferEnv(t)
	hash := strings.Repeat("e", 40)
	e.off.Queue = jobs.NewQueue(e.st)
	e.download(1, 2, 20, "movie-remux", hash, "Other.2002.2160p.WEB-DL-GRP", "imported")
	if _, err := e.off.Offer(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(OfferRetryFor)
	if _, err := e.off.Offer(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt <= OfferGaveUpLimit; attempt++ {
		var retries int
		var next sql.NullInt64
		var reasons string
		if err := e.st.Reader().QueryRow(`SELECT retry_attempts, next_at, reasons FROM router_offer
			WHERE version_key = 'movie-web' AND info_hash = ?`, hash).Scan(&retries, &next, &reasons); err != nil {
			t.Fatal(err)
		}
		if retries != attempt || e.outcome("movie-web", 2, hash) != OfferGaveUp {
			t.Fatalf("attempt %d: retries %d", attempt, retries)
		}
		if attempt == OfferGaveUpLimit {
			if next.Valid || !strings.Contains(reasons, "retry_limit") {
				t.Fatalf("cap: next %+v, reasons %s", next, reasons)
			}
			break
		}
		if !next.Valid || next.Int64 != store.Millis(e.now.Add(OfferGaveUpEvery)) {
			t.Fatalf("daily retry: next %+v", next)
		}
		var scheduled int
		if err := e.st.Reader().QueryRow(`SELECT COUNT(*) FROM job WHERE kind = ? AND run_at = ?`, JobOffer, next.Int64).Scan(&scheduled); err != nil || scheduled != 1 {
			t.Fatalf("daily job: count %d, %v", scheduled, err)
		}
		e.now = e.now.Add(OfferGaveUpEvery - time.Millisecond)
		if res, err := e.off.Offer(e.ctx, 1, false); err != nil || len(res) != 0 {
			t.Fatalf("early daily attempt: %+v, %v", res, err)
		}
		e.now = e.now.Add(time.Millisecond)
		if _, err := e.off.Offer(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
	}
	// Even a readiness change cannot reopen an exhausted offer.
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (21, 2, 'movie-web', 0, 0)`)
	e.now = e.now.Add(30 * OfferGaveUpEvery)
	if res, err := e.off.Offer(e.ctx, 1, false); err != nil || len(res) != 0 || len(e.enq.reqs) != 0 {
		t.Fatalf("exhausted offer retried: %+v, %v", res, err)
	}
}

func TestOfferGaveUpEarlyOnTitleAdded(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	hash := strings.Repeat("f", 40)
	e.download(1, 2, 20, "movie-remux", hash, "Other.2002.2160p.WEB-DL-GRP", "imported")
	if _, err := e.off.Offer(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(OfferRetryFor)
	if _, err := e.off.Offer(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (21, 2, 'movie-web', 0, 0)`)
	e.now = e.now.Add(time.Minute)
	res, err := e.off.Offer(e.ctx, 1, false)
	if err != nil || len(res) != 1 || res[0].Version != "movie-web" || res[0].Outcome != OfferRouted {
		t.Fatalf("early retry: %+v, %v", res, err)
	}
}

func TestOfferGaveUpEarlyWhenReadable(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	hash := strings.Repeat("1", 40)
	e.sourceFile("adopted", hash)
	e.storeSource(hash, "dmm")
	e.exec(`INSERT INTO library_dangling (path, target, root, first_seen_at, last_seen_at)
		VALUES ('/theatarr/Movie.mkv', '/mount/__all__/Movie/video.mkv', '/theatarr', 0, 0)`)
	if _, err := e.off.Sweep(e.ctx, false, 0); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(OfferRetryFor)
	if _, err := e.off.Sweep(e.ctx, false, 0); err != nil {
		t.Fatal(err)
	}
	e.exec(`DELETE FROM library_dangling`)
	sr, err := e.off.Sweep(e.ctx, false, 0)
	if err != nil || sr.Outcomes[OfferPlanned] != 1 || len(e.enq.reqs) != 1 {
		t.Fatalf("readable retry: %+v, %v", sr, err)
	}
}

func TestOfferFileRetryJob(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	hash := strings.Repeat("b", 40)
	e.off.Queue = jobs.NewQueue(e.st)
	e.sourceFile("adopted", hash)
	e.storeSource(hash, "dmm")
	e.exec(`DELETE FROM version WHERE id = 12`)
	if _, err := e.off.Sweep(e.ctx, false, 0); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE kind = ? ORDER BY id LIMIT 1`, JobOffer).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var p offerPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil || p.SourceKind != "link" || p.SourceID != 1 || p.DownloadID != 0 {
		t.Fatalf("file retry payload: %s, %v", payload, err)
	}
	e.now = e.now.Add(OfferRetryFor)
	job := &jobs.Job{Payload: []byte(payload)}
	if err := e.off.handle(e.ctx, job); err != nil || e.outcome("movie-extra", 1, hash) != OfferGaveUp {
		t.Fatalf("file gave up: %v", err)
	}
	e.now = e.now.Add(OfferGaveUpEvery)
	if err := e.off.handle(e.ctx, job); err != nil {
		t.Fatal(err)
	}
	var retries int
	if err := e.st.Reader().QueryRow(`SELECT retry_attempts FROM router_offer WHERE info_hash = ?`, hash).Scan(&retries); err != nil || retries != 1 {
		t.Fatalf("file daily retry: %d, %v", retries, err)
	}
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (12, 1, 'movie-extra', 0, 0)`)
	if err := e.off.handle(e.ctx, job); err != nil || e.outcome("movie-extra", 1, hash) != OfferPlanned || len(e.enq.reqs) != 1 {
		t.Fatalf("file early retry: routes %+v, %v", e.enq.reqs, err)
	}
	if err := e.off.handle(e.ctx, job); err != nil || len(e.enq.reqs) != 1 {
		t.Fatalf("replayed file job: routes %+v, %v", e.enq.reqs, err)
	}
}

func TestOfferMigratedGaveUpWaitsUntilDue(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	hash := strings.Repeat("c", 40)
	e.download(1, 2, 20, "movie-remux", hash, "Other.2002.2160p.WEB-DL", "imported")
	e.exec(`INSERT INTO router_offer (version_key, title_id, info_hash, from_version, source_id, outcome,
		first_at, next_at, updated_at) VALUES ('movie-web', 2, ?, 'movie-remux', 1, 'gave_up', ?, ?, ?)`,
		hash, store.Millis(e.now.Add(-OfferRetryFor)), store.Millis(e.now.Add(OfferGaveUpEvery)), store.Millis(e.now))
	res, err := e.off.Offer(e.ctx, 1, false)
	if err != nil || byVersion(res)["movie-web"].Outcome != "" {
		t.Fatalf("migrated offer retried early: %+v, %v", res, err)
	}
	e.now = e.now.Add(OfferGaveUpEvery)
	res, err = e.off.Offer(e.ctx, 1, false)
	if err != nil || byVersion(res)["movie-web"].Outcome != OfferGaveUp {
		t.Fatalf("migrated offer not retried when due: %+v, %v", res, err)
	}
}

func TestOfferSupersededDownload(t *testing.T) {
	t.Parallel()
	for _, inPlace := range []bool{false, true} {
		t.Run(map[bool]string{false: "trash", true: "in_place"}[inPlace], func(t *testing.T) {
			e := newOfferEnv(t)
			hash := strings.Repeat("2", 40)
			e.download(1, 2, 20, "movie-remux", hash, "Other.2002.2160p.WEB-DL-GRP", "imported")
			e.exec(`INSERT INTO link (id, path, target, title_id, download_id, created_at, updated_at)
				VALUES (1, '/library/old.mkv', '/mount/old/video.mkv', 2, 1, 0, 0)`)
			e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 20)`)
			e.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, link_id, path, target)
				VALUES (0, 'link', 'applied', 'movie-remux', 1, 1, '/library/old.mkv', '/mount/old/video.mkv')`)
			if _, err := e.off.Offer(e.ctx, 1, false); err != nil {
				t.Fatal(err)
			}
			if inPlace {
				e.download(2, 2, 20, "movie-remux", strings.Repeat("3", 40), "Other.2002.2160p.BluRay.REMUX-GRP", "imported")
				e.exec(`UPDATE link SET download_id = 2, target = '/mount/new/video.mkv' WHERE id = 1`)
			} else {
				e.exec(`UPDATE link SET state = 'unlinked' WHERE id = 1`)
			}
			e.exec(`INSERT INTO library_action (at, kind, mode, version_key, download_id, path, target)
				VALUES (0, 'replace', 'applied', 'movie-remux', 2, '/library/old.mkv', '/mount/old/video.mkv')`)
			res, err := e.off.Offer(e.ctx, 1, true)
			if err != nil || len(res) != 2 || res[0].Reasons[0] != "superseded" || e.outcome("movie-web", 2, hash) != OfferPending {
				t.Fatalf("dry cancellation: %+v, %v", res, err)
			}
			res, err = e.off.Offer(e.ctx, 1, false)
			if err != nil || len(res) != 2 || len(e.enq.reqs) != 0 || e.outcome("movie-web", 2, hash) != OfferDeclined {
				t.Fatalf("cancelled: %+v, %v", res, err)
			}
			if res, err := e.off.Offer(e.ctx, 1, false); err != nil || len(res) != 0 {
				t.Fatalf("superseded offered again: %+v, %v", res, err)
			}
			// Supersession also prevents the first offer; it does not depend on existing offer memory.
			e.exec(`DELETE FROM router_offer`)
			if res, err := e.off.Offer(e.ctx, 1, false); err != nil || len(res) != 0 || len(e.enq.reqs) != 0 {
				t.Fatalf("new offer of superseded download: %+v, %v", res, err)
			}
		})
	}
}

func TestOfferSupersededFileSources(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	hash := strings.Repeat("4", 40)
	e.sourceFile("adopted", hash)
	e.storeSource(hash, "dmm")
	e.exec(`DELETE FROM version WHERE id = 12`)
	if _, err := e.off.Sweep(e.ctx, false, 0); err != nil {
		t.Fatal(err)
	}
	// Refresh may reuse the adopted link id, overwriting its old hash and target.
	meta, _ := json.Marshal(map[string]string{"InfoHash": strings.Repeat("5", 40)})
	e.exec(`UPDATE link SET file_metadata = ?, target = '/mount/new/video.mkv' WHERE id = 1`, string(meta))
	e.exec(`INSERT INTO library_action (at, kind, mode, version_key, link_id, path, target)
		VALUES (0, 'replace', 'applied', 'movie-web', 1, '/theatarr/Movie.mkv', '/mount/__all__/Movie/video.mkv')`)
	sr, err := e.off.Sweep(e.ctx, false, 0)
	if err != nil || sr.Outcomes[OfferDeclined] < 1 || e.outcome("movie-extra", 1, hash) != OfferDeclined {
		t.Fatalf("superseded file: %+v, %v", sr, err)
	}
	var reasons string
	var next sql.NullInt64
	if err := e.st.Reader().QueryRow(`SELECT reasons, next_at FROM router_offer WHERE version_key = 'movie-extra' AND info_hash = ?`, hash).Scan(&reasons, &next); err != nil {
		t.Fatal(err)
	}
	if reasons != `["superseded"]` || next.Valid || len(e.enq.reqs) != 0 {
		t.Fatalf("cancellation reasons %s, next %+v, routes %+v", reasons, next, e.enq.reqs)
	}
}

// adoptedEnv is an adopted link without a hash of its own whose stored source gives the hash, release and indexer;
// indexers records the indexer each decision was given.
func adoptedEnv(t *testing.T, hash string) (*offerEnv, *[]string) {
	e := newOfferEnv(t)
	e.sourceFile("adopted", "")
	e.storeSource(hash, "dmm")
	var indexers []string
	decideFn := e.off.Decide
	e.off.Decide = func(ctx context.Context, rel decide.Release, t decide.Title, v decide.Version, f *decide.ExistingFile) (*decide.Decision, error) {
		indexers = append(indexers, rel.Indexer)
		return decideFn(ctx, rel, t, v, f)
	}
	return e, &indexers
}

// TestOfferAdoptedSourceStored: an adopted link is offered from its stored source, with the stored indexer; a dry run
// decides the same and writes nothing to the link.
func TestOfferAdoptedSourceStored(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("e", 40)
	e, indexers := adoptedEnv(t, hash)
	before, err := e.off.Sweep(e.ctx, true, 0)
	if err != nil || before.Outcomes[OfferPlanned] != 1 {
		t.Fatalf("dry run: %+v, %v", before, err)
	}
	if len(*indexers) == 0 || (*indexers)[0] != "dmm" {
		t.Fatalf("indexers %v", *indexers)
	}
	var stored string
	if err := e.st.Reader().QueryRow(`SELECT adopted_source FROM link WHERE id = 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	sr, err := e.off.Sweep(e.ctx, false, 0)
	if err != nil || fmt.Sprint(sr.Outcomes) != fmt.Sprint(before.Outcomes) || len(e.enq.reqs) != 1 ||
		e.enq.reqs[0].Candidates[0].Release.InfoHash != hash {
		t.Fatalf("sweep: %+v, before %+v, routes %+v, %v", sr, before, e.enq.reqs, err)
	}
	var after string
	if err := e.st.Reader().QueryRow(`SELECT adopted_source FROM link WHERE id = 1`).Scan(&after); err != nil || after != stored {
		t.Fatalf("stored source changed: %s, %v", after, err)
	}
}

// TestAdoptedLinkWithoutStoredSourceNotOffered: an adopted link with no stored source, or one stored for the arr file
// it was adopted from before a refresh moved it, has no indexer and is not offered; the arr history is not read.
func TestAdoptedLinkWithoutStoredSourceNotOffered(t *testing.T) {
	t.Parallel()
	for _, c := range []string{"none", "moved"} {
		t.Run(c, func(t *testing.T) {
			hash := strings.Repeat("e", 40)
			e := newOfferEnv(t)
			e.sourceFile("adopted", hash)
			e.exec(`INSERT INTO arr_instance (name, flavour, policy_key, created_at, updated_at)
				VALUES ('radarr', 'radarr', 'movie-remux', 0, 0)`)
			e.exec(`INSERT INTO arr_history (instance, arr_id, event_type, date_at, title_id, version_id, download_id,
				info_hash, source_title, data) VALUES ('radarr', 1, 'grabbed', 0, 1, 10, 'torrent-1', ?,
				'Movie.2001.2160p.WEB-DL-GRP', '{"indexer":"dmm","importedPath":"/arr/Movie.mkv"}')`, hash)
			if c == "moved" {
				e.storeSource(hash, "dmm")
				e.exec(`UPDATE link SET adopted_from = 'radarr /arr/Movie.2.mkv' WHERE id = 1`)
			}
			sr, err := e.off.Sweep(e.ctx, false, 0)
			if err != nil || sr.Downloads != 0 || len(sr.Outcomes) != 0 || len(e.asked) != 0 || len(e.enq.reqs) != 0 {
				t.Fatalf("offered: %+v, routes %+v, %v", sr, e.enq.reqs, err)
			}
		})
	}
}

// TestAdoptedLinkOffersWithoutFollowing: an adopted link in a library that follows nothing, with its source stored,
// is offered to the title's other libraries and never to the arr's library.
func TestAdoptedLinkOffersWithoutFollowing(t *testing.T) {
	t.Parallel()
	e, _ := adoptedEnv(t, strings.Repeat("e", 40))
	sr, err := e.off.Sweep(e.ctx, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	offered := map[string]bool{}
	for _, r := range sr.Offers {
		offered[r.Version] = true
	}
	if offered["movie-remux"] || offered["movie-web"] || !offered["movie-extra"] {
		t.Fatalf("offers %+v", sr.Offers)
	}
}

// TestArrFileOfferForgotten: an open offer (pending or retry memory) whose source was an imported arr file is
// deleted, not declined, so the same file from an adopted link with a stored source is offered normally.
func TestArrFileOfferForgotten(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{OfferPending, OfferGaveUp} {
		t.Run(outcome, func(t *testing.T) {
			e := newOfferEnv(t)
			hash := strings.Repeat("e", 40)
			e.exec(`INSERT INTO router_offer (version_key, title_id, info_hash, from_version, release_title, outcome,
				reasons, attempts, first_at, next_at, updated_at, source_kind, source_id) VALUES ('movie-extra', 1, ?,
				'movie-remux', 'Movie.2001.2160p.WEB-DL-GRP', ?, '[]', 1, 0, 0, 0, 'arr_file', 1)`, hash, outcome)
			e.sourceFile("adopted", hash)
			e.storeSource(hash, "dmm")
			sr, err := e.off.Sweep(e.ctx, false, 0)
			if err != nil || sr.Outcomes[OfferDeclined] != 0 || e.outcome("movie-extra", 1, hash) != OfferPlanned {
				t.Fatalf("sweep: %+v, outcome %q, %v", sr, e.outcome("movie-extra", 1, hash), err)
			}
			var n int
			if err := e.st.Reader().QueryRow(`SELECT COUNT(*) FROM router_offer WHERE source_kind = 'arr_file'`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("arr file offers left: %d, %v", n, err)
			}
		})
	}
}
