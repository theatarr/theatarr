package shadow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

// TestParserModeClassify: a disagreement that the grab's v4 decisions turn into agreement is agree / parser_mode;
// one that stays a disagreement under v4 keeps its row; hand grabs are left alone.
func TestParserModeClassify(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	arr := Decided{Release: "The.Show.S01E02.2160p.WEB-DL.DE-EN-TR-TURG", InfoHash: "aa", Accepted: true,
		Quality: "WEBDL-2160p", Score: 0, Arr: true}
	rej := arr
	rej.Accepted, rej.Rejections = false, []decide.Rejection{{Reason: "CustomFormatMinimumScore", Message: "below"}}
	higher := Decided{Release: "The.Show.S01E02.2160p.WEB-DL.JPN-X", InfoHash: "bb", Accepted: true,
		Quality: "WEBDL-2160p", Score: 500}
	arrPick := arr
	check := func(d Decided, pick *Decided) *ParserCheck {
		return &ParserCheck{Mode: policy.SonarrV4Plus, Decided: &d, Pick: pick}
	}
	with := func(d Decided, pc *ParserCheck) Decided { d.Parser = pc; return d }

	for _, tc := range []struct {
		name           string
		in             GrabInput
		bucket, reason string
		detail         string
	}{
		{"we grab another; with v4 the arr's", GrabInput{Source: "Search", Arr: with(arr, check(arr, &arrPick)),
			Pick: &higher, Searched: true, FoundArr: true}, BucketAgree, ReasonParserMode, "parser: v4plus; we_grab_another/higher_score"},
		{"we reject it; with v4 we accept it", GrabInput{Source: "Search", Arr: with(rej, check(arr, &arrPick)),
			Searched: true, FoundArr: true}, BucketAgree, ReasonParserMode, "parser: v4plus; arr_grabbed_we_wouldnt/below_min_score"},
		{"still another with v4", GrabInput{Source: "Search", Arr: with(arr, check(arr, &higher)), Pick: &higher,
			Searched: true, FoundArr: true}, BucketAnother, ReasonHigherScore, "so not the parser mode (v4plus)"},
		{"agree stays agree", GrabInput{Source: "Search", Arr: with(arr, check(rej, nil)), Pick: &arrPick,
			Searched: true, FoundArr: true}, BucketAgree, ReasonSameRelease, "we pick the same release"},
		{"hand grab left alone", GrabInput{Source: SourceInteractive, Arr: with(rej, check(arr, nil))},
			BucketWouldnt, ReasonManualRejected, "by hand"},
		{"RSS: our pick is rejected with v4", GrabInput{Source: SourceRSS, Arr: with(arr, check(arr, nil)),
			RSS: []RSSCandidate{{ID: 1, Indexer: "prowlarr", SeenAt: at, Decided: with(higher, check(rej, nil))}}},
			BucketAgree, ReasonParserMode, "parser: v4plus; we_grab_another/higher_score"},
		{"RSS: our pick is still above with v4", GrabInput{Source: SourceRSS, Arr: with(arr, check(arr, nil)),
			RSS: []RSSCandidate{{ID: 1, Indexer: "prowlarr", SeenAt: at, Decided: with(higher, check(higher, nil))}}},
			BucketAnother, ReasonHigherScore, "not the parser mode"},
		{"no check", GrabInput{Source: "Search", Arr: arr, Pick: &higher, Searched: true, FoundArr: true},
			BucketAnother, ReasonHigherScore, "we would grab"},
	} {
		tc.in.GrabbedAt = at
		c := ClassifyGrab(tc.in)
		if c.Bucket != tc.bucket || c.Reason != tc.reason || !strings.Contains(c.Detail, tc.detail) {
			t.Errorf("%s: %s/%s %q, want %s/%s containing %q", tc.name, c.Bucket, c.Reason, c.Detail, tc.bucket,
				tc.reason, tc.detail)
		}
	}
}

func TestArrParserVersion(t *testing.T) {
	t.Parallel()
	for mode, want := range map[string]bool{"": false, "v4": false, "v4plus": true, "v5": true} {
		v := decide.Version{Key: "tv", Policy: &policy.VersionPolicy{Kind: policy.KindSeries, Sonarr: mode}}
		v4, got, ok := arrParserVersion(v)
		if ok != want || ok && (got != mode || v4.Policy.Sonarr != "v4" || v.Policy.Sonarr != mode) {
			t.Errorf("%q: %v %q %+v", mode, ok, got, v4.Policy)
		}
	}
	if _, _, ok := arrParserVersion(decide.Version{Policy: &policy.VersionPolicy{Kind: policy.KindMovie}}); ok {
		t.Error("a movie version has no parser mode")
	}
}

// TestRunnerSeriesParserMode: on a v4plus series version the grab's stored arr decision and an RSS pick carry the
// v4 check, and a grab both parsers agree on stays same_release.
func TestRunnerSeriesParserMode(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.pol.Versions["tv"].Sonarr = policy.SonarrV4Plus
	newShow(e.tv)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.prow.shows["1001:1"] = []item{{title: showPack, hash: hShowPack, size: 16e9, pub: t0.Add(-48 * time.Hour), tv: true}}
	at := t0.Add(-30 * time.Minute)
	e.tv.add(showGrab(2, 52, at, showPack, hShowPack, "Search"), showGrab(3, 53, at.Add(time.Second), showPack, hShowPack, "Search"))
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	var arr Decided
	if err := json.Unmarshal([]byte(one[string](t, e.st, `SELECT arr_decision FROM shadow_grab WHERE instance = 'sonarr_tv'`)), &arr); err != nil {
		t.Fatal(err)
	}
	if arr.Parser == nil || arr.Parser.Mode != "v4plus" || arr.Parser.Decided == nil || !arr.Parser.Decided.Accepted ||
		arr.Parser.Pick == nil || arr.Parser.Pick.InfoHash != hShowPack {
		b, _ := json.Marshal(arr.Parser)
		t.Errorf("parser check %s", b)
	}
	rows, _, err := ListDiffs(e.ctx, e.st.Reader(), DiffFilter{Version: "tv"})
	if err != nil || len(rows) != 1 || rows[0].Bucket != BucketAgree || rows[0].Reason != ReasonSameRelease {
		t.Fatalf("diffs %+v %v", rows, err)
	}

	e.prow.setRSS(item{title: "Show.Alt.S01E03.2160p.WEB-DL-GRP", hash: hShowE03, size: 8e9, pub: t0, tv: true})
	if rs, err := e.r.RSS(e.ctx); err != nil || rs.Picks != 1 {
		t.Fatalf("rss %+v %v", rs, err)
	}
	if s := one[string](t, e.st, `SELECT json_extract(decision, '$.parser.mode') FROM shadow_rss_pick WHERE info_hash = ?`,
		hShowE03); s != "v4plus" {
		t.Errorf("rss pick parser mode %q", s)
	}
}
