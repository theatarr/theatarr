# The RSS sync (`internal/acquire/rss`)

Theatarr reads the RSS feeds of the indexers its **Live** libraries list, decides what is new for the titles that want
it and hands what it accepts to the download router. A Watch-only library gets nothing from RSS. Where it sits:
[overview.md](overview.md). Code: `internal/acquire/rss`, `cmd/theatarr/serverss.go`.

The names are older than the package and kept: the settings section `shadow` (fields `rss` and `rss_interval`), the
job kind `shadow.rss`, the tables `shadow_rss_seen`, `shadow_rss_cursor` and `shadow_run`, the metrics and the log
lines (`shadow job scheduled`, `shadow rss`).

## When it runs

| Job | When | Priority |
|---|---|---|
| `shadow.rss` | every `shadow.rss_interval` (15 min, the arrs' default), first 3 min after start; also on an arr view's `RssSync` command | -20, the lowest |

The schedule runs as the `rss` serve worker and follows each settings generation by itself: a new interval, `rss`
turned off, or no Live library listing a readable feed stops the queuing without a restart. The job has a dedupe key,
so a slow read never stacks; bulk work (imports, wanted searches, offers) runs first and RSS can lag behind it.

**Pause.** While downloads are paused (`router.enabled` false, the router gate's switch) a run reads no feed and routes
nothing, whether scheduled, queued or asked for: it writes a run row whose summary says `skipped: downloads are
paused` and moves no cursor or seen mark, so the items still in the feeds are read when the pause ends. The film
index is reloaded all the same.

## How a run works

- **Feeds:** the torznab (Prowlarr) indexers, SeaDex's recent feed and the catalog (Cardigann) sources the Live
  libraries list. Like the arrs, every item of every page read is evaluated each sync, those below the previous feed
  head too, so an item rejected for a temporary reason is decided again while it stays in the feed. A durable
  per-feed cursor (the head's key and publish date) only bounds catch-up paging: pages are read up to the one holding
  the previous head or an item published before it, at most ten. Page length and cursor keys come from the upstream
  page as sent, before any filtering (Torznab's repeated-GUID dedupe, identity conflicts, eligibility). The cursor
  moves only after routing, in the same transaction as the seen marks, so an error leaves both and the next sync
  reads the same pages. At the ten-page cap the cursor moves on to the new head at once and the run reports the gap
  (`gap` in the run summary, a warning in the log), as the arrs do. A feed that ends without the previous head
  reports the gap too. DMM's feed is its newly cached list, read on a DMM path naming `rd` or `ad`
  ([indexers.md](indexers.md)); a path naming both is two feeds, each with its own cursor and seen marks. DMM dates
  its list by the build that listed each release, so many items share one `pubDate`, which the cursor handles. A DMM
  path whose only provider is `tb` has no feed. Torrentio and Zilean have no RSS feed (they answer per title). A
  catalog source's feed is its definition's search without keywords, one page with no offset
  ([cardigann.md](cardigann.md#rss)); one that cannot be read is listed in the run summary with `skipped` and the
  reason. Budget-capped or backing-off feeds are skipped too.
- **Seen items:** immutable release or profile rejections are remembered by hash (or title and size) and policy
  revision for 14 days. Unknown titles, unaired packs, availability, existing-file rejections and routed items are
  reconsidered each sync; duplicate items within one sync are decided once, at most 1,000 items per feed.
- **Categories:** RSS items without categories are decided as movies only, as in the arrs. TV categories
  (5000–5999) select series decisions. Anime category 5070 permits standalone movie decisions, but episode, season,
  special and batch syntax stays in the series path even if no series matches. A bare film year parsed by Sonarr as
  compact season/episode numbering (1988 as S19E88) still permits the movie path.
- **Mapping:** the item's IMDb or TMDb id when the feed gives one, else the parsed title's clean title against the
  titles' names, original titles and aliases, filtered by primary or secondary year. Without a parsed year the name
  must identify one movie across the library. Series match by TVDB id, else by the title and its scene mappings. The
  film index (shared with the decision engine, which searches with it) is reloaded at the start of every run, so a
  film added since is matched.
- **Decided** for every monitored Live library of the title, including `cutoff_met`, with the library's current file.
  Per-file upgrade checks reject replacements that do not improve the full quality/score cutoff. Movies must reach
  their minimum availability first.
- **Routed:** the accepted items are ranked per title and library for the whole sync and handed to the router as one
  `router.route` job (source `rss`), unless every candidate overlaps an in-flight download. The router skips
  individual overlaps and places the disjoint releases in the same job. The grab journal records what the read decided
  between. The router's gate decides whether it writes (a run is skipped altogether while Pause is on). The summary
  counts `routed` and `route_skipped`.

## SeaDex standing

A run decides with the stored SeaDex standing, pinned once for all feeds and libraries after the feeds that answered
have stored theirs ([decide.md](decide.md#seadex-standing)). Catch-up tracks the raw upstream page separately from the
eligible releases: page length, duplicate-page detection, the newest cursor and cursor reach use the upstream items,
including rejected identities, and only eligible releases are decided. So one conflict on a 100-item page, or a fully
filtered page, cannot signal the feed's end or hide page two.

## Settings (`shadow`, optional)

```yaml
shadow:
  rss: true
  rss_interval: 15m        # default; minimum 10m
```

## Metrics

| Metric | |
|---|---|
| `theatarr_shadow_indexer_requests_total` | upstream indexer requests of the RSS reads (cached answers not counted) |
| `theatarr_shadow_rss_items_total` | new RSS items decided |

## Tables

| Table | Holds |
|---|---|
| `shadow_rss_cursor` | per feed, the head's key and publish date |
| `shadow_rss_seen` | feed items already decided (14 days) |
| `shadow_run` | one row per run with its summary (30 days) |
