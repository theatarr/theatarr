# Sonarr v5 parser and mapping (`versions.<k>.sonarr: v5`, `v4plus`)

Sonarr v5's parser and series-mapping changes, ported behind a per-version switch so one series version can decide
as v5 while the others stay v4. Source: Sonarr `develop` at `a6171f0ca` (the v5 betas). v4 is the port of
`v4.0.20.3014` (`cab419a`) and does not change with the switch: under v4, ParserTests pass 1,456/1,456 and the corpus
self-check agrees on every title.

## The switch

- **Policy:** `versions.<k>.sonarr: v4 | v4plus | v5`, series versions only, default `v4`. A movie version with the
  key, or another value, fails validation (`must be v4, v4plus or v5, got "v6"`; `only series versions take a Sonarr
  version`). `examples/policy/versions.yaml` sets `sonarr: v4plus` on `tv-2160p` and `anime-tv` (see v4plus below);
  nothing runs v5.
- **Engine:** `decideSeries` reads the version (`sonarr.ParseVersion`) and uses it for the parse, the season-title
  fallback, quality, clean titles, franchise-name matching, multi-language detection and language aggregation
  (`adapt.SonarrRelease` picks v5's language parser for a v5 parse). The mapping (`series.go`) branches on the parse's
  version (`ParsedEpisodeInfo.Parser`).
- **Parser package:** `sonarr.Version` (`V4` zero value, `V5`) with `ParseTitle`, `ParsePath`, `ParseSeasonTitle`,
  `CleanSeriesTitle`, `ParseQuality`, `ParseLanguages`, `ParseReleaseGroup`, `HasMultipleLanguages`,
  `FindIsoLanguage`, `Languages`. The plain functions stay v4. Each version is a `dialect` (pattern tables plus a `v5`
  flag for the logic changes); `parser_v5.go` holds v5's tables, generated from the C# sources.
- **Debug:** `theatarr sonarr-parse -sonarr v5 'Title'` (falls back to the season-title parse as v5's decision maker does).

## Fixture results

- **v5 ParserTests:** 1,688/1,688 (28 fixture files in `internal/decide/parser/sonarr/testdata/parsertests_v5`,
  extracted with `testdata/extract.py --v5` from a Sonarr v5 source checkout; `TestParserTestsV5`), no known failures. Includes v5's new
  `SeasonTitleParserFixture`, `SubGroupParserFixture` and `ParsedEpisodeInfoFixture`; `seasonNumber` expectations
  read as `seasonNumbers: [n]`. `ReleaseComparerFixture` is left out (Usenet blocklist matching, not the parser).
- **v4 ParserTests:** 1,456/1,456; the v4 corpus self-check agrees on every title.
- **Mapping:** v5's `MapFixture` and `GetEpisodesFixture` cases that apply here run as Go tests in
  `internal/decide/series_v5_test.go` (each under v4 and v5) and `internal/decide/profile/series_test.go`.

## Ported, item by item

Commit hashes and PR numbers are Sonarr's.

| Item | v5 source | Behaviour under `v5` | Tests | Deviations |
|---|---|---|---|---|
| Multi-season packs | #8941, #8958; `368a40ace` (full range), `b0bde26f8` (episodes across seasons), `8f8226786` (`SeasonNumbers` only) | `S01-S04`, `Season 1-4` and the like parse to every season (`SeasonNumbers`); mapping takes every season's episodes, with the scene mapping's offset applied to each; `MultiSeasonSpecification` still rejects them (as v5) | v5 fixtures; `TestSeriesDecisionsV5` (multi-season pack, and in a season search for S02) | `SeasonNumber` stays filled (first season) so v4 code paths read something sane |
| Season matching | same | `SeasonMatch` and `SingleEpisodeSearchMatch`: the searched season in `SeasonNumbers`, else the mapped season (none: wrong season) | `TestEpisodeRequestedAndSeasonMatch` (six v4/v5 cases) | none |
| Anime season packs by season title | #8836; `77ef058dc` | No parse: `ParseSeasonTitle` (`[Group] Series Title Curse (BD 1080p)` is a full season with `IsSeasonTitle` and no season); mapping takes the season from the scene mapping (`SceneSeasonNumber`, else `SeasonNumber`, non-negative); no season: no episodes (`UnknownEpisode`) | v5 `SeasonTitleParserFixture`; `TestSeriesDecisionsV5` (mapped, and unmapped) | none |
| Anime pack parsing | `da7fa1f8d`, `91c4ae17c`, `974d5377c`, `5e47cc3ba` | partial and multi-season packs with a subgroup prefix, anime-style groups after multi-episode ranges, 4-digit absolute batches | v5 fixtures | none |
| Chinese formats | `f2f4edad0` (#8042), `c40fbeed5` (#8035) | Chinese season packs and multi-episode releases; Chinese and English titles parsed as separate titles | v5 fixtures | none |
| Seven new languages | `99fc61e63` (Azerbaijani, Uzbek, Malay), `66633b9b0` (Urdu), `72b3b825e` (Romansh), `309b55fe3` (Georgian), `f22ca68e9` (Irish); ids 47-53 | parsed in titles and ISO codes (`V5.Languages()`, `V5.FindIsoLanguage`) | v5 `LanguageParserFixture`, `IsoLanguagesFixture` | the format layer's Sonarr language table stops at 46, so a decision names them by id (`52`); no custom format condition refers to them |
| "ENG SUBS" is not English audio | `224e74605`, `e06c6ba64`, `f055e8a3e` | a language followed by `SUBS` is a subtitle language, not audio | v5 fixtures; `TestSeriesV5Languages` | none |
| Other language fixes | `dacd46962`, `b376b63c9`, `a4e3be721`, `3e2e8e938`, `9e5ebdc62`, `030910bab`, `d09e7893b`, `4fb89252b` | `jap`/`jpn`/`(JA)` Japanese, `Por`, `FRA`/`FRE`, `GER`/`DE`, Swiss German, `ITA`, `multilang` | v5 fixtures | none |
| Diacritics removal for matching | `2a5667e63` (#8424), `156d30633` | `CleanSeriesTitle` removes diacritics, so `The.Shöw` matches `The Show` (and the clean original title, scene mappings) | v5 fixtures; `TestSeriesDecisionsV5` | Unicode decomposition plus the same extra table as `internal/library/naming`; Diacritical.Net may map a few rarer letters too |
| Original-title matching | #8990; `b4e9ab179` | the series' clean original title matches wherever the clean title does: `FindByTitle`, `FindByTitle(title, year)`, `FindByTitleInexact`, and the search shortcut in `FindSeries` | `TestSeriesDecisionsV5` (plain, search, with year); `TestLoadSeriesVersion` (load and decide) | see Original titles below |
| Aliases and titles with a year | `6131f1deb`, `fcfdac99d` (#8408), #8411 | `GetSeriesAliasTitleAndYear` (a scene mapping named by the title without its year, when the year is the series' year: `Alias` match); `GetSeriesByAllTitles` with a year tries the title with the year, then title and year as one title | `TestSeriesDecisionsV5` (alias and year, another year, title holding the year) | none |
| Mapping details | `ParsingService` diff | scene-mapping lookups by `SeasonNumbers.FirstOrDefault(-1)`; nullable mapped season; scene episode match on the first season or -1 | `TestSeriesDecisionsV5` | non-official episode orders (`SeasonType`) do not exist here (series are TVDB official order) |
| Daily releases | `4cfd439cb`, `5d16169e5`, `095126bfe`, `5e5f1835f`, `ae5b03bcf` | short month names, day-month-year, UK dates; a date up to 14 days ahead parses; `SeasonNumbers = [0]` | v5 `DailyEpisodeParserFixture` | none |
| Quality and release group fixes | `6421bb621`, `63431917f`, `dfc37be76`, `547cd5b48`, `7d59b466e`, `6ccab3cfc`, `bee7e4325`, `808d48361`, `b00229e53`, `83b2c9e97`, `5eb18fe27`, `350dea10d`, `a533a1a46`, `b16743bdd`, `5ef066352` | WEB with EAC3 is WEB-DL, WEB 2160p, H.266, 1440p, bit depth; non-ASCII groups, TAoE/QxR/Vialle, no `bit`/`N-Z-B`/hash as group | v5 fixtures | none |
| Other parser changes | everything else between `cab419a` and `a6171f0ca` in `Parser.cs` (`965b6144e` multiple titles, `7b0db46c2` "Part #" in episode titles, `2335657fe` NCOP/NCED, `5eaeb2c6a` 3-digit seasons, `b57e7e2db` Temporada, `900cc21d3` Polish trackers, `107e474f9`, `763c9c838`, `b84a621e9`, `1ef034af3`, `994faa60c`) | taken whole: `parser_v5.go` is the v5 table, not a pick of patterns | v5 fixtures | none |
| Decision maker | `DownloadDecisionMaker` | after the special-episode fallback, a release that does not parse is tried as a season title | `TestSeriesDecisionsV5` | none |

### Original titles

v5 takes `CleanOriginalTitle` from SkyHook: the series' translation in its original language. Theatarr reads the same thing
from the TVDB extended record the TVDB client caches (`GET <base>/series/<id>/extended?meta=translations&short=true` in
`metadata_cache`): the name translation in `originalLanguage`, not an alias. `LoadSeriesVersion` fills
`Title.OriginalTitle` for series (no migration). Only a v5 version matches it; a series with no cached record has no
original title. An arr corpus holds no TVDB cache rows, so the corpus measurement below cannot show this item; tests
cover it.

## Skipped

| Item | Why |
|---|---|
| Indexer flag `Subtitles` (256) | Only a custom format condition value (`IndexerFlag`); it changes no parse or decision. No indexer adapter sets it |
| Format-side names for languages 47-53 | `internal/decide/format` has no names for them; decisions show the id |
| Anime search changes (#8837 `bdd4f0b7d` season titles without season parameter, #8833 `cd0850938` single episodes for unaired seasons, `6c329e8a6` non-English aliases as search terms) | Search query building (indexer/router side), no parsing or matching change |
| Per-profile quality sizes, release profile additions (`AllowSeasonPackWithoutAllEpisodesAired`), metadata language and episode order, API v5 | Not ported |
| Pack threshold, dangerous file refusal, skipping filled seasons | Not behind the switch: they apply in every mode |

## Measurement: v4 against v5

The measurement tool (`TestSonarrV5Diff` and `theatarr decide shadow` in `internal/decide/shadow`) was removed with
the arr comparison; the results below stand as recorded. It parsed every Sonarr corpus title with both parsers, decided it under
both for the series the arr's own parse mapped it to (`tv-2160p` for standard series, `anime-tv` for anime
ones, no search, no files), and replayed the series grabs of `docs/design/series.md` under both.

On a corpus of real release titles of standard and anime series, v5 parses about 2% of titles differently from
v4 and decides about 1% differently; anime titles differ most (about 9% parse, 4% decide). The replayed series
grabs decide the same under both, apart from a few language-only differences of the kind in case 15 (score
unchanged).

**Kinds of parse difference** (a title can count in several): languages 171, series title 48, episodes 48, seasons
33, release group 22, release type 21, absolute 4, quality 3, parsed at all 2. The table below covers every
difference, grouped by cause (a few one-off language changes, such as `Dutch` gaining `Japanese` from `JPN`, are
folded into the nearest row).

**Judged, every distinct case** (counts are titles; better/worse is against what the release is):

| # | Titles | Example | v4 | v5 | Judgement |
|---|---:|---|---|---|---|
| 1 | 54 | `The Expanse 2015 S06E06 Ger Eng DL ... ZeroTwo`, `Dark.S02E02 ... ENG-GER` | English | German, English | better |
| 2 | 9 | `The.Mandalorian.S01E08.2160p.WEB-DL.DE-EN-TR ...` | Unknown (decision English) | German | better (English and Turkish are in the title too; neither version sees them) |
| 3 | 20 | `The.Last.of.Us.S01E07.Eng.Fre.Ger.Ita.Spa.Cze.Jpn ...`, `Cobra Kai ... ENG ITA FRE SPA DEU`, `Squid Game S01 KOREAN ENG FRA GER ...` | English, Italian, Spanish | adds French, German, Japanese (Portuguese, Hungarian) | better |
| 3b | 36 | `ReZero.kara.Hajimeru.Isekai.Seikatsu.S04E12.1080p.AMZN.WEB-DL.JPN.DDP2.0.H.264-ToonsHub`, `Asobi.Asobase ... JPN.FLAC ...`, `[iKaos] ... Dragon Ball Z ... JPN+ENG` | Unknown | Japanese | better (`JPN` names the audio) |
| 4 | 1 | `[Judas] Dororo (Season 1 S1) [BD 1080p][HEVC x265 10bit][Eng-Subs]` | English | Unknown | better (subtitles) |
| 5 | 3 | `[Anitsu] Sousou no Frieren ... [DUAL JAP PT-BR] [SUB PT-BR ENG]` | English | Japanese, English | better (Japanese audio found; English is subtitles, still counted) |
| 6 | 1 | `Silo.S03E02.MULTi.VFF.2160p.HDR.WEB.EAC3.Atmos ...` | HDTV-2160p | WEBDL-2160p | better |
| 7 | 3 | `[PMR] Re ZERO ... S03E01-12 (BD Remux ...)` (and `S03E01-08`, `S03 E01-05`) | S01E12 (absolute 12), not monitored | S03E01-12, accepted | better |
| 8 | 1 | `[LostYears] KonoSuba ... S3E01-11 (Batch)` | S03E01 (absolute 11) | S03E01-11 | better |
| 9 | 1 (2 grabs) | `Asobi Asobase - workshop of fun - S01 Bluray 1080p remux (e3cv9phesdihy9g)` | series title holds `S01 Bluray remux`: Unknown Series | season pack S01 mapped (`FullSeason` in the replay's single-episode search; `DiskCustomFormatScore` against the file) | better (grabbed by hand) |
| 10 | 3 | `1923 - S02E02 - ...[WEBDL-1080p.AC3.x264.Vialle]`, `Dune Prophecy ... - Goki)[TAoE]`, `Planet Earth II ... Silence) [QxR]` | no group, Silence | Vialle, TAoE, QxR | better |
| 11 | 4 | `Is It Wrong to Try to Pick Up Girls in a Dungeon - S03E10 (BDRip ... 10-bit AAC)` | group `bit` | none | better |
| 12 | 13 | `Ninja.Kamui.2024.S01E01 ... DUAL-Goki-TAoE` | Goki-TAoE | TAoE | neutral |
| 13 | 4 | `[Almighty] Jujutsu Kaisen S3 Vol.1 [BD ...]`, `[Almighty] Re Zero ... S4 - Vol.3`, `[Shadow Garden] ... S2 (Vol 1)`, `Dr. STONE ... S3 (S03) Part 1` | full season pack (all of S03) | partial season: no episodes (`UnknownEpisode`) | better in principle (Vol.1 is half the season; v4 would count it as the whole), but the release can no longer be grabbed |
| 14 | 2 | `[sam] Chainsaw Man - The Movie Reze Arc (2025) (WEB 1080p ...)` | no parse | season-title pack `Chainsaw Man - The Movie Reze Arc` | neutral (maps only through a scene mapping of that name) |
| 15 | 8 | `Halo.S02E03 04.WebDL.2160p ... ITA.ENG.SUBS.K-Z`, `Operazione Speciale - Lioness ... ITA ENG SUBS`, `Your Friends and Neighbors S02e08 [720p Ita Eng Spa SubS]` | Italian, English | Unknown (decision: English, the original) | worse: Italian audio lost along with the English subtitles. The 3 `tv-2160p` replay differences are of this kind (score unchanged) |
| 16 | 4 | `Anthony.Bourdain.No.Reservations.S08E05.Japan.Cook.It.Raw ...` | Unknown | Japanese (`JAP`/`JPN` without a word boundary) | worse (false positive on "Japan") |
| 17 | 4 | `Anthony.Bourdain.Parts.Unknown.S10E02.French.Alps ...`, `Emily.in.Paris.S02E10.French.Revolution ...` | decision French | decision English: under v5 the episode title's language is recognised, so the aggregation drops it (parse unchanged) | better |
| 18 | 1 | `Anthony.Bourdain.No.Reservations.2005.S01E08.Uzbekistan ...` | Unknown | Uzbek | worse (false positive) |
| 19 | 17 | `[SubsPlus+] Is It Wrong ... | Dungeon ni Deai wo Motomeru no wa Machigatteiru Darou ka | DanMachi` | Unknown (decision Japanese) | Georgian (`ka`), decision `52` | worse: romanised Japanese `ka` reads as Georgian and replaces the original language; an anime profile's original-language formats stop matching |
| 20 | 1 | `[sam] DanMachi Season 3 (S03) v2 + OVA | ...` | series `DanMachi` | series `DanMachi Season 3`, Georgian | worse |
| 21 | 20 | `Cosmos.Possible.Worlds.E12.2020.DSNP.1080p.WEB-DL ...` | S01E12 | series `Cosmos Possible Worlds E12`, S20E20 | worse: `7b0db46c2` moved the `E12`/`Part 12` pattern below the 4-digit `SSEE` pattern, so the year reads as S20E20 |
| 22 | 13 | `Gushing.over.Magical.Girls.E13.<German episode title>.German.2024.ANiME.DL ... STARS` | S01E13, German and original | series with the episode title, S20E24, Unknown Series | worse, same cause as 21 |
| 23 | 10 | `[ToonsHub] Solo Leveling E9 You've Been Hiding Your Skills 1080p ... (Multi-Subs) S01` (Frieren too) | S01E09 | season pack of a series named with the whole title: Unknown Series | worse, same cause as 21 |

Every case is v5's own behaviour, not a porting error: each was checked against the v5 patterns (the regex that
matches, its place in v5's list, and the commit that put it there). Cases 15-23 are upstream v5 regressions against
v4 on real-world titles.

## Choosing v5

- **`tv-2160p`:** switch when the arrs themselves move to v5, not before. On the 4k corpus v5 is better on languages
  (cases 1-5, 17) and a little better on groups and quality, the replay agreement does not move, and the losses are Italian dual-language releases with `ENG SUBS` (15) and documentary titles naming countries
  (16, 18). The year-as-`SSEE` regression (21) is real for `E12.2020`-style titles. Net: small gain, two regression
  classes; no reason to run ahead of upstream.
- **`anime-tv`:** stay on v4. v5 fixes multi-episode ranges (7, 8) and maps season-title packs and some mangled
  titles (9), but on the anime corpus it loses more than it gains: `ka` as Georgian (19, 20) removes the original
  language from DanMachi releases, and the `E9 ... S01` / `E13.<title>.2024` layouts (22, 23) stop mapping. Revisit
  when v5 fixes the pattern order (`7b0db46c2`) and the Georgian `ka`.
- **Original titles** (#8990) are the one v5 behaviour the replay cannot show. It needs TVDB data in the cache (the
  TVDB client fills it on first use).

Of the two, v4plus (below) is the mode the example policy uses.

## v4plus (`versions.<k>.sonarr: v4plus`)

v4plus is v4 with the v5 changes the corpus showed to be better, taken one by one, without the changes behind v5's
regressions, plus a few Theatarr fixes where v4 and v5 are both wrong. The table lists every change and its source,
so a later upstream fix can be weighed against it.

- **Code:** `internal/decide/parser/sonarr/parser_v4plus.go` (`sonarr.V4Plus`, the `v4plusd` dialect: v4's tables
  with the rows below swapped in; each v4 pattern changed by one commit is patched in place and the build panics if
  the v4 text moves). A v4plus parse has the v4 shape (`SeasonNumber`, no `SeasonNumbers`) and is marked
  `Parser = V4Plus`, so the mapping and the specifications follow their v4 paths; the language aggregation uses
  v4plus's language parser (`adapt.sonarrLangsOf`).
- **Tests:** `TestV4Plus` (one corpus title per row, and each v5 regression class, against v4 too),
  `TestV4PlusSubtitleLanguages`, `TestV4PlusVersion`. v4's ParserTests and corpus self-check are unchanged.
- **Debug:** `theatarr sonarr-parse -sonarr v4plus 'Title'`.

### Changes

| # | Change | Source | Example (corpus) |
|---|---|---|---|
| 1 | Anime multi-episode ranges with a subgroup: the v4 pattern "Season+Episode + Absolute" split into a multi-episode pattern (absolute optional) and a single-episode one | v5 `91c4ae17c` (#8948); hash kept as v4's `\w{8}` (`808d48361` not taken) | `[PMR] Re ZERO ... S03E01-12 (BD Remux ...)`: S03E01-12, was S03E01 absolute 12; KonoSuba `S3E01-11 (Batch)` |
| 2 | `GER` (word), Swiss German, case-sensitive `DE` as German; `-GER` not a group | v5 `9e5ebdc62`, `030910bab` | `Ger Eng DL`, `ENG-GER`, `DE-EN-TR` |
| 3 | `FRA`, `FRE` as French; `FR`/`ENG` found next to each other; `-FRA`/`-FRE` not a group | v5 `3e2e8e938`, `f055e8a3e` | `Eng.Fre.Ger.Ita`, `French.Revolution` as the episode title |
| 4 | `JPN`, `JAP`, `(JA)` as Japanese, **bounded by `\b`** | v5 `dacd46962`, `b376b63c9`; **Theatarr**: `\b` (v5's bare `JAP\|JPN` matches "Japan") | `...WEB-DL.JPN.DDP2.0...`; `S08E05.Japan.Cook.It.Raw` stays Unknown |
| 5 | `POR` as Portuguese, `国语音轨` as Chinese | v5 `a4e3be721`, `f2f4edad0` | `Eng.Fre.Ger.Ita.Por.Spa` |
| 6 | A single language directly before `SUB`/`SUBS` is a subtitle; **a list of languages before SUBS keeps v4's reading (audio)** | v5 `224e74605`; **Theatarr**, instead of v5 `e06c6ba64` (whole list = subtitles) | `[Eng-Subs]`: Unknown; `E-AC3+AC3 ITA ENG SUBS`: Italian, English (v5: Unknown) |
| 7 | Languages after a `[Multiple Subtitle]` marker are subtitles | **Theatarr** (v4 and v5 read them as audio) | Erai-raws `[Multiple Subtitle] [ENG][POR-BR][SPA-LA]...`: Unknown |
| 8 | Upper-case `EN` and `JA` as English and Japanese (case-sensitive) | **Theatarr**: the ISO codes Sonarr's own `{MediaInfo AudioLanguages}` naming writes; in the corpus every upper-case `EN`/`JA` word is such a tag | `[JA+EN]-RUDY`, `[EN-TR]-TURG`, `DE-EN-TR` |
| 9 | Every case-sensitive language match counts, not only the first | **Theatarr** (Sonarr reads `Match`, so `DE-EN` gives only German) | `DE-EN-TR`: German, English |
| 10 | `GER` only after a separator or at the start | **Theatarr** (v5's `\bger\b` matches mojibake) | `ProtÃÂ©ger et servir` stays Unknown (v5: German) |
| 11 | `TAoE`, `QxR`, `Vialle` are groups; the encoders behind them (Silence, afm72, Panda, Ghost, MONOLITH, Tigole, RZeroX) are not | v5 `83b2c9e97`, `b00229e53`, `5eb18fe27` | `... Silence) [QxR]`: QxR; `- Goki)[TAoE]`: TAoE |
| 12 | `-ITA` and `8/10/24-bit` are not groups | v5 `d09e7893b`, `350dea10d` | `(BDRip ... 10-bit AAC) [Dual Audio]`: no group, was `bit` |
| 13 | `WEB` followed by `EAC3` is WEB-DL | v5 `6421bb621` (#8923) | `Silo.S03E02...HDR.WEB.EAC3...`: WEBDL-2160p, was HDTV-2160p |

**Left out on purpose** (each re-evaluated when upstream changes it; rerun the measurement below):

| v5 change | Why |
|---|---|
| Pattern order of `7b0db46c2` ("Part #" in episode titles) | `E12.2020` read as S20E20; `Solo Leveling E9 ... S01` and `E13.<title>.2024` stop mapping (43 corpus titles) |
| Languages 47-53 (`99fc61e63`, `66633b9b0`, `72b3b825e`, `309b55fe3`, `f22ca68e9`) | romaji `ka` read as Georgian (18 DanMachi titles), "Uzbekistan" as Uzbek; the format layer has no names for them |
| `e06c6ba64` (a whole language list before SUBS is subtitles) | `ITA ENG SUBS` loses Italian (8 titles); replaced by change 6 |
| Multi-season packs, season-title packs, partial seasons (`Vol.1`), original titles, diacritics, alias years | need v5's mapping (`SeasonNumbers`); partial seasons would make `Vol.3` packs ungrabbable; original titles need TVDB data. Use `sonarr: v5` for those |
| `multilang` (`4fb89252b`), daily dates, Chinese formats, the remaining table edits | not seen to matter on the corpus; no measured gain |

### Measurement

The same measurement of v4 against v4plus and v5, same corpus, scene mappings and replay database as above. "Decided" is under the arr's series; decisions are without search
and files.

Against v4, v4plus parses about 1.6% of the corpus titles differently and decides 0.9% differently; v5 differs
from v4plus on fewer titles than from v4. On the replayed grabs, v4, v4plus and v5 agree with the recorded grabs equally often,
and v4plus decides no replayed grab differently from v4.

**v4 → v4plus, every bucket judged** (not a sample: every parse and decision difference was read; the rows add up to the 200 parse differences, plus 4 decision-only ones):

| Titles | What changes | Judgement |
|---:|---|---|
| 51 | `Ger Eng DL` and the like: English → German, English | better |
| 36 | `JPN` tags: Unknown → Japanese (decision unchanged: Japanese was the original) | better |
| 25 | `[JA+EN]`/`[EN+JA]`: Unknown → Japanese, English; the decision gains English next to Japanese, score unchanged | better |
| 13 | `[TR-EN]`/`[EN-TR]` (TURG): Unknown → English (decision unchanged) | better |
| 11 | `[JA]` (Asakura): Unknown → Japanese | better |
| 8 | `DE-EN-TR`: Unknown → German, English (decision English → German, English) | better (Turkish still not read, as in v4) |
| 16 | language lists (`Eng.Fre.Ger.Ita.Spa.Cze.Jpn`, `ENG ITA FRE SPA DEU`, `ITA FRE`): French, German, Japanese, Portuguese added | better |
| 4 | Erai-raws `[Multiple Subtitle] [ENG]...`: English, Spanish, Italian, Russian → Unknown | better (they are subtitles) |
| 4 | `French.Alps`, `French.Revolution` episode titles: decision French → English | better (the language is the episode title's) |
| 3 | `The Pitt ... EN FR`: French → English, French | better |
| 3 | `[DUAL JAP PT-BR] [SUB PT-BR ENG]`: English → Japanese, English | better (Japanese found; English is a subtitle, still counted, as in v5) |
| 3 | `ENG-GER`, `ENG.GER`: English → English, German | better |
| 4 | Re ZERO `S03E01-12`, `S03E01-08`, `S03 E01-05` (3 decide: EpisodeNotMonitored → accepted) and KonoSuba `S3E01-11` | better |
| 1 | `[Eng-Subs]`: English → Unknown | better |
| 1 | `HDR.WEB.EAC3`: HDTV-2160p → WEBDL-2160p | better |
| 1 | `Asobi.Asobase ... JPN.FLAC` (Dutch from the episode title): Dutch → Dutch, Japanese | better (Dutch is v4's own false positive) |
| 1 / 1 / 1 | groups: `Vialle`; `TAoE`; `Silence` → `QxR` | better |
| 4 | group `bit` → none | better |
| 13 | group `Goki-TAoE` → `TAoE` | neutral |

**None of the four v5 regression classes appears** in v4 → v4plus: no Georgian, no `E12.2020`/`E9 ... S01`/`E13`
remapping, no Italian lost before SUBS, no country read as a language. All of them show up in v4plus → v5 (v5's
side): `E12.2020`-style 20 + `E13` 13 + `E9 ... S01` 11 titles, Georgian 18 + 1, `ITA ENG SUBS` 8, `Japan` 4,
`Uzbekistan` 1, mojibake `ger` 1.

**v4plus → v5, where v5 is the better one:** `Asobi Asobase - workshop of fun - S01 Bluray 1080p remux` (1 title,
2 grabs: v5 maps it as a season pack; v4 and v4plus read the hash as an episode); `[Almighty] ... S4 - Vol.3` (3,
partial season; better in principle but ungrabbable); the season-title pack `Chainsaw Man - The Movie Reze Arc`
(2, neutral). v5 is also "right" by its own rule on the `ENG SUBS` lists, but there the lists are audio. Everything
else v4plus has and v5 lacks (`EN`/`JA`, `[Multiple Subtitle]`, lists before SUBS, bounded `JAP`/`GER`) is Theatarr's own.

**The bar** (none of the regression classes; replay agreement not below v4; every v4 → v4plus difference judged an
improvement) **is met**: no replayed grab decides differently from v4, and every difference above is better or
neutral.

### Recommendation

**`sonarr: v4plus` on `tv-2160p` and `anime-tv`**, set in `examples/policy/versions.yaml`. It keeps every v5 gain
the corpus shows except the season-pack mapping ones, has none of v5's regressions, and moves no replayed grab.
`v4` (exactly the arrs) and `v5` (upstream, with its mapping) stay available. Measure again when upstream fixes `7b0db46c2` or `ka`, or when the arrs move to v5.
