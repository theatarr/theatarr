package shadow

import (
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
)

func TestClassify(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	later := at.Add(time.Hour)
	arr := Decided{Release: "A.2024.2160p.WEB-DL-FLUX", InfoHash: "aa", Accepted: true, Quality: "WEBDL-2160p", Score: 1700, Arr: true}
	rej := arr
	rej.Accepted, rej.Rejections = false, []decide.Rejection{{Reason: "DiskCustomFormatScore", Message: "not better"}}
	same := Decided{Release: "a.2024.2160p.web-dl-flux", Quality: "WEBDL-2160p", Score: 1700}
	equal := Decided{Release: "A.2024.2160p.WEB-DL-NTb", InfoHash: "bb", Quality: "WEBDL-2160p", Score: 1700}
	remux := Decided{Release: "A.2024.2160p.REMUX-FraMeSToR", InfoHash: "cc", Quality: "Remux-2160p", Score: 1950}
	newer := remux
	newer.Published = &later
	higher := Decided{Release: "A.2024.2160p.WEB-DL-X", InfoHash: "dd", Quality: "WEBDL-2160p", Score: 1800}
	for _, tc := range []struct {
		name           string
		arr            Decided
		pick           *Decided
		searched       bool
		bucket, reason string
	}{
		{"arr pick", arr, &arr, true, BucketAgree, ReasonSameRelease},
		{"same title", arr, &same, true, BucketAgree, ReasonSameRelease},
		{"same quality and score", arr, &equal, true, BucketAgree, ReasonSameQualityScore},
		{"better quality", arr, &remux, true, BucketAnother, ReasonBetterQuality},
		{"newer", arr, &newer, true, BucketAnother, ReasonNewerRelease},
		{"higher score", arr, &higher, true, BucketAnother, ReasonHigherScore},
		{"not searched", arr, &arr, false, BucketAgree, ReasonNotSearched},
		{"rejected", rej, &remux, true, BucketWouldnt, "not_an_upgrade"},
		{"rejected, nothing found", rej, nil, true, BucketWouldnt, "not_an_upgrade"},
	} {
		o := Classify(tc.arr, tc.pick, at, tc.searched, true)
		if o.Bucket != tc.bucket || o.Reason != tc.reason || o.Detail == "" {
			t.Errorf("%s: %+v, want %s/%s", tc.name, o, tc.bucket, tc.reason)
		}
		// A search-sourced grab is classified the same way through ClassifyGrab.
		for _, src := range []string{"Search", "UserInvokedSearch", "ReleasePush", ""} {
			c := ClassifyGrab(GrabInput{Source: src, GrabbedAt: at, Arr: tc.arr, Pick: tc.pick, Searched: tc.searched, FoundArr: true})
			if c.Outcome != o || c.Basis != BasisSearch || c.SearchBetter != nil {
				t.Errorf("%s from %q: %+v", tc.name, src, c)
			}
		}
	}
}

func TestRejectionCategory(t *testing.T) {
	t.Parallel()
	for reason, want := range map[string]string{
		"DiskHigherPreference": "not_an_upgrade", "QualityUpgradesDisabled": "not_an_upgrade",
		"QualityNotWanted": "quality_not_wanted", "CustomFormatMinimumScore": "below_min_score",
		"BelowMinimumSize": "size", "WantedLanguage": "language", "UnknownMovie": "unmapped_title",
		"WrongMovie": "unmapped_title", "UnableToParse": "unparseable", "RepackDisabled": "repack",
		"Sample": "other",
	} {
		got := RejectionCategory(reason)
		if got != want {
			t.Errorf("%s: %s, want %s", reason, got, want)
		}
		found := false
		for _, c := range Categories[BucketWouldnt] {
			found = found || c == got
		}
		if !found {
			t.Errorf("%s: category %s not listed", reason, got)
		}
	}
}

// The Frankenstein case and its neighbours: an RSS grab is compared against our RSS picks near it; our later search
// is information on the row. Hand grabs are compared against our decision of the arr's release alone.
func TestClassifyGrabBySource(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	prof := &profile.Profile{Items: []profile.Item{
		{Quality: &profile.Quality{ID: 18, Name: "WEBDL-2160p"}, Allowed: true},
		{Quality: &profile.Quality{ID: 19, Name: "Bluray-2160p"}, Allowed: true},
		{Quality: &profile.Quality{ID: 31, Name: "Remux-2160p"}, Allowed: true},
	}}
	rank := ProfileRank(prof)
	arr := Decided{Release: "Frankenstein (2025) (2160p UHD BluRay x265 DV HDR TrueHD Atmos 7.1 r00t) [QxR]",
		Accepted: true, Quality: "Bluray-2160p", Score: 6500, Arr: true}
	rej := arr
	rej.Accepted, rej.Rejections = false, []decide.Rejection{{Reason: "QualityNotWanted", Message: "not wanted"}}
	ourSame := arr
	ourSame.Arr, ourSame.Indexer = false, "prowlarr-tpb"
	remux := Decided{Release: "Frankenstein 2025 The Reborn Cut 2160p UHD Blu-ray Remux -CiNEPHiLES", Accepted: true,
		Quality: "Remux-2160p", Score: 8575, InfoHash: "cc"}
	sameQS := Decided{Release: "Frankenstein.2025.2160p.UHD.BluRay.x265-OTHER", Accepted: true, Quality: "Bluray-2160p",
		Score: 6500, InfoHash: "dd"}
	higher := sameQS
	higher.Score, higher.InfoHash = 7000, "ee"
	proper := sameQS
	proper.Revision, proper.InfoHash = 2, "ab"
	web := Decided{Release: "Frankenstein.2025.2160p.NF.WEB-DL-FLUX", Accepted: true, Quality: "WEBDL-2160p", Score: 1700,
		InfoHash: "ff"}
	arrPick := arr
	cand := func(id int64, d Decided, after time.Duration) RSSCandidate {
		return RSSCandidate{ID: id, Indexer: "prowlarr-tpb", SeenAt: at.Add(after), Decided: d}
	}
	for _, tc := range []struct {
		name           string
		source         string
		arr            Decided
		pick           *Decided
		rss            []RSSCandidate
		rank           RankFunc
		bucket, reason string
		ours           string // the row's release of ours ("" none)
		rssPick        int64
		better         bool
	}{
		{"RSS same pick, search better", "Rss", arr, &remux, []RSSCandidate{cand(1, ourSame, 24*time.Second)}, rank,
			BucketAgree, ReasonSameRSSPick, ourSame.Release, 1, true},
		{"RSS same pick, search agrees", "Rss", arr, &arrPick, []RSSCandidate{cand(1, ourSame, -10*time.Minute)}, rank,
			BucketAgree, ReasonSameRSSPick, ourSame.Release, 1, false},
		{"RSS same pick, a lower one too", "Rss", arr, nil, []RSSCandidate{cand(2, web, 0), cand(1, ourSame, time.Minute)}, rank,
			BucketAgree, ReasonSameRSSPick, ourSame.Release, 1, false},
		{"RSS better pick", "Rss", arr, &remux, []RSSCandidate{cand(1, ourSame, 0), cand(3, remux, time.Minute)}, rank,
			BucketAnother, ReasonBetterQuality, remux.Release, 3, false},
		{"RSS higher score", "Rss", arr, nil, []RSSCandidate{cand(4, higher, 0)}, rank,
			BucketAnother, ReasonHigherScore, higher.Release, 4, false},
		{"RSS same quality and score, a proper", "Rss", arr, nil, []RSSCandidate{cand(5, proper, 0)}, rank,
			BucketAgree, ReasonSameQualityScore, proper.Release, 5, false},
		{"RSS tie", "Rss", arr, nil, []RSSCandidate{cand(5, sameQS, 0)}, rank,
			BucketAgree, ReasonRSSAccepted, arr.Release, 5, false},
		{"RSS lower pick", "Rss", arr, &remux, []RSSCandidate{cand(6, web, 0)}, rank,
			BucketAgree, ReasonRSSAccepted, arr.Release, 6, true},
		{"RSS no pick, accepted", "rss", arr, &remux, nil, rank, BucketAgree, ReasonRSSAccepted, arr.Release, 0, true},
		{"RSS no pick, not searched", "Rss", arr, nil, nil, rank, BucketAgree, ReasonRSSAccepted, arr.Release, 0, false},
		{"RSS no pick, rejected", "Rss", rej, &remux, nil, rank, BucketWouldnt, "quality_not_wanted", "", 0, true},
		{"RSS better pick, rejected", "Rss", rej, &remux, []RSSCandidate{cand(3, remux, 0)}, rank,
			BucketWouldnt, "quality_not_wanted", remux.Release, 3, false},
		{"RSS same pick, rejected again", "Rss", rej, nil, []RSSCandidate{cand(1, ourSame, 0)}, rank,
			BucketAgree, ReasonSameRSSPick, ourSame.Release, 1, false},
		{"RSS without a profile", "Rss", arr, nil, []RSSCandidate{cand(3, remux, 0)}, nil,
			BucketAgree, ReasonRSSAccepted, arr.Release, 3, false},
		{"hand grab accepted", "InteractiveSearch", arr, &remux, nil, rank,
			BucketAgree, ReasonManualAccepted, arr.Release, 0, true},
		{"hand grab accepted, search agrees", "InteractiveSearch", arr, &arrPick, nil, rank,
			BucketAgree, ReasonManualAccepted, arr.Release, 0, false},
		{"hand grab rejected", "InteractiveSearch", rej, &remux, nil, rank,
			BucketWouldnt, ReasonManualRejected, remux.Release, 0, false},
		{"search grab", "Search", arr, &remux, []RSSCandidate{cand(1, ourSame, 0)}, rank,
			BucketAnother, ReasonBetterQuality, remux.Release, 0, false},
	} {
		c := ClassifyGrab(GrabInput{Source: tc.source, GrabbedAt: at, Arr: tc.arr, Pick: tc.pick, Searched: tc.pick != nil,
			FoundArr: true, RSS: tc.rss, Rank: tc.rank})
		ours := ""
		if c.Ours != nil {
			ours = c.Ours.Release
		}
		var pid int64
		if c.RSSPick != nil {
			pid = c.RSSPick.ID
		}
		if c.Bucket != tc.bucket || c.Reason != tc.reason || ours != tc.ours || pid != tc.rssPick ||
			(c.SearchBetter != nil) != tc.better || c.Detail == "" {
			t.Errorf("%s: %s/%s ours %q pick %d better %v (%s)", tc.name, c.Bucket, c.Reason, ours, pid, c.SearchBetter, c.Detail)
		}
		if tc.better && (c.SearchBetter.Release != remux.Release || !strings.Contains(c.Detail, "kept as information")) {
			t.Errorf("%s: better %+v (%s)", tc.name, c.SearchBetter, c.Detail)
		}
		if want := BasisFor(tc.source); c.Basis != want {
			t.Errorf("%s: basis %s, want %s", tc.name, c.Basis, want)
		}
	}
	if c := ClassifyGrab(GrabInput{Source: "InteractiveSearch", Arr: rej}); !strings.Contains(c.Detail, "category quality_not_wanted") ||
		!strings.Contains(c.Detail, "by hand") {
		t.Errorf("hand grab detail %s", c.Detail)
	}
}

func TestProfileRank(t *testing.T) {
	t.Parallel()
	prof := &profile.Profile{Items: []profile.Item{
		{Quality: &profile.Quality{ID: 31, Name: "Remux-2160p"}},
		{ID: 1000, Name: "WEB 2160p", Items: []profile.Item{{Quality: &profile.Quality{ID: 18, Name: "WEBDL-2160p"}},
			{Quality: &profile.Quality{ID: 17, Name: "WEBRip-2160p"}}}},
		{Quality: &profile.Quality{ID: 19, Name: "Bluray-2160p"}},
	}}
	rank := ProfileRank(prof)
	d := func(q string, rev, score int) *Decided { return &Decided{Quality: q, Revision: rev, Score: score} }
	for _, tc := range []struct {
		a, b *Decided
		want int
	}{
		{d("Bluray-2160p", 1, 0), d("Remux-2160p", 1, 5000), 1}, // the profile's order, not the quality's
		{d("WEBRip-2160p", 1, 100), d("WEBDL-2160p", 1, 50), 1}, // one group: the score decides
		{d("WEBDL-2160p", 2, 0), d("WEBDL-2160p", 1, 50), 1},    // revision before score
		{d("Unknown", 1, 0), d("Remux-2160p", 1, 0), 0},         // unlisted ranks with the first item
		{d("Unknown", 1, 0), d("Bluray-2160p", 1, 0), -1},
		{d("WEBDL-2160p", 1, 10), d("WEBDL-2160p", 1, 10), 0},
	} {
		if got := rank(tc.a, tc.b); got != tc.want {
			t.Errorf("%+v vs %+v: %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
