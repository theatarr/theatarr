# The anime metadata engine (`internal/metadata/anime`)

The anime engine builds franchises from AniList, AniDB, Kitsu and the community mapping lists, maps each entry onto
TVDB and TMDb, and derives release, watch and story orders. Decisions and imports use those mappings, and Plex gets
watch-order playlists. Where it sits: [overview.md](overview.md); TVDB itself: [tvdb.md](tvdb.md). Its outputs match a set
of golden outputs exactly ([Golden outputs](#golden-outputs)).

Code: `internal/metadata/anime` (pipeline), `.../offlinecache` (an offline cache of the source responses), `.../live`
(sources through the gateway), `.../animestore` (database, review queue, the Sonarr-episode TVDB backend),
`.../animemap` (mappings for decisions and imports), `.../playlists` (Plex watch-order playlists), migrations
`00017_anime.sql` and `00018_anime2.sql`, `cmd/theatarr/anime.go`, `cmd/theatarr/animeplaylists.go`,
`examples/policy/overrides/`, `examples/policy/playlists.yaml`.

## What it does

`anime.Build` takes a franchise (slug, name, AniList seed, optional `scope: stem`) and returns the result:
entries with their TVDB and TMDb mappings, the TVDB series, excluded neighbours, unclaimed regular episodes, and the
release, watch and story orders. Each stage is a function; its output goes into `Result.Stages` and the database.

| # | Stage | Where |
|---|---|---|
| 1 | Typed AniList crawl: line, branch, side, leaf; series-stem check; `scope: stem` | `crawl.go` |
| 2 | Id joins: AniList → AniDB (Fribb, else manami), Kitsu (manami) | `mapper.go` |
| 3 | Candidate mappings: ScudLee (season, offset, mapping lists, `a` absolute; applied as HAMA does) and anibridge | `mapper.go`, `lists.go` |
| 4 | Arbitration: the score and keep/reject rules; status; the anchor signal; mappings to missing TVDB episodes dropped | `arbitrate.go` |
| 5 | Secondary entries (ScudLee by TVDB id, manami two-way links, Kitsu), heuristic match, fix-ups, TVDB-only specials | `secondary.go`, `flags.go`, `merge.go`, `tmdb.go` |
| 6 | Flags (recap, alternate, extra, short, unaired, ...) and target (sonarr, sonarr S00, radarr, both) | `flags.go` |
| 7 | Release order; automatic watch order (spine, alternates and route blocks, TVDB airs, air-date interleave) | `orders.go` |
| 8 | Story order: story-mode order plus the overrides' moves (slices, mid-season splits, `start`) | `story.go` |

### Arbitration and the review queue

The arbitration's decision is kept as it is (that is what the golden outputs check). On top, every mapped entry gets a status:

| Status | When | Mapping used to import |
|---|---|---|
| `auto` | the two lists agree, or one list maps it and the TVDB air dates fit (or there are no dates to check) | yes |
| `arbitrated` | the lists disagree and the winner leads by ≥ 0.5 (score range 0–5) or its placement matches TVDB airsBefore; or one list kept although the dates don't fit (the decision's "lists agree / title matches / direct AniDB join") | yes |
| `conflict` | the lists disagree within 0.5, or the winner's placement contradicts TVDB airsBefore | **no** (Sonarr's numbering), review item |
| `heuristic` | no list maps it; matched by air date and title | yes, review item |
| `override` | set by an overrides file, or accepted in review | yes |
| `rejected` | every candidate dropped (TVDB lacks the episodes, dates don't fit, no date), or rejected in review | no (Sonarr's numbering) |

**Anchor signal**: ScudLee's `<before>;1-13;</before>` says AniDB special 1 goes before episode 13. The special
is found through the record's season-0 mapping list (or as the candidate's n-th episode for a record whose default
TVDB season is 0) and compared with TVDB's `airedBeforeEpisodeNumber` for that special. In the twelve franchises it
never fires: only Bakemonogatari has a `<before>`, and it maps its specials to regular episodes, which have no airs
fields. Unit tests cover match and mismatch.

Across the twelve franchises: 183 auto, 23 arbitrated, 7 conflicts, 18 heuristic, 3 rejected, 40 entries without a
mapping (Radarr-only films, unaired entries). The 7 conflicts: Hanamonogatari, Tsukimonogatari, Owarimonogatari II
and Zoku Owarimonogatari (lossy ScudLee lists), Mushoku Tensei S2 (episode 0), DBZ Kai: The Final Chapters
(international vs Japanese numbering) and Dragon Ball Super: Beerus (2026 re-edit, one vs two episodes). Most are
known hard cases; the arbitration's pick (anibridge each time) wins, but by less than 0.5 (mostly through
coverage), so a person confirms each once.

**Review queue** (`anime_review`, `theatarr anime review`): `conflict` and `heuristic` items per entry; `refresh_shrink`
and `refresh_moves_linked` items when a refresh is held. A decision sticks: a later build proposing the same mapping
takes the accepted one as `override` (or keeps a rejected one out) instead of asking again. Items a new build does
not raise again are `superseded`.

**Refresh hold**: `Save` holds a build, keeping the applied one, when it would map more than 10% fewer of
a TVDB series' episodes, or would move a mapping whose episodes have files (an active `link_episode`, or an imported
`version_file_episode`). The held build is the franchise's `pending_result`; accepting its item applies it, rejecting
drops it.

### Overrides

One file per franchise in the policy's `metadata.overrides_dir` (`<slug>.yaml`; JSON loads too). Format in
`examples/policy/overrides/README.yaml`; five override files are there in YAML, with `franchises.yaml` (the
twelve franchises) and `fate-stay-night.yaml` (routes). All of it is applied (`overrides.go`, `story.go`,
`routes.go`):

| Key | Effect |
|---|---|
| `moves` | story order: entries or slices moved after a key, a mid-season point, a slice, or `start` |
| `mappings` | pins an entry's TVDB range: status `override` |
| `exclude` | out of every order |
| `routes` + `line_position` | route order (Fate). A route key is an alternate's root (the block is the alternate and every row the watch order routed to it: its specials, recaps, sequels) or a main-order key (the block runs from that row to the next route key). The blocks become one sequence in the watch and story orders (role `route`, not optional); the rest of the line goes `first` (default) or `last`. Unlisted alternates stay alternates; the release order keeps its dates and marks each row's `route`. Unknown keys are listed in `story_order.routes.failed` |
| `alt_cuts` | `{name, tvdb, entry?, match?, map: {DC1: S01E01, DC2: [S01E02, S01E03]}}`: a cut no source numbers. Keys end in the cut's episode number; values are TVDB episodes of `tvdb`. Stored in `anime_alt_cut`; a release whose title matches `match` (a case-insensitive regex) or every word of `name` maps its numbers through the table (decisions, below). The cut's own `entry` is marked in the orders with the cut's TVDB range |

Validation: keys, slices, ranges, routes listed twice, `line_position` without `routes`, alt cuts without `tvdb`, map
keys without a number, values that are not `SxxEyy`, a bad `match` regex, two cuts with one name.

With `fate-stay-night.yaml` (`routes: [al:356, al:19603, al:20791]`, `line_position: last`) the watch order is the
2006 series with its specials and the TV Reproduction recap, Unlimited Blade Works (with the El-Melloi and strange
Fake spin-offs that air inside it), Heaven's Feel I-III, then Fate/Zero and its side stories; the UBW 2010 film and
the sunny day special stay alternates.

### Import gating (`internal/decide`, `internal/library`, `.../animemap`)

`animemap.Load` reads every franchise mapping into a TVDB series (`anime_mapping`, entry names from `anime_entry`,
`anime_alt_cut`). Importable: `auto`, `arbitrated`, `override`, `heuristic` (a heuristic rejected in review becomes
`rejected`). A mapping that is not importable (`conflict`, `rejected`) is never used, and nothing waits for it: its
episodes import through Sonarr's numbering, exactly like an episode no entry maps (so specials land in Season 00 the
way Sonarr puts them). The best-scoring candidate of a conflict is never imported under. The review item is still
created, and Admin › Reviews shows it; playlists leave such an entry out ("missing") until it is reviewed.

**Decisions** (`decide/anime.go`; `Title.AnimeMap`, loaded by `LoadSeriesVersion` for any series with mappings):
1. An alternate cut the release title names: its numbers map through the cut's table (`anime_mapping: alt_cut:<name>`);
   a number the table lacks is `UnknownEpisode`.
2. A franchise entry whose name (Sonarr's clean title) is the release's series title, with absolute numbers: the
   entry's own episode n is the n-th TVDB episode of its range outside season 0 (`franchise`). A franchise name or an
   alternate cut also makes the series match when Sonarr's own lookup does not (`MatchAlias`). An entry that cannot
   hold every number, or two entries that both can, leave it to Sonarr; so does an entry whose mapping is not
   importable.
3. Otherwise Sonarr's mapping (`sonarr`).

SeaDex season packs carry their AniList entry id. When the franchise maps that entry into only part of a TVDB
season (one cour), the decision covers only those regular episodes, keeping the entry mapping for import. A pack
for one cour cannot take the rest of the season's episode slots (`TestSeaDexEntryCoversOnlyItsCour`). An entry whose
mapping (or a split part's) is not importable does not narrow the pack: Sonarr's numbering takes the season
(`TestSeaDexUnreviewedEntryUsesSonarrNumbering`).

Then every episode only non-importable ranges cover, and every episode of a release that named a non-importable
entry (step 2 or a SeaDex pack), is listed in the decision's `anime_fallback` with the entry and its status
(`TestAnimeFranchiseMapping`, `TestAnimeUnreviewedEntryUsesSonarrNumbering`). The decision JSON gains
`anime_mapping`, `anime_entry`, `anime_fallback`. Nothing is rejected for it.

**Library**: files resolve through the same rules (`decide.ResolveFile`); a decision's stored entry that is no longer
importable at import time falls back to Sonarr's numbering too. Nothing is held (`TestAnimeUnreviewedImports`).

**A mapping accepted later** changes `anime_mapping` only: nothing relinks a file that imported under Sonarr's
numbering; it stays where it landed. Later decisions and imports (an upgrade, a new grab) use the accepted mapping.

### Review screen and API (`internal/web/anime.go`)

- **`/admin/reviews/anime` (Anime mappings):** open items (filter by state and franchise) with the proposed mapping and every
  candidate: source, TVDB range, exists, date fit, cover, title similarity, score, anchor and why it was dropped.
  Buttons: accept the proposed mapping, **accept another candidate** (the entry takes that range, status
  `override`; the choice sticks across rebuilds while the same mapping is proposed: `anime_review.choice`), or reject.
  Each item shows the **override snippet** (a `mappings:` entry per candidate) to paste into `overrides/<slug>.yaml`.
  Below, the franchises with mapping counts by status.
- **`/admin/reviews/anime/{slug}`:** open items, the **order panel** (release, watch, story; each item with its source: release
  date, TVDB airs, the AniList chain, or `override` for moves, routes, alternate cuts and mappings decided in
  review; a series split by an interleaved special shows the part to stop at and to resume from, and each item
  its TVDB and TMDb ids), the mappings, the **numbering view** (each mapped episode: TVDB, absolute as the imported Sonarr has it, the
  AniList entry and the entry's own number, season/episode, mapping status, import eligibility and source).
  Alternate orders include their sources; alternate cuts show episode-to-TVDB mappings. Playlist management
  remains on Settings › Plex & playback, with playlist settings and the Playlists Theatarr keeps list.
- **Title page:** the watch-order panel links admins to franchise details for orders, mappings and episode numbering.
- **API** (`openapi.json`): `GET /anime/franchises`, `GET /anime/franchises/{slug}`, `GET /anime/review`,
  `GET /anime/review/{id}/snippet`, `POST /anime/review/{id}/accept` (`{"candidate": n, "note": ...}`),
  `POST /anime/review/{id}/reject`, `GET /anime/playlists`, and the build endpoints under "Automatic builds".
  Session auth with CSRF for session POSTs; API keys as for the rest. Accept, reject and the build requests are the
  only writes. `theatarr anime review` takes `-candidate N` (with
  `-accept`) and `-snippet ID`.

### Watch-order playlists in Plex (`.../playlists`, `internal/plex/playlists.go`)

One admin-owned regular video playlist per franchise and order kind (`playlists.orders`, default watch and story),
named `{franchise} ({order})` ("Re:Zero - Starting Life in Another World (story order)"), plus one per alternate route
left in the watch order (a root with routed rows; `playlists.routes`).

- **Items:** each order row's TVDB episodes come from the applied mapping (importable ranges only; a conflict row is
  listed as missing), narrowed to the row's part (`anime.RowEpisodes`: "up to before S01E13", story slices); an entry
  without episodes but with TMDb ids is a movie. Each item is the file Plex sees: Theatarr's active link, else the arr's file
  (`version_file`) while the arr owns the version. `playlists.skip_optional` leaves out recaps and side stories.
- **Sync** (gate open): the server's machine id, one listing per section of `library.versions[*].plex_section` and
  type (episodes, movies) to find rating keys by file path, then `plex.SyncPlaylist` per playlist: find it by the
  stored rating key or by title, create it, or remove unwanted entries and duplicates, append missing ones and move
  entries into order. A playlist already in order gets no write. Nothing deletes a playlist or a library item.
- **Gate:** Plex is written only when `library.enabled` **and** `playlists.enabled` are true (both default false)
  and `library.plex`'s token is set. Otherwise, or with `-dry-run`, a sync plans from the database alone, records the
  plan in `plex_playlist` (mode `dry_run`, with the reason) and makes **no Plex call at all**, not even a read.
  `TestGated` checks each closed combination with a fake Plex that records every call (none), and `TestPlanAndSync`
  that an open gate creates the playlists and a second sync writes nothing.
- **Command:** `theatarr anime playlists [-franchise SLUG] [-dry-run] [-items] [-json] -db ... -env-file ...`.
  `theatarr serve` runs the same sync, behind the same gates.

Policy (`examples/policy/playlists.yaml`, validated in `internal/policy/playlists.go`):

```yaml
playlists:
  enabled: false
  orders: [watch, story]   # release, watch, story
  routes: true
  skip_optional: false
  name: "{franchise} ({order})"
  interval: 6h             # for theatarr serve, minimum 1h
```

## Automatic builds (`.../anime/autobuild`)

`theatarr serve` builds franchises on its own while `metadata.anime.auto_build` is on (default true) and the policy
has the anime sources (`anilist`, `kitsu`, `thexem`, `anime-lists`, `fribb`, `anibridge`, `manami`; the gateway
refuses any other target). Building only writes mappings; episodes whose mapping is not settled import through
Sonarr's numbering, and their conflicts show in Admin › Reviews. With a source missing, the "Anime builds" health
check says which, and nothing builds; manual builds are refused the same way.

**Discovery.** Every anime series (`title.anime`, kind series, with a TVDB id) that no franchise maps into gets a
discovery pass, one at a time:

1. Covered when a franchise has a mapping into the TVDB series, or an entry for one of its AniList ids (a franchise
   whose mapping was rejected still covers it).
2. The series' AniList ids: ScudLee's records for the TVDB id, through Fribb (else manami) from AniDB to AniList.
   None is `no_mapping`.
3. A franchise in the shipped list (`<overrides_dir>/franchises.yaml`) seeded by one of them is built under its own
   slug and scope (origin `list`); covered if it is stored already.
4. Otherwise an ad-hoc franchise (origin `auto`). The seed is the record ScudLee puts at TVDB season 1 (or absolute)
   with no episode offset, lowest AniDB id first; else the earliest AniList start date (ties: lowest id). The crawl
   walks prequels and sequels from any seed, so a later season still yields the whole line. The slug comes from the
   series name (`Re:Zero - Starting Life in Another World` → `re-zero-starting-life-in-another-world`), with
   `-<tvdb id>` appended when another franchise has it, or `tvdb-<id>` for a name with no ASCII letters.

The outcome per title is kept in `anime_discovery` (`queued`, `built`, `covered`, `no_mapping`, `failed`, with the
franchise and the error). A title is tried again after `refresh_interval` (an hour when its job was lost while
`queued`), so a series the lists learn later is picked up.

**Refresh.** A franchise built more than `metadata.anime.refresh_interval` ago (default 168h, minimum 24h) is rebuilt;
one at most every interval / franchises, the stalest first, so the rebuilds spread over the interval. A franchise
with a refresh held for review (`pending_result`) waits for the review. The origin (`list`, `auto`, `manual`) and the
TVDB id are kept in the franchise's config, so a rebuild uses the same seed and scope; a shipped franchise takes its
current list entry.

**The job.** Builds run as `anime_build` jobs (dedupe by TVDB id, slug or seed; two attempts), one at a time in the
process: a second job that finds a build running is put back a minute later without counting an attempt. The loop
(every minute, first after two minutes) queues only when no `anime_build` job is queued or running. A job builds
through the same live sources as the CLI and saves like `-save`: a rebuild that drops episodes or moves linked
mappings is held for review. The lists (about 90 MB) are parsed once per process, kept a day, and released after
30 idle minutes.

**Screen and API** (`internal/web/animebuild.go`, admin only). Admin › Reviews › Anime mappings shows each franchise's
last build, its next refresh (built_at + interval while automatic builds are on and no refresh waits for review), the
latest build job while it is queued or running (or its error when it failed) and a **Rebuild** button; the franchise
page has the same. Below, **Anime series without a franchise** lists the discovery candidates with the last outcome,
its detail and time, and a **Build** button, with a note when builds cannot run (the missing source) or
`auto_build` is off (manual builds still work). The lists poll every 5 s while a job is queued or running.

- `POST /anime/franchises/{slug}/build`: queue a rebuild; 202 `{"job_id", "slug"}` (the same job while it is queued).
- `POST /anime/build`: `{"tvdb_id": n}` queues a discovery for an anime series (404 when it is not one);
  `{"anilist_id": n, "slug": "...", "name": "..."}` builds from an AniList seed (origin `manual`; slug from the name
  when empty; 409 when another seed has the slug). Both ids, neither, or a slug/name with `tvdb_id` is a 400.
- `GET /anime/uncovered`: `{"builds": {ready, off, auto_build, refresh_interval}, "items": [...]}`.
- A missing source (or no running builder) answers 503 and queues nothing. `GET /anime/franchises` adds
  `next_refresh` and `job` to each franchise.

## Golden outputs

The golden outputs (three franchises in `internal/metadata/anime/testdata/offline`) were produced by a Python
implementation of the same pipeline. Each build matches its `out/<slug>.json` (every field: entries, notes,
mappings, TMDb checks, excluded neighbours, gaps, the three orders, the community comparison) and `out/<slug>.md` (byte for byte).
The comparison treats a missing key and `null` alike and ignores Theatarr's additions (`mapping_decision`, `stages`).

| Franchise | Entries | Release / watch / story rows | Moves | auto | arbitrated | conflict | heuristic | rejected | JSON | Markdown |
|---|---|---|---|---|---|---|---|---|---|---|
| Made in Abyss | 14 | 10 / 11 / 10 | 0 | 12 | 0 | 0 | 1 | 0 | same | same |
| Re:Zero | 16 | 14 / 14 / 14 | 1 | 14 | 0 | 0 | 0 | 0 | same | same |
| Tensei Slime | 18 | 18 / 19 / 19 | 0 | 9 | 0 | 0 | 5 | 0 | same | same |
| Demon Slayer | 24 | 24 / 24 / 23 | 0 | 16 | 0 | 0 | 2 | 1 | same | same |
| Monogatari | 23 | 22 / 23 / 32 | 9 | 12 | 0 | 4 | 1 | 0 | same | same |
| Fate (Stay Night) | 24 | 24 / 24 / 15 | 0 | 18 | 1 | 0 | 0 | 1 | same | same |
| Attack on Titan | 51 | 48 / 50 / 49 | 1 | 41 | 0 | 0 | 2 | 0 | same | same |
| Jujutsu Kaisen | 12 | 12 / 15 / 15 | 1 | 8 | 0 | 0 | 2 | 1 | same | same |
| Mushoku Tensei | 9 | 9 / 10 / 10 | 0 | 7 | 0 | 1 | 0 | 0 | same | same |
| Dragon Ball Z | 42 | 42 / 60 / 45 | 0 | 18 | 19 | 2 | 2 | 0 | same | same |
| Haikyuu!! | 18 | 18 / 19 / 19 | 0 | 14 | 2 | 0 | 0 | 0 | same | same |
| Sword Art Online | 23 | 23 / 25 / 25 | 2 | 14 | 1 | 0 | 3 | 0 | same | same |

`TestGoldenFixture` runs Re:Zero, Mushoku Tensei and Made in Abyss on every `go test` from an offline cache
(`internal/metadata/anime/testdata/offline`, 690 KB: only the responses and list records these builds read, only the
fields used; sources and licences in its README) and compares with their golden outputs.

What it took to be exact, rather than close:
- **CPython set order.** The crawl and the manami walk iterate Python sets of ints; the order decides which path
  reaches a node first (its kind, direction and "found via"). `pyset.go` emulates CPython 3.10's set table (linear
  probing, perturbation, resizing) and is checked against orders recorded from CPython.
- **Python's `\b`** is Unicode-aware; titles include Japanese. `pyre.go` checks boundaries Python's way.
- `date.fromisoformat`'s strictness, `sort_date` padding, `round(x, 2)`, `repr()` of floats, lists, tuples and
  strings inside notes, `urllib.parse.quote` in the Radarr search keys, `list.index`/`remove` by equality in the story
  order, shallow `dict(e, ...)` copies on split.

**Deviations from the Python behaviour** (also in the package doc):
- `topo`: the golden outputs walk successors as a Python set of strings (order varies with `PYTHONHASHSEED`); the
  engine walks them in edge order, which is the same on every run. Ready nodes are re-sorted by date before each
  pick, so only ties could differ; none do in the twelve example franchises.
- `mergeDuplicates`: read whole, Radarr's `inCinemas` (`2019-01-04T00:00:00Z`) never parses, so "TMDb picked from
  grouped AniDB ids" would never run. The engine uses the date part, as `checkTMDb` does. No output of the twelve
  example franchises changes.

## Sources and caching

| Source | Offline (`offlinecache`) | Live (`live`, through the gateway) | Fresh for | Pace |
|---|---|---|---|---|
| AniList GraphQL | `cache/anilist` by the request body | POST, batched 50 ids, per franchise on demand (no mirroring) | a day if any media in the batch is airing or announced, else 30 days | 2.1 s (30/min) |
| Kitsu | `cache/kitsu` | GET, the same request | 7 days | 1/s |
| TVDB | `cache/skyhook` (SkyHook, TVDB-shaped) | the TVDB client ([tvdb.md](tvdb.md)), else **an imported Sonarr** (`animestore.SonarrTVDB`); `-tvdb-cache` adds a SkyHook-shaped cache behind it | — | — |
| ScudLee, Fribb, anibridge, manami | `cache/static` | GET daily, conditional (ETag/Last-Modified) | 1 day | — |
| TheXEM | `cache/xem` | GET | 7 days | 1/s |
| AniDB titles dump | — | Reserved fetch/cache policy; no active titles-dump caller | 1 day | — |
| SeaDex | — | `live.SeaDex` per AniList id | 1 day | 2 s |
| Movie lookup | `cache/arr` (Radarr's lookup) | the TMDb client: by id, and `search/movie` for entries without an id (`tmdb.SearchMovie`, adult titles left out) | movies per the client's TTLs; searches a week | TMDb client's |
| Sonarr library | `cache/arr` | the `arr_alias` episodes of any imported Sonarr (`-sonarr-instance` limits it to one) | — | — |

Responses go into `metadata_cache` (gzip, keyed by method + URL (+ body hash for POSTs), with `etag` and
`last_modified` from 00017). A stale entry is revalidated with `If-None-Match`/`If-Modified-Since`; a 304 extends it.
404s are cached for a day. **Breaker per source:** three consecutive failures skip the source for 5 minutes,
doubling to an hour; meanwhile stale cache is served, and with nothing cached the call fails (`ErrSourceDown`).
AniList GraphQL errors in a 200 are not cached. The gateway's own budgets and back-offs apply on top.

**TVDB from an imported Sonarr**, when no TVDB key is set or TVDB fails: only series some imported Sonarr has are known (a mapping to
another series is dropped as "TVDB does not have"); Sonarr's API has no airsBefore/After, so TVDB-only specials are
placed by date; no overviews, so the overview recap hint is silent. The TVDB v4 client sits behind the same `TVDB`
interface; `TVDBChain` combines backends ([tvdb.md](tvdb.md)).

## Storage (migration 00017)

| Table | Holds |
|---|---|
| `anime_franchise` | slug, seed, scope, the applied build (gzip JSON), a held `pending_result` and its source |
| `anime_stage` | each stage's output of the applied build (crawl, joins, mappings, flags, orders) |
| `anime_node` | the franchise graph: every AniList work met, as line/branch/side/leaf or excluded (with why) |
| `anime_entry` | entries with ids, flags, target, TMDb ids and the entry JSON |
| `anime_mapping` | per entry: status, winner, TVDB series and episodes, reason, candidates with scores |
| `anime_order` | release, watch and story rows (alternates after the main order) |
| `anime_review` | the review queue (one open item per franchise, entry and kind) |
| `metadata_cache` | + `etag`, `last_modified` |

Mappings used to import: `anime_mapping.status IN ('auto', 'arbitrated', 'override', 'heuristic')` (see "Import
gating").

## Storage (migration 00018)

| Table | Holds |
|---|---|
| `anime_alt_cut` | an overrides file's alternate cuts: name, TVDB series, entry, match, the number → episodes table |
| `anime_review.choice` | the candidate accepted instead of the proposed mapping |
| `plex_playlist` | per franchise, order and route: name, Plex rating key, items synced, the last plan, mode (`dry_run`, `applied`, `failed`) and why |

## Storage (migration 00110)

| Table | Holds |
|---|---|
| `anime_discovery` | per anime series (title): its TVDB id, the last discovery outcome, the franchise built or covering it, the error, when |

## Commands

```sh
# offline, from an offline cache: Markdown like out/<slug>.md plus a "Mapping decisions" section
theatarr anime build -franchise re-zero -cache <dir>/cache [-json] [-out DIR]
# store it (held for review if it drops episodes or moves linked mappings)
theatarr anime build -franchise re-zero -cache ... -db /data/theatarr.db -save
# live
theatarr anime build -franchise re-zero -env-file .../theatarr.env -db /data/theatarr.db -save
# ad hoc
theatarr anime build -franchise one-piece -seed 21 -db ...
# live builds use the same sources as theatarr serve; the "in Sonarr" check covers every imported Sonarr
theatarr anime build -franchise re-zero -sonarr-instance sonarr_anime ...
# the queue
theatarr anime review [-franchise re-zero] [-state open|accepted|rejected|superseded|all] [-json]
theatarr anime review -accept 12 -note "anibridge is right"      # or -reject 12
theatarr anime review -accept 12 -candidate 1                     # accept another candidate
theatarr anime review -snippet 12                                 # the overrides YAML that pins it
# playlists (a dry run unless library.enabled and playlists.enabled)
theatarr anime playlists -db ... [-franchise re-zero] [-dry-run] [-items]
```

The franchise list is `-franchises FILE`, else `<cache>/../franchises.json`, else
`<overrides_dir>/franchises.yaml` (the stored `metadata.overrides_dir`; a relative one resolves under the policy
directory, `THEATARR_POLICY`, default `/config/policy`). Overrides: `-overrides DIR`, else `<cache>/../overrides`, else
the stored `overrides_dir`. Commands read their settings from the `-db` database; they take no `-policy`.

## Live sources

What a live build needs:

1. **Policy** (`metadata.yaml` and `budgets.yaml`; the examples carry these):

   ```yaml
   metadata:
     sources:
       anilist:        {url: https://graphql.anilist.co, budget: anilist}
       kitsu:          {url: https://kitsu.app/api/edge, budget: kitsu}
       thexem:         {url: https://thexem.info, budget: thexem}
       anime-lists:    {url: https://raw.githubusercontent.com/Anime-Lists/anime-lists/master/anime-list-master.xml}
       fribb:          {url: https://raw.githubusercontent.com/Fribb/anime-lists/master/anime-list-full.json}
       anibridge:      {url: https://github.com/anibridge/anibridge-mappings/releases/download/v3/mappings.json}
       manami:         {url: https://github.com/manami-project/anime-offline-database/releases/latest/download/anime-offline-database-minified.json}
       github-assets:  {url: https://release-assets.githubusercontent.com}   # GitHub release downloads redirect here
       github-objects: {url: https://objects.githubusercontent.com}          # ... or here
       anidb:          {url: https://anidb.net/api/anime-titles.xml.gz}
       seadex:         {url: https://releases.moe, budget: seadex}
   budgets:
     anilist: {requests: 30, per: 1m}
     kitsu:   {requests: 60, per: 1m}
     thexem:  {requests: 60, per: 1m}
   ```

   The lists' URLs are taken from the policy; AniList's endpoint too. manami's database is archived; its last
   release still downloads, and a continuation URL can replace it here.
2. A recent `theatarr import-arr` of the anime Sonarr: its episodes are the fallback TVDB backend and the
   "in Sonarr" check.
3. Optionally the TMDb key (`metadata.sources.tmdb`) for the TMDb checks.
4. Run, for example, `theatarr anime build -franchise re-zero -env-file ... -db ... -out <dir> -save`.
   The first run downloads about 90 MB of lists (cached a day); each franchise then takes AniList batches 2.1 s
   apart (5–15 requests) and Kitsu at 1/s.

## Limits

- Tie-break hints in the overrides and route playlists for the story order's alternates are not supported.
- Anime versions are not searched by scene numbering or per episode (indexer adapters ask TVDB seasons).
- There are no same-name Plex collections in custom order.
