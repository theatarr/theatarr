// Package tvdb is a TVDB v4 client (docs/design/tvdb.md): episodes, airsBefore/After, absolute and alternate orders;
// /updates drives refresh. It replaces SkyHook and the Sonarr-episode stand-in behind the anime engine's TVDB interface
// ([Client.Show]), and offers episode lookups to the series side (package tvdbdecide).
//
// Endpoints: POST /login; GET /series/{id}/extended (short, meta=translations); /series/{id}/episodes/{season-type}
// and .../{lang}, every page; /episodes/{id}/extended (airs fields of specials); /seasons/{id}/extended;
// /seasons/types; /search; /search/remoteid/{id}; /updates. The record shapes (types.go) were written from
// knowledge of TVDB's v4 OpenAPI (https://thetvdb.github.io/v4-api/), not fetched from it, and keep only the fields
// we read; the test fixtures are hand-written in the same shapes.
//
// Auth: the project API key (and, for a user-supported key, the subscriber PIN) goes to /login once; the bearer
// token is kept in memory for TokenLifetime (TVDB's last a month) and renewed on a 401, once per request. The key,
// PIN and token are never logged, put in an error, or part of a cache key; nothing secret is stored.
//
// HTTP goes through the gateway (Options.Doer; host allowlist, the tvdb budget, back-offs) via the anime engine's
// live.Fetcher, so the cache and breaker behave like every other metadata source: answers in metadata_cache (source
// "tvdb", gzip, ETag/Last-Modified revalidation where TVDB sends them), a time to live per answer (an airing or
// upcoming series a day, an ended one a month, searches a week), 404s cached a day, a breaker that skips TVDB after
// three failures (5 minutes, doubling to an hour) and stale answers served meanwhile. With nothing cached and TVDB
// down a call fails, and anime.TVDBChain falls back to the next backend.
//
// Refresh reads /updates since the last run (watermark in tvdb_sync, migration 00019) and marks the cached answers
// of changed series and episodes stale; nothing is fetched for series not already cached.
//
// Differences from SkyHook (what Sonarr gets): no aniListIds; titles are the English translation where TVDB has one
// (SkyHook's choice too), else TVDB's own name; absolute numbers come from the absolute order when the series has
// one.
package tvdb
