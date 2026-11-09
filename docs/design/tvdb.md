# The TVDB v4 client (`internal/metadata/tvdb`)

The TVDB v4 source: episodes, airsBefore/After, absolute and alternate orders, `/updates` driving refresh, caching,
and what happens when TVDB is down. With a key configured it comes before the imported-Sonarr episode source
described in [anime.md](anime.md) ("TVDB from an imported Sonarr").

Code: `internal/metadata/tvdb` (client, `Show` for the anime engine, `/updates` refresh),
`.../tvdbdecide` (episodes for the series decision side), migration `00019_tvdb.sql`, `cmd/theatarr/tvdb.go`, the wiring
in `cmd/theatarr/anime.go` (`liveSources`), `examples/policy/metadata.yaml` and `budgets.yaml` (`tvdb`).

The record shapes and the test fixtures follow TVDB's v4 OpenAPI (<https://thetvdb.github.io/v4-api/>).

## Auth

`POST /v4/login` with `{"apikey": ...}`, plus `"pin"` when `THEATARR_TVDB_PIN` is set (user-supported keys only). The
bearer token stays in memory for 25 days (TVDB's last a month) and is renewed on a 401, once per request; each `theatarr`
run logs in once. Key, PIN and token are never logged, put in an error, part of a cache key, or stored.

## Endpoints

| Call | Endpoint | Fresh for |
|---|---|---|
| `Series` | `GET /series/{id}/extended?meta=translations&short=true` (aliases, translations, seasons and season types, remote ids; no artworks or characters) | a day while Continuing/Upcoming, a month once Ended |
| `Episodes` | `GET /series/{id}/episodes/{season-type}[/{lang}]?page=N`, every page (`links.next`) | same, from the listing's series status |
| `Episode` | `GET /episodes/{id}/extended` (the airs fields) | the series' TTL |
| `Season`, `SeasonTypes` | `GET /seasons/{id}/extended`, `GET /seasons/types` | a day, a month |
| `Search`, `SearchRemoteID` | `GET /search?query=&type=`, `GET /search/remoteid/{imdb or tmdb id}` | a week |
| `Refresh` | `GET /updates?since=&type=series\|episodes\|translatedseries\|translatedepisodes&page=N` | not kept |

Season types for `Episodes`: `default` (the series' default order), `official`, `dvd`, `absolute`, `alternate`,
`regional`, and others TVDB lists in `/seasons/types`.

## Caching and TVDB down

Through the anime engine's `live.Fetcher` with its own policy (source `tvdb`, 200 ms between requests, the
gateway's `tvdb` budget on top): answers in `metadata_cache` (gzip, keyed by URL), revalidated with
`If-None-Match`/`If-Modified-Since` when TVDB sent validators, 404s cached a day, a breaker that skips TVDB after three
failures (5 minutes, doubling to an hour), stale answers served meanwhile. With TVDB down and nothing cached a call
fails with `ErrSourceDown`.

**`/updates` drives refresh.** `Refresh` (`theatarr tvdb updates`) asks `/updates` for the window since the last run
(watermark in `tvdb_sync`; the first run only sets it; at most 7 days back, older gaps are left to the TTLs and the
result says `truncated`), and marks the cached answers of every changed series (`/series/{id}/...`) and episode
(`/episodes/{id}/...`) stale. They are revalidated on next use; series never cached cost nothing. A failed run
keeps the watermark. Up to 50 pages per entity type per run; an hourly run reads a page or two.

## Anime engine

`(*tvdb.Client).Show` implements `anime.TVDB` and returns the series in SkyHook's shape:

- the title in English when TVDB has a translation (else TVDB's own name);
- the default order's episodes, with names and overviews from the English listing when the series is not English
  (an empty translated name keeps TVDB's own);
- `absoluteEpisodeNumber` from the absolute order when the series has one, else the episode's `absoluteNumber`;
- `airedBeforeSeasonNumber`/`airedBeforeEpisodeNumber`/`airedAfterSeasonNumber` from the listing, and for each
  special without them from its extended record (one cached request per special; a failure leaves them empty);
- no `aniListIds` (TVDB has none; the Markdown's "SkyHook AniList ids" column stays empty with TVDB).

A cold series costs 2 + pages + specials requests (Re:Zero-sized: about 5-15); afterwards a day or a month of cache.

**Wiring** (`liveSources` in `cmd/theatarr/anime.go`): when the policy has `metadata.sources.tvdb` and its key is set,
`src.TVDB = anime.TVDBChain{tvdb, SonarrTVDB}`: TVDB first, the imported Sonarr episodes when TVDB fails with nothing
cached or does not have the series. Without a key the build prints so and uses the Sonarr backend alone. `-tvdb-cache`
appends the SkyHook-shaped cache. The offline path (`-cache`, `offlinecache.SkyHook`) reads a recorded cache;
`TestGoldenFixture` checks it (JSON and Markdown).

What the TVDB client adds over the Sonarr backend: series no imported Sonarr has (mappings to them are kept instead of
dropped as "TVDB does not have"), airs fields (TVDB-only specials placed by airsBefore/After instead of date; the anchor signal can fire), and
overviews (the recap hint).

## Series decision side (`tvdbdecide`)

Not called by the decision path, which reads the episodes the series refresh stores ([tv-metadata.md](tv-metadata.md)):

- `tvdbdecide.Episodes(ctx, src, tvdbID)` returns `[]decide.Episode` (TVDB season/number, absolute, title, air date,
  runtime; no database ids).
- `tvdbdecide.Fill(ctx, client, *decide.Title)` gives a series title without episodes (no Sonarr imported it) TVDB's
  episodes, each with the title's `Monitored`, and fills an empty name, year, runtime and aliases (TVDB aliases and
  name translations). A title with episodes is not touched. `Fill` is kept in a test file
  (`tvdbdecide_exports_test.go`).

## Commands

```sh
# all take: -db /data/theatarr.db [-policy DIR] [-env-file FILE] [-json]
theatarr tvdb series 305089                       # extended record (JSON)
theatarr tvdb episodes 305089                     # default order, table: id, SxxEyy, abs, aired, airs, name
theatarr tvdb episodes 305089 -type absolute      # any order; -lang eng for English names
theatarr tvdb show 305089                         # what the anime engine gets (SkyHook shape)
theatarr tvdb search "re zero" -type series
theatarr tvdb remote <tt-id or TMDb id>           # outside id -> TVDB record
theatarr tvdb updates                             # refresh from /updates (first run sets the watermark)
```

Each prints a summary line on stderr: requests, cached, stale, logins.

## Setting it up

1. Policy: the `tvdb` source and budget as in `examples/policy/metadata.yaml` and `budgets.yaml`
   (`https://api4.thetvdb.com/v4`, `api_key_env: THEATARR_TVDB_KEY`, `tvdb: {requests: 120, per: 1m, burst: 10}`);
   `THEATARR_TVDB_KEY` in the environment (and `THEATARR_TVDB_PIN` only for a user-supported key: a login refusal
   says so).
2. `theatarr migrate` (00019) or any command (they migrate).
3. `theatarr tvdb series 305089` then `theatarr tvdb episodes 305089 -type absolute` and `theatarr tvdb show 305089`:
   English title, episodes, absolute numbers, airs fields on specials. Run again: 0 requests.
4. `theatarr tvdb updates` twice (the first sets the watermark; an hour later the second reads a page or two).
   `theatarr serve` runs the same refresh every hour.

## Limits

- The indexer adapters get no episode lookups in other numberings from TVDB (scene numbering comes from TheXEM).

## Series refresh

`SeriesEpisodes` (the default order with English names and absolute numbers, as `Show` reads them) feeds the series
metadata refresh, `Invalidate` drops one series' cached answers for a forced refresh, and `Refresh` returns the changed
series ids so the refresh queues them. See [tv-metadata.md](tv-metadata.md).
