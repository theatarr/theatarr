# Re:Zero - Starting Life in Another World: watch order and arr mapping

Generated 2026-10-25 by `theatarr anime` from open data (AniList seed 21355). See the JSON next to this file for every field.

## TVDB series

| TVDB id | Title | In Sonarr | SkyHook AniList ids | XEM scene != TVDB |
|---|---|---|---|---|
| 305089 | Re: ZERO, Starting Life in Another World | True | 21355, 100049, 108632, 119661, 163134, 189046 | 0 of 85 |

## Automatic watch order

Spine = AniList prequel/sequel chain by premiere; other entries go after the last spine entry that premiered before them; TVDB-only specials use TVDB airsBefore/After. *opt* = optional.

| # | Entry | Format | Date | Role | TVDB (Sonarr numbering) | TMDb | Flags | Notes |
|---|---|---|---|---|---|---|---|---|
| 1 | Re:ZERO -Starting Life in Another World- `al:21355` | TV | 2016-04-04 | mainline | S01E01-E25 |  |  | alternate: kitsu:42903 |
| 2 | Re:ZERO ~Starting Break Time From Zero~ `al:21780` | TV_SHORT | 2016-04-08 | spin-off *opt* | S00E01-E11 |  | chibi-short, short | TVDB places after al:21355 (mid-entry, before S01E02); mapping: anibridge |
| 3 | Re:PETIT ~Re: Starting Life in Another World From PETIT~ `al:21891` | TV_SHORT | 2016-06-24 | spin-off *opt* | S00E12-E25 |  | chibi-short, short | TVDB places after al:21355 (mid-entry, before S01E13); mapping: anibridge |
| 4 | Re:ZERO -Starting Life in Another World- OVAs #1: Memory Snow `al:100049#1` | OVA | 2018-10-06 | side-story *opt* | S00E26 | 532321 |  |  |
| 5 | Re:ZERO -Starting Life in Another World- OVAs #2: Frozen Bonds `al:100049#2` | OVA | 2019-11-08 | side-story *opt* | S00E28 | 566451 |  | TVDB places after start |
| 6 | Re:ZERO -Starting Life in Another World- Season 2 `al:108632` | TV | 2020-07-08 | mainline | S02E01-E13 |  |  |  |
| 7 | Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 2nd Season `al:120700` | ONA | 2020-07-10 | spin-off *opt* | S00E30-E42 |  | chibi-short, short | TVDB places after al:108632 (mid-entry, before S02E02); mapping: anibridge |
| 8 | Re:ZERO -Starting Life in Another World- Season 2 Part 2 `al:119661` | TV | 2021-01-06 | mainline | S02E14-E25 |  |  |  |
| 9 | Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 2nd Season Part 2 `al:128306` | ONA | 2021-01-08 | spin-off *opt* | S00E43-E54 |  | chibi-short, short | TVDB places after al:119661 (mid-entry, before S02E15); mapping: anibridge |
| 10 | Re:ZERO -Starting Life in Another World- Season 3 `al:163134` | TV | 2024-10-02 | mainline | S03E01-E16 |  |  |  |
| 11 | Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 3rd Season `al:182417` | ONA | 2024-10-03 | spin-off *opt* | S00E55-E70 |  | chibi-short, short | TVDB places after al:163134 (mid-entry, before S03E02); mapping: anibridge |
| 12 | Re:ZERO -Starting Life in Another World- Season 4 `al:189046` | TV | 2026-04-08 | mainline | S04E01-E19 |  |  |  |
| 13 | Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 4th Season `al:210687` | ONA | 2026-04-08 | spin-off *opt* |  |  | chibi-short, short |  |
| - | Re:Zero Starting Life in Another World - Director's Cut `kitsu:42903` | TV | 2020-01-01 | alternate *opt* |  |  | alternate-version, directors-cut, not-on-anidb, not-on-anilist | alternate of al:21355 |

## Story order (candidate)

Basis: AniList chain (topological) + TVDB airs for single specials. Curated moves applied from `overrides/re-zero.json`: 1 (source: https://myanimelist.net/stacks/53018).

1. Re:ZERO -Starting Life in Another World- OVAs #2: Frozen Bonds `al:100049#2` *opt* [tvdb-airs]
2. Re:ZERO -Starting Life in Another World- (S01E01-E11) `al:21355`
3. Re:ZERO -Starting Life in Another World- OVAs #1: Memory Snow `al:100049#1` *opt* [tvdb-airs (after season)] - override: Memory Snow is set between arc 2 and arc 3 (after S1 ep 11); TVDB only says 'after season 1'
4. Re:ZERO -Starting Life in Another World- (S01E12-E25) `al:21355`
5. Re:ZERO ~Starting Break Time From Zero~ `al:21780` *opt*
6. Re:PETIT ~Re: Starting Life in Another World From PETIT~ `al:21891` *opt*
7. Re:ZERO -Starting Life in Another World- Season 2 `al:108632`
8. Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 2nd Season `al:120700` *opt*
9. Re:ZERO -Starting Life in Another World- Season 2 Part 2 `al:119661`
10. Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 2nd Season Part 2 `al:128306` *opt*
11. Re:ZERO -Starting Life in Another World- Season 3 `al:163134`
12. Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 3rd Season `al:182417` *opt*
13. Re:ZERO -Starting Life in Another World- Season 4 `al:189046`
14. Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 4th Season `al:210687` *opt*

## Release order

| # | Entry | Format | Premiere | Premiered during |
|---|---|---|---|---|
| 1 | Re:ZERO -Starting Life in Another World- `al:21355` | TV | 2016-04-04 |  |
| 2 | Re:ZERO ~Starting Break Time From Zero~ `al:21780` | TV_SHORT | 2016-04-08 | al:21355 |
| 3 | Re:PETIT ~Re: Starting Life in Another World From PETIT~ `al:21891` | TV_SHORT | 2016-06-24 | al:21355 |
| 4 | Re:ZERO -Starting Life in Another World- OVAs #1: Memory Snow `al:100049#1` | OVA | 2018-10-06 |  |
| 5 | Re:ZERO -Starting Life in Another World- OVAs #2: Frozen Bonds `al:100049#2` | OVA | 2019-11-08 |  |
| 6 | Re:Zero Starting Life in Another World - Director's Cut `kitsu:42903` | TV | 2020-01-01 |  |
| 7 | Re:ZERO -Starting Life in Another World- Season 2 `al:108632` | TV | 2020-07-08 |  |
| 8 | Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 2nd Season `al:120700` | ONA | 2020-07-10 | al:108632 |
| 9 | Re:ZERO -Starting Life in Another World- Season 2 Part 2 `al:119661` | TV | 2021-01-06 |  |
| 10 | Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 2nd Season Part 2 `al:128306` | ONA | 2021-01-08 | al:119661 |
| 11 | Re:ZERO -Starting Life in Another World- Season 3 `al:163134` | TV | 2024-10-02 |  |
| 12 | Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 3rd Season `al:182417` | ONA | 2024-10-03 | al:163134 |
| 13 | Re:ZERO -Starting Life in Another World- Season 4 `al:189046` | TV | 2026-04-08 |  |
| 14 | Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 4th Season `al:210687` | ONA | 2026-04-08 |  |

## Entries and mapping detail

| Key | Title | Format | Start | Eps | AniDB | TVDB | Method | Target | TMDb check | anibridge agrees | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `al:21355` | Re:ZERO -Starting Life in Another World- | TV | 2016-04-04 | 25 | 11370 | 305089 S01E01-E25 | scudlee | sonarr |  | True |  |
| `al:21780` | Re:ZERO ~Starting Break Time From Zero~ | TV_SHORT | 2016-04-08 | 11 |  | 305089 S00E01-E11 | anibridge | sonarr S00 |  | True |  |
| `al:21891` | Re:PETIT ~Re: Starting Life in Another World From PETIT~ | TV_SHORT | 2016-06-24 | 14 |  | 305089 S00E12-E25 | anibridge | sonarr S00 |  | True |  |
| `al:100049#1` | Re:ZERO -Starting Life in Another World- OVAs #1: Memory Snow | OVA | 2018-10-06 | 1 | 13430 | 305089 S00E26 | scudlee | sonarr S00 | 532321 Re:ZERO -Starting Life in Another World- Memory Snow (2018-10-06) | True | split from al:100049 (episodes released 2018-10-06 .. 2019-11-08); parent TMDb ids [532321] not assigned per part; TMDb 532321 found by Radarr title search 'Re:ZERO -Starting Life in Another World- OVAs #1: Memory Snow' (date match) |
| `tvdb:305089:S00E27` | Memory Snow - Theater Manners | TVDB_SPECIAL | 2018-10-06 | 1 |  | 305089 S00E27 | tvdb-only | sonarr S00 |  |  | ScudLee: AniDB special S1 of al:100049#2 |
| `al:100049#2` | Re:ZERO -Starting Life in Another World- OVAs #2: Frozen Bonds | OVA | 2019-11-08 | 1 | 13430 | 305089 S00E28 | scudlee | sonarr S00 | 566451 Re:ZERO -Starting Life in Another World- The Frozen Bond (2019-11-08) | True | split from al:100049 (episodes released 2018-10-06 .. 2019-11-08); kitsu:42006 'Re:Zero kara Hajimeru Isekai Seikatsu: Hyouketsu no Kizuna' is the same work on another site (merged); TMDb 566451 found by Radarr title search 'Re:ZERO -Starting Life in Another World- OVAs #2: Frozen Bonds' (date match) |
| `tvdb:305089:S00E29` | Frozen Bonds - Theater Manners | TVDB_SPECIAL | 2019-11-08 | 1 |  | 305089 S00E29 | tvdb-only | sonarr S00 |  |  | ScudLee: AniDB special S2 of al:100049#2 |
| `kitsu:42903` | Re:Zero Starting Life in Another World - Director's Cut | TV | 2020-01-01 | 13 |  |  |  | none |  |  |  |
| `al:108632` | Re:ZERO -Starting Life in Another World- Season 2 | TV | 2020-07-08 | 13 | 14792 | 305089 S02E01-E13 | scudlee | sonarr |  | True | Kitsu 43300 'Re:Zero kara Hajimeru Break Time 2nd Season' looks like a duplicate of this entry |
| `al:120700` | Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 2nd Season | ONA | 2020-07-10 | 13 |  | 305089 S00E30-E42 | anibridge | sonarr S00 |  | True |  |
| `al:119661` | Re:ZERO -Starting Life in Another World- Season 2 Part 2 | TV | 2021-01-06 | 12 | 15593 | 305089 S02E14-E25 | scudlee | sonarr |  | True |  |
| `al:128306` | Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 2nd Season Part 2 | ONA | 2021-01-08 | 12 |  | 305089 S00E43-E54 | anibridge | sonarr S00 |  | True |  |
| `al:163134` | Re:ZERO -Starting Life in Another World- Season 3 | TV | 2024-10-02 | 16 | 17947 | 305089 S03E01-E16 | scudlee | sonarr |  | True |  |
| `al:182417` | Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 3rd Season | ONA | 2024-10-03 | 16 |  | 305089 S00E55-E70 | anibridge | sonarr S00 |  | True |  |
| `al:189046` | Re:ZERO -Starting Life in Another World- Season 4 | TV | 2026-04-08 | 19 | 19242 | 305089 S04E01-E19 | scudlee | sonarr |  | True |  |
| `al:210687` | Re:Zero kara Hajimeru Kyuukei Jikan (Break Time) 4th Season | ONA | 2026-04-08 | 19 |  |  |  | none |  |  |  |

## Related but excluded (crossovers, cameos)

- AniList 104454 Isekai Quartet (TV_SHORT): CHARACTER from 21355 (crossover or cameo)
- AniList 110178 Isekai Quartet 2 (TV_SHORT): CHARACTER from 21355 (crossover or cameo)
- AniList 117074 Isekai Quartet: Another World (MOVIE): CHARACTER from 21355 (crossover or cameo)
- AniList 166093 Snack Basue (TV): CHARACTER from 21355 (crossover or cameo)
- AniList 194447 Isekai Quartet 3 (TV_SHORT): CHARACTER from 21355 (crossover or cameo)

## Against the community order

- recommended: https://myanimelist.net/stacks/63483 (Memory Snow and Frozen Bond after S1, before S2)
- chronological: https://myanimelist.net/stacks/53018 (Frozen Bond first; Memory Snow after S1 ep 11)

- **watch_order vs recommended**: 7/7 community entries matched. Missing here: none. Extra here: none. Order differences: 0
- **story_order vs chronological**: 7/7 community entries matched. Missing here: none. Extra here: none. Order differences: 0
