# Indexers: search sources

The indexer layer searches; it never grabs. Grabbing is the router's ([router.md](router.md)). Where this sits:
[overview.md](overview.md).

## What it does

`internal/indexer` turns a policy indexer into a `Searcher` and maps every result to one `Release`. Every request
goes through `internal/gateway` with `Target{Class: indexer, Key: <policy key>}`, so the allowlist, the VPN egress
guard, budgets, back-offs and `Retry-After` all apply. No adapter builds an HTTP client.

| Piece | File | Notes |
|---|---|---|
| `Release`, `Link`, hashes, RD name rule | `release.go` | Links in memory only (below) |
| `Query`, validation, normalised key | `query.go` | Movie, series (season or episode), text, RSS |
| Sonarr's series criteria on Torznab | `criteria.go` | Tiers of requests per criteria (`docs/design/series.md` "Search criteria"). The router judges each answer with its own query's criteria (scene numbers); the other sources are asked TVDB numbers |
| `Searcher`, `Result`, shared request code | `client.go` | Non-2xx is an `HTTPError` without the URL |
| Adapters | `torznab.go` (torznab and dmm), `torrentio.go`, `zilean.go`, `seadex.go`, `cardigann.go` | `cardigann` runs a catalog definition natively: [cardigann.md](cardigann.md) |
| `Service`: single-flight and result cache | `service.go` | |
| Search planner skeleton | `planner.go` | |
| `theatarr search` | `cmd/theatarr/search.go` | |

Searches add no tables of their own. SeaDex standing is kept in `metadata_cache` ([below](#seadex-standing-storage-and-concurrent-searches)).

## Release

`Indexer`, `Kind`, `Title`, `InfoHash` (lower-case hex, from the item, its magnet, or base32 converted), `Size`,
`Seeders`/`Peers` (-1 when unknown), `PublishDate`, `Privacy` and `Private`, `Categories` (Newznab ids), `Flags`
(Torznab tags; `seadex`, `seadex-best`), `Languages` (Torznab `language` attributes, as written), `IMDb`/`TMDb`/`TVDB` when the source attaches them, and `Extras` per source.

**Links stay in memory.** `GUID`, `Link` (download URL) and `Magnet` are `json:"-"`. `Link` prints as `<link>` or
`<magnet>`, marshals to `null` and logs redacted; `URL()` returns it. `Link.Keyed()` says whether it may carry a
credential (a query string, user info, a long token or a `=` in the path, or such a tracker in a magnet): fail
closed. `BareMagnet(hash)` is the only form debrid gets.

**Privacy is a label.** A release carries the stricter of its item's privacy (for
Prowlarr feeds, `<prowlarrindexer type>`) and its indexer's `privacy` (semi-private and unknown count as private; a
built-in public source, `torrentio`, `dmm`, `zilean` or `seadex`, with no `privacy` set reads as public when its
settings record is read, `policy.Indexer.ResolvePrivacy`), for badges, filters and sorting. Nothing branches on it: whether an indexer's releases go to debrid is the user's choice,
made by listing the indexer in a version (`versions.<key>.indexers`) and optionally limiting it to some providers
(`indexers.<key>.providers`, empty: all).

## Sources

| Kind | Endpoint | Queries | Paging and limits | Mapping notes |
|---|---|---|---|---|
| `torznab` | the policy URL as is (Prowlarr `http://<host>:9696/<id>/api`), `apikey` from `api_key_env` | `t=caps` first (cached 24 h); requested categories are intersected with the feed's advertised categories. Movie: `t=movie` with `imdbid` (digits, as Radarr sends), else `tmdbid`, then a title-search tier when ids return nothing; TV: `t=tvsearch` with `tvdbid`, else `imdbid` (with `tt`, as Sonarr), else `tmdbid`, else `q`, plus `season`/`ep`. Without movie or TV search in the caps: `t=search` with `q`. RSS: `t=search` with no `q`; shadow RSS pages back to its durable per-feed cursor. A series query with `Criteria` (scene numbers, air date, absolute number, scene names) is asked as Sonarr's `NewznabRequestGenerator`: tiers of requests, a later tier (title searches) only when the earlier found nothing; one failed request of several marks the answer truncated | Page size: caps `max`, at most 100. `page_cap` caps results per search (default 1000), and at most 50 pages | `title`, `guid`, `link` or enclosure, `pubDate`, `size` (or enclosure length or attr), `category`, `prowlarrindexer type`, attrs `seeders`, `peers`, `infohash`, `magneturl`, `tag` (flags), `language` (`Languages`; else `<language>` elements, as `TorznabRssParser.GetLanguages`), `imdb`, `tmdbid`, `tvdbid`, volume factors, `grabs`, `files`. `<error code description>` is a `TorznabError` |
| `dmm` | DMM's Torznab feed: `…/api/torznab/cached` (RD or AD), `…/api/torznab/<rd\|ad\|tb>/cached` (one provider) or several providers, each once, any order, `/cached` last (`…/api/torznab/rd/ad/tb/cached`: cached on any of them), plus `/api` as Prowlarr's Generic Torznab and zurg append it. Key from `api_key_env` | Fixed caps, no caps request: movie and TV by IMDb id (needs one), text by `q`. RSS: DMM's newly cached list, asked of the path's `rd` and `ad` only, because DMM answers an RSS request with `tb` in the path the old way: a path naming one of them reads that provider's list (`…/tb/rd/cached` reads `…/rd/cached/api`; plain `/cached` stays plain), and a path naming both reads each provider's own list, one request each per sync (`…/rd/ad/tb/cached` reads `…/rd/cached/api` and `…/ad/cached/api`; `Service.RSSFeeds`, `Query.RSSFeed`; the shadow RSS sync keys each list's cursor and seen marks `<indexer>#rd`, `<indexer>#ad`; a list's first sync catches up to the old combined cursor's publish date, within the page cap; a run fails only when every list and feed read failed, and each list's error is in the run summary under its own key). A path whose only provider is `tb` has no RSS | DMM pages at the `limit` sent, at most 100 (ten when none is sent), biggest first (newest first on RSS), with a total: the next offset counts the items received and the total is believed. A search asks pages of `min(100, page_cap)`; `page_cap` defaults to 200. An RSS request always sends `limit`: the one asked (the RSS sync asks 100), never above 100, and `page_cap` does not shrink it (the RSS sync takes a short page as the end of the feed). The shared `dmm-key` budget paces it; DMM's 20 requests a minute are per key, shared with any other tool using the same key | As torznab, plus: the `{imdb-tt…}` title suffix is stripped (its id fills `IMDb`), `Extras.cached_on` = `rd`, `ad`, `tb` or `rd,ad` from the path; on an endpoint (search path or RSS endpoint) naming two or more providers, the item's own `cachedon` attribute kept to the providers that endpoint names, and nothing for an item without one. The same key and `dmm-key` budget ask DMM's availability (`POST /api/sponsor/availability`) about the other releases of a search (docs/design/router.md "Cache evidence") |
| `torrentio` | `<url>/stream/movie/<tt>.json`, `<url>/stream/series/<tt>:<s>:<e>.json`. The URL may hold one config segment with `providers`, `sort`, `qualityfilter`, `language`, `limit`, `sizefilter` only: any other option (a debrid key) is refused when the service starts | Movie or series by IMDb id; series needs a season. A season search asks for episode 1 and keeps only packs (it answers only `show:season:episode`) | One request | First title line is the name; a pack has the file on line 2 (`Extras.file`). `👤` seeders, `💾` size (powers of 1024), `⚙️` source. A pack's size is Torrentio's file size; with `Query.EpisodeCounts` it is scaled (per-episode size ranges and complete-series rules) and `size_scaled_by` set, otherwise `size_is_file`. `title` or `description` field. Bare magnet from the hash |
| `zilean` | `<url>/dmm/filtered` with `ImdbId`, or `Query` (+ `Year` for movies), plus `Season`, `Episode` | Movie and series by IMDb id and/or title, text | An IMDb search plus a title search, merged by hash; a title hit with another IMDb id is dropped. Zilean caps at 200 per query. A failed title search keeps the IMDb results with a note | `raw_title`, `info_hash`, `size` (string or number), `imdb_id`, `ingested_at`. No seeders. Bare magnet |
| `seadex` | `<url>/api/collections/entries/records?filter=alID=<id>&expand=trs&page=1&perPage=100`; RSS sorts by `-updated` | Movie or series with an AniList id; RSS | One request | Nyaa entries only (public); other trackers, `Incomplete` tags and missing hashes are dropped with a note. SeaDex titles use the mapped movie/year or series/season plus record id. Quality comes from agreeing feature filenames; mixed/unidentified files stay Unknown, independently of curator preference. Size is the sum of files; `Link` the Nyaa `.torrent` (no key); flags `seadex`, `seadex-best`; extras group, tags, dual audio, features, AniList |
| `cardigann` | the site at the policy URL, driven by the catalog definition `definition` ([cardigann.md](cardigann.md): synced from Prowlarr/Indexers, or the operator's own in `<data dir>/cardigann/definitions/custom/`, which wins on the same id); the engine's requests go through the gateway one hop at a time, with the indexer's own budget, and may reach the hosts in `hosts` | Keywords only: movie `title year`, series criteria with scene names: `title SxxEyy` / `title Sxx`, daily `title YYYY MM DD`, anime `title NN` plus standard numbering; text and RSS (no keywords). Categories 2000 (movie), 5000 (series), plus 5070 (anime) unless overridden | One search (login and the definition's paths as needed); bounded at 60 s, 150 s with FlareSolverr | Definition fields: title, size, seeders/leechers (-1 when the site gives none), date, categories, infohash, magnet, download link (in memory), IMDb. Privacy (a label) is the stricter of the policy and the definition's `type` |

Response shapes without a live sample were written from the source: Prowlarr `NewznabResults.cs` @ 12c3278 (torznab),
the releases.moe API's records (SeaDex), riven's `plugin-torrentio` (Torrentio) and Zilean's API. Each fixture says where its shape comes from.

## Single-flight and cache

`Service.Search(ctx, key, query)` validates the query and checks the source supports it. Identical searches (same
indexer, same normalised query: type, lower-cased title, ids, season, episode, categories, episode counts) share one
upstream search; the others get `Result.Shared`. The upstream search outlives a caller that gives up, so the rest
still get it (bounded by `SearchTimeout`, 5 min, budget waits included). Successful results are cached for 15 min
(`CacheTTL`) and come back with `Result.Cached`. Errors are not cached. RSS pages
(`QueryRSS`) are never read from or written to the cache: each RSS poll asks the feed fresh. Every caller gets its own
copy of the result.

A shared upstream search's requests count under the first caller's purpose, and a cached answer sends none
([gateway.md](gateway.md) "Indexer requests by purpose"). The Indexers settings page shows each indexer's requests
today and over the last 7 UTC days, by purpose (interactive, missing, upgrade, RSS, shadow, tests, other), beside its
budget.

## Search planner

`PlanSearch(policy, service, title)` takes the versions that apply to a title (same kind; `anime` matching), unions
their indexers, and returns one step per indexer with the versions it serves and the queries it can answer:

- **Order:** SeaDex (one cheap request with the best pick), Prowlarr feeds, DMM (tight
  per-key budget, and its results carry cache state), Torrentio, Zilean; then the key.
- **Queries:** one per movie, one per season for a series. Anime seasons don't fan out per episode.
- **Skipped:** indexers that can't search the title with its ids (Torrentio and DMM without IMDb, SeaDex without
  AniList).

The planner does not handle aliases, numbering schemes or request priorities.

## `theatarr search`

```
theatarr search -indexer <key>[,<key>…|all] [-type movie|series|text|rss] [-imdb tt…] [-tmdb N] [-tvdb N]
          [-anilist N] [-title …] [-year N] [-season N] [-episode N] [-anime] [-json] [-limit 50] [-db file] [-v]
```

- **Egress first.** It runs the egress check on every path. When the egress settings require the VPN for indexers
  and a path does not say VPN OK, it stops before any indexer request: `refusing to search: indexer traffic must leave through the VPN, and it does not from here. Run theatarr search
  where the VPN routes it (…) (egress direct: exit address … is not in the VPN set)`.
- **Output:** a table per indexer (size, seeders, privacy, hash prefix, whether a magnet or link exists, title), or
  JSON lines with `-json`. Links are never printed.
- **`-db`** (env `THEATARR_DB`) keeps budget caps and back-offs across runs; without it they live in memory.
- **Exit status:** 1 when any indexer failed.

## Checking sources

To check each source against a deployment:

1. **Settings.** Configure the indexers in the settings pages of a running service, or import a policy directory
   into a database without settings (`theatarr settings import -policy <dir> -db <db>`, then again with `-confirm`).
   - Prowlarr URLs: `http://prowlarr:9696/<id>/api` with `api_key_env: THEATARR_PROWLARR_KEY`;
   - DMM: one indexer, `url: https://debridmediamanager.com/api/torznab/rd/ad/tb/cached`, `page_cap: 200`,
     `api_key_env: THEATARR_DMM_KEY`, listed once in each version instead of one DMM indexer per provider, with
     `arr_names` the union of theirs. Each item says which of RD, AD and TB hold it (`cachedon`), and its RSS
     reads `…/rd/cached` and `…/ad/cached`, one list each. TorBox answers come from the TorBox key linked in DMM: without one DMM fails the whole
     request (error 102), and the settings Test (a movie search on the configured path) shows it. Prefer it to plain
     `/cached`, which tags every item `rd,ad`, so its RSS would record AllDebrid evidence for hashes only Real-Debrid
     holds. Caveats:
     - **Only when the per-provider indexers agree.** Collapse them only if they are listed by the same versions and
       none has a `providers` allowlist. One indexer has one allowlist, which cannot say "RD for these, TB for those";
       where the choices differ, keep the indexers separate.
     - **`page_cap: 200`** is two requests a search over the union of the three providers, largest first. A
       provider's smallest releases can still fall past it, where its own indexer would have listed them on its
       first page.
     - **RSS reads each provider's list.** DMM's combined `rd/ad` list names a hash once, at its first listing on
       either provider, so a later cache on the other provider would never show there; Theatarr reads `…/rd/cached`
       and `…/ad/cached` separately instead (one more `dmm-key` request a sync: 4 more an hour at a 15-minute RSS
       interval). Each RSS item's `pubDate` is when DMM listed it as cached on that
       list's provider, so a listing newer than Theatarr's own uncached or miss verdict at that provider reopens the
       verdict once (router.md "Precedence in `provider_cache`"); a `pubDate` ahead of Theatarr's clock reopens
       nothing (under clock skew it would reopen every retry);
   - Zilean's address;
   - SeaDex: `https://releases.moe`. Another host only works if it answers without redirecting to an
     unlisted host;
   - `egress.check_url` and `vpn_ips` for the VPN in use.
2. **Keys.** Pass them in the container's environment, never on the command line.
3. **Egress.** `docker exec theatarr theatarr egress-check` must print `VPN OK`.
4. **One search per source,** with `-v` to see the gateway lines:

   ```sh
   docker exec theatarr theatarr search -indexer prowlarr-public -imdb tt0133093 -title "The Matrix" -year 1999 -v
   docker exec theatarr theatarr search -indexer dmm -imdb tt0133093 -v
   docker exec theatarr theatarr search -indexer dmm -imdb tt0903747 -season 1 -v
   docker exec theatarr theatarr search -indexer torrentio -imdb tt0903747 -season 2
   docker exec theatarr theatarr search -indexer zilean -imdb tt4574334 -title "Stranger Things" -season 1
   docker exec theatarr theatarr search -indexer seadex -type series -anilist 154587 -title Frieren -season 1
   ```

5. **What to look for:**
   - **DMM:** the request path ends `/cached/api`. DMM pages at whatever `limit` is sent, at most 100 (ten when
     none is sent): a search asks `min(100, page_cap)` a page and stops at `page_cap`; an RSS page asks 100. Titles
     have no `{imdb-…}` suffix.
   - **Torrentio:** a season search shows only packs.
   - **Zilean:** a note `title search added N`.
   - **SeaDex:** Nyaa entries only.
   - **Everywhere:** no key in the `-v` lines.
6. **If a shape differs,** save a sanitised response (no keys or passkeys; links replaced) and turn it into a fixture
   in `internal/indexer/testdata/`.

## Design notes

| Choice | Why |
|---|---|
| `RealDebridRefuses`: `WEB-DL`, `WEB.x264`, `WEB.H264` exact case; the rip family, `HDTV.x264`, `HDTV.XviD`, `BluRay.x264` and `p.BluRay.DTS` in any case; a loose form for dot-to-space titles | RD's name rule as measured by zurg's maintainers. Only a helper: the router decides per provider |
| Torrentio pack sizes are scaled by `Query.EpisodeCounts`, which the caller passes | Metadata belongs to Theatarr's metadata layer; the indexer layer makes no TMDb call |
| Zilean's title search uses `Query.Title` | Same |
| Single-flight and the cache are keyed per (indexer, query), 15 min TTL | The gateway leaves them to the adapters |
| `page_cap` is results per search: DMM default 200, others 1000, at most 50 pages | The policy says "caps paging per search" |
| The example `dmm` URL is the Torznab feed, with `api_key_env: THEATARR_DMM_KEY` (`examples/policy/indexers.yaml`) | The adapter needs the feed path |
| SeaDex keeps Nyaa entries only | SeaDex redacts the hashes of picks on other trackers, and their torrents need a passkey, so there is nothing to add; such a release comes through that tracker's own indexer |
| Nyaa `.torrent` links are kept in `Link` | `nyaa.si` is not on the allowlist; the router adds magnets only |

**Open points:**
- Prowlarr feeds proxy to the trackers from Prowlarr's own host, so Prowlarr must egress via the VPN too
  ([gateway.md](gateway.md#limits-of-the-guard)).
- DMM's search budget: a 200-result DMM search is 20 requests, about 75 s at 16 per minute. A lower `page_cap` (50
  to 100) is probably enough, since DMM sorts biggest first.

## Tests

`go test ./internal/indexer/ ./cmd/theatarr/` never touches the network. Host names under `.test` go to local `httptest`
servers through the gateway's `DialContext`, and every request passes a real gateway with a passing egress check.
Fixtures in `internal/indexer/testdata/` are hand-written and sanitised (no keys, passkeys or real download URLs).

Covered:
- each adapter's request parameters and mapping;
- Torznab caps fallback, paging to `page_cap`, and DMM's ten-a-page paging with a total;
- a spent budget ending paging, while a first page without budget fails;
- `<error>` answers, and an HTTP 500 without the URL;
- keys never in errors or logs; links never in JSON or `fmt`;
- the privacy label (the stricter of item and policy);
- the VPN guard: no dial without a check;
- Torrentio season packs, scaling, and debrid config refused;
- the Zilean merge and its drops;
- SeaDex filtering and naming;
- single-flight across six callers, with the cache, the TTL, a caller giving up, and errors not cached;
- the planner's order, versions and skips;
- `Link.Keyed`, hashes, the RD rule and query keys;
- `theatarr search` end to end: refused off the VPN with nothing dialed, and a table and JSON on it.

## SeaDex standing: storage and concurrent searches

How standing is used in decisions: [decide.md](decide.md#seadex-standing). This section covers how the indexer
layer records it.

SeaDex standing stores upstream scoring facts and feature filenames/sizes only for entries a catalog title maps or a held file references. Unix writers share a canonical database sidecar lock from before fetch through SQLite commit. A failed lock or standing write fails the search when any version prefers SeaDex. With preference off everywhere, the answer is returned uncached with a warning and no unlocked standing write. Unix opens `<db>.seadex-lock` read/write with create, mode 0660 masked by umask, falling back to read-only. Static symlinks, nonregular files and database hardlink aliases are refused before open; O_NOFOLLOW/O_NONBLOCK and fstat validate the opened sidecar without blocking on FIFOs. The database itself is only statted, preserving SQLite's POSIX locks. The sidecar is never unlinked; flock polls with caller cancellation and releases on close or process death. There is no publication file, temporary inode, permission repair, cleanup or creation retry loop. The deployer sets data-directory permissions. Hostile local processes, directory tampering and rare multi-uid races are outside this design. Each flight has a bounded lifetime (five minutes by default), counts live waiters, and is cancelled when its last waiter leaves. Callers return promptly on their own cancellation. `theatarr_seadex_lock_wait_seconds` measures acquisition time, including failed waits. Non-Unix systems use an in-process fallback, without cross-process exclusion. Do not run an older binary or CLI that ignores the lock beside these writers.

Independent indexers in a router search run concurrently, at most eight at once, with queries of one indexer sequential. Cancellation stops queued queries before a new flight is created; a VPN-required refusal cancels sibling workers’ pending queries. A flight survives only for remaining waiters; unshared sibling fetches are cancelled too. A completed answer may still finish its bounded standing commit. Results retain planner order, deduplication and request accounting. RSS feeds are read one after another, because the feed loop also coordinates cursor advancement. Cached/shared answers do not replay observations. Refused redirects still count the original request; pre-send refusals cost zero.

Incoming hash identity resolves standing independently of held episode coverage. An unresolved incoming release cannot keep stale curator rank or acquire rank from held-only legacy evidence. Import keeps refreshed incoming evidence through the upgrade gate. Before saving each file and computing its cutoff state, one incoming resolver uses its verified episodes to set current rank, entry, episode scope and curated alternatives; it never restores legacy-only or stored rank. Curator-only wanted takes alternatives only from mapped entries covering the held file, including its own entry, and covering every known held episode. Series size checks map the alternative's stored feature filenames with the existing episode mapper and use the decider's size specifications. Unknown shape/size/runtime is ineligible; known episode runtimes suffice when series runtime is zero. Existing snapshots without feature names remain ineligible until a normal refresh supplies them. Cutoff passes load version-specific series metadata, scene mappings and AnimeMap once per title/version. Each alternative’s feature episodes and size eligibility are computed once per pass, using its unique source entry (including suffixed AniList keys) for local-number fallback and the synthetic release title for alternate cuts; its actual episodes must contain every held episode. Standard-type absolute filenames that cannot be mapped remain ineligible. Adopt/Refresh pin standing once per pass, persist the resolved entry/rank in link metadata, and use the arr cutoff flag if standing cannot load. Held files keep their saved protection when evidence is insufficient; with all viable memberships known, an unsupported saved rank is clamped to the highest current rank. Pending memberships count until retirement. A held series file with verified episodes, like an incoming one, takes standing only from entries covering all of them, also when it carries a saved entry and when its title has no mapping (a held file's saved entry that does not cover them is dropped only when another entry of the title covers all of them; otherwise it stays the file's scope): a hash listed only under another cour's entry (or another title's) leaves its saved standing, and the file still gets its covering entries' alternatives. A held rank kept because mapped entries disagree or lack records (disagreement never promotes, even below every current rank) names no entry, so later resolutions keep it, and still gets the matched entries' alternatives. Router dry-run, like decide -search, lays its SeaDex answers over the stored standing. Each loaded title has its own cutoff cache with its own lock (keyed by built profile/engine and mapping identity), shared by value copies of the title; a series reload starts a fresh one, so a cutoff pass writes no title field. Unknown provider folders are not reconstructed. Prefer-on import metadata scoring also uses version-specific series type, scene mappings and AnimeMap, which may change Languages/Formats relative to prefer-off.

Every search result carries one release identity: conflicting or unverifiable hash/magnet identities are dropped (logged, noted), and a hashless release takes its magnet's hash. Missing or ambiguous response fields (including a torrent record without hash or tracker) never retire standing; fully identified torrents in an incomplete answer still clear a pending retirement; a second complete omission at least 12 hours later confirms retirement. Unchanged snapshots are not rewritten. Each entry has one versioned record (version 2): listed, pending, tombstone and legacy members plus one comparison set per first omission, shared by every torrent that answer omitted; a pending torrent is ranked within that set under current settings until it retires. Version 1 rows (per-torrent cohorts, or none) are converted on read with every rank unchanged and written back as version 2 on the entry's next stored observation; there is no migration. One transition (`ApplySeaDexObservation`) applies every answer, stored or laid over in memory. Each operation reads records and mappings once, in one read transaction, when a prefer-on version first needs them; every version policy gets one immutable view of that read, and a prefer-off version an empty view without any read. Retention prunes rows no title maps to and no held file references, in key batches of 1,000. A SeaDex entry without a relation list (`trs` absent or null) gives no release and no standing; relations are complete only when `trs` is a set of unique ids matching the expansion both ways. Magnets are parsed leniently and a magnet naming no v1 identity beside another identity is a conflict. Torznab records the RSS page (length and cursor keys) before its GUID dedupe. Snapshot quality uses the same exact-hash inventory fallback as releases. A membership still stored without facts (legacy) is read as a zero-rank tombstone by prefer-on versions until the entry's next observation rewrites it. Curator-only wanted judges each alternative on the release a curator search would return for the held file's season (`SeaDexCandidateRelease`: the search's title, flags, size and shape from the stored record: a multi-feature movie record and a one-feature series record are dropped), through the decider's size, format-minimum and language checks, with live `provider_cache` misses read with the standing; a title with a mapped entry lacking a record gives no alternatives. Curator scoring is per complete AniList entry; legacy RSS/search seeder signals remain unchanged for opted-out versions.

With preference off, split entries are unioned and their episodes deduplicated. Disjoint `al:N#k`
parts are one AniList entry, not ambiguous ids. Only overlapping parts or an exact `al:N` alongside parts fail
closed. Cutoff still requires a unique entry key for ordinal file mapping, so split parts are not eligible there.
