// Package indexer searches release sources (docs/design/indexers.md): search only, nothing is grabbed here.
//
// Every source is a [Searcher] built from a policy indexer by [New]. Each kind has its own adapter:
//
//   - torznab: Prowlarr's per-indexer Torznab feeds (caps, t=movie/tvsearch/search, torznab attributes);
//   - dmm: Debrid Media Manager's Torznab feed, with its per-key search budget, the 200-result page cap and the
//     "{imdb-tt…}" title suffix stripped;
//   - torrentio: the Stremio stream API by IMDb id, plain config only (no debrid keys);
//   - zilean: its /dmm/filtered API, an IMDb search merged with a title search;
//   - seadex: the releases.moe (SeaDex) best releases per AniList id, public trackers only.
//
// Every request goes through internal/gateway with Target{Class: indexer, Key: <policy key>}, so the host
// allowlist, the VPN egress guard, budgets, back-offs and Retry-After all apply. No adapter builds an HTTP client.
//
// # Releases and privacy
//
// Every result is a [Release]. Its Privacy is a label for display, filters and sorting: the item's own (a Prowlarr
// feed's <prowlarrindexer type>, a catalog definition's type) or else the policy indexer's. Nothing branches on it:
// whether an indexer's releases go to debrid is the user's choice (the version's indexer list and the indexer's
// providers allowlist).
//
// Download links and magnets live only in memory: [Link] prints as a placeholder, marshals to null and logs
// redacted, so a passkey in a link cannot reach a log, the database or JSON output by accident.
//
// # Cache evidence
//
// Which debrid providers hold a release cached (cachedon.go): DMM's cached feeds tag their releases (cached_on);
// [Service.ApplyCachedOn] copies that to every release of the same hash in one search and adds stored evidence
// (provider_cache, state cached); [Service.CheckCached] asks TorBox's checkcached, with our own key and bounded by
// router.cache_evidence.torbox_check, about the best hashes of indexers that a debrid version being
// searched lists with TorBox allowed (versions.K.providers and indexers.K.providers). DMM results are stored as they arrive. Real-Debrid and AllDebrid have no cache check, so DMM is
// their only source: its feeds, and [Service.CheckAvailability], DMM's availability for the releases of a search that
// have no rd or ad evidence yet (availability.go). No debrid key is ever sent to an indexer.
//
// # Single-flight and cache
//
// [Service.Search] runs at most one upstream search per (indexer, normalised query) at a time; concurrent callers
// share it. Successful results are cached for Options.CacheTTL (15 min by default). Errors are not cached.
//
// # Series criteria
//
// A series query with Criteria is one of Sonarr's search criteria (criteria.go): Prowlarr's feeds are asked scene
// numbers, air dates, absolute numbers and scene names in Sonarr's tiers of requests. DMM and the other sources keep
// the plain query in TVDB numbering.
//
// # Deviations
//
//   - Torrentio reports a season pack with the size of its matched file. To scale it, the caller
//     passes the counts (Query.EpisodeCounts) from our metadata; without them a pack keeps Torrentio's file size and
//     is marked size_is_file.
//   - Zilean: the title is not looked up on TMDb from the IMDb id; the caller's Query.Title is used.
//   - SeaDex gives no release names. Titles use the mapped title/season/year and record id, with quality parsed
//     from agreeing feature filenames with explicit source and resolution. Parser defaults are not evidence. This deliberately replaces seadexerr's fixed 1080p remux label; mixed or
//     unidentified features stay Unknown. Its curator flags/scoring remain independent of quality.
//     When feature names omit quality, agreeing provider-reported torrent names for the exact hash may supply it;
//     synthetic arr/search titles are never used as fallback evidence.
//   - Real-Debrid's refused names are not filtered here: the router decides per
//     provider (docs/design/router.md). [RealDebridRefuses] is the rule.
package indexer
