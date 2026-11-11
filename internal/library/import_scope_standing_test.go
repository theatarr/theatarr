package library

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// With prefer_seadex on, a file of two or more episodes whose release's SeaDex entry covers only some of them takes
// no standing from that entry (docs/design/decide.md, "SeaDex standing"). The import's checks and plan are as before;
// a plan that would link such a file that is not an upgrade without the standing is held for review whole. So every
// import ends as before, or with nothing linked or moved aside.

// scopeEntry adds AniList entry id mapped to episodes of the title (tvdb 5; "" adds no mapping), listing hashA as
// best (or, with alt, an alternative) and, with otherBest, another torrent as best.
func scopeEntry(e *env, id int, episodes string, alt, otherBest bool) {
	e.t.Helper()
	e.exec(`INSERT OR IGNORE INTO anime_franchise(slug,name,seed_anilist,created_at,updated_at) VALUES('s','S',11,0,0)`)
	e.exec(`INSERT INTO anime_entry(franchise,key,origin,anilist_id,tmdb_movie,body) VALUES('s',?,'anilist',?,'[]','{}')`, fmt.Sprintf("al:%d", id), id)
	if episodes != "" {
		e.exec(`INSERT INTO anime_mapping(franchise,entry_key,status,tvdb_series,episodes,updated_at) VALUES('s',?,'auto',5,?,0)`, fmt.Sprintf("al:%d", id), episodes)
	}
	extra := ""
	if otherBest {
		extra = fmt.Sprintf(`,{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true,"files":[{"name":"Other.mkv","length":20000000}]}}`, strings.Repeat("b", 40))
	}
	body := fmt.Sprintf(`{"anilist":%d,"torrents":[{"info_hash":"%s","preference":2,"is_best":%t,"facts":{"isBest":%t,"files":[{"name":"Show.mkv","length":10000000}]}}%s]}`,
		id, hashA, !alt, !alt, extra)
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing',?,200,?,0,253402214400000)`, fmt.Sprint(id), body)
}

// scopeDownload is download 1 of release rel (one file, WEBDL-2160p, hashA) for eps, with release entry entry.
func scopeDownload(e *env, rel, eps, entry string) {
	e.t.Helper()
	scopePack(e, eps, entry, rel)
}

// scopePack is download 1 (WEBDL-2160p, hashA) for eps with the given files (names without .mkv) in one folder.
func scopePack(e *env, eps, entry string, files ...string) {
	e.t.Helper()
	folder := packRelease
	if len(files) == 1 {
		folder = files[0]
	}
	m := map[string]float64{}
	for _, f := range files {
		m[f+".mkv"] = 2700
	}
	e.release(folder, m)
	e.seriesDownload(1, 2, 20, hashA, folder, eps, nil)
	extras := map[string]string{}
	if entry != "" {
		extras["anilist"] = entry
	}
	raw, _ := json.Marshal(indexer.Release{InfoHash: hashA, Extras: extras})
	e.exec(`UPDATE download SET quality = 'WEBDL-2160p', release_data = ? WHERE id = 1`, string(raw))
}

// curated gives held link id the stored standing of hash under entry (preference 3, best).
func curated(e *env, id int64, hash string, entry int) {
	e.exec(`UPDATE link SET file_metadata = ? WHERE id = ?`,
		fmt.Sprintf(`{"Revision":1,"InfoHash":"%s","SeaDexPreference":3,"SeaDex":2,"SeaDexEntry":%d}`, hash, entry), id)
}

// scopeOutcome is the import's result: linked episodes per file, trash rows, the held (-OLD) links' states, the
// download state and the skipped files' reasons.
func (e *env) scopeOutcome(p *Plan) string {
	var links []string
	for _, l := range p.Links {
		links = append(links, fmt.Sprint(l.Episodes))
	}
	sort.Strings(links)
	var skips []string
	for rel, why := range skipped(p) {
		skips = append(skips, rel+": "+strings.ReplaceAll(why, e.shows, "<shows>"))
	}
	sort.Strings(skips)
	return fmt.Sprintf("links=%s trash=%s held=%s download=%s skipped=%v", strings.Join(links, ","),
		e.one(`SELECT COUNT(*) FROM link_trash`),
		e.one(`SELECT COALESCE(GROUP_CONCAT(state), 'none') FROM (SELECT state FROM link WHERE release_title LIKE '%-OLD' ORDER BY id)`),
		e.one(`SELECT state FROM download WHERE id = 1`), skips)
}

// nothingImported: no link planned, nothing trashed, every held link active, the download still ready.
func (e *env) nothingImported(p *Plan) bool {
	return len(p.Links) == 0 && e.one(`SELECT COUNT(*) FROM link_trash`) == "0" &&
		e.one(`SELECT COUNT(*) FROM link WHERE release_title LIKE '%-OLD' AND state <> 'active'`) == "0" &&
		e.one(`SELECT state FROM download WHERE id = 1`) == "ready"
}

const (
	scopeCombined = "Show.S01E01E02.2160p.WEB-DL.DDP5.1.H.265-GRP"
	scopeSingle   = "Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP"
	scopeHeld     = "held for review" // HEAD's outcome where it is not base's: nothing imported
)

type scopeCase struct {
	name string
	// base is the outcome on 2913d536 (before the SeaDex scope work); head is "" when HEAD's equals base, else
	// scopeHeld.
	base, head string
	setup      func(e *env)
}

func scopeCases() []scopeCase {
	remuxE01 := func(e *env) { e.heldLink(1, "Bluray-2160p Remux", "Show.S01E01.2160p.BluRay.REMUX.HEVC-OLD", 201) }
	remuxE02 := func(e *env) { e.heldLink(1, "Bluray-2160p Remux", "Show.S01E02.2160p.BluRay.REMUX.HEVC-OLD", 202) }
	sameTorrent := func(quality, rel string, withFormat bool) func(e *env) {
		return func(e *env) {
			if withFormat {
				scopeCuratedFormat(e)
			}
			scopeEntry(e, 11, `[[1,1]]`, false, false)
			e.heldLink(1, quality, rel, 201)
			curated(e, 1, hashA, 11)
			scopeDownload(e, scopeCombined, "[201, 202]", "11")
		}
	}
	upgradesOff := func(profile bool) func(e *env) {
		return func(e *env) {
			vp := e.pol.Versions["tv"]
			if profile {
				e.lib.engine.Profile(vp.Profile, format.Sonarr).Profile.UpgradeAllowed = false
			} else {
				vp.Upgrades = policy.UpgradesOff
			}
			scopeEntry(e, 11, `[[1,1]]`, false, false)
			e.heldLink(1, "WEBDL-1080p", "Show.S01E02.1080p.WEB-DL.H.264-OLD", 202)
			scopeDownload(e, scopeCombined, "[201, 202]", "11")
		}
	}
	return []scopeCase{
		// The defect: an E01E02 file best only for the E01-only entry al:11 replaced E02's better uncurated remux.
		{"reproduction, E01 missing", "links=[201 202] trash=1 held=unlinked download=imported skipped=[]", scopeHeld, func(e *env) {
			scopeEntry(e, 11, `[[1,1]]`, false, false)
			scopeEntry(e, 12, `[[1,2],[1,3],[1,4]]`, false, true)
			remuxE02(e)
			scopeDownload(e, scopeCombined, "[201, 202]", "11")
		}},
		{"reproduction, E01 held worse", "links=[201 202] trash=2 held=unlinked,unlinked download=imported skipped=[]", scopeHeld, func(e *env) {
			scopeEntry(e, 11, `[[1,1]]`, false, false)
			scopeEntry(e, 12, `[[1,2],[1,3],[1,4]]`, false, true)
			remuxE02(e)
			e.heldLink(2, "WEBDL-1080p", "Show.S01E01.1080p.WEB-DL.H.264-OLD", 201)
			scopeDownload(e, scopeCombined, "[201, 202]", "11")
		}},
		{"combined, entry covering both", "links=[201 202] trash=1 held=unlinked download=imported skipped=[]", "", func(e *env) {
			scopeEntry(e, 13, `[[1,1],[1,2]]`, false, false)
			remuxE02(e)
			scopeDownload(e, scopeCombined, "[201, 202]", "13")
		}},
		{"combined, prefer off", "links= trash=0 held=active download=ready skipped=[" + scopeCombined +
			".mkv: Not an upgrade for existing file(s). Existing quality: Bluray-2160p Remux. New Quality WEBDL-2160p.]", "", func(e *env) {
			e.pol.Versions["tv"].PreferSeaDex = false
			scopeEntry(e, 11, `[[1,1]]`, false, false)
			remuxE02(e)
			scopeDownload(e, scopeCombined, "[201, 202]", "11")
		}},
		{"single episode, its entry", "links=[201] trash=1 held=unlinked download=imported skipped=[]", "", func(e *env) {
			scopeEntry(e, 11, `[[1,1]]`, false, false)
			remuxE01(e)
			scopeDownload(e, scopeSingle, "[201]", "11")
		}},
		{"season pack, entry covering every file", "links=[201],[202],[203],[204] trash=1 held=unlinked download=imported skipped=[]", "", func(e *env) {
			scopeEntry(e, 14, `[[1,1],[1,2],[1,3],[1,4]]`, false, false)
			remuxE02(e)
			e.pack(1, "WEBDL-2160p")
			raw, _ := json.Marshal(indexer.Release{InfoHash: hashA, Extras: map[string]string{"anilist": "14"}})
			e.exec(`UPDATE download SET release_data = ? WHERE id = 1`, string(raw))
		}},
		{"conflicting ranks for one hash, single episode", "links= trash=0 held=active download=ready skipped=[" + scopeSingle +
			".mkv: Not an upgrade for existing file(s). Existing quality: Bluray-2160p Remux. New Quality WEBDL-2160p.]", "", func(e *env) {
			scopeEntry(e, 11, `[[1,1]]`, false, false)
			scopeEntry(e, 12, `[[1,2]]`, true, true)
			remuxE01(e)
			scopeDownload(e, scopeSingle, "[201]", "")
		}},
		{"curated special over an uncurated remux", "links=[205] trash=1 held=unlinked download=imported skipped=[]", "", func(e *env) {
			e.exec(`UPDATE title SET series_type='anime' WHERE id=2`)
			e.exec(`INSERT INTO episode(id,title_id,season,number,name,created_at,updated_at) VALUES(205,2,0,1,'Special',0,0)`)
			scopeEntry(e, 11, `[[0,1]]`, false, false)
			e.heldLink(1, "Bluray-2160p Remux", "Show.S00E01.2160p.BluRay.REMUX.HEVC-OLD", 205)
			scopeDownload(e, "Show.S00E01.2160p.WEB-DL.DDP5.1.H.265-GRP", "[205]", "11")
		}},
		{"entry without episode coverage, single file", "links=[201] trash=1 held=unlinked download=imported skipped=[]", "", func(e *env) {
			scopeEntry(e, 11, `[]`, false, false)
			remuxE01(e)
			scopeDownload(e, scopeSingle, "[201]", "11")
		}},
		{"unmapped series, single file", "links=[201] trash=1 held=unlinked download=imported skipped=[]", "", func(e *env) {
			scopeEntry(e, 11, "", false, false)
			remuxE01(e)
			scopeDownload(e, scopeSingle, "[201]", "11")
		}},
		{"unmapped series, combined file", "links=[201 202] trash=1 held=unlinked download=imported skipped=[]", "", func(e *env) {
			scopeEntry(e, 11, "", false, false)
			remuxE02(e)
			scopeDownload(e, scopeCombined, "[201, 202]", "11")
		}},
		{"same torrent, equal quality", "links=[201 202] trash=1 held=unlinked download=imported skipped=[]", "",
			sameTorrent("WEBDL-2160p", "Show.S01E01.2160p.WEB-DL.H.265-OLD", false)},
		{"same torrent, equal quality, SeaDex format", "links= trash=0 held=active download=ready skipped=[" + scopeCombined +
			".mkv: Not a Custom Format upgrade for existing file(s). New: [] (0) do not improve on Existing: [Curated old group] (100)]", "",
			sameTorrent("WEBDL-2160p", "Show.S01E01.2160p.WEB-DL.H.265-OLD", true)},
		{"same torrent, held better", "links= trash=0 held=active download=ready skipped=[" + scopeCombined +
			".mkv: Not an upgrade for existing file(s). Existing quality: Bluray-2160p Remux. New Quality WEBDL-2160p.]", "",
			sameTorrent("Bluray-2160p Remux", "Show.S01E01.2160p.BluRay.REMUX.HEVC-OLD", false)},
		{"same torrent, held better, SeaDex format", "links= trash=0 held=active download=ready skipped=[" + scopeCombined +
			".mkv: Not an upgrade for existing file(s). Existing quality: Bluray-2160p Remux. New Quality WEBDL-2160p.]", "",
			sameTorrent("Bluray-2160p Remux", "Show.S01E01.2160p.BluRay.REMUX.HEVC-OLD", true)},
		// Same-torrent curated E01 and an uncurated E02 remux of another torrent (round 3).
		{"same-torrent E01 and other-torrent E02 remux", "links=[201 202] trash=2 held=unlinked,unlinked download=imported skipped=[]", scopeHeld, func(e *env) {
			scopeEntry(e, 11, `[[1,1]]`, false, false)
			e.heldLink(1, "WEBDL-1080p", "Show.S01E01.1080p.WEB-DL.H.264-OLD", 201)
			curated(e, 1, hashA, 11)
			e.heldLink(2, "Bluray-2160p Remux", "Show.S01E02.2160p.BluRay.REMUX.HEVC-OLD", 202)
			scopeDownload(e, scopeCombined, "[201, 202]", "11")
		}},
		{"upgrades off (version), E02 held WEB-1080p", "links= trash=0 held=active download=ready skipped=[" + scopeCombined +
			".mkv: upgrades are disabled]", "", upgradesOff(false)},
		{"upgrades off (profile), E02 held WEB-1080p", "links= trash=0 held=active download=ready skipped=[" + scopeCombined +
			".mkv: upgrades are disabled]", "", upgradesOff(true)},
		{"nothing held, partly covered combined", "links=[201 202] trash=0 held=none download=imported skipped=[]", "", func(e *env) {
			scopeEntry(e, 11, `[[1,1]]`, false, false)
			scopeDownload(e, scopeCombined, "[201, 202]", "11")
		}},
		{"held E01E02, incoming E02E03 and E01", "links=[201],[202 203] trash=1 held=unlinked download=imported skipped=[]", scopeHeld, func(e *env) {
			scopeEntry(e, 11, `[[1,1],[1,2]]`, false, false)
			e.heldLink(1, "Bluray-2160p Remux", "Show.S01E01E02.2160p.BluRay.REMUX.HEVC-OLD", 201, 202)
			scopePack(e, "[201, 202, 203]", "11", "Show.S01E02E03.2160p.WEB-DL.DDP5.1.H.265-GRP", scopeSingle)
		}},
		{"held E01E02E03 remux, incoming E01E02 and E03", "links=[201 202],[203] trash=1 held=unlinked download=imported skipped=[]", scopeHeld, func(e *env) {
			scopeEntry(e, 11, `[[1,1],[1,3]]`, false, false)
			e.heldLink(1, "Bluray-2160p Remux", "Show.S01E01E02E03.2160p.BluRay.REMUX.HEVC-OLD", 201, 202, 203)
			scopePack(e, "[201, 202, 203]", "11", scopeCombined, "Show.S01E03.2160p.WEB-DL.DDP5.1.H.265-GRP")
		}},
		// Overlapping incoming files: base links the first, the second's E02 being taken.
		{"overlapping E01E02 and E02E03, E02 held remux", "links=[201 202] trash=1 held=unlinked download=imported skipped=[]", scopeHeld, func(e *env) {
			scopeEntry(e, 11, `[[1,1]]`, false, false)
			remuxE02(e)
			scopePack(e, "[201, 202, 203]", "11", scopeCombined, "Show.S01E02E03.2160p.WEB-DL.DDP5.1.H.265-GRP")
		}},
		// Round 3 (Terra): overlapping incoming files with E02 remux and E03 WEB held.
		{"overlapping files, E02 remux and E03 WEB held", "links=[201 202] trash=1 held=unlinked,active download=imported skipped=[]", scopeHeld, func(e *env) {
			scopeEntry(e, 11, `[[1,1]]`, false, false)
			remuxE02(e)
			e.heldLink(2, "WEBDL-1080p", "Show.S01E03.1080p.WEB-DL.H.264-OLD", 203)
			scopePack(e, "[201, 202, 203]", "11", scopeCombined, "Show.S01E02E03.2160p.WEB-DL.DDP5.1.H.265-GRP")
		}},
		// Round 3 (Astra): held remuxes overlap at E02; disjoint incoming E01 and E03E04, entry covers E01-E03.
		{"held overlapping remuxes, disjoint incoming", "links= trash=0 held=active,active download=ready skipped=[" +
			"Show.S01E01.2160p.WEB-DL.H.265-GRP.mkv: Episode file on disk contains more episodes than this file contains: " +
			"<shows>/Show (2020)/Season 01/Show.S01E01E02.2160p.BluRay.REMUX.HEVC-OLD.mkv " +
			"Show.S01E03E04.2160p.WEB-DL.H.265-GRP.mkv: Episode file on disk contains more episodes than this file contains: " +
			"<shows>/Show (2020)/Season 01/Show.S01E02E03.2160p.BluRay.REMUX.HEVC-OLD.mkv]", "", func(e *env) {
			scopeEntry(e, 11, `[[1,1],[1,2],[1,3]]`, false, false)
			e.heldLink(1, "Bluray-2160p Remux", "Show.S01E01E02.2160p.BluRay.REMUX.HEVC-OLD", 201, 202)
			e.heldLink(2, "Bluray-2160p Remux", "Show.S01E02E03.2160p.BluRay.REMUX.HEVC-OLD", 202, 203)
			scopePack(e, "[201, 202, 203, 204]", "11", "Show.S01E01.2160p.WEB-DL.H.265-GRP", "Show.S01E03E04.2160p.WEB-DL.H.265-GRP")
		}},
		// Round 3 (Terra): refusal reasons keep their order.
		{"refusal precedence", "links= trash=0 held=active,active download=ready skipped=[" + scopeCombined +
			".mkv: Not a Custom Format upgrade for existing file(s). New: [] (0) do not improve on Existing: [Good] (100)]", "", func(e *env) {
			scopeEntry(e, 11, `[[1,1]]`, false, false)
			scopeEntry(e, 12, `[[1,2]]`, true, true)
			remuxE01(e)
			e.heldLink(2, "WEBDL-2160p", "Show.S01E02.2160p.WEB-DL.GOOD.H.265-OLD", 202)
			curated(e, 2, strings.Repeat("b", 40), 12)
			scopeDownload(e, scopeCombined, "[201, 202]", "11")
		}},
		{"manual import of the reproduction", "links=[201 202] trash=1 held=unlinked download=imported skipped=[]", "", func(e *env) {
			scopeEntry(e, 11, `[[1,1]]`, false, false)
			remuxE02(e)
			scopeDownload(e, scopeCombined, "[201, 202]", "11")
			e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, updated_at) VALUES (1, ?, '[201,202]', 'manual', 0)`, scopeCombined+".mkv")
		}},
	}
}

// scopeCuratedFormat scores a SeaDex-best file of the OLD group 100.
func scopeCuratedFormat(e *env) {
	e.t.Helper()
	p := e.lib.engine.Profile(e.pol.Versions["tv"].Profile, format.Sonarr)
	fs, err := format.ParseArrFormats(format.Sonarr, []byte(`[{"id":900,"name":"Curated old group","specifications":[
		{"name":"SeaDex best","implementation":"SeaDexSpecification","negate":false,"required":true,"fields":{"value":"best"}},
		{"name":"old group","implementation":"ReleaseTitleSpecification","negate":false,"required":true,"fields":{"value":"OLD"}}]}]`))
	if err != nil {
		e.t.Fatal(err)
	}
	p.Formats = fs
	p.Profile.FormatItems = []format.FormatItem{{Format: 900, Score: 100}}
}

// TestImportStandingDifferential: every import ends as on 2913d536, or held with nothing imported where a partly
// covered file would have been linked.
func TestImportStandingDifferential(t *testing.T) {
	t.Parallel()
	for _, tc := range scopeCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := importUpEnv(t, policy.ModeActive, "")
			e.pol.Versions["tv"].PreferSeaDex = true
			tc.setup(e)
			plan, err := e.lib.Import(e.ctx, 1, false)
			if err != nil {
				t.Fatal(err)
			}
			got := e.scopeOutcome(plan)
			t.Logf("outcome %s", got)
			switch {
			case tc.head == scopeHeld:
				if !e.nothingImported(plan) {
					t.Errorf("got %s, want nothing imported", got)
				}
				if why := e.one(`SELECT COALESCE(MAX(last_error), '') FROM library_import WHERE download_id = 1 AND state = 'rejected'`); !strings.Contains(why, "covers only part of its episodes") {
					t.Errorf("hold reason %q", why)
				}
			case got != tc.base:
				t.Errorf("got %s, want base %s", got, tc.base)
			}
		})
	}
}

// A dry run of the reproduction passes through the same gate: nothing planned to link, held for review.
func TestImportStandingDryRun(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, policy.ModeActive, "")
	e.pol.Versions["tv"].PreferSeaDex = true
	scopeEntry(e, 11, `[[1,1]]`, false, false)
	e.heldLink(1, "Bluray-2160p Remux", "Show.S01E02.2160p.BluRay.REMUX.HEVC-OLD", 202)
	scopeDownload(e, scopeCombined, "[201, 202]", "11")
	plan, err := e.lib.Import(e.ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if !e.nothingImported(plan) {
		t.Errorf("dry run planned %s", e.scopeOutcome(plan))
	}
}
