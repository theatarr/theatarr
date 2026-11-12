# The shadow runner (`internal/shadow`, `theatarr shadow`)

Shadow mode runs Theatarr beside the arrs without acting: it re-decides every arr grab with Theatarr's own engine and
search, reads RSS, and reports where the two agree. Where it sits: [overview.md](overview.md). Code:
`internal/shadow`, migration `00010_shadow.sql`, `internal/policy/shadow.go`, `internal/web/shadow.go`,
`cmd/theatarr/shadow.go`.

For shadow versions nothing grabs or writes outside Theatarr's database. The arrs get the same GET requests as
`theatarr import-arr`; indexers are searched through the gateway (VPN guard, allowlist, budgets) exactly as
`theatarr search` does.

## How it works

`theatarr serve` builds the runner next to the provider sync and runs three job kinds:

| Job | When | Does |
|---|---|---|
| `arr.import` | every `shadow.import_interval` (15 min), first a minute after start | An incremental `theatarr import-arr` of every `import.arr` instance whose key is set. Then, per instance, every new `grabbed` event after the instance's cursor becomes a `shadow.grab` job, and the cursor moves to the newest history id, in the same transaction as the jobs (an enqueue error fails the import and keeps the cursor). Pending grabs with no queued, running or failed job are enqueued again. **The first run only sets the cursor**, so old history is never searched |
| `shadow.grab` | per new arr grab, priority -10 | See below |
| `shadow.rss` | every `shadow.rss_interval` (15 min), first 3 min after start, priority -20 | See below |

Every job has a dedupe key, so a slow import or RSS read never stacks. The other jobs (provider sync, the nightly
backup) run at priority 0 and go first.

### Re-scoring an arr grab (`shadow.grab`)

1. **The arr's release, decided by Theatarr's engine** for the instance's version (`import.arr[x].version`), with the
   version's file at grab time:
   - the `version_file` row the import saw before or after it ran, whichever was added before the grab;
   - else the file rebuilt from the movie's import and delete events (`internal/decide/shadow.LoadGrab`);
   - else none. The source is stored (`existing_source`).
   - Theatarr's own rule (the version's indexer list) is left out for the arr's release: it carries the arr's
     indexer name, not a policy key.
2. **Theatarr's own search** for the title, through the planner (`indexer.PlanSearch`) over the indexers of the shadowed
   versions:
   - **once per title:** the results serve every grab of the title for `shadow.search_reuse` (6 h), so the other
     instance grabbing the same movie costs nothing;
   - **one search at a time,** and at most `shadow.searches_per_hour` titles (20) per clock hour;
   - an indexer whose budget is spent for the hour or day, or which (or whose budget) is backing off, is skipped
     with a note;
   - **over the cap, or with every indexer failing** (VPN down), the grab is stored as `pending` and re-enqueued:
     at the next hour for the cap, in 15 minutes otherwise. The pending row and its job commit together. A grab older than a day is decided without a search
     (`searched = 0`, with the reason in the note).
3. **Every result is decided** for the version with the same file, and ranked together with the arr's release by
   the arr's comparer (`decide.Rank`). The first accepted one is **Theatarr's pick**. The best `shadow.keep_results` (10)
   are stored as summaries, plus both decisions; never the full result list.
4. The version's open RSS picks are linked to the grab, and **the diff row** is written by the grab's release source
   ([classification](#classification)): an RSS grab is compared against Theatarr's RSS picks near it, a hand grab
   against Theatarr's decision of it, any other grab against Theatarr's search.

### RSS (`shadow.rss`)

- **Feeds:** reads the public torznab (Prowlarr) indexers, SeaDex's recent feed and the public catalog (Cardigann)
  sources the shadowed versions list. Like the arrs, every item of
  every page read is evaluated each sync, those below the previous feed head too, so an item rejected for a
  temporary reason is decided again while it stays in the feed. A durable per-feed cursor (the head's key and
  publish date) only bounds catch-up paging: pages are read up to the one holding the previous head or an item
  published before it, at most ten. Page length and cursor keys come from the upstream page as sent, before any
  filtering (Torznab's repeated-GUID dedupe, identity conflicts, eligibility). The cursor moves only after routing, in the same transaction as the seen marks,
  so an error leaves both and the next sync reads the same pages. At the ten-page cap the cursor moves on to the new
  head at once and the run reports the gap (`gap` in the run summary, a warning in the log), as the arrs do: older
  items past the tenth page are not read. A feed that ends (a short page, or one with nothing
  new because the indexer ignores the offset) without the previous head reports the gap too. DMM's feed is
  its newly cached list, read on a DMM path naming `rd` or `ad` from the path's `rd`/`ad`
  endpoint ([indexers.md](indexers.md)); DMM dates it by the build that listed each release, so many items share
  one `pubDate`, which the cursor handles (only an item published strictly before the head ends catch-up early;
  `TestRSSCatchupWithSharedPubDates`). A DMM path whose only provider is `tb` has no feed and is left out. Its RSS
  items reach the same decide and plan path as any feed's (dry-run plans while `router.enabled` is false) and store
  their cache evidence like a search's. Torrentio and Zilean have no RSS feed (they answer per title), so they
  are skipped. A catalog source's feed is its definition's search without keywords, one page with no offset
  ([cardigann.md](cardigann.md#rss)); one that cannot be read (no catalog, a missing definition, a site without movie
  or TV categories) is listed in the run summary with `skipped` and the reason. Budget-capped or
  backing-off feeds are skipped too.
- **Seen items:** durable shadow picks and immutable release/profile rejections are remembered by hash (or title
  and size) for 14 days. Unknown titles, unaired packs, availability, existing-file rejections and active routes
  are reconsidered each sync; duplicate items within one sync are decided once, at most 1,000 items per feed.
- **Categories:** RSS items without categories are decided as movies only, as in the arrs. TV categories
  (5000–5999) select series decisions. Anime category 5070 permits standalone movie decisions, but episode,
  season, special and batch syntax stays in the series path even if no series matches. A bare film year parsed by
  Sonarr as compact season/episode numbering (1988 as S19E88) still permits the movie path.
- **Mapping:** the item's IMDb or TMDb id when the feed gives one, else the parsed title's clean title against the
  catalog titles' names, original titles and aliases, filtered by primary or secondary year. Without a parsed year the
  name must identify one movie across the library; same-named films need an id or year. The engine confirms it.
  The film index (shared with the engine) is reloaded at the start of every run, so a film added since is matched.
- **Decided** for every monitored shadowed version of the title, including `cutoff_met`,
  with the version's current file. Accepted decisions are ranked together per title/version for the whole sync;
  only the releases selected by episode coverage become **RSS picks** (one for movies).
  Per-file upgrade checks reject replacements that do not improve the full quality/score cutoff. Imported arr
  movies without adopted links remain eligible: the arr import's state can reflect quality alone. Movies must reach their minimum availability before RSS can accept them.
- **Linking:** an arr grab of the same title and version links every open pick (the grab's diff row names the best).
  A pick still open after `shadow.link_window` (24 h) closes into one `we_grab_arr_didnt` row per title and
  version, showing the best pick. A grab found at that moment links instead.
- **A pick after an RSS grab:** when Theatarr's read picks a release within an hour of an arr RSS grab of the same title and
  version that is already decided (the arr read first), the pick is linked to that grab at once and the grab is
  classified again, so the row compares the same read.

### Router plans and active versions

`theatarr serve` gives the runner the download router (docs/design/router.md "Triggers"):

- **A plan per shadow grab or RSS batch.**
  - After a grab is stored, `Router.Route` gets its ranked accepted decisions, the arr's release among them.
  - All accepted RSS items for a title/version share one ranked request and dry-run plan. Disjoint episode releases
    become separate picks linked to that plan; overlapping alternatives are skipped, and a pack covers all its episodes.
  - For a shadow version the router only records a dry-run plan (`router_plan`, mode `shadow`), even with
    `router.enabled: true`.
  - The plan's id goes on `shadow_grab.router_plan_id` or `shadow_rss_pick.router_plan_id` (00016).
- **Where the plan shows.**
  - The diff rows carry `router_plan`, one line: "would add X on P", "would wait to add X" (every provider paced),
    or "would add nothing: <codes>" (`no_hash`, `rd rd_name_rule`, `miss`, `blocklisted` …).
  - The detail (API `grab.router_plan` / `rss_pick.router_plan`, and the page's "Router plan" card) lists every step:
    release, provider, outcome, code and why.
  - `theatarr shadow status` prints the line after each of the latest rows.
- **Active versions in RSS.** The feeds of active versions are read too.
  - Their accepted items are not picks and are not in the report.
  - They are ranked per title and version and handed to the router as one `router.route` job, unless every candidate
    overlaps an in-flight download. The router skips individual overlaps and places the disjoint releases in the
    same job. The summary counts `routed` and `route_skipped`.
- `theatarr shadow run-once` makes no plans; only `theatarr serve` gives the runner a router.

## Budget use

Per source, for a movie version listing two Prowlarr feeds, DMM, Torrentio and Zilean:

| Source | One title search | RSS per hour | Worst hour (20 searches) | Budget |
|---|---:|---:|---:|---|
| Prowlarr (two feeds) | 2–4 (a page of 100 each, more only for bigger answers; `caps` once a day) | 4 | ~80 | `prowlarr` 60/min |
| DMM | 1 (a page of up to `page_cap`, at most 100) | 4 per DMM list (a page of 100 per sync; more while catching up); a path naming rd and ad has two lists | 24 | `dmm-key` (DMM allows 20/min per key) |
| Torrentio | 1 | 0 | 20 | `torrentio` 30/min, 5000/day |
| Zilean | 2 (IMDb and title) | 0 | 40 | none |
| **Total** | **~6–8** | **8** | **~164** | |

- **Steady state:** arr instances mostly grab the same movies, and one search serves every grab of a title, so a
  handful of title searches a day is typical, plus 96 Prowlarr RSS requests and 96 DMM RSS requests per DMM indexer
  the shadowed versions list.
- **Bursts** (a batch of release pushes) drain at `shadow.searches_per_hour` titles an hour.
- **The arrs** get the import's GETs every 15 minutes: about 17 per instance (status, root folders, tags, profiles,
  movies, files in batches of 50, one history page), at most 2.5 per second.
- `theatarr_shadow_indexer_requests_total` counts the upstream requests; the dashboard's budget table shows the use.

## Policy (`shadow`, optional)

```yaml
shadow:
  import_interval: 15m     # minimum 5m
  rss_interval: 15m        # default; minimum 10m
  rss: true
  search: true
  searches_per_hour: 20    # 1-200
  search_reuse: 6h         # at most 24h
  keep_results: 10         # 1-50
  link_window: 24h         # 1h to 7 days
```

`examples/policy/shadow.yaml` has the defaults. The RSS default is 15m, the arrs' default RSS sync interval, in one
place (`policy.DefaultShadowRSSInterval`). `shadow.rss` and `shadow.rss_interval` are not only shadow settings: the same `shadow.rss` job is production RSS
for active versions (it routes their picks), so `rss: false` stops RSS grabbing too. It runs at job priority -20, the
lowest, so bulk work (imports, wanted searches, offers) runs first and RSS can lag behind it; the arrs run RSS on its
own timer. Versions with `mode: shadow` whose profile the engine built are
shadowed, movies and series ([`series.md`](series.md)); the others are logged at start.

## Reading the report

Open **Advanced › Sonarr/Radarr migration › Comparison**, then a row. Its React detail page shows all stored
candidates, their custom formats and scores beside the existing file's quality, score and formats, all rejection
reasons, the arr/search/RSS decisions and the stored router plans of the search grab and the RSS pick (release, provider,
outcome, code, why per step), each candidate's hash, size, seeders, languages and publish time, and the history,
search and RSS context. Tables scroll inside their card on phones, with quality always shown.
`theatarr shadow status` and `/api/theatarr/v1/shadow/{summary,diffs,diffs/{id}}` read the same tables.

### Classification

Each arr grab is compared against what Theatarr's engine would have done **from the same source**. The basis follows the
history's `releaseSource` (Radarr and Sonarr use the same values) and is shown on every row (`basis`):

| Release source | Basis | Compared against |
|---|---|---|
| `Rss` | `rss` | Theatarr's RSS picks for the title and version seen within **one hour** of the grab (before or after; not those linked to another grab). Both read their feeds every 15 minutes by default, so the same item reaches both within the hour |
| `InteractiveSearch` | `manual` | Theatarr's decision of the arr's release alone: a person picked it from a list |
| `Search`, `UserInvokedSearch`, `ReleasePush`, anything else | `search` | Theatarr's search for the title |

Releases are ranked as the arr's comparer starts: quality in the profile's order (a group is one step), then
revision, then format score. A summary keeps no more, so seeders and age do not break ties here.

| Basis | Case | Bucket | Reason |
|---|---|---|---|
| rss | A Theatarr RSS pick ranks above the arr's release (or, if Theatarr rejects the arr's release, above its RSS pick of it) | `we_grab_another` | `better_quality` (another quality), `higher_score` |
| rss | The same, with the same quality and score (a higher revision) | `agree` | `same_quality_score` |
| rss | Theatarr's RSS read picked the arr's release, and nothing ranked above it | `agree` | `same_rss_pick` (even when deciding it again from the arr's history rejects it; the detail says so) |
| rss | No pick of the arr's release, nothing above it, and Theatarr accepts the arr's release | `agree` | `rss_accepted` (also with no Theatarr RSS pick at all) |
| rss | No pick of the arr's release, and Theatarr rejects it | `arr_grabbed_we_wouldnt` | the first rejection's category |
| manual | Theatarr accepts the arr's release | `agree` | `manual_grab_accepted` (a hand grab: listed, not in any agreement ratio) |
| manual | Theatarr rejects it | `arr_grabbed_we_wouldnt` | `manual_grab_rejected` (the detail names the rejection and its category; a hand grab: listed, not in any agreement ratio) |
| search | Theatarr's pick is the arr's release, or one with the same quality and score | `agree` | `same_release`, `same_quality_score` |
| search | No search ran; Theatarr accepts the arr's release | `agree` | `accepted_not_searched` |
| search | Theatarr's pick ranks above the arr's release | `we_grab_another` | `newer_release` (published after the grab), `better_quality`, `higher_score` |
| search | Theatarr rejects the arr's release | `arr_grabbed_we_wouldnt` | `not_an_upgrade` (Disk… specs), `quality_not_wanted`, `below_min_score`, `size`, `language`, `unmapped_title`, `unparseable`, `repack`, `other` |
| rss, search | A series version on a Sonarr parser mode other than v4 (`v4plus`, `v5`) disagrees, and the same grab decided with Sonarr v4's parser (as the arr) agrees | `agree` | `parser_mode` (detail `parser: <mode>; ...`, both classifications; `docs/design/sonarr-v5.md` "Shadow") |
| — | An RSS pick no arr grab followed within the link window | `we_grab_arr_didnt` | `arr_no_grab` |

- **`search_found_better`:** on an `rss` or `manual` row, Theatarr's search still runs (up to an import interval later)
  and its pick, when it ranks above the row's own release, is kept on the row (release, indexer, quality, score) and
  named in the detail. It does not change the bucket: it measures timing, not the engine. The summary counts these
  rows overall and per reason; `?search_found_better=1` lists them.
- **Example:** an arr grabs a 2160p BluRay encode from RSS, and Theatarr's RSS read picks the same release 24 s
  later. Theatarr's search, run with the grab job, finds a Remux. The row is `agree / same_rss_pick` with the Remux
  as `search_found_better`.
- **Reclassify:** `theatarr serve` classifies every stored grab again at start from the stored decisions and RSS picks (no
  search, no new decision), so rows follow these rules; `theatarr shadow reclassify` does it by hand. Grabs of versions no
  longer shadowed are left as they are.
- Every row has the machine-readable `reason` and a free-text `detail`: both releases with quality and score, the
  first rejection, the RSS pick it rests on, whether Theatarr's search returned the arr's release, and the search notes
  (skipped or failed indexers).
- In a diff row, `arr_quality` and `arr_score` are **Theatarr's** for the arr's release; the arr's own grab-time score is
  in the grab detail (`arr_score` there). `our_release` is the release the row compares: the RSS pick, the arr's
  release on an accepted RSS or hand grab, or Theatarr's search's pick.
- The detail view shows the basis, both decisions with their format breakdowns, the file it was compared against,
  Theatarr's search's pick and best results, and the RSS pick.
- **"no search"** marks grabs decided without Theatarr's search; `searched` counts the others.

### The exit bar

Agreement = `agree` / (`agree` + `we_grab_another` + `arr_grabbed_we_wouldnt`) over the arr grabs of the last 14
days, **hand grabs left out** of both sides (`theatarr_shadow_agreement_ratio{window="14d"}`, NaN with none). The same
holds for the per-kind (`kinds`) and per-version ratios, and for `grabs` and `searched` in the summary. RSS-only rows
are counted and shown but are not in the ratio: they are not arr grabs. Hand grabs (`InteractiveSearch`, the
`manual` basis) are still rows in their buckets and reasons, and the summary counts them apart as `hand_grabs`
(`grabs`, `accepted` = `manual_grab_accepted`, `rejected` = `manual_grab_rejected`), shown in the UI's Agreement
card and in `theatarr shadow status`. The bar (`shadow.AgreementBar`) is two weeks of this at **≥ 95%**, for movies
and series each, **and** every difference explained: the reason categories and the per-reason counts are what to go
through. Cautions:

- **Hand grabs** are out of the ratio: a person picked the release, so it says nothing about what the engine would
  pick. An accepted hand grab is `agree` (`manual_grab_accepted`); a rejected one is `manual_grab_rejected`, under its
  own reason rather than the policy categories. The rejected ones are still worth reading (a size limit or a format
  the person overrode).
- **RSS grabs** are only as good as Theatarr's RSS coverage: with no Theatarr pick near the grab (its feeds lack the
  item, or RSS is off), an accepted release is `rss_accepted`. Check the RSS picks against `rss_accepted` rows.
- `accepted_not_searched` counts as agree. Check `searched` against `grabs` in the summary; a fair window has
  nearly all grabs searched.
- When the profiles are Theatarr's own (TRaSH-based) rather than the arrs' (`from_arr`), the report measures how that
  policy would have behaved against what the arrs did, not engine parity (that is `theatarr decide shadow`,
  [decide.md](decide.md)). The usual early disagreements are then policy differences, each explained: `size` (a remux
  over the policy's size limit), `quality_not_wanted` (a quality the profile does not allow) and `not_an_upgrade`
  (the profile does not score one audio format over another).

## Comparison after go-live

Active versions are not compared against an arr kept beside them; followers cover that case
([parallel.md](../parallel.md)). An `import.arr` entry is always imported; a stored entry with `role: compare` or a
`sink_path` is refused. The report still reads `basis: live` rows already stored.

## Metrics

| Metric | |
|---|---|
| `theatarr_shadow_agreement_ratio{window="14d"}` | the ratio above |
| `theatarr_shadow_version_agreement_ratio{version, window="14d"}` | per-version ratio, including live theatarr-only grabs |
| `theatarr_shadow_disagreement_reasons{version, bucket, reason, window="14d"}` | disagreement rows by version and reason |
| `theatarr_shadow_diffs{bucket, window="14d"}` | rows per bucket in the window |
| `theatarr_shadow_grabs_total{bucket}` | grabs re-decided |
| `theatarr_shadow_searches_total{indexer, outcome}` | per-indexer searches, `ok` or `error` |
| `theatarr_shadow_indexer_requests_total` | upstream indexer requests (cached answers not counted) |
| `theatarr_shadow_rss_items_total` | new RSS items decided |
| `theatarr_shadow_grab_deferrals_total` | grabs whose search waited |

Live rows update the agreement, diff and reason gauges; `theatarr_shadow_grabs_total` remains the dry-run re-decision counter.

## Schema (00010, 00013, 00016, 00035)

Migration 00035 adds `shadow_compare_source` (durable start), `shadow_compare_grab` (history observations, optionally
mapped to existing titles/versions), and the unique `(compare_instance, compare_key)` identity on live `shadow_diff` rows.
Nothing writes them; the report reads rows already stored.

| Table | Holds |
|---|---|
| `shadow_cursor` | per instance, the last history id looked at |
| `shadow_grab` | one arr grab re-decided: the arr's record, the file source, state (`pending`, `done`), search facts, both decisions and the kept results as JSON summaries |
| `shadow_rss_seen` | feed items already decided (14 days) |
| `shadow_rss_pick` | RSS picks: release, title, version, quality, score, decision summary, `open` / `linked` / `expired` |
| `shadow_diff` | the report: one row per grab or expired pick, with bucket, reason, detail, both releases; `search_better` (00013): Theatarr's later search's pick when it ranks above the row's own on an RSS or hand grab (JSON) |
| `shadow_run` | import and RSS runs with their JSON summary (30 days) |
| `router_plan_id` on `shadow_grab` and `shadow_rss_pick` (00016) | the router's dry-run plan for that grab or pick |

## Commands

- `theatarr shadow status [-db] [-json]`: the summary, cursors, last runs and the latest ten rows, each with its router
  plan.
- `theatarr shadow run-once -grab N [-instance NAME] [-no-search]`: re-decide one grab by history id now, replacing its
  stored rows. Off the VPN it decides the arr's release only.
- `theatarr shadow run-once -import`: one import; new grabs are decided at once, in this process.
- `theatarr shadow run-once -rss`: one RSS read; refuses off the VPN.
- `theatarr shadow run-once` takes `-db`, `-env-file`, `-json` and `-v`.
- `theatarr shadow reclassify [-db] [-json] [-v]`: classify every stored grab again (above); prints the moves
  (`old bucket/reason -> new`). It searches nothing; it needs the policy for the profiles' quality order.

## Verify a deployment

1. **TRaSH formats.** Profiles that reference TRaSH formats by `trash_id` need a TRaSH-Guides checkout: mount one
   read-only and set `THEATARR_TRASH` (for example `/config/trash`). Without it, those profiles are not built and
   nothing is shadowed; the start log says `shadow: profile not built` and `shadow runner versions=[]`.
2. Start the service, then in the log: `shadow runner versions=[...]`, `shadow job scheduled` for `arr.import` and
   `shadow.rss`.
3. After a minute: `docker exec theatarr theatarr shadow status` shows the cursors (`first_run` in the last import's
   summary).
4. Check one recent grab end to end, with Theatarr's search:
   `docker exec theatarr theatarr shadow run-once -instance <instance> -grab <history id> -v`
   (a history id from the arr's history, or one already in the report). Look for both decisions, Theatarr's pick,
   `searched yes`, and no key in the `-v` lines.
5. After the first RSS read (3 minutes): `theatarr shadow status` shows `last shadow.rss ... ok`; the dashboard's
   `prowlarr` budget shows the requests.
6. As the arrs grab, rows appear in the report; `curl -s http://theatarr:7700/metrics | grep theatarr_shadow_`.

## Tests

- `internal/shadow`: the whole runner over a temporary database, two fake arrs (in memory) and a Prowlarr and a DMM
  feed on the test network (through a real gateway with a passing egress check): the first import only sets the
  cursors; three grabs land in the three buckets (same release, a better-quality release from DMM, a
  below-minimum-score hand grab), each decided against the file of the time; one search per title; RSS picks for
  wanted versions, a non-upgrade refused, items read twice decided once; a pick linked by a later arr grab and one
  expiring into `we_grab_arr_didnt`; the summary, filters, detail and metrics; run-once. The hourly cap defers a
  grab to the next hour and a day-old grab is decided without a search; budget back-offs skip an indexer; the
  classification table and the rejection categories.
- Classification by source (`classify_test.go`, `basis_test.go`): RSS same pick (with and without a better search
  result), RSS better pick, higher score, same quality and score, a lower pick, no pick (accepted, rejected, not
  searched), a pick of a release Theatarr rejects on the second decision, no profile; hand grabs accepted and rejected;
  search grabs from every other source; the profile ranking. On the runner: a movie RSS grab decided
  before Theatarr's RSS read sees the same release (moves to `same_rss_pick`, search kept as `search_found_better`), an
  RSS grab after Theatarr's better pick (`we_grab_another`), reclassify of a row stored under the old rules (and a no-op
  second run, no search, no job); series RSS grabs with and without Theatarr's pick, hand grabs accepted and rejected, and
  a search grab.
- `internal/decide/shadow`: `LoadGrab`.
- `internal/policy`: the `shadow` section, defaults and limits.
- `internal/web`: the API (summary, filters including `search_found_better`, one diff, 400 and 404) and both pages
  with the basis and the search's better release; OpenAPI covers the routes.
- `cmd/theatarr`: `theatarr shadow status` text and JSON (with the router plan), argument errors; `theatarr shadow reclassify` on a
  stored row.
- `internal/shadow` `route_test.go`: `TestShadowPlansNeverWrite` (a plan for every grab and pick, zero provider
  writes, no download and no route job with the switch on) and `TestActiveRSSRoutes` (an active version's RSS items
  routed and added, the shadow version's pick planned only).
- `go test` makes no outside connection; `TestNoOtherOutboundClients`, staticcheck and `-race` pass.

## Known gaps

- **File at grab time** comes from the arr import and history, not from a library scan.
- **Search time:** Theatarr's search runs up to an import interval after the arr's grab (longer when deferred), so a
  newer release can win; `newer_release` marks those. RSS and hand grabs do not rest on it (`search_found_better`).
- **Not searched** grabs (cap held them a day, search off) count as agree when Theatarr accepts the arr's release.
- **RSS mapping** by clean title does not try roman/arabic numeral variants (the engine's mapping does, once a
  candidate is found); an item it cannot place is skipped.
- **Deferred specifications** (queue, blocklist, delay, availability; `docs/design/profiles.md`) are not evaluated, so
  `we_grab_another` can name a release the arr would have held back.
- `import_run` gets a row per instance per import (about 200 a day); `importarr.PruneRuns` keeps seven days and each
  instance's latest run.

## SeaDex standing

Metadata-only runtime and native SeaDex quality waits (prefer-on versions only), including both holds together, use
`metadata_pending`. Live replays and shadow grabs (searched or not) decide with the stored standing; RSS picks match
the router's plan by normalised release identity; held files loaded by the RSS sweep are never rewritten from it (each
prefer-on version resolves them in its own decision). Summary, metrics and go-live exclude these rows from agreement
and minimum sample counts and report them separately. Live unmatched runtime holds use the same classification.
Independent upgrade or other rejections still count normally. How standing is kept:
[decide.md](decide.md#seadex-standing).

RSS catch-up tracks the raw upstream page separately from eligible releases. Page length, duplicate-page detection,
the newest cursor and cursor reach use the upstream items, including rejected identities. Only eligible releases
are decided. Thus one conflict on a 100-item page, or even a fully filtered page, cannot signal feed end or hide
page two, for prefer-off and prefer-on versions alike.
