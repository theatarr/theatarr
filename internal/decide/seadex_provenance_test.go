package decide

import (
	"context"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/indexer"
	"strings"
	"testing"
)

func TestVerifiedRecordIdentityAndRetirement(t *testing.T) {
	const id = "6vdej49pr26gca0"
	old := &ExistingFile{SceneName: "Show S01 Bluray 1080p remux (" + id + ")", IndexerFlags: 92, Quality: "Bluray-720p"}
	hash := strings.Repeat("a", 40)
	snapshot := indexer.SeaDexSnapshot{AniList: 1535, Torrents: []indexer.SeaDexStanding{{RecordID: id, InfoHash: hash, Preference: 3}}}
	x := resolveSeaDex(WithSeaDexTitle(WithSeaDex(context.Background(), []indexer.SeaDexSnapshot{snapshot}), 1), old)
	source := indexer.Release{InfoHash: hash, Extras: map[string]string{"original": "kept"}}
	// Production refreshes always carry the title (unscoped resolution never assigns an entry).
	refreshed := RefreshSeaDexRelease(WithSeaDexTitle(WithSeaDex(context.Background(), []indexer.SeaDexSnapshot{snapshot}), 1), source)
	if refreshed.Extras["anilist"] != "1535" || source.Extras["anilist"] != "" {
		t.Fatal("cross-source release did not retain owned entry provenance for import/retirement")
	}
	if x.InfoHash != "" || x.SeaDexPreference != 3 || x.SeaDexEntry != 1535 {
		t.Fatalf("hashless bridge file not verified by record id: %+v", x)
	}
	if SameTorrent(&x, &ExistingFile{InfoHash: hash}) {
		t.Fatal("record-only match claimed torrent identity")
	}
	if SameTorrent(&x, &ExistingFile{InfoHash: strings.Repeat("b", 40), SceneName: old.SceneName}) {
		t.Fatal("record suffix overrode conflicting known hashes")
	}
	ctx := WithSeaDex(context.Background(), []indexer.SeaDexSnapshot{{AniList: 1535}})
	retired := resolveSeaDex(ctx, &x)
	if retired.SeaDexPreference != 0 || retired.SeaDex != format.SeaDexNone {
		t.Fatalf("retired recommendation remained protected: %+v", retired)
	}
	unavailable := resolveSeaDex(context.Background(), &x)
	if unavailable.SeaDexPreference != 3 {
		t.Fatal("unavailable metadata cleared verified standing")
	}
	e := testEngine(t)
	v := Version{Key: "movie-remux"}
	cfg := *e.Policy.Versions[v.Key]
	cfg.PreferSeaDex = true
	v.Policy = &cfg
	current := &ExistingFile{Quality: "Remux-2160p", Revision: 1, Size: 60 * gb, SceneName: "Dune Part Two (" + id + ")"}
	r := rel("Dune.Part.Two.2024.2160p.BluRay.REMUX ("+id+")", "public", 60*gb)
	r.InfoHash = hash
	r.Flags = []string{indexer.FlagSeaDex, indexer.FlagSeaDexPreferred}
	d, err := e.Decide(WithSeaDex(context.Background(), []indexer.SeaDexSnapshot{snapshot}), r, dune, v, current)
	if err != nil || d.Accepted || d.Upgrade {
		t.Fatalf("hashless held file upgraded to its own torrent: %+v %v", d, err)
	}
	resolved := resolveSeaDex(WithSeaDex(context.Background(), []indexer.SeaDexSnapshot{snapshot}), current)
	current = &resolved
	newFile := LinkedFile(cfg.Kind, cfg.Sonarr, r, "Remux-2160p")
	ok, why, err := e.ImportUpgrade(&dune, v, newFile, current)
	if err != nil || !ok || strings.Contains(why, "preferred SeaDex") {
		t.Fatalf("same-record import invented curator upgrade: %v %s %v", ok, why, err)
	}
}

func TestLegacyFlagsAreNotSeaDexProvenance(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	v := Version{Key: "movie-remux"}
	cfg := *e.Policy.Versions[v.Key]
	cfg.PreferSeaDex = true
	v.Policy = &cfg
	mask, _ := format.Radarr.IndexerFlagsValue("G_Freeleech25, G_DoubleUpload, G_Internal, G_Scene")
	cur := &ExistingFile{Quality: "WEBDL-2160p", Revision: 1, SceneName: "Dune.Part.Two.2024.2160p.WEB-DL-GRP", Size: 20 * gb, IndexerFlags: mask}
	r := rel("Dune.Part.Two.2024.2160p.BluRay.REMUX-GRP", "public", 60*gb)
	d, err := e.Decide(context.Background(), r, dune, v, cur)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Accepted {
		t.Fatalf("non-SeaDex torrent flags blocked genuine quality upgrade: %s", reasons(d))
	}
}

func TestSameHashStandingIsNotRememberedAcrossCandidates(t *testing.T) {
	t.Parallel()
	e := testEngine(t)
	v := Version{Key: "movie-remux"}
	cfg := *e.Policy.Versions[v.Key]
	cfg.PreferSeaDex = true
	v.Policy = &cfg
	cur := &ExistingFile{Quality: "WEBDL-2160p", Revision: 1, SceneName: "Dune.Part.Two.2024.2160p.WEB-DL-GRP", Size: 20 * gb, InfoHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	curated := rel(cur.SceneName, "public", 20*gb)
	curated.InfoHash = cur.InfoHash
	curated.Flags = []string{indexer.FlagSeaDex, indexer.FlagSeaDexPreferred}
	ctx := WithSeaDex(context.Background(), []indexer.SeaDexSnapshot{factsSnapshot(indexer.SeaDexSnapshot{AniList: 1, Torrents: []indexer.SeaDexStanding{{RecordID: "6vdej49pr26gca0", InfoHash: cur.InfoHash, Preference: 3}}})})
	same, err := e.Decide(ctx, curated, dune, v, cur)
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.Decide(ctx, rel("Dune.Part.Two.2024.2160p.BluRay.REMUX-GRP", "public", 60*gb), dune, v, cur)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("same preferred hash: accepted=%v reasons=%s; other hash accepted=%v reasons=%s", same.Accepted, reasons(same), other.Accepted, reasons(other))
	if other.Accepted {
		t.Fatal("accepted non-curated replacement despite observing current hash as preferred")
	}
}
