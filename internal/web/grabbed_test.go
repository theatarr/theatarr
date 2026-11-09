package web

import (
	"testing"

	"github.com/theatarr/theatarr/internal/store"
)

func TestShortReason(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ err, provider, want string }{
		{"provider status error", "rd", "RD status error"},
		{"provider status magnet_error", "", "provider status magnet_error"},
		{"stalled: not ready 6h0m0s after the add (provider status downloading)", "tb", "stalled"},
		{"miss on every provider", "rd", "miss on every provider"},
		{"no video file in the torrent; the finished torrent was kept on rd", "rd", "no video file in the torrent"},
		{"", "rd", ""},
	} {
		if got := shortReason(c.err, c.provider); got != c.want {
			t.Errorf("shortReason(%q, %q) = %q, want %q", c.err, c.provider, got, c.want)
		}
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
		h1: {Outcome: "failed", Provider: "rd", Reason: "RD status error"},
		h2: {Outcome: "current", Provider: "rd"},
		h3: {Outcome: "grabbed", Provider: "tb"},
		h4: {Outcome: "replaced", Provider: "rd", ReplacedBy: "Alpha.2020.2160p.WEB-TWO"},
	}
	if len(r.Outcomes) != len(want) {
		t.Errorf("outcomes %+v", r.Outcomes)
	}
	for h, o := range want {
		if r.Outcomes[h] != o {
			t.Errorf("outcome of %s: %+v, want %+v", h[:4], r.Outcomes[h], o)
		}
	}
	if r.Source != "journal" || r.Grabbed != "Grabbed #1 via rd → imported (the current file)" {
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
		if v.Key == "movie-remux" {
			for _, h := range v.History {
				if h.Event == "grabbed" && h.Trigger != "old manager (radarr_4k_debrid)" {
					t.Errorf("arr event trigger %q", h.Trigger)
				}
			}
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
		{"upgraded", "search again after a failure", "rd", "", ""},
		{"grabbed", "search again after a failure", "rd", "", ""},
		{"searched", "search again after a failure", "rd", "", "Grabbed #1 via rd → imported (the current file)"},
		{"failed", "automatic search", "rd", "RD status error", ""},
		{"grabbed", "automatic search", "rd", "", ""},
		{"searched", "automatic search", "rd", "", "Grabbed #1 via rd → failed (RD status error)"},
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
