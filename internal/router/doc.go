// Package router is the download router (docs/design/router.md, docs/migration.md for cutover per area). Given the accepted,
// ranked releases of one search for one version of a title, it adds the best one to a debrid provider, in the
// version's provider order, and then follows it until the provider holds it.
//
// # Gate
//
// The router writes to a provider (adds a magnet, selects Real-Debrid files) only when router.enabled is true in the
// policy (the global kill switch, off by default) and the version's mode is active. [Router.Gate] is that rule, and
// every write passes it last. Otherwise [Router.Route] runs the same decisions as a dry run, sends nothing to any
// provider, and records what it would have done in router_plan for the shadow report. [Router.Plan] is the same dry
// run for any version, recorded nowhere (theatarr router dry-run). theatarr serve logs the switch and the active versions at
// start ([Router.LogState]).
//
// # One route
//
// For each candidate, best first (at most router.max_candidates):
//  1. A release with no v1 info hash is skipped (a .torrent is never fetched or sent; adder.NewMagnet makes the
//     only magnet: bare, plus the policy's public trackers when any). Privacy is a label: a private-labelled
//     indexer's releases go where the version (and the indexer's providers allowlist) says.
//  2. Cache evidence orders the version's providers that the indexer allows (indexers.K.providers; empty means all):
//     those that evidence says hold the release cached (the release's CachedOn and cached_on, filled per search by
//     indexer.Service.ApplyCachedOn, CheckAvailability (DMM's availability) and CheckCached; live provider_cache rows in state cached) go first, each group in the version's order. The change is a step
//     (ordered/cached_first) in the plan and a log line. Evidence never skips a provider: one without it is tried
//     exactly as before.
//  3. For each provider, in that order:
//     - no API key: skipped, and the provider does not count;
//     - Real-Debrid and its name rule refuses the title (indexer.RealDebridRefusesRelease): skipped, a miss there;
//     - a live miss for the hash at the provider (provider_cache): skipped, a miss; known uncached and
//     router.cached_only: skipped, a miss (a later listing on DMM's newly cached RSS reopens our own uncached
//     verdict once: indexer put);
//     - the provider already holds the hash (registry from the provider sync, or a download of ours): the download
//     points at that torrent and nothing is added;
//     - paced or backing off (router_pacing): deferred until it is free;
//     - TorBox with cached_only: its cache check (a read) first; uncached is a miss;
//     - Real-Debrid and AllDebrid with cached_only: the hash's presence on the account is noted first (registry, our
//     downloads, AllDebrid's magnet list: a hash there is reused), and after the add the status is read back
//     (uncached.go): a cached add is evidence (add:<provider>); an uncached add provably ours is deleted and the next
//     provider tried (uncached_deleted; on AllDebrid only with router.alldebrid_delete_uncached, otherwise it stays
//     there and the next provider is tried, uncached_kept); one that is not provably ours stays (uncached_kept_shared);
//     - the add; its error code decides (docs/design/router.md has the table): accepted ends the route; a content code
//     (not cached, blocked, invalid) is a miss there and the next provider is tried; an account or rate code backs
//     the provider off (or cools it down) and the next is tried.
//  4. A miss on every provider that has a key: the hash is cached as a miss for router.miss_ttl, the download row
//     fails with that reason, and the next candidate is tried without searching again. When every candidate missed,
//     the title is searched again at once (research.go). A candidate skipped without a request does not count
//     against router.max_candidates.
//  5. No acceptance but some provider was only paced: the route is deferred to the earliest free time (job
//     router.route re-enqueues itself, resuming the queued download).
//
// # Upgrade approval
//
// Before any candidate is tried, versions[*].upgrades is applied to releases that would replace a file the version
// already has (hold.go): auto routes them, off skips them, approve holds the best one per episode set in
// upgrade_hold instead of adding it. A manager's approval (Holds.Approve) enqueues a route of the held release that
// is not held again and passes every rule above. Releases for items without a file are never held.
//
// # Downloads
//
// One download row per release tried (migration 00014): queued, adding, added, downloading, ready, then the library
// sets importing and imported; or failed with the reason. Every provider considered is a download_attempt row.
// [Router.Poll] reads added and downloading downloads back from their provider (read-only Provider.Torrent): it
// selects Real-Debrid's files when RD waits for them (every playable file, movie or series: playable.go), records
// progress and the provider's name, and marks ready or failed. A download still in added router.stalled_after past
// its add fails as stalled and is blocklisted (stalled.go). A torrent that left the provider fails its download
// with that reason and is blocklisted, as are provider error statuses and torrents without video. Downloads still ready past router.ready_timeout fail and are blocklisted unless a live library
// claim holds them (failure.go). Blocklisted failures are searched again at once (research.go).
//
// # Deletes
//
// The router deletes on a provider in two cases only, through adder.Deleter and adder.StalledDeleter, the only
// deletes in Theatarr: an add of this route attempt on Real-Debrid or AllDebrid that turned out uncached with
// router.cached_only (AllDebrid also needs router.alldebrid_delete_uncached, default off), and a stalled
// Real-Debrid add. Both need proof that the entry is a new one of ours (the hash
// was not on the account before the add, the id is new); a reused or possibly shared entry is never deleted. A
// cached-only movie on Real-Debrid whose every playable file came back uncached is added once more with its
// largest video alone (altRetry): that second add is deleted the same way when it is uncached too. The torrent
// manager's restore uses the same delete for its own one retry (internal/torrents).
//
// # Deviations from the design
//
//   - The design routes through decypharr with a per-add debrid field. The router adds to the providers directly
//     (through the gateway), with decypharr's error codes widened for the cases decypharr folds into plain errors
//     (account hold, rate limit, auth). decypharr stays a client of the arr views.
//   - Cached-only on Real-Debrid and AllDebrid is decypharr's add, read, delete (neither has a cache check), with a
//     stricter guard than decypharr's: an entry the router cannot prove its own stays and downloads (it counts
//     against uncached_adds_per_hour).
//   - Real-Debrid's 451 on a name its rule does not refuse is an account hold (a cooldown), not a verdict, because
//     RD answers every add that way during short account-wide blocks.
//   - A miss is only declared when every provider gave a verdict; a provider that is only paced defers the route
//     instead of letting a lower-ranked release win.
package router
