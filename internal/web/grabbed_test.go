package web

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

const audioRefusal = "Import refused: French and Spanish audio only; this library wants English or Japanese. " +
	"Another release with the same audio would be taken."

func TestShortReason(t *testing.T) {
	if activityMessage("library: "+audioRefusal) != "library: "+audioRefusal {
		t.Errorf("the activity list cuts the wrong-audio refusal: %q", activityMessage("library: "+audioRefusal))
	}
	t.Parallel()
	for _, c := range []struct{ err, provider, want string }{
		{"provider status error", "rd", "RD reported an error"},
		{"provider status magnet_error", "", "The provider reported an error"},
		{"stalled: not ready 6h0m0s after the add (provider status downloading)", "tb", "Download stalled"},
		{"miss on every provider", "rd", "No provider accepted this release"},
		{"no video file in the torrent; the finished torrent was kept on rd", "rd", "No video file in the torrent; the finished torrent was kept on rd"},
		{"library: no file passed the sample guard", "rd", "Not imported: no file passed the library's checks"},
		{"", "rd", ""},
		// The import's wrong-audio refusal arrives whole (it is longer than maxReason).
		{"library: " + audioRefusal, "rd", audioRefusal},
	} {
		if got := shortReason(c.err, c.provider, strings.ToUpper); got != c.want {
			t.Errorf("shortReason(%q, %q) = %q, want %q", c.err, c.provider, got, c.want)
		}
	}
}

// TestFailureWords: every failure text the router and the library store (and the old library wrapper still stored on
// live) reads as a plain headline and, when it is stored, a detail.
func TestFailureWords(t *testing.T) {
	t.Parallel()
	const cf = "Not a Custom Format upgrade for existing file(s). New: [Anime BD Tier 03 (SeaDex Muxers) - Notifiarr, v2 - " +
		"Notifiarr] (1202) do not improve on Existing: [10bit - Notifiarr, Anime BD Tier 03 (SeaDex Muxers) - Notifiarr, " +
		"Seadex] (6200)"
	for _, c := range []struct {
		name         string
		f            failureFacts
		head, detail string
	}{
		// The old wrapper: the rejection inside the blocklist row says what failed.
		{"old wrapper, not an upgrade", failureFacts{Err: "library: no file passed the sample guard",
			ImportState: "rejected", ImportErr: "no file passed the sample guard",
			Blocked: "no file passed the sample guard (" + cf + ")"},
			notUpgradeWords, "This release scores 1,202, yours 6,200. Nothing was replaced."},
		{"old wrapper alone", failureFacts{Err: "library: no file passed the sample guard"}, notCheckedWords, ""},
		{"wrapper, sample", failureFacts{Err: "library: no file passed the sample guard",
			Blocked: "no file passed the sample guard (sample: runtime 240s is under 600s)"},
			sampleWords, "The file runs 4 min, under the 10 min minimum."},
		{"wrapper, short", failureFacts{Err: "library: no file passed the sample guard",
			Blocked: "no file passed the sample guard (runtime 40 min is under 50% of the expected 128 min)"},
			sampleWords, "The file runs 40 min, under 50% of the expected 128 min."},
		{"wrapper, resolution", failureFacts{Err: "library: no file passed the sample guard",
			Blocked: "no file passed the sample guard (resolution 1280x720 contradicts the release's 2160p)"},
			mislabelWords, "Labelled 2160p, but the video is 1280x720."},
		{"wrapper, invalid", failureFacts{Err: "library: no file passed the sample guard",
			Blocked: "no file passed the sample guard (runtime of 0: not a valid video file)"}, invalidWords, ""},
		{"mislabel 2160p", failureFacts{Err: "library: mislabelled 2160p: the video is 1920x1080"},
			mislabelWords, "Labelled 2160p, but the video is 1920x1080."},
		{"mislabel remux", failureFacts{Err: "library: mislabelled remux: x264 encode with crf=18 (disc video is never CRF)"},
			mislabelWords, "Labelled Remux, but it is an x264 encode with crf=18 (disc video is never CRF)."},
		// Held for review: the download row has no error; the import's reason speaks.
		{"held, not an upgrade", failureFacts{ImportState: "rejected", ImportErr: "not an upgrade for the existing files"},
			"Held for review: these files do not improve on the files you have", "Nothing was replaced."},
		{"held, quality", failureFacts{ImportState: "rejected", ImportErr: "not an upgrade for the existing files (" +
			"Not an upgrade for existing file(s). Existing quality: Bluray-1080p. New Quality WEBDL-1080p.)"},
			"Held for review: these files do not improve on the files you have", "This release is WEBDL-1080p, yours Bluray-1080p. Nothing was replaced."},
		// A coverage refusal is not a worse file: it says why nothing was replaced.
		{"wrapper, coverage", failureFacts{Err: "library: no file passed the sample guard",
			Blocked: "no file passed the sample guard (Episode file on disk contains more episodes than this file contains: /lib/S01E01-E02.mkv)"},
			coverWords, "Nothing was replaced."},
		// Held for review (the download is ready, not failed): the same sentence, said as held.
		{"held, coverage (the saved file refusal)", failureFacts{ImportState: "rejected", ImportErr: "not an upgrade for the existing files",
			Refusals: []string{"Episode file on disk contains more episodes than this file contains: /lib/S01E01-E02.mkv"}},
			"Held for review: the files you have cover more episodes", "Nothing was replaced."},
		{"held, scores (the saved file refusal)", failureFacts{ImportState: "rejected", ImportErr: "not an upgrade for the existing files",
			Refusals: []string{cf}}, "Held for review: these files do not improve on the files you have",
			"This release scores 1,202, yours 6,200. Nothing was replaced."},
		{"failed wrapper, sample (the saved file refusal)", failureFacts{Err: "library: no file passed the sample guard",
			ImportState: "rejected", ImportErr: "no file passed the sample guard", Refusals: []string{"sample: runtime 240s is under 600s"}},
			sampleWords, "The file runs 4 min, under the 10 min minimum."},
		// Every saved refusal counts: one class reads as it (its scores when every file compared the same); a pack's
		// short extras refused as samples do not hide why its episodes were refused; mixed ones say so.
		{"two comparisons, the same scores", failureFacts{Err: "library: no file passed the sample guard",
			Blocked: "no file passed the sample guard (" + cf + ")", Refusals: []string{cf, cf}}, notUpgradeWords,
			"This release scores 1,202, yours 6,200. Nothing was replaced."},
		{"two comparisons, different scores", failureFacts{Err: "library: no file passed the sample guard",
			Blocked: "no file passed the sample guard (" + cf + ")", Refusals: []string{cf, strings.Replace(cf, "(1202)", "(900)", 1)}},
			notUpgradeWords, "Nothing was replaced."},
		{"comparisons and sample extras", failureFacts{Err: "library: no file passed the sample guard",
			Blocked: "no file passed the sample guard (" + cf + ")", Refusals: []string{cf, cf, "sample: runtime 90s is under 600s"}},
			notUpgradeWords, "This release scores 1,202, yours 6,200. Nothing was replaced."},
		{"only samples", failureFacts{Err: "library: no file passed the sample guard", ImportState: "rejected",
			ImportErr: "no file passed the sample guard", Refusals: []string{"sample: runtime 90s is under 600s", "sample: runtime 300s is under 600s"}},
			sampleWords, ""},
		{"comparison and coverage", failureFacts{Err: "library: no file passed the sample guard",
			Blocked: "no file passed the sample guard (" + cf + ")", Refusals: []string{cf,
				"Episode file on disk contains more episodes than this file contains: /lib/S01E01-E02.mkv"}},
			mixedWords, "Different files were refused for different reasons."},
		{"held, mixed", failureFacts{ImportState: "rejected", ImportErr: "not an upgrade for the existing files", Refusals: []string{cf,
			"Episode file on disk contains more episodes than this file contains: /lib/S01E01-E02.mkv"}},
			"Held for review: the library refused its files", "Different files were refused for different reasons."},
		{"held, no match", failureFacts{ImportState: "rejected", ImportErr: "no file matched the download's episodes"},
			"Held for review: none of its files matched the wanted episodes", ""},
		{"held, other", failureFacts{ImportState: "rejected", ImportErr: "naming collision: two titles name S01E01 alike"},
			"Naming collision: two titles name S01E01 alike", ""},
		{"miss, attempts known", failureFacts{Err: "miss on every provider", MissedOn: []string{"rd", "ad", "tb"}},
			"Not cached on RD, AD and TB", ""},
		{"miss, one known", failureFacts{Err: "miss on every provider", MissedOn: []string{"rd"}}, "Not cached on RD", ""},
		{"miss, none known", failureFacts{Err: "miss on every provider"}, noProviderWords, ""},
		{"failed copy", failureFacts{Err: "a failed copy is on the account (ad, rd); an admin can delete it under Torrents"},
			"A failed copy is on AD and RD", "An admin can delete it under Torrents."},
		{"stalled, deleted", failureFacts{Err: "stalled: not ready 10m0s after the add (provider status magnet_conversion); " +
			"our add was deleted on rd", Provider: "rd"}, stalledWords, "Deleted from RD."},
		{"stalled, kept", failureFacts{Err: "stalled: no progress for 2h0m0s while downloading (provider status downloading, " +
			"12.0%); kept on tb: this reused torrent is shared and was not deleted"}, stalledWords, "Kept on TB."},
		{"stalled, old", failureFacts{Err: "stalled: not ready 10m0s after the add (provider status magnet_conversion)"},
			stalledWords, ""},
		{"ready timeout", failureFacts{Err: "ready timeout: not imported after 6h0m0s"},
			"Finished at the provider, but its files could not be imported within 6 hours", ""},
		{"ready timeout, days", failureFacts{Err: "ready timeout: not imported after 48h0m0s"},
			"Finished at the provider, but its files could not be imported within 2 days", ""},
		{"provider status", failureFacts{Err: "provider status magnet_error", Provider: "rd"},
			"RD reported an error", "Its status: magnet_error."},
		{"provider prefix", failureFacts{Err: "provider returned a different torrent for this ID"},
			"Returned a different torrent for this ID", ""},
		{"other", failureFacts{Err: "the torrent is no longer on rd"}, "The torrent is no longer on rd", ""},
		{"library other", failureFacts{Err: "library: [link failed]"}, "[link failed]", ""},
		{"empty", failureFacts{}, "", ""},
	} {
		h, d := failureWords(c.f, strings.ToUpper)
		if h != c.head || d != c.detail {
			t.Errorf("%s: %q / %q, want %q / %q", c.name, h, d, c.head, c.detail)
		}
	}
	if got, _ := failureWords(failureFacts{Err: strings.Repeat("x", 200)}, strings.ToUpper); utf8.RuneCountInString(got) != maxReason+1 {
		t.Errorf("long text not cut: %d", utf8.RuneCountInString(got))
	}
}

// TestGrabWords: every grab source the router records reads as what started it.
func TestGrabWords(t *testing.T) {
	t.Parallel()
	run := func(o runOrigin) *runOrigin { return &o }
	for _, c := range []struct {
		source string
		run    *runOrigin
		want   string
	}{
		{"acquire:4", run(runOrigin{source: "wanted"}), "automatic search"},
		{"acquire:4", run(runOrigin{source: "wanted", reason: "cutoff"}), "automatic upgrade search"},
		{"acquire:4", run(runOrigin{source: "wanted", research: "download_failed"}), "search again after a failure"},
		{"acquire:4", run(runOrigin{source: "wanted", research: "playing"}), "search after a file stopped working while playing"},
		{"acquire:4", run(runOrigin{source: "wanted", research: "asked"}), "Search now"},
		{"acquire:4", run(runOrigin{source: "wanted", research: "added"}), "added by a list"},
		{"acquire:4", run(runOrigin{source: "wanted", research: "new", reason: "missing"}), "retry of a new release"},
		{"acquire:4", run(runOrigin{source: "wanted", research: "fresh"}), "retry of a new release"},
		{"acquire:4", run(runOrigin{source: "command"}), "manual search"},
		{"acquire:4", run(runOrigin{source: "command", command: true, view: "radarr", agent: "Jellyseerr/2.7"}), "Seerr request"},
		{"acquire:4", run(runOrigin{source: "command", command: true, view: "radarr", agent: "LunaSea/10"}), "arr search (radarr)"},
		{"acquire:4", nil, "search"},
		{"rss", nil, "RSS"},
		{"ui:grab:alice", nil, "manual grab by alice"},
		{"ui:grab:key:ops", nil, "manual grab by API key ops"},
		{"ui:retry:12", nil, "manual retry"},
		{"arrview:release:radarr", nil, "manual grab through the arr API (radarr)"},
		{"arrview:push:radarr", nil, "release push (radarr)"},
		{"upgrade:9", nil, "approved upgrade"},
		{"offer", nil, "offer from another library"},
		{"research", nil, "search again after a failure"},
		{"job", nil, "automatic search"},
		{"api:manualimport", nil, "manual import"},
	} {
		if got := grabWords(c.source, c.run); got != c.want {
			t.Errorf("grabWords(%q) = %q, want %q", c.source, got, c.want)
		}
	}
}

// TestReleaseOutcomesAndHistory: a search grabbed a release that failed at the provider, a re-search grabbed the
// next one, which is the file now; a person grabbed a third, and an older RSS import was replaced. The releases
// answer badges each hash and sums up its search; the history says what started each event, the provider, the
// failure's reason, and what each search grabbed.
func TestReleaseOutcomesAndHistory(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	now := store.Millis(testNow)
	const h1, h2, h3, h4 = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222",
		"3333333333333333333333333333333333333333", "4444444444444444444444444444444444444444"
	e.exec(`INSERT INTO arrview_command (id, view, name, status, user_agent, queued_at) VALUES
		(5, 'radarr', 'MoviesSearch', 'completed', 'Jellyseerr/2.7.3', ?)`, now-9000)
	e.exec(`INSERT INTO acquire_run (id, source, command_id, title_id, version_id, version_key, target, state, results,
		accepted, message, research, created_at, updated_at) VALUES
		(29, 'command', NULL, 1, 2, 'movie-web', 'movie', 'nothing', 4, 0, 'no accepted release among 4 results', NULL, ?, ?),
		(30, 'command', 5, 1, 2, 'movie-web', 'movie', 'nothing', 0, 0, 'no accepted release among 0 results', NULL, ?, ?),
		(31, 'wanted', NULL, 1, 2, 'movie-web', 'movie', 'routed', 12, 2, '2 of 12 results accepted', NULL, ?, ?),
		(32, 'wanted', NULL, 1, 2, 'movie-web', 'movie', 'routed', 9, 1, '1 of 9 results accepted', 'download_failed', ?, ?)`,
		now-9000, now-9000, now-8000, now-8000, now-5000, now-5000, now-4000, now-4000)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, created_at) VALUES
		(81, 'acquire:31', 31, 1, 'movie-web', 'movie', 12, 2, ?, ?),
		(82, 'acquire:32', 32, 1, 'movie-web', 'movie', 9, 1, ?, ?)`,
		`[{"rank":1,"release":"Alpha.2020.2160p.WEB-ONE","info_hash":"`+h1+`","accepted":true},
		  {"rank":2,"release":"Alpha.2020.2160p.WEB-TWO","info_hash":"`+h2+`","accepted":true},
		  {"rank":5,"release":"Alpha.2020.2160p.WEB-THREE","info_hash":"`+h3+`","accepted":false}]`, now-5000,
		`[{"rank":1,"release":"Alpha.2020.2160p.WEB-TWO","info_hash":"`+h2+`","accepted":true},
		  {"rank":2,"release":"Alpha.2020.2160p.WEB-FOUR","info_hash":"`+h4+`","accepted":false}]`, now-4000)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, error, source, created_at, updated_at, imported_at) VALUES
		(1, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-FOUR', 'idx', 'rd', 'imported', NULL, 'rss', ?, ?, ?),
		(2, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-ONE', 'idx', 'rd', 'failed', 'provider status error', 'acquire:31', ?, ?, NULL),
		(3, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-TWO', 'idx', 'rd', 'imported', NULL, 'acquire:32', ?, ?, ?),
		(4, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-THREE', 'idx', 'tb', 'downloading', NULL, 'ui:grab:alice', ?, ?, NULL)`,
		h4, now-20000, now-19000, now-19000, h1, now-4900, now-4500,
		h2, now-3900, now-3000, now-3000, h3, now-2000, now-2000)
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, release_title, state, created_at, updated_at)
		VALUES (1, '/lib/Alpha (2020)/Alpha.mkv', '/mnt/__all__/Alpha/Alpha.mkv', 1, 3, 'Alpha.2020.2160p.WEB-TWO',
		'active', ?, ?), (2, '/lib/Alpha (2020)/old/Alpha.mkv', '/mnt/__all__/Old/Alpha.mkv', 1, 1,
		'Alpha.2020.2160p.WEB-FOUR', 'unlinked', ?, ?)`, now-3000, now-3000, now-19000, now-3000)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 2), (2, 2)`)

	var r Releases
	if code := e.getKey(APIBase+"/titles/1/releases?version=movie-web", key, &r); code != 200 {
		t.Fatalf("releases %d", code)
	}
	want := map[string]ReleaseOutcome{
		h1: {Outcome: "failed", Provider: "rd", Reason: "RD reported an error", Download: 2},
		h2: {Outcome: "current", Provider: "rd", Download: 3},
		h3: {Outcome: "grabbed", Provider: "tb", Download: 4, State: "downloading", Words: "Downloading (0%)"},
		h4: {Outcome: "replaced", Provider: "rd", ReplacedBy: "Alpha.2020.2160p.WEB-TWO", Download: 1},
	}
	if len(r.Outcomes) != len(want) {
		t.Errorf("outcomes %+v", r.Outcomes)
	}
	for h, o := range want {
		if !reflect.DeepEqual(r.Outcomes[h], o) {
			t.Errorf("outcome of %s: %+v, want %+v", h[:4], r.Outcomes[h], o)
		}
	}
	if r.Source != "journal" || r.Grabbed != "Grabbed #1 via RD → imported (the current file)" {
		t.Errorf("stored search: %q %q", r.Source, r.Grabbed)
	}

	var d TitleDetail
	if code := e.getKey(APIBase+"/titles/1", key, &d); code != 200 {
		t.Fatalf("title %d", code)
	}
	var hist []HistoryEvent
	for _, v := range d.Versions {
		if v.Key == "movie-web" {
			hist = v.History
		}
	}
	type ev struct{ event, trigger, provider, reason, summary string }
	var got []ev
	latest := map[int64]bool{}
	for _, h := range hist {
		got = append(got, ev{h.Event, h.Trigger, h.Provider, h.Reason, h.Summary})
		if h.Event == "searched" {
			latest[*h.RunID] = h.Latest
			if *h.RunID == 31 && (h.Results == nil || *h.Results != 12 || *h.Accepted != 2 || h.InfoHash != h1) {
				t.Errorf("search 31: %+v", h)
			}
		}
	}
	wantEv := []ev{
		{"grabbed", "manual grab by alice", "tb", "", ""},
		// A second import with no recorded replacement is an import, not an upgrade.
		{"imported", "search again after a failure", "rd", "", ""},
		{"grabbed", "search again after a failure", "rd", "", ""},
		{"searched", "search again after a failure", "rd", "", "Grabbed #1 via RD → imported (the current file)"},
		{"failed", "automatic search", "rd", "RD reported an error", ""},
		{"grabbed", "automatic search", "rd", "", ""},
		{"searched", "automatic search", "rd", "", "Grabbed #1 via RD → failed (RD reported an error)"},
		{"searched", "Seerr request", "", "", "Nothing grabbed: no accepted release among 0 results"},
		{"searched", "manual search", "", "", "Nothing grabbed: no accepted release among 4 results"},
		{"imported", "RSS", "rd", "", ""},
		{"grabbed", "RSS", "rd", "", ""},
	}
	if len(got) != len(wantEv) {
		t.Fatalf("history:\n%+v\nwant\n%+v", got, wantEv)
	}
	for i := range wantEv {
		if got[i] != wantEv[i] {
			t.Errorf("history[%d] = %+v, want %+v", i, got[i], wantEv[i])
		}
	}
	if !latest[32] || latest[31] || latest[30] || latest[29] {
		t.Errorf("latest: %v", latest)
	}
}

// A search's summary says what its own download did, not a later retry of the same release; a release's newest
// failure wins its badge over an older import that was replaced.
func TestSearchSummaryKeepsItsOwnDownload(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	now := store.Millis(testNow)
	const h1, h5 = "1111111111111111111111111111111111111111", "5555555555555555555555555555555555555555"
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
		message, created_at, updated_at) VALUES (41, 'wanted', 1, 2, 'movie-web', 'movie', 'routed', 5, 1,
		'1 of 5 results accepted', ?, ?)`, now-6000, now-6000)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, created_at) VALUES (91, 'acquire:41', 41, 1, 'movie-web', 'movie', 5, 1, ?, ?)`,
		`[{"rank":1,"release":"Alpha.2020.2160p.WEB-ONE","info_hash":"`+h1+`","accepted":true},
		  {"rank":2,"release":"Alpha.2020.2160p.WEB-FIVE","info_hash":"`+h5+`","accepted":false}]`, now-6000)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, error, source, created_at, updated_at, imported_at) VALUES
		(1, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-FIVE', 'idx', 'rd', 'imported', NULL, 'rss', ?, ?, ?),
		(2, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-ONE', 'idx', 'rd', 'failed', 'provider status error', 'acquire:41', ?, ?, NULL),
		(3, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-ONE', 'idx', 'tb', 'imported', NULL, 'ui:retry:2', ?, ?, ?),
		(4, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-FIVE', 'idx', 'rd', 'failed', 'provider timeout: no answer', 'ui:grab:alice', ?, ?, NULL)`,
		h5, now-20000, now-19000, now-19000, h1, now-5900, now-5500, h1, now-3000, now-2500, now-2500, h5, now-1000, now-900)
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, release_title, state, created_at, updated_at)
		VALUES (1, '/lib/Alpha (2020)/Alpha.mkv', '/mnt/__all__/Alpha/Alpha.mkv', 1, 3, 'Alpha.2020.2160p.WEB-ONE',
		'active', ?, ?), (2, '/lib/Alpha (2020)/old/Alpha.mkv', '/mnt/__all__/Old/Alpha.mkv', 1, 1,
		'Alpha.2020.2160p.WEB-FIVE', 'unlinked', ?, ?)`, now-2500, now-2500, now-19000, now-2500)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 2), (2, 2)`)

	const want = "Grabbed #1 via RD → failed (RD reported an error)"
	var r Releases
	if code := e.getKey(APIBase+"/titles/1/releases?version=movie-web", key, &r); code != 200 {
		t.Fatalf("releases %d", code)
	}
	if r.Grabbed != want {
		t.Errorf("stored summary %q, want %q", r.Grabbed, want)
	}
	if o := r.Outcomes[h1]; o.Outcome != "current" || o.Provider != "tb" {
		t.Errorf("retried release: %+v", o)
	}
	if o := r.Outcomes[h5]; o.Outcome != "failed" || o.Reason != "Timeout: no answer" {
		t.Errorf("replaced, then failed again: %+v", o)
	}
	var d TitleDetail
	if code := e.getKey(APIBase+"/titles/1", key, &d); code != 200 {
		t.Fatalf("title %d", code)
	}
	found := false
	for _, v := range d.Versions {
		for _, h := range v.History {
			if h.Event == "searched" && h.RunID != nil && *h.RunID == 41 {
				found = true
				if h.Summary != want {
					t.Errorf("searched summary %q, want %q", h.Summary, want)
				}
			}
		}
	}
	if !found {
		t.Fatal("no searched event for run 41")
	}
}

// Last search describes the download that counted: of a run whose #1 failed and whose #2 imported (the current
// file), the line names #2 and counts the other by its state, in the releases view and the title History alike (with
// #2's hash and provider); a run with a download still in flight names it over the failure.
func TestSearchSummaryDescribesTheImport(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	now := store.Millis(testNow)
	const h1, h2, h3 = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222",
		"3333333333333333333333333333333333333333"
	cands := `[{"rank":1,"release":"Alpha.2020.2160p.WEB-ONE","info_hash":"` + h1 + `","accepted":true},
		{"rank":2,"release":"Alpha.2020.2160p.WEB-TWO","info_hash":"` + h2 + `","accepted":true},
		{"rank":3,"release":"Alpha.2020.2160p.WEB-THREE","info_hash":"` + h3 + `","accepted":true}]`
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
		message, created_at, updated_at) VALUES (41, 'wanted', 1, 2, 'movie-web', 'movie', 'routed', 3, 3, '', ?, ?)`,
		now-6000, now-6000)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, created_at) VALUES (91, 'acquire:41', 41, 1, 'movie-web', 'movie', 3, 3, ?, ?)`, cands, now-6000)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, error, source, created_at, updated_at, imported_at) VALUES
		(1, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-ONE', 'idx', 'rd', 'failed', 'provider status error', 'acquire:41', ?, ?, NULL),
		(2, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-TWO', 'idx', 'ad', 'imported', NULL, 'acquire:41', ?, ?, ?)`,
		h1, now-5900, now-5800, h2, now-5700, now-5000, now-5000)
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, release_title, state, created_at, updated_at)
		VALUES (1, '/lib/Alpha (2020)/Alpha.mkv', '/mnt/__all__/Alpha/Alpha.mkv', 1, 2, 'Alpha.2020.2160p.WEB-TWO',
		'active', ?, ?)`, now-5000, now-5000)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 2)`)

	check := func(run int64, want, hash, provider string) {
		t.Helper()
		var r Releases
		if code := e.getKey(APIBase+"/titles/1/releases?version=movie-web", key, &r); code != 200 {
			t.Fatalf("releases %d", code)
		}
		if r.Grabbed != want {
			t.Errorf("run %d: stored summary %q, want %q", run, r.Grabbed, want)
		}
		var d TitleDetail
		if code := e.getKey(APIBase+"/titles/1", key, &d); code != 200 {
			t.Fatalf("title %d", code)
		}
		found := false
		for _, v := range d.Versions {
			for _, h := range v.History {
				if h.Event == "searched" && h.RunID != nil && *h.RunID == run {
					found = true
					if h.Summary != want || h.InfoHash != hash || h.Provider != provider {
						t.Errorf("run %d: History %q %s %s, want %q %s %s", run, h.Summary, h.InfoHash, h.Provider,
							want, hash, provider)
					}
				}
			}
		}
		if !found {
			t.Fatalf("no searched event for run %d", run)
		}
	}
	check(41, "Grabbed #2 via AD → imported (the current file); 1 other failed", h2, "ad")

	// A later run: #1 failed again, #3 still downloading.
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
		message, created_at, updated_at) VALUES (42, 'wanted', 1, 2, 'movie-web', 'movie', 'routed', 3, 3, '', ?, ?)`,
		now-3000, now-3000)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, created_at) VALUES (92, 'acquire:42', 42, 1, 'movie-web', 'movie', 3, 3, ?, ?)`, cands, now-3000)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, error, source, created_at, updated_at) VALUES
		(3, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-ONE', 'idx', 'rd', 'failed', 'provider status error', 'acquire:42', ?, ?),
		(4, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-THREE', 'idx', 'ad', 'downloading', NULL, 'acquire:42', ?, ?)`,
		h1, now-2900, now-2800, h3, now-2700, now-2600)
	check(42, "Grabbed #3 via AD; 1 other failed", h3, "ad")
}

// A release grabbed twice: the second download supplies the current file, so only its search says "the current
// file"; the first run's line says imported, later replaced (the link's download decides, never the hash).
func TestSearchSummaryCurrentFileByDownload(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	now := store.Millis(testNow)
	const h = "1111111111111111111111111111111111111111"
	cands := `[{"rank":1,"release":"Alpha.2020.2160p.WEB-ONE","info_hash":"` + h + `","accepted":true}]`
	for i, at := range []int64{now - 9000, now - 4000} {
		run := 41 + i
		e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
			message, created_at, updated_at) VALUES (?, 'wanted', 1, 2, 'movie-web', 'movie', 'routed', 1, 1, '', ?, ?)`, run, at, at)
		e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
			candidates, created_at) VALUES (?, ?, ?, 1, 'movie-web', 'movie', 1, 1, ?, ?)`, 91+i, fmt.Sprintf("acquire:%d", run), run, cands, at)
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
			state, source, created_at, updated_at, imported_at) VALUES (?, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-ONE', 'idx', 'rd',
			'imported', ?, ?, ?, ?)`, i+1, h, fmt.Sprintf("acquire:%d", run), at+100, at+500, at+500)
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, release_title, state, created_at, updated_at)
		VALUES (1, '/lib/Alpha (2020)/Alpha.mkv', '/mnt/__all__/Alpha/Alpha.mkv', 1, 2, 'Alpha.2020.2160p.WEB-ONE', 'active', ?, ?)`,
		now-3500, now-3500)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 2)`)
	var d TitleDetail
	if code := e.getKey(APIBase+"/titles/1", key, &d); code != 200 {
		t.Fatalf("title %d", code)
	}
	got := map[int64]string{}
	for _, v := range d.Versions {
		for _, ev := range v.History {
			if ev.Event == "searched" && ev.RunID != nil {
				got[*ev.RunID] = ev.Summary
			}
		}
	}
	if got[42] != "Grabbed #1 via RD → imported (the current file)" || strings.Contains(got[41], "current file") {
		t.Errorf("summaries %v", got)
	}
}

// An episode's releases badge a season pack by the files of that episode, and its stored decision says what its
// search grabbed for the episode.
func TestEpisodeReleasesScopedToEpisode(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	now := store.Millis(addNow)
	const hp, hu = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
		message, created_at, updated_at) VALUES
		(50, 'wanted', 10, 10, 'tv-2160p', 's01', 'routed', 3, 1, '1 of 3 results accepted', ?, ?),
		(51, 'wanted', 10, 10, 'tv-2160p', 's01e02', 'routed', 2, 1, '1 of 2 results accepted', ?, ?)`,
		now-9000, now-9000, now-5000, now-5000)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, created_at) VALUES
		(95, 'acquire:50', 50, 10, 'tv-2160p', 's01', 3, 1, ?, ?),
		(96, 'acquire:51', 51, 10, 'tv-2160p', 's01e02', 2, 1, ?, ?)`,
		`[{"rank":1,"release":"The.Show.S01.2160p.WEB-PACK","info_hash":"`+hp+`","accepted":true,"episodes":["S01E01","S01E02"]}]`, now-9000,
		`[{"rank":1,"release":"The.Show.S01E02.2160p.REMUX-UP","info_hash":"`+hu+`","accepted":true,"episodes":["S01E02"]},
		  {"rank":2,"release":"The.Show.S01.2160p.WEB-PACK","info_hash":"`+hp+`","accepted":false,"episodes":["S01E01","S01E02"]}]`, now-5000)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, source, episodes, created_at, updated_at, imported_at) VALUES
		(10, 10, 10, 'tv-2160p', ?, 'The.Show.S01.2160p.WEB-PACK', 'idx', 'rd', 'imported', 'acquire:50', '[101,102]', ?, ?, ?),
		(11, 10, 10, 'tv-2160p', ?, 'The.Show.S01E02.2160p.REMUX-UP', 'idx', 'tb', 'imported', 'acquire:51', '[102]', ?, ?, ?)`,
		hp, now-8900, now-8000, now-8000, hu, now-4900, now-4000, now-4000)
	e.exec(`DELETE FROM link WHERE id IN (900, 901)`) // the seed's adopted files: this test's links replaced them
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, release_title, state, created_at, updated_at) VALUES
		(10, '/tv/Show/S01E01.mkv', '/mnt/__all__/Pack/E01.mkv', 10, 10, 'The.Show.S01.2160p.WEB-PACK', 'active', ?, ?),
		(11, '/tv/Show/old/S01E02.mkv', '/mnt/__all__/Pack/E02.mkv', 10, 10, 'The.Show.S01.2160p.WEB-PACK', 'unlinked', ?, ?),
		(12, '/tv/Show/S01E02.mkv', '/mnt/__all__/Up/E02.mkv', 10, 11, 'The.Show.S01E02.2160p.REMUX-UP', 'active', ?, ?)`,
		now-8000, now-8000, now-8000, now-4000, now-4000, now-4000)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (10, 10), (11, 10), (12, 10)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (10, 101), (11, 102), (12, 102)`)

	var r Releases
	e.call("GET", APIBase+"/titles/10/episodes/102/releases", nil, 200, &r)
	if o := r.Outcomes[hp]; o.Outcome != "replaced" || o.ReplacedBy != "The.Show.S01E02.2160p.REMUX-UP" {
		t.Errorf("E02 pack: %+v", o)
	}
	if o := r.Outcomes[hu]; o.Outcome != "current" {
		t.Errorf("E02 upgrade: %+v", o)
	}
	if r.Grabbed != "Grabbed #1 via TB → imported (the current file)" {
		t.Errorf("E02 summary %q", r.Grabbed)
	}
	r = Releases{}
	e.call("GET", APIBase+"/titles/10/episodes/101/releases", nil, 200, &r)
	if o := r.Outcomes[hp]; o.Outcome != "current" {
		t.Errorf("E01 pack: %+v", o)
	}
	if _, ok := r.Outcomes[hu]; ok {
		t.Errorf("E01 has E02's upgrade: %+v", r.Outcomes)
	}
	if r.Grabbed != "Grabbed #1 via RD → imported (the current file)" {
		t.Errorf("E01 summary %q", r.Grabbed)
	}
	// Current is the episode's own file: E01 is held by the season pack's link; S02E01 holds nothing.
	if r.Current == nil || !r.Current.Held {
		t.Errorf("E01 current: %+v", r.Current)
	}
	r = Releases{}
	e.call("GET", APIBase+"/titles/10/episodes/103/releases", nil, 200, &r)
	if r.Current == nil || r.Current.Held {
		t.Errorf("S02E01 current: %+v", r.Current)
	}
	// A season's releases have no one current file.
	r = Releases{}
	e.call("GET", APIBase+"/titles/10/releases?version=tv-2160p&season=1", nil, 200, &r)
	if r.Current != nil {
		t.Errorf("season current: %+v", r.Current)
	}
}

// An episode's releases from a season search count only the candidates that cover the episode, even when every
// stored candidate does; an episode search keeps its stored counts.
func TestEpisodeReleasesCountsFromKeptCandidates(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	now := store.Millis(addNow)
	e.exec(`INSERT INTO journal_decision (id, source, title_id, version_key, target, results, accepted, candidates,
		rejections, created_at) VALUES (97, 'acquire:60', 10, 'tv-2160p', 's01', 40, 5, ?, '{"size":20,"quality":15}', ?)`,
		`[{"rank":1,"release":"The.Show.S01.2160p.WEB-PACK","accepted":true,"episodes":["S01E01","S01E02"]},
		  {"rank":2,"release":"The.Show.S01E02.2160p.WEB-GRP","accepted":true,"episodes":["S01E02"]},
		  {"rank":3,"release":"The.Show.S01E01.720p.WEB-GRP","accepted":false,"episodes":["S01E01"],
		   "rejections":[{"reason":"quality","message":"720p"},{"reason":"quality","message":"again"}]}]`, now-9000)
	var r Releases
	e.call("GET", APIBase+"/titles/10/episodes/101/releases", nil, 200, &r)
	if r.Results != 2 || r.Accepted != 1 || len(r.Rejections) != 1 || r.Rejections["quality"] != 1 || len(r.Candidates) != 2 {
		t.Errorf("E01: results %d accepted %d rejections %v candidates %d", r.Results, r.Accepted, r.Rejections, len(r.Candidates))
	}
	r = Releases{}
	e.call("GET", APIBase+"/titles/10/episodes/102/releases", nil, 200, &r)
	if r.Results != 2 || r.Accepted != 2 || len(r.Rejections) != 0 {
		t.Errorf("E02: results %d accepted %d rejections %v", r.Results, r.Accepted, r.Rejections)
	}
	// A season search whose stored candidates all cover E01: the counts are still E01's own.
	e.exec(`INSERT INTO journal_decision (id, source, title_id, version_key, target, results, accepted, candidates,
		rejections, created_at) VALUES (99, 'acquire:62', 10, 'tv-2160p', 's01', 18, 18, ?, '{}', ?)`,
		`[{"rank":1,"release":"The.Show.S01E01.2160p.WEB-A","accepted":true,"episodes":["S01E01"]},
		  {"rank":2,"release":"The.Show.S01E01.2160p.WEB-B","accepted":true,"episodes":["S01E01"]}]`, now-7000)
	r = Releases{}
	e.call("GET", APIBase+"/titles/10/episodes/101/releases", nil, 200, &r)
	if r.Results != 2 || r.Accepted != 2 || len(r.Rejections) != 0 {
		t.Errorf("E01 all kept: results %d accepted %d rejections %v", r.Results, r.Accepted, r.Rejections)
	}
	// Two copies of one torrent in a season search's sample are one row; the count is of copies.
	h := strings.Repeat("e", 40)
	e.exec(`INSERT INTO journal_decision (id, source, title_id, version_key, target, results, accepted, candidates,
		rejections, created_at) VALUES (96, 'acquire:63', 10, 'tv-2160p', 's01', 18, 18, ?, '{}', ?)`,
		`[{"rank":1,"release":"The.Show.S01E01.2160p.WEB-A","indexer":"a","info_hash":"`+h+`","accepted":true,"episodes":["S01E01"]},
		  {"rank":2,"release":"The.Show.S01E01.2160p.WEB-A","indexer":"b","info_hash":"`+h+`","accepted":true,"episodes":["S01E01"]}]`, now-6000)
	r = Releases{}
	e.call("GET", APIBase+"/titles/10/episodes/101/releases", nil, 200, &r)
	if r.Results != 2 || r.Accepted != 2 || len(r.Candidates) != 1 || !slices.Equal(r.Candidates[0].Indexers, []string{"a", "b"}) {
		t.Errorf("E01 merged: results %d accepted %d candidates %+v", r.Results, r.Accepted, r.Candidates)
	}
	// An episode search's candidates all cover the episode: the stored counts stand.
	e.exec(`INSERT INTO journal_decision (id, source, title_id, version_key, target, results, accepted, candidates,
		rejections, created_at) VALUES (98, 'acquire:61', 10, 'tv-2160p', 's01e02', 30, 3, ?, '{"size":27}', ?)`,
		`[{"rank":1,"release":"The.Show.S01E02.2160p.WEB-GRP","accepted":true,"episodes":["S01E02"]}]`, now-5000)
	r = Releases{}
	e.call("GET", APIBase+"/titles/10/episodes/102/releases", nil, 200, &r)
	if r.Results != 30 || r.Accepted != 3 || r.Rejections["size"] != 27 {
		t.Errorf("E02 episode search: results %d accepted %d rejections %v", r.Results, r.Accepted, r.Rejections)
	}
}

// latest marks the search the releases page shows: the newest journal decision.
func TestLatestSearchFollowsShadow(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`DELETE FROM journal_decision WHERE title_id = 1`)
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
		message, created_at, updated_at) VALUES (60, 'command', 1, 1, 'movie-remux', 'movie', 'nothing', 1, 0,
		'no accepted release among 1 results', 1000, 1000)`)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, created_at) VALUES (97, 'acquire:60', 60, 1, 'movie-remux', 'movie', 1, 0, '[]', 1000)`)
	latest := func() map[int64]bool {
		var d TitleDetail
		e.call("GET", APIBase+"/titles/1", nil, 200, &d)
		out := map[int64]bool{}
		for _, v := range d.Versions {
			for _, h := range v.History {
				if h.Event == "searched" && h.RunID != nil {
					out[*h.RunID] = h.Latest
				}
			}
		}
		return out
	}
	var r Releases
	e.call("GET", APIBase+"/titles/1/releases?version=movie-remux", nil, 200, &r)
	if r.Source != "journal" {
		t.Fatalf("releases page shows %q", r.Source)
	}
	if l := latest(); len(l) != 1 || !l[60] {
		t.Errorf("before a newer journal search: %v", l)
	}
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
		message, created_at, updated_at) VALUES (61, 'command', 1, 1, 'movie-remux', 'movie', 'nothing', 1, 0,
		'no accepted release among 1 results', 3000, 3000)`)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, created_at) VALUES (98, 'acquire:61', 61, 1, 'movie-remux', 'movie', 1, 0, '[]', 3000)`)
	if l := latest(); !l[61] || l[60] {
		t.Errorf("after a newer journal search: %v", l)
	}
}

// A pack grabbed for E01 and E02 whose recorded files hold only E01 is not imported for E02, and a season run that opened nothing
// for an episode says so for that episode.
func TestEpisodeOutcomeFromLinks(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	now := store.Millis(addNow)
	const hp, h1 = "cccccccccccccccccccccccccccccccccccccccc", "dddddddddddddddddddddddddddddddddddddddd"
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
		message, created_at, updated_at) VALUES (70, 'wanted', 10, 10, 'tv-2160p', 's01', 'routed', 2, 1,
		'1 of 2 results accepted', ?, ?)`, now-9000, now-9000)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, created_at) VALUES (99, 'acquire:70', 70, 10, 'tv-2160p', 's01', 2, 1, ?, ?)`,
		`[{"rank":1,"release":"The.Show.S01.2160p.WEB-PACK","info_hash":"`+hp+`","accepted":true,"episodes":["S01E01","S01E02"]}]`, now-9000)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, source, episodes, created_at, updated_at, imported_at) VALUES
		(20, 10, 10, 'tv-2160p', ?, 'The.Show.S01.2160p.WEB-PACK', 'idx', 'rd', 'imported', 'acquire:70', '[101,102]', ?, ?, ?)`,
		hp, now-8900, now-8000, now-8000)
	e.exec(`DELETE FROM link WHERE id IN (900)`) // the seed's adopted files: this test's links replaced them
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, release_title, state, created_at, updated_at) VALUES
		(20, '/tv/Show/S01E01.mkv', '/mnt/__all__/Pack/E01.mkv', 10, 20, 'The.Show.S01.2160p.WEB-PACK', 'active', ?, ?)`,
		now-8000, now-8000)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (20, 10)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (20, 101)`)
	// The import's record of the pack's files: one, for E01.
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, how, updated_at) VALUES
		(20, 'The.Show.S01E01.mkv', '[101]', 'import', 'number', ?)`, now-8000)

	var r Releases
	e.call("GET", APIBase+"/titles/10/episodes/102/releases", nil, 200, &r)
	if o, ok := r.Outcomes[hp]; ok {
		t.Errorf("E02 pack badged %+v without a file for E02", o)
	}
	if r.Grabbed != "Grabbed #1 via RD → imported, without a file for this episode" {
		t.Errorf("E02 summary %q", r.Grabbed)
	}
	r = Releases{}
	e.call("GET", APIBase+"/titles/10/episodes/101/releases", nil, 200, &r)
	if r.Outcomes[hp].Outcome != "current" || r.Grabbed != "Grabbed #1 via RD → imported (the current file)" {
		t.Errorf("E01: %+v %q", r.Outcomes[hp], r.Grabbed)
	}

	// A newer season search that routed only E01's single.
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
		message, created_at, updated_at) VALUES (71, 'wanted', 10, 10, 'tv-2160p', 's01', 'routed', 1, 1,
		'1 of 1 results accepted', ?, ?)`, now-5000, now-5000)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, created_at) VALUES (100, 'acquire:71', 71, 10, 'tv-2160p', 's01', 1, 1, ?, ?)`,
		`[{"rank":1,"release":"The.Show.S01E01.2160p.WEB-ONE","info_hash":"`+h1+`","accepted":true,"episodes":["S01E01"]}]`, now-5000)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, source, episodes, created_at, updated_at) VALUES
		(21, 10, 10, 'tv-2160p', ?, 'The.Show.S01E01.2160p.WEB-ONE', 'idx', 'rd', 'downloading', 'acquire:71', '[101]', ?, ?)`,
		h1, now-4900, now-4900)
	r = Releases{}
	e.call("GET", APIBase+"/titles/10/episodes/102/releases", nil, 200, &r)
	if r.Grabbed != "No new grab for this episode from this search" {
		t.Errorf("E02 after a season search that routed E01 only: %q", r.Grabbed)
	}

	// A newer RSS read is not called a search.
	e.exec(`INSERT INTO journal_decision (id, source, title_id, version_key, target, results, accepted, candidates,
		created_at) VALUES (101, 'rss', 10, 'tv-2160p', 'rss', 1, 1, ?, ?)`,
		`[{"rank":1,"release":"The.Show.S01E02.2160p.WEB-RSS","info_hash":"`+h1+`","accepted":true,"episodes":["S01E02"]}]`, now-4000)
	r = Releases{}
	e.call("GET", APIBase+"/titles/10/episodes/102/releases", nil, 200, &r)
	if r.Grabbed != "No new grab for this episode from this RSS match" {
		t.Errorf("E02 after an RSS read: %q", r.Grabbed)
	}
}

// A live search no indexer answered is a failed search: the stored decision answers, with the reason.
func TestLiveReleasesNoAnswer(t *testing.T) {
	t.Parallel()
	e := newAddEnv(t, func(p *policy.Policy) {
	})
	e.exec(`INSERT INTO version (title_id, policy_key, state, created_at, updated_at) VALUES (1, 'movie-web', 'wanted', 0, 0)`)
	e.acq.noAnswer = true
	var r Releases
	e.call("GET", APIBase+"/titles/1/releases?version=movie-web&live=1", nil, 200, &r)
	if r.Source == "live" || !strings.HasPrefix(r.Message, "Live search failed: no indexer answered (dmm: timeout)") {
		t.Errorf("movie: %q %q", r.Source, r.Message)
	}
	// Nothing stored for the library: no promise of a stored decision.
	if r.Source != "none" || strings.Contains(r.Message, "showing the last stored decision") {
		t.Errorf("movie with nothing stored: %q %q", r.Source, r.Message)
	}
	r = Releases{}
	e.call("GET", APIBase+"/titles/10/episodes/101/releases?live=1", nil, 200, &r)
	if r.Source == "live" || !strings.HasPrefix(r.Message, "Live search failed: no indexer answered") {
		t.Errorf("episode: %q %q", r.Source, r.Message)
	}
	if _, i := e.acq.counts(); i != 2 {
		t.Errorf("searches %d", i)
	}
}

// A later release that replaced a pack's episode at the same library path rewrote the pack's link in place. With no
// recorded files the pack's hold on the episode is unknown: no badge, and its search says only that it imported.
// With them it reads replaced.
func TestEpisodeReplacedInPlace(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	now := store.Millis(addNow)
	const hp, hu = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "ffffffffffffffffffffffffffffffffffffffff"
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
		message, created_at, updated_at) VALUES (80, 'wanted', 10, 10, 'tv-2160p', 's01', 'routed', 1, 1,
		'1 of 1 results accepted', ?, ?)`, now-9000, now-9000)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, created_at) VALUES (110, 'acquire:80', 80, 10, 'tv-2160p', 's01', 1, 1, ?, ?)`,
		`[{"rank":1,"release":"The.Show.S01.2160p.WEB-PACK","info_hash":"`+hp+`","accepted":true,"episodes":["S01E01","S01E02"]}]`, now-9000)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, source, episodes, created_at, updated_at, imported_at) VALUES
		(30, 10, 10, 'tv-2160p', ?, 'The.Show.S01.2160p.WEB-PACK', 'idx', 'rd', 'imported', 'acquire:80', '[101,102]', ?, ?, ?),
		(31, 10, 10, 'tv-2160p', ?, 'The.Show.S01E01.2160p.REMUX-UP', 'idx', 'tb', 'imported', 'ui:grab:alice', '[101]', ?, ?, ?)`,
		hp, now-8900, now-8000, now-8000, hu, now-4900, now-4000, now-4000)
	// E01's link, first the pack's, now points at the upgrade (same path).
	e.exec(`DELETE FROM link WHERE id IN (900)`) // the seed's adopted files: this test's links replaced them
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, release_title, state, created_at, updated_at) VALUES
		(30, '/tv/Show/S01E01.mkv', '/mnt/__all__/Up/E01.mkv', 10, 31, 'The.Show.S01E01.2160p.REMUX-UP', 'active', ?, ?)`,
		now-8000, now-4000)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (30, 10)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (30, 101)`)

	var r Releases
	e.call("GET", APIBase+"/titles/10/episodes/101/releases", nil, 200, &r)
	if o, ok := r.Outcomes[hp]; ok {
		t.Errorf("pack on E01 badged %+v with nothing known for E01", o)
	}
	if r.Grabbed != "Grabbed #1 via RD → imported" {
		t.Errorf("E01 summary %q", r.Grabbed)
	}
	// With the import's record of the pack's files.
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, how, updated_at) VALUES
		(30, 'The.Show.S01E01.mkv', '[101]', 'import', 'number', ?), (30, 'The.Show.S01E02.mkv', '[102]', 'import', 'number', ?)`,
		now-8000, now-8000)
	r = Releases{}
	e.call("GET", APIBase+"/titles/10/episodes/101/releases", nil, 200, &r)
	if o := r.Outcomes[hp]; o.Outcome != "replaced" || o.ReplacedBy != "The.Show.S01E01.2160p.REMUX-UP" ||
		r.Grabbed != "Grabbed #1 via RD → imported, later replaced" {
		t.Errorf("with recorded files: %+v %q", r.Outcomes[hp], r.Grabbed)
	}
}

// Another download of the same release speaks only for itself: an E02-only re-import's recorded files say nothing
// about what the pack's first import held for E01.
func TestEpisodeEvidenceOwnDownloadOnly(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	now := store.Millis(addNow)
	const hp, hu = "1212121212121212121212121212121212121212", "3434343434343434343434343434343434343434"
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
		message, created_at, updated_at) VALUES (90, 'wanted', 10, 10, 'tv-2160p', 's01', 'routed', 1, 1,
		'1 of 1 results accepted', ?, ?)`, now-9000, now-9000)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, created_at) VALUES (120, 'acquire:90', 90, 10, 'tv-2160p', 's01', 1, 1, ?, ?)`,
		`[{"rank":1,"release":"The.Show.S01.2160p.WEB-PACK","info_hash":"`+hp+`","accepted":true,"episodes":["S01E01","S01E02"]}]`, now-9000)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, source, episodes, created_at, updated_at, imported_at) VALUES
		(40, 10, 10, 'tv-2160p', ?, 'The.Show.S01.2160p.WEB-PACK', 'idx', 'rd', 'imported', 'acquire:90', '[101,102]', ?, ?, ?),
		(41, 10, 10, 'tv-2160p', ?, 'The.Show.S01E01.2160p.REMUX-UP', 'idx', 'tb', 'imported', 'ui:grab:alice', '[101]', ?, ?, ?),
		(42, 10, 10, 'tv-2160p', ?, 'The.Show.S01.2160p.WEB-PACK', 'idx', 'rd', 'imported', 'api:manualimport', '[102]', ?, ?, ?)`,
		hp, now-8900, now-8000, now-8000, hu, now-4900, now-4000, now-4000, hp, now-3000, now-2500, now-2500)
	e.exec(`DELETE FROM link WHERE id IN (900)`) // the seed's adopted files: this test's links replaced them
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, release_title, state, created_at, updated_at) VALUES
		(40, '/tv/Show/S01E01.mkv', '/mnt/__all__/Up/E01.mkv', 10, 41, 'The.Show.S01E01.2160p.REMUX-UP', 'active', ?, ?)`,
		now-8000, now-4000)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (40, 10)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (40, 101)`)
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, how, updated_at) VALUES
		(42, 'The.Show.S01E02.mkv', '[102]', 'import', 'number', ?)`, now-2500)

	var r Releases
	e.call("GET", APIBase+"/titles/10/episodes/101/releases", nil, 200, &r)
	if r.Grabbed != "Grabbed #1 via RD → imported" {
		t.Errorf("E01 summary %q", r.Grabbed)
	}
	if o, ok := r.Outcomes[hp]; ok {
		t.Errorf("pack on E01: %+v", o)
	}
}

// A manual choice recorded before its import is not a file yet: the pending pack stays grabbed while another
// release holds the episode.
func TestEpisodePendingManualChoice(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	now := store.Millis(addNow)
	const hp, hu = "5656565656565656565656565656565656565656", "7878787878787878787878787878787878787878"
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, source, episodes, created_at, updated_at, imported_at) VALUES
		(50, 10, 10, 'tv-2160p', ?, 'The.Show.S01E01.2160p.REMUX-UP', 'idx', 'tb', 'imported', 'ui:grab:alice', '[101]', ?, ?, ?),
		(51, 10, 10, 'tv-2160p', ?, 'The.Show.S01.2160p.WEB-PACK', 'idx', 'rd', 'ready', 'api:manualimport', '[101,102]', ?, ?, NULL)`,
		hu, now-8000, now-7000, now-7000, hp, now-3000, now-2000)
	e.exec(`DELETE FROM link WHERE id IN (900)`) // the seed's adopted files: this test's links replaced them
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, release_title, state, created_at, updated_at) VALUES
		(50, '/tv/Show/S01E01.mkv', '/mnt/__all__/Up/E01.mkv', 10, 50, 'The.Show.S01E01.2160p.REMUX-UP', 'active', ?, ?)`,
		now-7000, now-7000)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (50, 10)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (50, 101)`)
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, how, updated_at) VALUES
		(51, 'The.Show.S01E01.mkv', '[101]', 'manual', 'manual', ?), (51, 'The.Show.S01E02.mkv', '[102]', 'manual', 'manual', ?)`,
		now-2500, now-2500)

	var r Releases
	e.call("GET", APIBase+"/titles/10/episodes/101/releases", nil, 200, &r)
	if o := r.Outcomes[hp]; o.Outcome != "grabbed" {
		t.Errorf("pending manual pack on E01: %+v", o)
	}
	if o := r.Outcomes[hu]; o.Outcome != "current" {
		t.Errorf("E01's file: %+v", o)
	}
}

// TestTitlePageMissReason: the title page's failed event already says which providers did not have the release.
func TestTitlePageMissReason(t *testing.T) {
	t.Parallel()
	evs := downloadEvents(nil, HistoryEvent{Source: "rd", MissedOn: []string{"rd", "tb"}}, "failed", 0, 0, "wanted",
		"miss on every provider", false, nil, strings.ToUpper)
	if last := evs[len(evs)-1]; last.Event != "failed" || last.Reason != "Not cached on RD and TB" {
		t.Errorf("title page failure %+v", last)
	}
}

// TestFailureNamesProviderByTag: a failure names a provider account by its tag (Web.providerTag: rd_main, a
// Real-Debrid account, reads RD), in Last search as in Activity, not by its key in upper case.
func TestFailureNamesProviderByTag(t *testing.T) {
	t.Parallel()
	tag := func(k string) string {
		if k == "rd_main" {
			return "RD"
		}
		return strings.ToUpper(k)
	}
	dls := []runDownload{{id: 1, hash: "aa", release: "Alpha.2020.1080p.WEB-GRP", provider: "rd_main", state: "failed",
		err: "a failed copy is on the account (rd_main); an admin can delete it under Torrents"}}
	if got := searchSummary(dls, 0, nil, nil, nil, "done", "", tag); !strings.HasSuffix(got,
		" via RD → failed (A failed copy is on RD)") {
		t.Errorf("summary %q", got)
	}
}

// TestSearchSummaryQueued: a release that only waits behind provider pacing (its download row still queued, no
// provider has taken it) reads "Queued #2" in Last search, not "Grabbed #2".
func TestSearchSummaryQueued(t *testing.T) {
	t.Parallel()
	dls := []runDownload{{id: 1, hash: "bb", release: "Alpha.2020.1080p.WEB-GRP", state: "queued"}}
	cands := []snapshot.Candidate{{InfoHash: "aa", Rank: 1}, {InfoHash: "bb", Rank: 2}}
	if got := searchSummary(dls, 0, nil, cands, nil, "done", "", strings.ToUpper); got != "Queued #2" {
		t.Errorf("summary %q", got)
	}
	dls[0].state, dls[0].provider = "adding", "rd_main"
	if got := searchSummary(dls, 0, nil, cands, nil, "done", "", strings.ToUpper); got != "Grabbed #2 via RD_MAIN" {
		t.Errorf("summary %q", got)
	}
}

func TestStoredReleasesDropPublicVolumeFlags(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.exec(`INSERT INTO journal_decision (id, source, title_id, version_key, target, results, accepted, candidates,
		created_at) VALUES (81, 'acquire:31', 1, 'movie-web', 'movie', 2, 0, ?, ?)`,
		`[{"rank":1,"release":"Alpha.2020.2160p.WEB-PUB","info_hash":"1111111111111111111111111111111111111111",
		   "privacy":"public","flags":["freeleech","seadex"]},
		  {"rank":2,"release":"Alpha.2020.2160p.WEB-PRIV","info_hash":"2222222222222222222222222222222222222222",
		   "privacy":"private","flags":["freeleech","seadex"]}]`, store.Millis(testNow))
	var r Releases
	if code := e.getKey(APIBase+"/titles/1/releases?version=movie-web", e.apiKey(), &r); code != 200 {
		t.Fatalf("releases %d", code)
	}
	if len(r.Candidates) != 2 || !slices.Equal(r.Candidates[0].Flags, []string{"seadex"}) ||
		!slices.Equal(r.Candidates[1].Flags, []string{"freeleech", "seadex"}) {
		t.Errorf("flags: %+v", r.Candidates)
	}
}

func TestEpisodeReleasesDropPublicVolumeFlags(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`INSERT INTO journal_decision (id, source, title_id, version_key, target, results, accepted, candidates,
		rejections, created_at) VALUES (97, 'acquire:60', 10, 'tv-2160p', 's01e01', 2, 2, ?, '{}', ?)`,
		`[{"rank":1,"release":"The.Show.S01E01.2160p.WEB-PUB","accepted":true,"episodes":["S01E01"],
		   "privacy":"public","flags":["freeleech","seadex"]},
		  {"rank":2,"release":"The.Show.S01E01.2160p.WEB-PRIV","accepted":true,"episodes":["S01E01"],
		   "privacy":"private","flags":["freeleech","seadex"]}]`, store.Millis(addNow)-9000)
	var r Releases
	e.call("GET", APIBase+"/titles/10/episodes/101/releases", nil, 200, &r)
	if len(r.Candidates) != 2 || !slices.Equal(r.Candidates[0].Flags, []string{"seadex"}) ||
		!slices.Equal(r.Candidates[1].Flags, []string{"freeleech", "seadex"}) {
		t.Errorf("flags: %+v", r.Candidates)
	}
}

// GET /titles/{id}/release-outcomes reads what came of each release now: an in-flight download with the words
// Activity shows (over an older import of the same hash), an import held for review without the working spinner, and
// a failure with whether a grab of it now would be routed.
func TestReleaseOutcomesLive(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	now := store.Millis(addNow)
	const (
		hDl, hHeld, hMiss    = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1", "a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2", "a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3"
		hOld, hBlock, hStall = "a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4a4", "a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5", "a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6a6"
		hAgain               = "a7a7a7a7a7a7a7a7a7a7a7a7a7a7a7a7a7a7a7a7"
	)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, error, progress, source, episodes, created_at, updated_at) VALUES
		(30, 1, 1, 'movie-remux', ?, 'Alpha.DL', 'idx', 'ad', 'downloading', NULL, 43.4, 'ui', '[]', ?, ?),
		(31, 1, 1, 'movie-remux', ?, 'Alpha.HELD', 'idx', 'rd', 'ready', NULL, 100, 'ui', '[]', ?, ?),
		(32, 1, 1, 'movie-remux', ?, 'Alpha.MISS', 'idx', NULL, 'failed', 'miss on every provider', 0, 'ui', '[]', ?, ?),
		(33, 1, 1, 'movie-remux', ?, 'Alpha.OLDMISS', 'idx', NULL, 'failed', 'miss on every provider', 0, 'ui', '[]', ?, ?),
		(34, 1, 1, 'movie-remux', ?, 'Alpha.BLOCK', 'idx', 'rd', 'failed', 'miss on every provider', 0, 'ui', '[]', ?, ?),
		(35, 1, 1, 'movie-remux', ?, 'Alpha.STALL', 'idx', 'rd', 'failed', 'stalled: nothing fetched', 0, 'ui', '[]', ?, ?),
		(36, 1, 1, 'movie-remux', ?, 'Alpha.AGAIN', 'idx', 'rd', 'imported', NULL, 100, 'ui', '[]', ?, ?),
		(37, 1, 1, 'movie-remux', ?, 'Alpha.AGAIN', 'idx', NULL, 'queued', NULL, 0, 'ui', '[]', ?, ?)`,
		hDl, now-60000, now-1000, hHeld, now-60000, now-1000, hMiss, now-60000, now-60000,
		hOld, now-9*3600000, now-7*3600000, hBlock, now-60000, now-60000, hStall, now-60000, now-60000,
		hAgain, now-90000, now-80000, hAgain, now-2000, now-2000)
	e.exec(`INSERT INTO library_import (download_id, state, last_error, created_at, updated_at) VALUES (31, 'rejected', 'sample', ?, ?)`, now, now)
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, created_at)
		VALUES ('movie-remux', ?, 1, 'Alpha.BLOCK', 'stalled', 'router', ?)`, hBlock, now)
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, release_title, state, created_at, updated_at) VALUES
		(40, '/movies/Alpha/Alpha.mkv', '/mnt/__all__/Alpha/Alpha.mkv', 1, 36, 'Alpha.AGAIN', 'active', ?, ?)`, now, now)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (40, 1)`)

	var r ReleaseOutcomes
	e.call("GET", APIBase+"/titles/1/release-outcomes?version=movie-remux", nil, 200, &r)
	o := r.Outcomes
	if g := o[hDl]; g.Outcome != "grabbed" || g.State != "downloading" || g.Words != "Downloading (43%)" || g.Idle {
		t.Errorf("downloading: %+v", g)
	}
	if g := o[hHeld]; g.Outcome != "grabbed" || g.Words != "Held for review" || !g.Idle {
		t.Errorf("held import: %+v", g)
	}
	if g := o[hAgain]; g.Outcome != "grabbed" || g.State != "queued" || g.Words != "Queued" {
		t.Errorf("a newer download in flight over the current file: %+v", g)
	}
	// What each provider answered for hMiss: its last attempt per provider, in the order they were first tried; only
	// explicit uncached answers make the headline (a plain miss may be a 451 or a refused file name).
	provs := e.pol.Versions["movie-remux"].Providers
	if len(provs) < 2 {
		t.Fatal("movie-remux has fewer than two providers")
	}
	e.exec(`INSERT INTO download_attempt (download_id, provider, outcome, code, message, attempted_at) VALUES
		(32, ?, 'skipped', 'miss', 'missed on every provider', ?), (32, ?, 'refused', 'uncached_deleted', 'uncached', ?),
		(32, ?, 'skipped', 'title_451', 'refused', ?), (32, '', 'skipped', 'cached_first', 'x', ?),
		(33, ?, 'skipped', 'miss', 'x', ?)`,
		provs[0], now-60000, provs[1], now-59000, provs[0], now-58000, now-57000, provs[0], now-7*3600000)
	r = ReleaseOutcomes{}
	e.call("GET", APIBase+"/titles/1/release-outcomes?version=movie-remux", nil, 200, &r)
	o = r.Outcomes
	tag := func(pk string) string { return strings.ToUpper(e.web.cacheCodes([]string{pk})[0]) }
	want := []ReleaseAttempt{{Provider: tag(provs[0]), Outcome: "skipped", Code: "title_451", Message: "refused"},
		{Provider: tag(provs[1]), Outcome: "refused", Code: "uncached_deleted", Message: "uncached"}}
	if g := o[hMiss]; g.Outcome != "failed" || g.Blocklisted || !slices.Equal(g.Attempts, want) ||
		g.Reason != "Not cached on "+tag(provs[1]) {
		t.Errorf("failed with answers: %+v", g)
	}
	if g := o[hOld]; g.Outcome != "failed" || g.Blocklisted || g.Reason != "No provider accepted this release" ||
		len(g.Attempts) != 1 {
		t.Errorf("a plain miss: %+v", g)
	}
	if g := o[hBlock]; g.Outcome != "failed" || !g.Blocklisted {
		t.Errorf("blocklisted: %+v", g)
	}
	if g := o[hStall]; g.Outcome != "failed" || g.Blocklisted || g.Reason == "" || g.Attempts != nil {
		t.Errorf("stalled: %+v", g)
	}
	if g := o[hBlock]; g.BlockedReason != e.web.blockedWords("router", "stalled") || g.BlockedReason == "" {
		t.Errorf("blocked reason: %+v", g)
	}
	// The stalled download dismissed in Activity with blocklist: the block's own reason beside blocklisted, in the 409's
	// words, while the Failed row keeps the download's failure.
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, created_at)
		VALUES ('movie-remux', ?, 1, 'Alpha.STALL', 'removed in Activity by admin', ?, ?)`, hStall, BlocklistUI, now)
	r = ReleaseOutcomes{}
	e.call("GET", APIBase+"/titles/1/release-outcomes?version=movie-remux", nil, 200, &r)
	if g := r.Outcomes[hStall]; !g.Blocklisted || g.BlockedReason != e.web.blockedWords(BlocklistUI, "removed in Activity by admin") ||
		g.BlockedReason == g.Reason || g.Reason != o[hStall].Reason {
		t.Errorf("dismissed with blocklist: %+v (failure %q)", g, o[hStall].Reason)
	}

	// The release lists give the same outcomes.
	var rl Releases
	e.call("GET", APIBase+"/titles/1/releases?version=movie-remux", nil, 200, &rl)
	if rl.Outcomes[hDl].Words != "Downloading (43%)" || rl.Outcomes[hAgain].Outcome != "grabbed" {
		t.Errorf("release list outcomes: %+v", rl.Outcomes)
	}
	// The route's checks: a version the title lacks, an episode of another title.
	e.call("GET", APIBase+"/titles/1/release-outcomes?version=tv-2160p", nil, 400, nil)
	e.call("GET", APIBase+"/titles/10/release-outcomes?version=tv-2160p&episode=999999", nil, 404, nil)
	e.call("GET", APIBase+"/titles/10/release-outcomes?version=tv-2160p&episode=101", nil, 200, &r)
}

// A season search that grabbed nothing says, in an episode's window, that its result count is the season search's
// (its notes are the panel's own); the season's panel keeps the run's message.
func TestEpisodeNothingGrabbedNamesSeasonSearch(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	now := store.Millis(addNow)
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
		message, created_at, updated_at) VALUES (71, 'wanted', 10, 10, 'tv-2160p', 's01', 'nothing', 508, 0,
		'no accepted release among 508 results (indexer nyaa: timeout)', ?, ?)`, now-1000, now-1000)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, created_at) VALUES (100, 'acquire:71', 71, 10, 'tv-2160p', 's01', 508, 0, ?, ?)`,
		`[{"rank":0,"release":"The.Show.S01E02.1080p.WEB-GRP","accepted":false,"episodes":["S01E02"],"rejections":[{"reason":"size"}]}]`, now-1000)
	var r Releases
	e.call("GET", APIBase+"/titles/10/episodes/102/releases", nil, 200, &r)
	if r.Results != 1 || r.Grabbed != "Nothing grabbed: no accepted release among 508 results of the season search" {
		t.Errorf("E02: results %d, %q", r.Results, r.Grabbed)
	}
	r = Releases{}
	e.call("GET", APIBase+"/titles/10/releases?version=tv-2160p", nil, 200, &r)
	// The headline is for people (F6): built from the run's count when read, the notes left to the panel's own
	// lines (Search incomplete, Technical details). This pinned the notes in the line before.
	if r.Grabbed != "Nothing grabbed: no accepted release among 508 results" {
		t.Errorf("season: %q", r.Grabbed)
	}
	// A partial run (an indexer timed out) keeps its early-retry hint in the episode's line.
	e.exec(`UPDATE acquire_run SET infra = 2, message = message || '; partial answer: due again sooner' WHERE id = 71`)
	r = Releases{}
	e.call("GET", APIBase+"/titles/10/episodes/102/releases", nil, 200, &r)
	if r.Grabbed != "Nothing grabbed: no accepted release among 508 results of the season search; partial answer: due again sooner" {
		t.Errorf("E02 partial: %q", r.Grabbed)
	}
	// An exact episode's own search with both indexers' notes stored in its message (an old row too): the line is the
	// count and the early-retry hint; the notes stay the panel's (Notes). Title History says the same line.
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted, infra,
		message, created_at, updated_at) VALUES (72, 'wanted', 10, 10, 'tv-2160p', 's01e01', 'nothing', 351, 0, 2,
		'no accepted release among 351 results (title search added 0; dropped 0 entries for another IMDb id); partial answer: due again sooner', ?, ?)`,
		now-500, now-500)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, notes, created_at) VALUES (101, 'acquire:72', 72, 10, 'tv-2160p', 's01e01', 351, 0, '[]',
		'title search added 0; dropped 0 entries for another IMDb id', ?)`, now-500)
	const clean = "Nothing grabbed: no accepted release among 351 results; partial answer: due again sooner"
	r = Releases{}
	e.call("GET", APIBase+"/titles/10/episodes/101/releases", nil, 200, &r)
	if r.Grabbed != clean || r.Notes != "title search added 0; dropped 0 entries for another IMDb id" {
		t.Errorf("E01 own search: %q, notes %q", r.Grabbed, r.Notes)
	}
	var d TitleDetail
	e.call("GET", APIBase+"/titles/10", nil, 200, &d)
	found := false
	for _, v := range d.Versions {
		for _, h := range v.History {
			if h.Event == "searched" && h.RunID != nil && *h.RunID == 72 {
				found = true
				if h.Summary != clean {
					t.Errorf("history line: %q", h.Summary)
				}
			}
		}
	}
	if !found {
		t.Error("run 72 is not in the title's history")
	}
}

// searchScope: a season target is "s" and digits only, as acquire writes it (s01, s100, s2024).
func TestSearchScope(t *testing.T) {
	t.Parallel()
	for target, want := range map[string]string{"s01": "season", "S100": "season", "s2024": "season", "": "series",
		"series": "series", "s01e02": "", "S01E02": ""} {
		if got := searchScope(target, "S01E02"); got != want {
			t.Errorf("%q: %q, want %q", target, got, want)
		}
	}
}

// A merged search's member reads the downloads of the root's route that hold one of its episodes: season 2's
// History shows the season-2 pack, season 1's (the root) does not.
func TestMergedSearchHistoryByEpisodes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	now := store.Millis(testNow)
	const h1 = "5555555555555555555555555555555555555555"
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (30, 3, 'tv-2160p', 'wanted', ?, ?)`, now, now)
	e.exec(`INSERT INTO episode (id, title_id, season, number, created_at, updated_at) VALUES (301, 3, 1, 1, 0, 0), (302, 3, 2, 1, 0, 0)`)
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, season, episodes, state, results,
		accepted, message, search_run, created_at, updated_at) VALUES
		(41, 'wanted', 3, 30, 'tv-2160p', 's01', 1, '[301]', 'routed', 2, 1, '1 of 2 results accepted', NULL, ?, ?),
		(42, 'wanted', 3, 30, 'tv-2160p', 's02', 2, '[302]', 'routed', 2, 1, '1 of 2 results accepted', 41, ?, ?)`,
		now-5000, now-5000, now-5000, now-5000)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		episodes, state, source, created_at, updated_at) VALUES
		(7, 3, 30, 'tv-2160p', ?, 'Gamma.S02.2160p.WEB-GRP', 'idx', 'rd', '[302]', 'downloading', 'acquire:41', ?, ?)`,
		h1, now-4000, now-4000)
	var d TitleDetail
	if code := e.getKey(APIBase+"/titles/3", key, &d); code != 200 {
		t.Fatalf("title %d", code)
	}
	sums := map[int64]string{}
	for _, v := range d.Versions {
		for _, h := range v.History {
			if h.Event == "searched" {
				sums[*h.RunID] = h.Summary
			}
		}
	}
	if !strings.Contains(sums[42], "Grabbed") || strings.Contains(sums[41], "Grabbed") {
		t.Fatalf("summaries %v", sums)
	}
}

// The stored search of a file that stopped working while playing says so in the Last search's words.
func TestStoredPlayingSearchWords(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := store.Millis(testNow)
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
		message, research, created_at, updated_at) VALUES (51, 'wanted', 1, 2, 'movie-web', 'movie', 'nothing', 3, 0,
		'', 'playing', ?, ?)`, now-5000, now-5000)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, created_at) VALUES (95, 'acquire:51', 51, 1, 'movie-web', 'movie', 3, 0, '[]', ?)`, now-5000)
	var r Releases
	if code := e.getKey(APIBase+"/titles/1/releases?version=movie-web", e.apiKey(), &r); code != 200 {
		t.Fatalf("releases %d", code)
	}
	if r.Message != "From a search after a file stopped working while playing" {
		t.Errorf("message %q", r.Message)
	}
}

// A season search started because one episode's file stopped working while playing says so for the search, not for the
// episode shown: another episode's window names the cause apart from what the search covered.
func TestStoredPlayingSearchWordsInAnotherEpisode(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	now := store.Millis(addNow)
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
		message, research, created_at, updated_at) VALUES (60, 'wanted', 10, 10, 'tv-2160p', 's01', 'nothing', 2, 0,
		'', 'playing', ?, ?)`, now-5000, now-5000)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, created_at) VALUES (97, 'acquire:60', 60, 10, 'tv-2160p', 's01', 2, 0, '[]', ?)`, now-5000)
	var r Releases
	e.call("GET", APIBase+"/titles/10/episodes/102/releases", nil, 200, &r)
	if r.Message != "From a search for S01 after a file stopped working while playing, which covered S01E02" {
		t.Errorf("message %q", r.Message)
	}
}

// TestChosenLine: Last search says why the release its run took for this library beat the next accepted one (the
// reason recorded with the search), with no claim about better-ranked ones.
func TestChosenLine(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	now := store.Millis(testNow)
	const h0, h1, h2, h3 = "0000000000000000000000000000000000000000", "1111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222", "3333333333333333333333333333333333333333"
	cands := `[{"rank":1,"release":"Alpha.2020.2160p.REMUX-ZERO","info_hash":"` + h0 + `","accepted":true},
		{"rank":2,"release":"Alpha.2020.2160p.REMUX-ONE","info_hash":"` + h1 + `","accepted":true},
		{"rank":3,"release":"Alpha.2020.2160p.REMUX-TWO","info_hash":"` + h2 + `","accepted":true,
		  "over":{"release":"Alpha.2020.2160p.WEB-THREE","reason":"higher quality (Remux-2160p over WEBDL-2160p)"}},
		{"rank":4,"release":"Alpha.2020.2160p.WEB-THREE","info_hash":"` + h3 + `","accepted":true}]`
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
		message, created_at, updated_at) VALUES (41, 'wanted', 1, 2, 'movie-web', 'movie', 'routed', 4, 4, '', ?, ?)`,
		now-6000, now-6000)
	e.exec(`INSERT INTO journal_decision (id, source, run_id, title_id, version_key, target, results, accepted,
		candidates, created_at) VALUES (91, 'acquire:41', 41, 1, 'movie-web', 'movie', 4, 4, ?, ?)`, cands, now-6000)
	// #1 has no download record (not counted); #2 missed every provider before an add; #3 was taken.
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, error, source, created_at, updated_at, added_at) VALUES
		(1, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.REMUX-ONE', 'idx', NULL, 'failed', 'miss on every provider: not cached', 'acquire:41', ?, ?, NULL),
		(2, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.REMUX-TWO', 'idx', 'ad', 'downloading', NULL, 'acquire:41', ?, ?, ?)`,
		h1, now-5800, now-5800, h2, now-5700, now-5000, now-5600)
	var r Releases
	if code := e.getKey(APIBase+"/titles/1/releases?version=movie-web", key, &r); code != 200 {
		t.Fatalf("releases %d", code)
	}
	if want := "Chosen over Alpha.2020.2160p.WEB-THREE: higher quality (Remux-2160p over WEBDL-2160p)"; r.Chosen != want {
		t.Errorf("chosen %q, want %q", r.Chosen, want)
	}
}
