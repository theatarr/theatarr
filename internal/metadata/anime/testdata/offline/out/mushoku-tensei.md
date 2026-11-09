# Mushoku Tensei: watch order and arr mapping

Generated 2026-10-25 by `theatarr anime` from open data (AniList seed 108465). See the JSON next to this file for every field.

## TVDB series

| TVDB id | Title | In Sonarr | SkyHook AniList ids | XEM scene != TVDB |
|---|---|---|---|---|
| 371310 | Mushoku Tensei: Jobless Reincarnation | True | 108465, 127720, 146065, 166873, 178789 | 0 of 61 |

## Automatic watch order

Spine = AniList prequel/sequel chain by premiere; other entries go after the last spine entry that premiered before them; TVDB-only specials use TVDB airsBefore/After. *opt* = optional.

| # | Entry | Format | Date | Role | TVDB (Sonarr numbering) | TMDb | Flags | Notes |
|---|---|---|---|---|---|---|---|---|
| 1 | Mushoku Tensei: Jobless Reincarnation `al:108465` | TV | 2021-01-11 | mainline | S01E01-E11 |  |  |  |
| 2 | Mushoku Tensei: Jobless Reincarnation Cour 2 `al:127720` | TV | 2021-10-04 | mainline | S01E12-E23 |  |  |  |
| 3 | Mushoku Tensei: Jobless Reincarnation Cour 2 - Eris the Goblin Slayer `al:141534` | SPECIAL | 2022-03-16 | side-story *opt* | S00E01 |  |  | TVDB places after al:127720 (mid-entry, before S01E17); mapping: anibridge |
| 4 | Mushoku Tensei: Jobless Reincarnation Season 2 `al:146065` | TV | 2023-07-03 | mainline | S00E02, S02E01-E12 |  |  | mapping: anibridge |
| 5 | Mushoku Tensei: Jobless Reincarnation Season 2 Part 2 (up to before S02E19) `al:166873` | TV | 2024-04-08 | mainline | S02E13-E24 |  |  |  |
| 6 | Mushoku Tensei II ~I'll be serious when I go to another world~ [Transfer Labyrinth Edition] Last Minute Special `tvdb:371310:S00E03` | TVDB_SPECIAL | 2024-05-19 | tvdb-special *opt* | S00E03 |  |  | mapping: tvdb-only |
| 7 | Mushoku Tensei II: Jobless Reincarnation II ~I Will Seriously Try If I Go to Another World~ Special Program `tvdb:371310:S00E04` | TVDB_SPECIAL | 2024-05-20 | tvdb-special *opt* | S00E04 |  |  | mapping: tvdb-only |
| 8 | Mushoku Tensei: Jobless Reincarnation Season 2 Part 2 (from S02E19) `al:166873` | TV | 2024-04-08 | mainline | S02E13-E24 |  |  |  |
| 9 | Mushoku Tensei: Jobless Reincarnation Season 3 `al:178789` | TV | 2026-07-04 | mainline | S03E01-E14 |  |  |  |
| 10 | Mushoku Tensei III: Isekai Ittara Honki Dasu Part 2 `al:217434` | TV | 2027 | mainline |  |  | unaired |  |

## Story order (candidate)

Basis: AniList chain (topological) + TVDB airs for single specials. Curated moves applied from `overrides/mushoku-tensei.json`: 0.

1. Mushoku Tensei: Jobless Reincarnation `al:108465`
2. Mushoku Tensei: Jobless Reincarnation Cour 2 (up to before S01E17) `al:127720`
3. Mushoku Tensei: Jobless Reincarnation Cour 2 - Eris the Goblin Slayer `al:141534` *opt* [tvdb-airs]
4. Mushoku Tensei: Jobless Reincarnation Cour 2 (from S01E17) `al:127720`
5. Mushoku Tensei: Jobless Reincarnation Season 2 `al:146065`
6. Mushoku Tensei: Jobless Reincarnation Season 2 Part 2 `al:166873`
7. Mushoku Tensei II ~I'll be serious when I go to another world~ [Transfer Labyrinth Edition] Last Minute Special `tvdb:371310:S00E03` *opt*
8. Mushoku Tensei II: Jobless Reincarnation II ~I Will Seriously Try If I Go to Another World~ Special Program `tvdb:371310:S00E04` *opt*
9. Mushoku Tensei: Jobless Reincarnation Season 3 `al:178789`
10. Mushoku Tensei III: Isekai Ittara Honki Dasu Part 2 `al:217434`

## Release order

| # | Entry | Format | Premiere | Premiered during |
|---|---|---|---|---|
| 1 | Mushoku Tensei: Jobless Reincarnation `al:108465` | TV | 2021-01-11 |  |
| 2 | Mushoku Tensei: Jobless Reincarnation Cour 2 `al:127720` | TV | 2021-10-04 |  |
| 3 | Mushoku Tensei: Jobless Reincarnation Cour 2 - Eris the Goblin Slayer `al:141534` | SPECIAL | 2022-03-16 |  |
| 4 | Mushoku Tensei: Jobless Reincarnation Season 2 `al:146065` | TV | 2023-07-03 |  |
| 5 | Mushoku Tensei: Jobless Reincarnation Season 2 Part 2 `al:166873` | TV | 2024-04-08 |  |
| 6 | Mushoku Tensei II ~I'll be serious when I go to another world~ [Transfer Labyrinth Edition] Last Minute Special `tvdb:371310:S00E03` | TVDB_SPECIAL | 2024-05-19 | al:166873 |
| 7 | Mushoku Tensei II: Jobless Reincarnation II ~I Will Seriously Try If I Go to Another World~ Special Program `tvdb:371310:S00E04` | TVDB_SPECIAL | 2024-05-20 | al:166873 |
| 8 | Mushoku Tensei: Jobless Reincarnation Season 3 `al:178789` | TV | 2026-07-04 |  |
| 9 | Mushoku Tensei III: Isekai Ittara Honki Dasu Part 2 `al:217434` | TV | 2027 |  |

## Entries and mapping detail

| Key | Title | Format | Start | Eps | AniDB | TVDB | Method | Target | TMDb check | anibridge agrees | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `al:108465` | Mushoku Tensei: Jobless Reincarnation | TV | 2021-01-11 | 11 | 14758 | 371310 S01E01-E11 | scudlee | sonarr |  | True |  |
| `al:127720` | Mushoku Tensei: Jobless Reincarnation Cour 2 | TV | 2021-10-04 | 12 | 15954 | 371310 S01E12-E23 | scudlee | sonarr |  | True |  |
| `al:141534` | Mushoku Tensei: Jobless Reincarnation Cour 2 - Eris the Goblin Slayer | SPECIAL | 2022-03-16 | 1 |  | 371310 S00E01 | anibridge | sonarr S00 |  | True |  |
| `al:146065` | Mushoku Tensei: Jobless Reincarnation Season 2 | TV | 2023-07-03 | 13 | 17236 | 371310 S00E02, S02E01-E12 | anibridge | sonarr |  | True | mappings differ: anibridge S00E02, S02E01-E12 (date fit 1.0, title 0.0); scudlee S02E01-E13 (date fit 0.92, title 0.0) -> anibridge; Kitsu 47683 'Mushoku Tensei II: Isekai Ittara Honki Dasu - Shugo Jutsushi Fitz' looks like a duplicate of this entry |
| `al:166873` | Mushoku Tensei: Jobless Reincarnation Season 2 Part 2 | TV | 2024-04-08 | 12 | 18104 | 371310 S02E13-E24 | scudlee | sonarr |  | True |  |
| `tvdb:371310:S00E03` | Mushoku Tensei II ~I'll be serious when I go to another world~ [Transfer Labyrinth Edition] Last Minute Special | TVDB_SPECIAL | 2024-05-19 | 1 |  | 371310 S00E03 | tvdb-only | sonarr S00 |  |  |  |
| `tvdb:371310:S00E04` | Mushoku Tensei II: Jobless Reincarnation II ~I Will Seriously Try If I Go to Another World~ Special Program | TVDB_SPECIAL | 2024-05-20 | 1 |  | 371310 S00E04 | tvdb-only | sonarr S00 |  |  |  |
| `al:178789` | Mushoku Tensei: Jobless Reincarnation Season 3 | TV | 2026-07-04 | 14 | 18727 | 371310 S03E01-E14 | scudlee | sonarr |  | True |  |
| `al:217434` | Mushoku Tensei III: Isekai Ittara Honki Dasu Part 2 | TV | 2027 |  |  |  |  | none |  |  | Kitsu 51093 'Mushoku Tensei III: Isekai Ittara Honki Dasu Part 2' looks like a duplicate of this entry |

## Against the community order

- recommended: https://myanimelist.net/stacks/49710
- chronological: https://en.wikipedia.org/wiki/List_of_Mushoku_Tensei_episodes (Eris special during ep 16)

- **watch_order vs recommended**: 7/7 community entries matched. Missing here: none. Extra here: none. Order differences: 0
- **story_order vs chronological**: 8/8 community entries matched. Missing here: none. Extra here: none. Order differences: 0
