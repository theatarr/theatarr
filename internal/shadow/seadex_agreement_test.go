package shadow

import (
	"encoding/json"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"strings"
	"testing"
)

func TestRuntimeHoldMustNotBecomeShadowAgreement(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	title := decide.Title{Name: "Alien: Romulus", Year: 2024, Runtime: 0, Kind: policy.KindMovie}
	p := e.eng.Profile(e.pol.Versions["movie-remux"].Profile, format.Radarr)
	q, _ := profile.QualityByName(format.Radarr, "Bluray-720p")
	min := 1.0
	p.Engine.Definitions.Set(profile.Definition{Quality: q, MinSize: &min})
	r := decide.Release{Release: indexer.Release{Title: "Alien.Romulus.2024.720p.BluRay-GRP", Indexer: "prowlarr", Size: 10e9, Seeders: 20, Privacy: policy.PrivacyPublic}}
	d, err := e.eng.Decide(e.ctx, r, title, decide.Version{Key: "movie-remux"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("before shadow filtering: accepted=%v rejections=%+v", d.Accepted, d.Rejections)
	withoutOurs(d)
	got := Classify(summarize(d, r, true), nil, t0, false, false)
	if got.Bucket == BucketAgree {
		t.Fatalf("runtime hold became agreement: %+v; accepted=%v rejections=%+v", got, d.Accepted, d.Rejections)
	}
	if !hasReason(d, "QualityNotWanted") {
		t.Fatal("runtime wait hid independent quality rejection")
	}
}

func hasReason(d *decide.Decision, reason string) bool {
	for _, r := range d.Rejections {
		if r.Reason == reason {
			return true
		}
	}
	return false
}

func TestPendingMetadataExcludedFromAllAgreementRatios(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	db := e.st.Writer()
	if _, err := db.Exec(`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Movie',0,0)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{ReasonSameRelease, ReasonPendingMetadata, ReasonPendingMetadata} {
		bucket := BucketWouldnt
		if r == ReasonSameRelease {
			bucket = BucketAgree
		}
		if _, err := db.Exec(`INSERT INTO shadow_diff(at,title_id,version_key,bucket,reason,created_at,kind) VALUES(?,1,'movie-remux',?,?,?,'movie')`, t0.UnixMilli(), bucket, r, t0.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	s, err := LoadSummary(e.ctx, e.st.Reader(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if s.PendingMetadata != 2 || s.Grabs != 1 || s.Agreement == nil || *s.Agreement != 1 || len(s.Versions) != 1 || s.Versions[0].Grabs != 1 || s.Kinds[0].Grabs != 1 {
		t.Fatalf("pending holds polluted agreement: %+v", s)
	}
}

func TestRSSClassificationMustRespectCuratorPreference(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.pol.Versions["movie-remux"].PreferSeaDex = true
	p := e.eng.Profile(e.pol.Versions["movie-remux"].Profile, format.Radarr)
	p.Profile.MinFormatScore = 0
	q, _ := profile.QualityByName(format.Radarr, "Bluray-720p")
	for i := range p.Profile.Items {
		if p.Profile.Items[i].Quality != nil && p.Profile.Items[i].Quality.ID == q.ID {
			p.Profile.Items[i].Allowed = true
		}
	}
	title := decide.Title{Name: "Alien: Romulus", Year: 2024, Runtime: 119, Kind: policy.KindMovie}
	low := decide.Release{Release: indexer.Release{Title: "Alien.Romulus.2024.720p.BluRay-GRP", Indexer: "prowlarr", Size: 10e9, Seeders: 20, Privacy: policy.PrivacyPublic, Flags: []string{indexer.FlagSeaDex, indexer.FlagSeaDexPreferred}}}
	low.InfoHash = strings.Repeat("a", 40)
	low.Extras = map[string]string{"anilist": "1"}
	var snapshot indexer.SeaDexSnapshot
	if err := json.Unmarshal([]byte(`{"anilist":1,"torrents":[{"info_hash":"`+low.InfoHash+`","preference":3,"is_best":true,"facts":{"isBest":true}}]}`), &snapshot); err != nil {
		t.Fatal(err)
	}
	ctx := decide.WithSeaDex(e.ctx, []indexer.SeaDexSnapshot{snapshot})
	high := low
	high.InfoHash = strings.Repeat("b", 40)
	high.Title = "Alien.Romulus.2024.2160p.BluRay.REMUX-GRP"
	high.Size = 60e9
	high.Flags = nil
	ld, err := e.eng.Decide(ctx, low, title, decide.Version{Key: "movie-remux"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hd, err := e.eng.Decide(ctx, high, title, decide.Version{Key: "movie-remux"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !ld.Accepted || !hd.Accepted || decide.Rank([]*decide.Decision{hd, ld})[0] != ld {
		t.Fatalf("bad setup low=%+v high=%+v", ld, hd)
	}
	got := ClassifyGrab(GrabInput{Source: SourceRSS, Arr: summarize(hd, high, true), RSS: []RSSCandidate{{Decided: summarize(ld, low, false)}}, Rank: e.r.rankFor("movie-remux")})
	if got.Bucket != BucketAnother {
		t.Fatalf("engine chooses curated 720p, report says %+v", got)
	}
}

func TestQualityHoldMustNotBecomeShadowAgreement(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.pol.Versions["movie-remux"].PreferSeaDex = true
	e.eng.Profile(e.pol.Versions["movie-remux"].Profile, format.Radarr).Profile.MinFormatScore = 0
	title := decide.Title{Name: "Alien: Romulus", Year: 2024, Runtime: 119, Kind: policy.KindMovie}
	r := decide.Release{Release: indexer.Release{Title: "Alien.Romulus.2024.Unknown", Kind: "seadex", Indexer: "prowlarr", Size: 10e9, Seeders: 20, Privacy: policy.PrivacyPublic, Flags: []string{indexer.FlagSeaDex, indexer.FlagSeaDexPreferred}}}
	d, err := e.eng.Decide(e.ctx, r, title, decide.Version{Key: "movie-remux"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("before shadow filtering: accepted=%v rejections=%+v", d.Accepted, d.Rejections)
	withoutOurs(d)
	if d.Accepted {
		t.Fatalf("SeaDexQualityPending became accepted after shadow filtering")
	}
}

func TestCombinedMetadataHoldsRemainPending(t *testing.T) {
	t.Parallel()
	d := Decided{Rejections: []decide.Rejection{{Reason: "UnknownRuntime"}, {Reason: "SeaDexQualityPending"}, {Reason: "CustomFormatMinimumScore"}}}
	if pendingReason(d) == "" {
		t.Fatal("combined readiness holds counted as disagreement")
	}
	d.Rejections = append(d.Rejections, decide.Rejection{Reason: "DiskNotUpgrade"})
	if pendingReason(d) != "" {
		t.Fatal("upgrade rejection hidden by readiness hold")
	}
}
