# Made in Abyss: watch order and arr mapping

Generated 2026-10-25 by `theatarr anime` from open data (AniList seed 97986). See the JSON next to this file for every field.

## TVDB series

| TVDB id | Title | In Sonarr | SkyHook AniList ids | XEM scene != TVDB |
|---|---|---|---|---|
| 326109 | Made in Abyss | True | 97986, 100643, 114745 | 0 of 25 |

## Automatic watch order

Spine = AniList prequel/sequel chain by premiere; other entries go after the last spine entry that premiered before them; TVDB-only specials use TVDB airsBefore/After. *opt* = optional.

| # | Entry | Format | Date | Role | TVDB (Sonarr numbering) | TMDb | Flags | Notes |
|---|---|---|---|---|---|---|---|---|
| 1 | Made in Abyss `al:97986` | TV | 2017-07-07 | mainline | S01E01-E13 |  |  |  |
| 2 | Made in Abyss: Journey's Dawn `al:101343` | MOVIE | 2019-01-04 | recap *opt* | S00E01 | 526426 | recap | mapping: anibridge |
| 3 | Made in Abyss: Wandering Twilight `al:101344` | MOVIE | 2019-01-18 | recap *opt* | S00E02 | 526429 | recap | mapping: anibridge |
| 4 | Made in Abyss: Dawn of the Deep Soul `al:100643` | MOVIE | 2020-01-17 | mainline | S00E03 | 573730 |  |  |
| 5 | Marulk's Daily Life `al:114744` | SPECIAL | 2020-01-17 | side-story *opt* | S00E04-E07 |  | short | mapping: anibridge |
| 6 | Made in Abyss: The Golden City of the Scorching Sun (up to before S02E12) `al:114745` | TV | 2022-07-06 | mainline | S02E01-E12 |  |  |  |
| 7 | Made in Abyss Retsunichi Koganekyo Just before the final episode retrospective special! ! ~ Six layers of the deep world The secret of the village ~ `tvdb:326109:S00E15` | TVDB_SPECIAL | 2022-09-21 | recap *opt* | S00E15 |  | recap? | mapping: tvdb-only |
| 8 | Made in Abyss: The Golden City of the Scorching Sun (from S02E12) `al:114745` | TV | 2022-07-06 | mainline | S02E01-E12 |  |  |  |
| 9 | Made in Abyss: Retsujitsu no Ougonkyou - Papa to Issho `al:153791` | SPECIAL | 2022-10-26 | side-story *opt* | S00E14 |  |  | mapping: anibridge |
| 10 | Made in Abyss: Mezameru Shinpi `al:160275` | MOVIE | 2026-10-23 | mainline | S00E16 | 1526917 |  | mapping: heuristic |
| 11 | Made in Abyss: Mezameru Shinpi Part 2 `al:218721` | MOVIE |  | mainline |  |  | unaired |  |

## Story order (candidate)

Basis: AniList chain (topological) + TVDB airs for single specials. Curated moves applied from `overrides/made-in-abyss.json`: 0.

1. Made in Abyss `al:97986`
2. Made in Abyss: Journey's Dawn `al:101343` *opt* [tvdb-airs (after season)]
3. Made in Abyss: Wandering Twilight `al:101344` *opt* [tvdb-airs (after season)]
4. Made in Abyss: Dawn of the Deep Soul `al:100643`
5. Marulk's Daily Life `al:114744` *opt*
6. Made in Abyss: The Golden City of the Scorching Sun `al:114745`
7. Made in Abyss Retsunichi Koganekyo Just before the final episode retrospective special! ! ~ Six layers of the deep world The secret of the village ~ `tvdb:326109:S00E15` *opt*
8. Made in Abyss: Retsujitsu no Ougonkyou - Papa to Issho `al:153791` *opt* [tvdb-airs (after season)]
9. Made in Abyss: Mezameru Shinpi `al:160275`
10. Made in Abyss: Mezameru Shinpi Part 2 `al:218721`

## Release order

| # | Entry | Format | Premiere | Premiered during |
|---|---|---|---|---|
| 1 | Made in Abyss `al:97986` | TV | 2017-07-07 |  |
| 2 | Made in Abyss: Journey's Dawn `al:101343` | MOVIE | 2019-01-04 |  |
| 3 | Made in Abyss: Wandering Twilight `al:101344` | MOVIE | 2019-01-18 |  |
| 4 | Made in Abyss: Dawn of the Deep Soul `al:100643` | MOVIE | 2020-01-17 |  |
| 5 | Marulk's Daily Life `al:114744` | SPECIAL | 2020-01-17 |  |
| 6 | Made in Abyss: The Golden City of the Scorching Sun `al:114745` | TV | 2022-07-06 |  |
| 7 | Made in Abyss Retsunichi Koganekyo Just before the final episode retrospective special! ! ~ Six layers of the deep world The secret of the village ~ `tvdb:326109:S00E15` | TVDB_SPECIAL | 2022-09-21 | al:114745 |
| 8 | Made in Abyss: Retsujitsu no Ougonkyou - Papa to Issho `al:153791` | SPECIAL | 2022-10-26 |  |
| 9 | Made in Abyss: Mezameru Shinpi `al:160275` | MOVIE | 2026-10-23 |  |
| 10 | Made in Abyss: Mezameru Shinpi Part 2 `al:218721` | MOVIE |  |  |

## Entries and mapping detail

| Key | Title | Format | Start | Eps | AniDB | TVDB | Method | Target | TMDb check | anibridge agrees | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `al:97986` | Made in Abyss | TV | 2017-07-07 | 13 | 12681 | 326109 S01E01-E13 | scudlee | sonarr |  | True | Kitsu 51146 'Made in Abyss: Mezameru Shinpi Part 2' looks like a duplicate of this entry |
| `tvdb:326109:S00E08` | The Making of MADE IN ABYSS 01 | TVDB_SPECIAL | 2017-10-25 | 1 |  | 326109 S00E08 | tvdb-only | sonarr S00 |  |  |  |
| `tvdb:326109:S00E09` | The Making of MADE IN ABYSS 02 | TVDB_SPECIAL | 2017-10-25 | 1 |  | 326109 S00E09 | tvdb-only | sonarr S00 |  |  |  |
| `tvdb:326109:S00E10` | Music in Abyss | TVDB_SPECIAL | 2017-10-25 | 1 |  | 326109 S00E10 | tvdb-only | sonarr S00 |  |  |  |
| `tvdb:326109:S00E13` | Kevin Penkin Long Interview | TVDB_SPECIAL | 2017-10-25 | 1 |  | 326109 S00E13 | tvdb-only | sonarr S00 |  |  |  |
| `al:101343` | Made in Abyss: Journey's Dawn | MOVIE | 2019-01-04 | 1 | 13941 | 326109 S00E01 | anibridge | both (sonarr S00 + radarr) | 526426 Made in Abyss: Journey's Dawn (2019-01-04) | True | TMDb id from anibridge only; AniDB 13941 ('Gekijouban Soushuuhen Made in Abyss') groups this with al:101344 |
| `al:101344` | Made in Abyss: Wandering Twilight | MOVIE | 2019-01-18 | 1 | 13941 | 326109 S00E02 | anibridge | both (sonarr S00 + radarr) | 526429 Made in Abyss: Wandering Twilight (2019-01-18) | True | TMDb id from anibridge only; AniDB 13941 ('Gekijouban Soushuuhen Made in Abyss') groups this with al:101343 |
| `al:100643` | Made in Abyss: Dawn of the Deep Soul | MOVIE | 2020-01-17 | 1 | 13612 | 326109 S00E03 | scudlee | both (sonarr S00 + radarr) | 573730 Made in Abyss: Dawn of the Deep Soul (2020-01-17) | True |  |
| `al:114744` | Marulk's Daily Life | SPECIAL | 2020-01-17 | 4 |  | 326109 S00E04-E07 | anibridge | sonarr S00 |  | True |  |
| `al:114745` | Made in Abyss: The Golden City of the Scorching Sun | TV | 2022-07-06 | 12 | 15359 | 326109 S02E01-E12 | scudlee | sonarr |  | True |  |
| `tvdb:326109:S00E15` | Made in Abyss Retsunichi Koganekyo Just before the final episode retrospective special! ! ~ Six layers of the deep world The secret of the village ~ | TVDB_SPECIAL | 2022-09-21 | 1 |  | 326109 S00E15 | tvdb-only | sonarr S00 |  |  | ScudLee: AniDB special S2 of al:114745 |
| `al:153791` | Made in Abyss: Retsujitsu no Ougonkyou - Papa to Issho | SPECIAL | 2022-10-26 | 1 |  | 326109 S00E14 | anibridge | sonarr S00 |  | True |  |
| `al:160275` | Made in Abyss: Mezameru Shinpi | MOVIE | 2026-10-23 | 1 | 17824 | 326109 S00E16 | heuristic | both (sonarr S00 + radarr) | 1526917 Made in Abyss: Mezameru Shinpi (2026-10-23) |  | TMDb id from anibridge only |
| `al:218721` | Made in Abyss: Mezameru Shinpi Part 2 | MOVIE |  | 1 |  |  |  | none |  |  |  |

## Related but excluded (crossovers, cameos)

- AniList 143086 Poputepipikku 2 (TV): CHARACTER from 114745 (crossover or cameo)

## Against the community order

- recommended: https://myanimelist.net/stacks/41867 (MAL series guide); release and story order are the same

- **watch_order vs recommended**: 8/8 community entries matched. Missing here: none. Extra here: none. Order differences: 1
  - community: al:114744 before al:100643; here: after
- **story_order vs recommended**: 8/8 community entries matched. Missing here: none. Extra here: none. Order differences: 1
  - community: al:114744 before al:100643; here: after
