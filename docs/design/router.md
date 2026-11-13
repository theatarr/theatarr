# The download router (`internal/router`, `internal/provider/adder`)

The router takes the decisions for a version and adds the chosen release to a debrid provider or a download client.
Where it sits in the pipeline: [overview.md](overview.md). Live and Watch-only libraries: [overview.md](overview.md).
Code: `internal/router`, `internal/provider/adder`, migration
`00014_router.sql`, the policy's `router` section, `internal/arrview/queue.go`, `cmd/theatarr/router.go`, a few lines in
`cmd/theatarr/serve.go`, acquisition fanout in migration `00033_acquire_fanout.sql`, and the upgrade rule
(`internal/router/upgrades.go`).

The provider tests run on fixtures written from the providers' API docs and decypharr's source.

## The gate

The router writes to a provider (adds a magnet, selects Real-Debrid files) only when **both** hold:

1. `router.enabled: true`. With DB settings it is derived from **Pause** (true when Pause is off; Admin header,
   `PUT /settings/switches {paused}`); without the section it is off. The other `router` options are a settings
   section that applies on save (the poll loop restarts on a changed interval).
2. The version's `mode` is `active` (Live). A Watch-only version (`mode: shadow`) gets dry runs only. There is no
   per-library downloads switch (the former `grab` field went in migration 00122: a Live library without it became
   Watch only). The automatic searches (the search schedule, re-searches) run only for the Live versions while Pause is off
   (`Policy.SearchVersions`). User-invoked searches (interactive, arr commands) still run and their routes are dry
   runs while the gate is closed. `theatarr router status` shows Pause and each library's state and whether it
   downloads.

`Router.Gate` is that rule (`Policy.Grabs`), and every write passes it last: the add in `Route`, and the file
selection in `Poll`. A router of an older settings generation also asks the published runtime's gate
(`SetCurrentRuntime`), so turning Pause on (or a library to Watch only) stops a route already running at its
next write: the download it holds ends (`route stopped: ...`) and nothing is added (`TestPauseStopsRunningRoute`).
Preparing and publishing that generation never waits for the running route: `New` copies the previous generation's
account-change grace, and `SetMountCheck` / `PublishProviderChanges` set theirs, under each router's own small lock,
never the shared route lock. So a save turning Pause on while a route is sending is published at
once, and the route's next add is refused (`TestPausePublishedDuringRunningRoute`).

When the gate refuses, `Route` runs the same decisions as a **dry run**:
- it sends nothing to any provider and makes no cache check;
- it records what it would have done in `router_plan`: mode `shadow` or `disabled`, the release, the provider, and
  every step with its reason, for the journal (`plan` entries);
- a queued download it resumes ends (`failed`, `route ended: ...`), as a switch to shadow ends it through the
  freshness check. It is not resumed every 10 minutes while Pause is on and then
  grabbed whenever they come back on; the next search grabs the title then (`TestGrabOffQueuedDownloadIsEnded`).
  When no add was ever sent for it to a provider or client (no such `download_attempt`), Activity shows it as Not
  started (`activity.NotStarted`) and it does not fail a request; one tried before (a refused add waiting for its
  retry) stays a failure.

`Plan` is the same dry run for any version, recorded nowhere (`theatarr router dry-run`).

`theatarr serve` logs the gate at start, at WARN:
- `ROUTER DISABLED: router.enabled is false; ...`;
- `ROUTER ENABLED but no version is active`;
- `ROUTER ENABLED: adds go to debrid providers for the active versions` with their names.

**Proof (tests, `go test ./internal/router`).** The fake adders log every call, and they are the only writer the
router has.
- `TestShadowVersionNeverWrites`: switch on, version in shadow. Zero adds, no download rows, one `router_plan` row,
  and the poller doesn't select files for a shadow download.
- `TestKillSwitchBlocksWrites`: section missing, then `enabled: false`, version active. Zero adds and zero
  selections, and `disabled` plans are recorded. `Plan` never writes, even when the gate would allow it.
- `TestDeferredRouteReEnqueues`: a shadow version's `router.route` job records a plan and enqueues nothing.
- `TestPrivateLabelFollowsIndexerProviders`: privacy is a label: a private-labelled release of an indexer
  the version lists is routed like any other, only to the providers its indexer allows (`indexers.<key>.providers`).
- `web.TestPauseClosesBothGates`: with Pause on, `Router.Gate`, the library gate and `catalog.Searchable` all refuse
  a Live library; with it off all three let it through.

**Before a route opens a download**: a candidate every usable provider would skip anyway (a recorded
cache miss, uncached under `cached_only`, or a release Real-Debrid refuses) is skipped with step `known_miss` and
opens no download row (`TestKnownMissOpensNoDownload`).

**Policy changes while a route waits**: every route job carries the policy revision it was decided
under (`decision_rev`). A deferred automatic route that runs under another revision decides its candidates again
with the running decision engine: a candidate now rejected is skipped (`rejected_now`), an accepted one carries the
new quality, score and formats, and the accepted ones are tried in the order the new decisions rank them
(`decide.Rank`). Without a decision engine the route is skipped (`stale_decision`). Manual and
interactive grabs are not decided again (`TestDeferredAutomaticRouteRedecidedAfterPolicyChange`,
`TestRedecisionRanksCandidatesAgain`). A series release the new decision maps to other episodes than it was queued
with (a scene/XEM mapping changed while it waited) takes the new episodes and is checked for freshness again before
any add (`TestDeferredRouteTakesNewEpisodeMapping`). A resumed series route that is not decided again (SeaDex
preference off, same revision) still has each RSS release mapped again: only its episodes change, on an accepted
decision (`remapResumed`, `TestDeferredRemapWithoutSeaDex`).

A resumed queued download takes its candidate as the route refreshed it (episodes the freshness check dropped, a new
decision's quality, score, formats, episode mapping and policy revision) in the write that resumes it, before any
add, so the library imports what was placed (`TestResumedPackPersistsNarrowedEpisodes`). A resumed
download no candidate of its route took up (another release ranked first and was placed or waits under its own row,
or its release was skipped: blocklisted, in flight elsewhere, held) ends (`route ended: its release was not placed by
the route that resumed it`) instead of being resumed by the stray sweep forever
(`TestRedecisionEndsSupersededQueuedDownload`).

A series freshness check reads episode monitoring and current files together once for the title/version, then
reuses that snapshot across candidates and repeated episodes. A new check reads them again; the snapshot is never
cached across routes. Current files are still scored under the running profile.

The freshness check (is the release still an upgrade of the current file) and the movie queue check judge with the
decision engine's compiled profile (`SetProfiles`: `decide.Engine.Profile`), with its minimum score, cutoff score and
minimum upgrade step; a router without an engine builds the same thresholds from the policy (`TestFreshnessUsesProfileFormatThresholds`, `TestFreshnessUsesEngineProfile`). With the engine's profile the
current file's custom format score is recalculated under it from the file's stored inputs (`decide.ScoreFileWith`,
as the decision compares an existing file), not taken from `link.score` or `version_file.custom_format_score`, which
are the import-time profile's: after a profile or TRaSH score change the router no longer cancels as
`no_longer_upgrade` an upgrade the decision accepted (`TestFreshnessRescoresCurrentFile`). Without the engine's profiles the stored score stands.

Form and JSON adder responses share bounded reading and body closure (1 MiB). A 429's numeric `Retry-After` is
returned directly to the provider decoder as a duration. Confirmed user-delete JSON requests keep their existing target and refusal classification.

## Provider writes (`internal/provider/adder`)

The writes live in their own package. `internal/provider` stays GET-only: its `TestOnlyGET` and
`TestNoWriteMethods` still pass unchanged, so code that holds a `Provider` can't add anything.

| Provider | Add | Files |
|---|---|---|
| Real-Debrid | `POST /torrents/addMagnet` `magnet=` → 201 `{id}` | `POST /torrents/selectFiles/{id}` `files=1,3` → 204, or 202 when already done |
| AllDebrid | `POST /v4.1/magnet/upload` `magnets[]=` → `data.magnets[0]` `{id, hash, name, ready}` or `{error}` | takes every file |
| TorBox | `POST /api/torrents/createtorrent` with `magnet=`, `allow_zip=false`, `seed=3` (never seed), and `add_only_if_cached=true` when cached only | takes every file |

- **TorBox never seeds.** Every TorBox add (routes and the torrent manager's restore) sends `seed=3`: with seeding on
  a finished torrent holds an active slot for up to 30 days, and any value sent replaces the account's Seeding
  setting ([notes: TorBox limits](https://notes.debridmediamanager.com/zurg/providers/torbox-limits/)).
- **Magnets are bare.** A magnet is `magnet:?xt=urn:btih:<hash>`, plus `router.trackers` when any (default none).
  - No `.torrent` is ever sent: no method takes one.
  - Only `adder.NewMagnet` makes a `Magnet`. It refuses a missing or non-v1 hash and a keyed `router.trackers` entry.
  - Every adder refuses the zero `Magnet` without a request.
- **Privacy is a label.** Nothing branches on `indexers.<key>.privacy`:
  whether an indexer's releases go to debrid is the user's choice, made by listing it in a version
  (`versions.<key>.indexers`, decide's `IndexerNotInVersion`) and optionally limited per indexer by
  `indexers.<key>.providers` (empty: every provider). The router walks `Policy.ReleaseProviders(version, indexer)`:
  the version's provider order filtered by that allowlist; none left is a skip ("indexer X allows none of the
  version's providers").
- **A manual add has no indexer.** A release a person adds by magnet or hash (`POST titles/{id}/releases/add`,
  admins; `arrview.Service.AddRelease`) was found by no search. It carries `policy.ManualIndexer` (`manual add`) where
  the indexer goes, because the adders refuse a magnet that names no source; it is not an indexer of the policy and,
  having a space, cannot be the key of one. Such a release is not held to the version's indexer list (decide's
  `IndexerNotInVersion` and the retry's check skip it) and gets the version's whole provider order
  (`ReleaseProviders`). Everything else is a grab by hand: the same decision (rejected needs `override`), the source
  `ui:grab:<who>`, the same gate, blocklist, pacing, cached-only settle and ledger of our own adds. Its size and
  seeders are unknown (0 and -1); a size limit set for its quality is not checked and does not reject it, as for a
  searched release with no size (the person picked the release). Only the name the person gave and
  the hash are used; the magnet's trackers are not sent. An offer to another library declines it
  (`unknown_indexer`).
  - *One marker, one writer.* Only `AddRelease` writes the placeholder (`policy.IsManualAdd` reads it). A release
    push that names it as its indexer is refused (400), and so does the decide API; search results carry a policy
    key and stored downloads keep what they were queued with (an Activity retry stays a manual add).
  - *The hash may be another tool's.* The account is shared, and a hash another tool already holds is this
    feature's ordinary input. When the add's answer is lost and the hash is then found on the account ("Crash
    between add and record"), the entry is adopted as **reused**, whoever added it: no files are selected on it and
    nothing deletes it. (An add of our own whose answer was lost is then left without a selection too:
    it stalls and fails, the torrent kept; accepted.) `TestReconciledManualAddIsReused`.
  - *A failed manual add starts no search.* The person named one release, not "any release of the title". It fails
    as a grab by hand fails: a miss on every provider records the miss (cache-miss evidence for `miss_ttl`, no
    blocklist entry), and a download that fails later (stalled, provider failure, ready deadline, dangerous files,
    a library guard) is blocklisted for the version. But neither queues the re-search of "Re-search after a
    failure" (`manualAdd` in `research.go`; the blocklist row is marked researched). A release grabbed by hand
    from a search is searched again as before. The search schedule still searches a title that stays missing,
    when its switch is on. `TestFailedManualAddStartsNoSearch` (e2e),
    `TestMissedManualAddStartsNoSearch`, `TestStalledManualAddStartsNoSearch`.

  `TestManualAddByHash` (e2e) runs it from the add to the filled title.
- **One delete, no restart, unrestrict or link request.** The only deletes in Theatarr are in `adder/delete.go`, outside the
  `Adder` interface: `Deleter.DeleteOwnUncachedAdd` (Real-Debrid and AllDebrid) and `StalledDeleter.DeleteOwnStalledAdd`
  (Real-Debrid only) for the router, and `UserDeleter.DeleteUserChosen` for the torrent manager only (torrents a person
  chose and confirmed, after a backup; `docs/design/torrents.md`, `TestOnlyTorrentsDeletesChosen`). `DeleteOwnUncachedAdd` runs on AllDebrid only with `router.alldebrid_delete_uncached`; see
  "Cached-only on Real-Debrid and AllDebrid" and "Stalled downloads". `TestNoDeletes` checks
  the method sets (TorBox has neither router delete, AllDebrid no stalled delete), the paths (`/torrents/delete/`,
  `/v4/magnet/delete`, `/api/torrents/controltorrent`, in `delete.go` only) and the HTTP methods (DELETE in `delete.go` only, no PUT or PATCH).
- The key goes in the `Authorization` header only. Error messages go through `provider.Redactor.Sanitize`, so
  AllDebrid's `AUTH_BLOCKED` address is dropped.

### Error codes and what the router does

The first four codes are decypharr's (`internal/customerror/http.go`). The rest separate cases decypharr folds into
plain errors.

| Code | From | Router |
|---|---|---|
| `torrent_not_cached` | TorBox 400 `DOWNLOAD_NOT_CACHED` (with `add_only_if_cached`) | Miss at this provider. `provider_cache` gets `uncached` for `miss_ttl`, and the refusal counts toward TorBox's uncached adds per hour. Next provider |
| `torrent_blocked` | RD 451 or error 35 on a name RD's rule refuses; RD 26, 28, 29; TorBox `DOWNLOAD_TOO_LARGE`, `BOZO_TORRENT` | Miss at this provider. Next provider |
| `invalid` | RD 2, 30; AD `MAGNET_INVALID_URI`, `MAGNET_NO_URI`; TorBox `INVALID_OPTION` | Miss at this provider. Next provider |
| `too_many_active_downloads` | RD 509 or 21; AD `MAGNET_TOO_MANY_ACTIVE`; TorBox `ACTIVE_LIMIT` | Exponential back-off (1, 2, 4 … min, up to `backoff_max`). Next provider |
| `rate_limited` | HTTP 429, RD 5 or 34 | Exponential back-off; `Retry-After` wins when it is later. Next provider |
| `traffic_exceeded` | RD 23, 36; AD `MAGNET_TOO_MANY`; TorBox `MONTHLY_LIMIT`, `COOLDOWN_LIMIT` | Back off straight to `backoff_max`. Next provider |
| `auth` | RD 8, 9, 14, 15, 22, 401, 403; AD `AUTH_*`, `MAGNET_MUST_BE_PREMIUM`, `MAGNET_NO_SERVER`, `NO_SERVER`; TorBox `AUTH_ERROR`, `BAD_TOKEN`, `NO_AUTH`, `PLAN_RESTRICTED_FEATURE` | Back off straight to `backoff_max`. Next provider |
| `account_hold` | RD 451 or error 35 on a name RD's rule does **not** refuse | The hash is blocked at this provider for `miss_ttl` (`provider_cache` `miss`, source `router:451`). Only a 451 on a hash of another title from another 451 within 10 minutes, or one during a running hold (`router_refusal_451`, migrations 00084 and 00086), holds the account instead: cooldown, escalating 5 → 15 → 60 min while it repeats (floor: the provider's `cooldown`; see "Pacing"); such a 451 blocks no hash. After 451s on 3 different hashes of one title within `miss_ttl`, the title's other releases are skipped there (`title_451`). Next provider, else the next release |
| `unavailable` | Gateway refusal (back-off, budget, VPN), transport error, 5xx, RD 6 or 25, AD `GENERIC` or `MAINTENANCE` | Exponential back-off; the gateway's `RetryAt` wins. A gateway refusal (never sent) is no add and no failure: deferred to its `RetryAt` (else 1 min) without back-off. Next provider |
| `unknown` | Anything else, including bare HTTP 4xx and unrecognised provider codes | Back off the provider as `unavailable`. Only recognised per-release codes count as a release miss; a definite refusal of an add alone does not prove a problem with that release. |

Bare HTTP 451 without a recognised RD refusal code also backs the provider off; it does not poison the hash.

**Why `account_hold`.** Real-Debrid answers every add with 451 `infringing_file` during short blocks on the whole
account: seconds to minutes, after bursts (zurg's maintainers measured this). It also refuses single releases with
451 every time, and its `infringing_file` blocks are usually per title: most releases of a title it has blocked get 451. So
one 451 blocks only its hash on RD and the route moves on; only 451s on hashes of two different titles within 10
minutes hold the account. The same release refused again, or the next release of the same title, never counts as a
hold, so a title RD refuses cannot pin RD on the hold ladder (`TestOne451BlocksTheHashNotTheAccount`,
`TestTitleAll451DoesNotHoldAccount`). After 451s on 3 different hashes of one title within `miss_ttl` RD is skipped
for the title's other releases (step `title_451`, with the count), so a route does not spend an add on each of them.
A 451 that holds the account, or comes during a running hold, is the hold's and not the release's: its hash is not
blocked, the window's earlier 451s lose their block too (the hold explains them), and none of them counts against its
title; the resumed route tries the same release again after the hold (`TestHeld451DoesNotBlockHash`).
Refusals already explained by a hold do not start another one after it ends. The all-provider miss retains
`router:451` provenance, so a later explaining hold lifts it too; a hold started by torrent restore uses the same
cleanup. The explained hashes are removed in one database delete.
A name `indexer.RealDebridRefuses` matches is skipped before any request (`rd_name_rule`). The torrent manager's
restore records its 451s the same way (with no title: any other hash's 451 within 10 minutes holds the account):
one fails that torrent only.

## One route

The input is a `Request`:
- the title, the version and its row id;
- the accepted decisions, ranked by `decide.Rank`, built by `router.Candidates`;
- `router.SearchRequest` does search, decide and candidates in one call.

Candidate order is `decide.Rank`'s: the arr comparer with Theatarr's cache tie-break after the format score
("Deviations", "Cache tie-break"). Its evidence is what step 4 orders the providers by: the release's `CachedOn` and
`cached_on` extra, which the search fills from the feeds, DMM's availability, stored `provider_cache` rows in state
`cached` and the TorBox check. The TorBox check runs on the ranked releases (`checkTop`); when it adds evidence to any
of them the search ranks again, so the order the releases page shows (`#`) is the order the route tries
(`TestCheckTopEvidenceReranks`). RSS applies the same release-carried and stored evidence
before deciding (`ApplyCachedOn`; no DMM or TorBox request: `TestRSSStoredCacheBreaksTie`). A route decided again
under a newer policy revision reads the stored rows again for its candidates first (stored evidence only:
`TestRedecideReadsStoredCacheEvidence`). Per-account stored rows order the accounts; their projection onto provider
kinds only ranks: in RSS and the re-decision the routed or planned candidates keep the releases
as read or queued, and step 4 orders the provider accounts from its own per-account rows
(`TestRSSKeepsAccountOrder`, `TestRedecideKeepsAccountOrder`).

At most `router.max_candidates` releases are tried, best first. A release skipped without a request to any provider
(a live miss or known uncached everywhere, blocklisted, no provider its indexer allows, no hash) does not count, so a search made again after
a failure reaches releases the first route never tried. A release placed does not count either (a season of
single-episode releases fills in one route); each placement still counts against the usual provider pacing and
gateway budgets. For series, walk the ranked candidates and place every release whose episode
set is disjoint from releases placed or deferred in this batch or already in flight for the version; a pack covers
all its mapped episodes. A release that misses everywhere reserves nothing, so its overlapping alternatives remain
eligible. Movies still stop after one placement.

Season targets still make one search, including when only some wanted episodes are in flight. Dry-run plans record
every placement in their steps and simulate pacing between adds without writing it. A deferred batch retries from
its first deferred release, retaining the later alternatives and resuming queued rows without duplicating successful
placements. For each release:

Automatic wanted, RSS, research, offer and resumed routes recheck current monitoring, movie availability, episode
removal and files before placement; stale candidates are skipped and a queued download is cancelled when none remain.
Explicit grabs keep their override behavior.

1. **Skip it if it can't go to debrid:** its indexer allows none of the version's providers (`indexers.<key>.providers`), or no v1 info hash. A `.torrent` is never fetched; a
   hash-less release is skipped (`no_hash`): `.torrent` files are not fetched.
2. **Skip it if it is in flight:** this version and title already hold the same hash in hand (`in_flight`). A
   `queued` row with no `router.route` job queued or running for it is not in flight but **stranded** (a row from a
   build before the row and its follow-up were written together, a route that died before it deferred, or a
   follow-up that ran out of attempts): the route resumes that row instead (`TestRetriedRouteResumesStrandedQueuedRow`;
   the poller resumes strays too, step 8).
   **Movies: another release in flight** (Radarr's `QueueSpecification`, `queue.go`). A movie
   version gets no second release while another hash of the same title and version is in flight (`queued`, `adding`,
   `added`, `downloading`, `ready`, `importing`), unless the new release is an upgrade over the queued one by the
   version's profile: refused when the queued release already meets the cutoff, when the new one is no upgrade of it
   (`IsUpgradable`), when the profile does not allow the upgrade (`IsUpgradeAllowed`) or `versions.<key>.upgrades` is
   off. A release that cannot be compared (no profile, an unknown quality) is refused: it fails closed. Skipped
   `in_flight` ("download N holds another release of the movie: …"), the next candidate is tried, and a route that
   placed nothing ends `in_flight` on that download. Every automatic source is checked (RSS, searches, re-searches,
   offers, release pushes, deferred jobs; dry runs too). A release push (`arrview:push:<view>`, autobrr) was decided
   against the current files when it was pushed; one deferred behind a paced provider gets the freshness check when
   it resumes (still wanted, still an upgrade of the current file, decided again under a new policy revision), as an
   RSS route does (`TestDeferredPushRechecksFreshness`). A person's grab is not (`ui:grab:`, `ui:retry:`,
   `arrview:release:` and upgrades approved on an older build `upgrade:`), as Radarr lets an interactive grab through. Tests:
   `TestMovieSecondReleaseWhileFirstInFlight`, `TestMovieQueuedReleaseUpgradeAndManual`,
   `TestMovieInFlightIsPerVersion`.
3. **Open a download row** (`queued`), or resume the queued row of a deferred or stranded route. A route that
   resumes a download which was ended meanwhile (removed from the queue, failed, imported) stops there, `cancelled`
   (`download_moved_on`): no new download replaces it ("Guarded transitions and queue removal"). A stranded row taken over
   by a candidate that found the same hash on another indexer becomes that candidate's in the same write, before any
   add: its indexer, title, source, size (none when the candidate has no known size) and stored release are
   rewritten, and the row dates from the takeover (`created_at`), so the journal shows the taking route's decision. A
   route resuming its own row changes none of them (`TestStrandedRowTakenOverByAnotherIndexer`,
   `TestTakeoverWritesCandidateSize`).
4. **Order the providers by cache evidence** (below): providers that evidence says hold the hash cached go first,
   each group in the version's order. When that changes the order, the plan gets a step `ordered`/`cached_first`
   ("cached on ad (dmm-ad): tried first; order ad, rd, tb (version order rd, ad, tb)") and the log a line
   `providers reordered by cache evidence`. `PlanSummary` leaves the step out.
5. **Try each provider, in that order:**

| Check | Verdict |
|---|---|
| No API key | `no_key`: the provider doesn't count |
| RD and its name rule refuses the title | `rd_name_rule`: miss |
| `provider_cache` has a live `miss` | `miss` |
| `provider_cache` has a live `uncached`, and `cached_only` is on | `known_uncached`: miss |
| `router_pacing` has the provider backing off because it is **down** (`backoff_down`: unreachable, 5xx, its maintenance, or downloads that stalled while it was down: provider unavailable) | `paced`: deferred until the back-off ends, **reuse included**: a route queued again after an outage does not go straight back to the provider that was down. A back-off from refused or rate-limited adds does not stop a reuse (it needs no add; skipping it would add the hash again elsewhere): it is checked with the add pacing below |
| The provider already holds the hash (registry `content_provider` from the provider sync, or one of Theatarr's downloads; with DB settings only a download recorded under the current provider epoch, see docs/design/settings.md), and a read of that torrent id finds it with the hash (an entry not found or holding another hash is not reused; its registry row is marked `absent`, a download's stale id is passed over and the next known id tried) | `reused`: the download points at that torrent, and **nothing is added**; a reuse sends no add and takes no add-pacing slot (gap, rate, cooldown and hold are checked after it) |
| The read of that known torrent id fails otherwise (transport error, 5xx, gateway refusal), and fails again when retried once 2 s later (within the route's or pass's deadline; `TestHeldReadRetriedOnce`): presence not verified | `held_unread`: not reused, deferred 1 min; the route tries the next provider. When the read was only paced (a 429, or the gateway's request budget or VPN check refused it) the provider is not shown down, and adding the release elsewhere would leave the torrent it holds unreferenced: the whole release waits (until the refusal's retry time, at least 1 min) and no other provider is tried for it in this route (`TestPacedHeldReadDefersRelease`). The gateway's back-off after failed requests still counts as the provider down |
| `router_pacing` says wait (add pacing) | `paced`: deferred until then |
| TorBox with `cached_only`: its cache check (a read, cheaper than a refused add) says uncached | `known_uncached`: miss; `provider_cache` `uncached` (source `router:check`) for 30 min, at most `miss_ttl` (a read is cheap; DMM keeps its TorBox misses as long) |
| AllDebrid with `cached_only`: a fresh read of the account's magnet list shows the hash | `reused`: nothing added |
| The add | Its code, per the table above |
| RD or AD with `cached_only`, the add accepted: the status read back (below) | `added` (cached, or no verdict), `uncached_deleted` (refused: miss here, next provider), `uncached_kept` (AD with deletes off: refused here, the magnet stays, next provider), `uncached_kept_shared` (accepted: the download waits) |

6. **The release is placed** if a provider accepted it: the download is `added`. On Real-Debrid, one status read
   follows, and the files are selected at once when RD already waits for that (with `cached_only`, the reads of
   "Cached-only on Real-Debrid and AllDebrid" instead).
7. **A miss on every provider that has a key**:
   - every such provider gets a `miss` row in `provider_cache` for `miss_ttl` (6 h; 1 h for new content, below),
     except where a live 451 (`router:451`) or a live read-check verdict (`router:check`, `check:*`) stands: that
     row keeps its source and expiry (`TestFreshContentMissLivesAnHour`, `TestMissKeepsCheckVerdict`);
   - the download fails with `miss on every provider`;
   - the next candidate from the same search is tried, without searching again;
   - when every candidate ended that way, the title is searched again at once ("Re-search after a failure").
8. **Otherwise, deferred.** Some provider was only paced or backing off, so the download goes back to `queued`.
   - The `router.route` job that resumes the download at the earliest free time is written in the same transaction
     as the queued row, whoever called `Route` (a job, RSS, the CLI with a queue). It carries the
     request from this release on and the time it first waited. An add whose answer did not say records its
     follow-up with the unsettled row the same way.
   - A lower-ranked release does not win just because a provider is busy.
   - Deferred routes waiting on one provider get staggered slots: each next one wakes one `min_gap`
     (else one `adds_per_minute` interval, else 2 s) after the last slot handed out, so a backlog does not all
     wake at the same instant and defer again. The slots are kept in memory; a restart starts them afresh. A slot
     is taken only once the route's continuation is recorded: a route that gives up or whose download moved on
     leaves none behind (`TestDeferSlotTakenOnlyWhenUsed`).
   - A deferral counts only when the run sent an add (refused, rate-limited); a wake-up that found the provider
     still paced does not. After 100 counted deferrals, or 48 h of waiting (`maxDeferWait`), the download fails
     ("giving up").
   - The poller resumes a `queued` download with no route job queued or running for it after 10 minutes
     (`resumeStrays`): a route rebuilt from the row (`requestOf`). One whose route job failed since it was queued
     fails with that job's error instead ("its route job failed: …"), so a failing route is not retried forever.
     Tests: `TestDeferredRouteWritesItsContinuation`, `TestStrayQueuedDownloadIsResumed`.

Every provider considered is a `download_attempt` row: accepted, refused, skipped, error or deferred, with the code,
a sanitised message and `retry_at`.

## Cache evidence

Which providers hold a release cached, for every release of a search, not only DMM's. Code:
`internal/indexer/cachedon.go`, `internal/router/cachedon.go`, `router.SearchRequest`.

The arrs got this from per-provider Torrentio indexers with the debrid key in Torrentio's URL. Theatarr never sends a
debrid key to a third party (keys leaked through a third-party proxy before), so the evidence comes from:

| Source | What | Stored |
|---|---|---|
| DMM's cached feeds | `/torznab/<rd\|ad\|tb>/cached` tags its releases `cached_on` with that provider, `/torznab/cached` with `rd,ad`; a path naming several (`/torznab/rd/ad/tb/cached`) tags each release with its own `cachedon` attribute (the named providers holding it), and a release without one gets none. Searches and the RSS sync's newly cached list alike | Every DMM result, whoever searched (acquirer, views, shadow runner, `theatarr search -db`): `provider_cache` state `cached`, source `feed:<indexer>`, for every policy provider of that kind, for `cache_evidence.ttl` |
| Cross-feed, one search | Any release whose hash a DMM feed of the same search lists inherits those providers (a Torrentio release in `dmm-ad` is `ad`) | In memory (the DMM row is already stored) |
| DMM's availability | `POST /api/sponsor/availability` on the DMM indexer's host with that indexer's key (`x-api-key`), through its gateway target and budget (DMM allows 25 calls a minute; keep the shared DMM budget at or under that). Asked right after `ApplyCachedOn`: the public-labelled releases of the search with no `rd`/`ad` evidence and no live row at a provider of that kind, for the kinds (`rd`, `ad`) a debrid version being searched may send them to (`indexers.K.providers` applies), 100 hashes a call, `rd` and `ad` taking turns, at most 4 calls and 15 s a search, without `imdbId` (an unscoped request lists more hashes). DMM lists a hash when a DMM user's own account showed it fully downloaded; a hash it does not list is unknown, not uncached. Needs an enabled `dmm` indexer with its key; the first by key is used | Listed hashes only: `provider_cache` `cached`, source `feed:<dmm indexer>/availability`, for every policy provider of that kind, for `cache_evidence.ttl` (feed evidence: the precedence below applies). Nothing for the rest; a hash DMM answered for is not asked again for 15 minutes (in memory) |
| Stored evidence | Live `cached` rows, read back into every later search | — |
| TorBox `checkcached` | The configured TorBox key, through the gateway (`torbox-api` budget): the best `top` hashes of a search with no live TorBox evidence whose indexer is listed by a debrid version being searched that has TorBox among its providers and that the indexer's `providers` allowlist allows (`TestTorBoxCheckFollowsIndexerProviders`), batched per source indexer | `cached` for `ttl`, `uncached` for 30 min (at most `router.miss_ttl`), source `check:torbox` |

- **Where it is applied.** `router.SearchRequest` (acquirer searches and the arr views' interactive search) calls
  `Service.ApplyCachedOn` on the merged releases before deciding, then `Service.CheckAvailability` (DMM's
  availability) on the same releases, and `Service.CheckCached` on the accepted decisions in rank order after ranking
  (rejected decisions of the same hash get the answer too). `theatarr search` applies all three to the releases it
  prints, every version counting (`-torbox-check=false` skips the TorBox check). A failed, refused or slow
  availability call ends that pass with one log line; the search goes on without it. Each call's answer stands on its
  own: positives from the calls that answered before it are kept (applied and stored), and only the failed call's
  hashes stay unmarked, to be asked again by the next search. Storing a pass's positives is bounded (5 s): when the
  writer is busy the search keeps its marks in memory and nothing is stored. Availability
  evidence, like every other kind, orders providers, fills the cached badges and breaks ties between releases of
  equal quality and format score ("Cache tie-break"): it never skips an add or a check and never puts a release
  ahead of a better quality or format score. It is never asked on the add or poll path. `theatarr router dry-run` applies the cross-feed part
  only and makes no check (it sends nothing to a provider). The check is a search-side read, not the router's: it
  runs in `SearchRequest` whatever the version's mode (the views' interactive search serves shadow versions too) and
  whatever `router.enabled` says; `torbox_check.enabled: false` turns it off. The router's dry run still makes no
  cache check of its own.
- **Release fields.** `indexer.Release.CachedOn` (`["rd","ad","tb"]` order) and `CachedVia` (code → `dmm-ad`,
  `feed:dmm-ad`, `feed:dmm/availability`, `check:torbox`); JSON `cached_on`, `cached_via` in `theatarr search -json`. `Release.CachedList` merges
  them with the release's own `cached_on` extra, for releases that never went through a search.
- **Precedence in `provider_cache`.** A provider's own verdict beats a feed: feed evidence never replaces a live
  row from the router (`router:add`, `router:check`, `router:miss`) or a check; nothing written here replaces a
  live `miss`. Expired rows are replaced. The router's own writes are unchanged. One exception: an item on DMM's
  newly cached RSS list (not a search result) whose `pubDate`, when DMM listed it as cached, is newer than a live
  verdict of Theatarr's own at a provider the item names (`uncached` from `router:add` or `router:check`, or the all-provider
  `miss`, `router:miss`), turns that row `cached`: the next route tries the add and cached-only check there again. A
  retry that misses writes a newer `checked_at`, so the same listing reopens it once. Never a 451 (`router:451`) nor
  another provider's row. For new content a route's `router:add` and `router:miss` rows last 1 hour, not `miss_ttl`
  ("Re-search after a failure"), and so does the `router:add` row a resumed cached-only check writes when the poller
  deletes Theatarr's uncached add (`TestCachedCheckResumedFreshTTL`).
- **Router.** Evidence only orders providers (and, at ranking, breaks ties between releases: "Cache tie-break").
  **Absence of evidence never means uncached**: a provider with none
  keeps its place and is tried exactly as before. Only the existing rules skip one (`miss`, `known_uncached` with
  `cached_only`, which a TorBox check's `uncached` answer now feeds too). The dry-run "would add" message names
  the evidence.
- **TorBox check budget and DMM's TorBox probe.** On a DMM path naming `tb`, DMM asks TorBox live with the TorBox
  key linked in DMM, but for at most 400 previously unknown hashes per request (`LIVE_PROBE_LIMIT`), and it
  remembers answers (hits for 6 hours, misses for 30 minutes; `providerCache.ts`). On a large title some releases
  TorBox holds are therefore not tagged `tb`; they stay unknown, and Theatarr's own check may still spend on them.
  Because the probe answers per request, a later page of one search can tag `tb` on a release an earlier page
  listed without it (Theatarr keeps one copy with the union of its providers, in path order), or surface a
  TorBox-only release that sorts ahead of the offset already read, which that search then does not see. A
  release that does arrive with `tb` in `cached_on` is skipped by the check: a candidate whose `CachedOn` has `tb` (its own DMM tag, or the same hash in a DMM result of
  that search, applied by `ApplyCachedOn` before ranking) is never asked, nor a hash with a live TorBox row
  (`knownOnTorBox`: any live `cached` row, or a first-party one). What DMM lists without `tb` is not stored as
  uncached (feeds store positives only), so those hashes can still be asked. The `torbox_check` budget (60 an hour,
  top 10) therefore goes to hashes DMM did not show on TorBox: Torrentio, Prowlarr, Zilean and SeaDex releases DMM
  does not list, DMM-listed releases held only on RD or AD, and TorBox holders DMM's probe bound left untagged.
- **Real-Debrid and AllDebrid** have no first-party cache check. RD's `instantAvailability` answers
  `disabled_endpoint` since 2024-11. AllDebrid removed `magnet/instant` in 2024; v4.1 has only `magnet/upload`
  (whose `ready` answers after the add, a write) — decypharr's AllDebrid client reports availability unsupported,
  and zurg's AllDebrid client has no availability call either. So DMM is their only
  source before an add: its `rd` and `ad` feeds, and its availability for the releases the feeds did not list
  (Torrentio, Zilean, Prowlarr, anime sources). After one, the router's own answer is evidence too: a cached add
  stores `cached`, source `add:<provider>`, for `cache_evidence.ttl` (decypharr's `hearsay` learning step); an
  uncached add of Theatarr's own stores `uncached`, source `router:add`, for `miss_ttl`.
- **Keys.** `TestDebridKeysOnlyGoToTheirProvider` runs Torrentio (movie, series), both DMM feeds and the TorBox check
  with all three debrid keys set: no debrid key reaches any host but its own provider (URL or headers), Torrentio
  URLs carry no query and no debrid option, TorBox's key is in its header only, nothing reaches the logs.
  `TestTorrentioRefusesDebridConfig` and the gateway's `TestNoOtherOutboundClients` still hold. DMM's availability
  carries DMM's key in its header only, never in the URL (`TestDMMAvailabilityEvidence`).

Tests: `TestCrossFeedCachedOn` (Torrentio + `dmm-ad` → `[ad]`, stored, a later Torrentio-only search still sees
it, gone after the TTL), `TestFeedEvidenceKeepsFirstPartyRows`, `TestTorBoxCheckAtSearch` (httptest TorBox: top,
batching, per_hour, stored answers, nothing asked twice, off switch), `TestCheckTopFillsDecisions`,
`TestRouterOrdersProvidersByCacheEvidence`, `TestCacheEvidenceNeverSkipsUnknownProviders`,
`TestCacheEvidenceSettings`. DMM's availability: `TestDMMAvailabilityEvidence` (positives stored and applied,
private-labelled and unset-privacy releases asked like any other, unknown hashes stay unknown, evidence and live
verdicts not asked, the 15-minute memo, no `imdbId`),
`TestDMMAvailabilityFollowsVersionProviders`, `TestDMMAvailabilityBatches` (100 a call, rd/ad turns, the call bound),
`TestDMMAvailabilityFailureAndNoKey`, `TestDMMAvailabilityPartialFailureKeepsAnswers`,
`TestDMMAvailabilityStoreBounded`, `TestAvailabilityEvidenceKeepsFirstPartyRows`,
`TestDMMAvailabilityOrdersProviders` (router), `TestSearchCommandDMMAvailability` (`theatarr search`: the cached column and `cached_via`).

## Cached-only on Real-Debrid and AllDebrid

The rule: Theatarr may delete its own add right after it turns out uncached; a torrent other tools or instances
may use is never deleted. Code: `internal/router/uncached.go`, `internal/provider/adder/delete.go`.

decypharr (`pkg/manager/processor.go` `SubmitMagnet`, the providers' `CheckStatus`) with `download_uncached: false`
adds, reads the status, takes `downloaded` as cached and anything still fetching as not cached, deletes that torrent
id (`TorrentNotCachedError`) and tries the next debrid. Theatarr does the same with `router.cached_only`, on RD and AD:

**Before the add (the guard).** Is the hash already on that account?
- a registry row (`content_provider`, from the provider sync) in state `present`, with or without a torrent id (with
  one, the hash is `reused` and nothing is added, as before);
- AllDebrid only: a download of Theatarr's own on that provider with a torrent id, in any state (a failed
  download's torrent was never deleted). On Real-Debrid such an old download does not count:
  every `addMagnet` creates a new torrent, so the id the add returns is Theatarr's even when an old
  failed Theatarr download (another version's, say) holds the same hash, and an uncached one is deleted as usual. The
  id itself must still be unknown to every other download and the registry;
- AllDebrid only: a fresh read of the whole magnet list (`GET /v4.1/magnet/status`, one request, no paging), since
  the registry may be stale and an upload of a hash already there returns the existing magnet's id. A hash in the
  list is `reused` (nothing added); its ids are kept. RD needs no list: every `addMagnet` creates a new torrent.

**After the add (the reads).** `router.cached_grace` (default 2 s, decypharr's sleep):

| Provider | Reads | Cached | Uncached |
|---|---|---|---|
| RD | `GET /torrents/info/{id}`; at `waiting_files_selection` the files are selected (every playable file: "File selection on Real-Debrid"), then one more read after the grace | `downloaded` | `queued`, `downloading` … after the grace; a magnet still converting (`magnet_conversion`, or queued with no file selection sent) is no verdict yet and is re-read after each grace, up to the four reads, and is uncached only when it is still converting at the last one (at most 3 graces, 6 s at the default) |
| AD | `ready: true` in the upload answer, else `GET /v4.1/magnet/status?id=` | `ready`, or statusCode 4 | statusCode 0–3 after one re-read |

At most four reads (five when the files are selected at the fourth: a selection is always followed by one read after
its grace); a read error, a provider error status or a rejected file list is "no verdict": the add stays and the poller finishes the check ("Unfinished checks" below). decypharr's
effective behaviour is the same (one read after a 2 s sleep, one more after a selection), except that Theatarr gives
a converting Real-Debrid magnet the remaining reads, as its poller does not judge one either.

**Then:**
- **cached:** `accepted/added`; `provider_cache` `cached`, source `add:<provider>`, for `cache_evidence.ttl`; the
  download is recorded ready at once when the read showed it.
- **uncached, provably Theatarr's (on AllDebrid with `router.alldebrid_delete_uncached`, on by default)** (the hash was not on the account, and the id is new: RD always, AD not in the list read
  before; on both no registry row or other download knows the id; the entry shows the hash added): deleted
  (`adder.Deleter.DeleteOwnUncachedAdd`), `provider_cache` `uncached` (source `router:add`) for `miss_ttl`, counted in
  `uncached_adds_per_hour`, the download back to `queued` with no provider, step and attempt `refused/uncached_deleted`,
  and the next provider is tried. If every provider misses, the usual miss rule applies.
- **uncached, provably Theatarr's, on AllDebrid with `router.alldebrid_delete_uncached: false`:** nothing is
  deleted. Logged at INFO, `provider_cache` `uncached` for `miss_ttl`, counted in `uncached_adds_per_hour`, step and
  attempt `refused/uncached_kept`, the download back to `queued`, and the next provider is tried, exactly as after a
  delete. The magnet stays on AllDebrid and finishes downloading there (no uncached-add limit is known for AD); the
  provider sync later lists it; the attempt and journal step keep its magnet id. Why: the proof has a hole on AD (see "Deviations"), and debrid torrents are shared and
  never deleted except Theatarr's own just-made add. Real-Debrid needs no switch: every `addMagnet` returns a new id.
  Volume: without the once-per-route guard, a route could leave up to
  `max_candidates` (5) kept AD magnets, one per candidate tried. They count against the shared AllDebrid account, so many could hit AD's active-magnet limit
  (`MAGNET_TOO_MANY_ACTIVE`) for decypharr and the arrs too. `router.alldebrid_delete_uncached` deletes Theatarr's
  own uncached adds, and the once-per-route skip below bounds them. `uncached_adds_per_hour` stays unset on `ad` and
  `rd` (see "Pacing"): a reached cap would block cached adds too.
- **uncached, not provably Theatarr's** (the guard found the hash, the AD list could not be read, the id was there before,
  on AllDebrid or TorBox a torrent manager restore of the hash journaled since the guard's look less `addSettle`
  (`RestoredSince`: a restore's row is written as `sending` before its add, from any process;
  `TestRestoreInFlightIsNeverOurAdd`)), or Theatarr's own with the delete blocked or failed (the delete is then owed:
  "Unfinished checks" below): nothing deleted now, `accepted/uncached_kept_shared` with the reason, logged at WARN;
  the download waits for it as before.
After a cached-only delete error, Theatarr reads that id back with a bounded timeout, twice, `router.cached_grace`
apart. Two definite not-founds confirm the delete and allow provider fallback; a single one is not trusted (the
poller needs repeated misses too), and a present torrent or a failed read keeps it (`TestUncachedDeleteErrorReadBack`). The original
ownership proof is still required before sending any delete. Before the delete is sent the download records it
(`error` "deleting our uncached add <id> on <provider>"): a crash between the delete and its record leaves the
download added with that note, and the poller that then finds the torrent gone fails it without blocklisting the
release, since Theatarr's own delete took it (`TestCrashAfterUncachedDeleteNotBlocklisted`). A kept add clears
the note.

**Unfinished checks** (`internal/router/cachedcheck.go`, migration
00104). When the reads give no verdict, or the verdict is uncached on an add of Theatarr's own but the delete is blocked by the
stop switch (Pause on, or the version Watch only), the route's lost job lease, or a failed delete, the add
stays and the check stays open on the download: `download.cached_check` `pending` (no verdict) or `uncached` (the
delete is owed). Kept for ownership reasons (not provably Theatarr's, the adder cannot delete, AllDebrid with
`alldebrid_delete_uncached` off) sets nothing, as before. The marker counts only while the download is `added` or
`downloading`; every new add and every reset to `queued` clears it. The poller finishes it after a successful read,
before the stall timeouts, so an uncached add ends as a miss (nothing blocklisted), not as stalled:
- `cached_only` off: the marker is cleared and the add stays;
- the torrent is `downloaded`: the marker is cleared and nothing is deleted (no cache evidence is written);
- `pending`, queued or downloading `router.cached_grace` after the add (AllDebrid) or after Theatarr's file selection
  (Real-Debrid, `download.selected_at`, taken when the selection's answer came): uncached. On Real-Debrid there is
  no verdict before a selection is recorded or inferred: a magnet still converting, waiting for files, or queued with
  no selection recorded stays `pending` (the stall timeout still applies). A selection whose answer was lost is
  inferred when Real-Debrid then shows Theatarr's add queued, downloading or downloaded, and its grace counts from there;
  for a `pending` row the poller infers it before the stall timeouts and holds that poll, so a selection sent just
  before `stalled_after` gets its full grace (`TestCachedCheckLostSelectionAtStallDeadline`,
  `TestCachedCheckRDNeedsSelection`, `TestCachedCheckLostSelectionQueued`,
  `TestCachedCheckGraceFromSelectionAnswer`);
- `uncached`: deleted with the stalled delete's guard (Real-Debrid only, not reused, `own_add`, the entry shows the
  hash, no other download or registry content knows the id, the gate open, `StalledDeleter`) and the route's
  journaled delete (the "deleting our uncached add" note, a read-back after a failed call); then `provider_cache`
  `uncached` for `miss_ttl`, counted in `uncached_adds_per_hour` (unless it was counted when it started
  downloading), and the download goes back to `queued` with no provider and a route job, nothing blocklisted. That
  route tries the version's next provider; when every allowed provider is a known miss it fails the download ("miss
  on every provider") and searches the title again (`ReasonExhausted`), as a miss in a route does. Without a route
  queue (a CLI poll) the download fails, not blocklisted. The largest-video second add (altRetry) is not tried from
  the poller;
- the gate closed, a failed ownership read or a failed delete, or `pending` within its grace: the marker stays for
  the next poll, and that poll does not end the download as stalled or without progress (it would blocklist the
  release and drop the check); progress and status are still recorded. A failed status read leaves the stall clock
  as before (`TestCachedCheckGateClosedPastStall`);
- a delete call that errors where the read-back says neither present nor gone (twice not found): the journal note
  stays, in the route's settle and in the poller. A later poll that finds the torrent gone (the usual two misses
  over 5 minutes) takes it as Theatarr's delete: the miss is recorded and the release routed again as above, nothing
  blocklisted. A present torrent clears the note and the delete is tried again (
  `TestCachedCheckAmbiguousDelete`, `TestCachedCheckAmbiguousInitialDelete`);
- not provably Theatarr's (not Real-Debrid, reused, `own_add` unset, another hash, the id known elsewhere) and every
  AllDebrid add (its just-made proof is gone by now): the marker is cleared, the add kept, and the download's error
  says "uncached add kept: <why>; review it" (Activity), logged at WARN.

If the gate stays closed long enough for RD to finish the uncached download, it is downloaded: the marker clears,
it imports, and nothing is deleted. `theatarr router status` prints the number of unfinished checks, and the health
check `CachedOnlyCheck` warns when one has been open more than 30 minutes after its add. Tests: `TestCachedCheck*`
(`cachedcheck_test.go`), `TestUnfinishedCacheChecks`.

- **One second selection on Real-Debrid (movies).** Real-Debrid caches per selected file
  set, so an uncached answer is a verdict on the set tried, not on the torrent. When a movie's every playable file
  comes back uncached on RD and Theatarr's add was deleted (`uncached_deleted`, its message ending "adding it once more with
  the largest video alone selected"), the route adds the same release to RD once more with only its largest video
  selected: the set tools streaming one file select. It
  is the normal add (`provider`): it takes a pacing slot (the route first waits for RD's pacing, at most
  `altPaceWait`, 1 minute, which covers the 20 s `min_gap`; paced longer, as after a burst cooldown or a used-up
  `uncached_adds_per_hour`, it records `skipped/alt_selection_skipped` and moves on), it ignores the `uncached` mark
  the first add just wrote (that verdict was on the other set), and its own outcome is settled as any add: cached
  is `accepted/added` (and `provider_cache` `cached`), uncached is deleted, counted and recorded again, and the
  next provider is tried. Never a third add of the release on RD in a route. No second add for a series (no other
  set holds the episodes, and every file is what risks the RAR), for a movie with one playable file (the same set),
  on AllDebrid or TorBox (no selection), or without `cached_only` (an uncached add is kept and downloads anyway).
  The second set is recorded with its torrent (`download.select_alt_torrent`, migration 00096): if its
  selection fails in the route's reads, the poller selects the largest video alone too, after a restart as well,
  never the set RD just answered uncached for (`TestAltSelectionSurvivesFailedSelect`). The intent is written
  with the add attempt before the add is sent (`adding` row), so a second add whose answer is lost and
  that reconciliation adopts later selects the largest video alone as well (`TestAltSelectionSurvivesInterruptedAddAnswer`).
  A new add has a new torrent id, so the mark ends with its add. The pacing wait before the second add does not hold the add lock
  (`TestAltRetryWaitsWithoutLock`): the add after it is fenced on the download still queued,
  reserves its pacing slot in one transaction and reads the account first, as any add. Tests: `TestCachedOnlyAltSelection` (playable set uncached, largest alone
  cached: two adds, one delete, min_gap waited, one uncached counted), `TestCachedOnlyAltSelectionOnce` (both
  uncached: two deletes then AllDebrid; a series and a single-file movie: one add), `TestCachedOnlyAltSelectionPaced`.
- **AllDebrid once per route:** after an uncached AD add of Theatarr's own stayed there (`uncached_kept`,
  or `uncached_kept_shared` on AD), AllDebrid is not tried again in that route: later releases of the batch get
  `skipped/provider_done` there (it reserves nothing and records no miss), so a season of single-episode releases
  leaves at most one magnet per route on the shared account. The next route (a deferred retry is a new route) may
  try it again.

**On by default (the owner's decision, 2026-11-13):** `router.alldebrid_delete_uncached` is `true` unless a stored `false`
keeps it off, with `uncached_adds_per_hour` only on `tb` (60, TorBox's own limit; unset on `rd` and `ad`, see "Pacing").
Where a cap is set it works the same on every provider (`TestUncachedCapOnRealDebridAndAllDebrid`): once used, the
provider is `paced` until the next clock hour and the route moves on, or waits when no other provider is free.

**The remaining race.** With the switch on, AD's proof is a list read right before the upload: the hash is
not on the account, and the id the upload returns was not in that list. AllDebrid answers an upload of a hash already
on the account with that magnet's id, so if another client (decypharr, an arr, a person) uploads the same hash in the
moment between Theatarr's list read and Theatarr's upload (about a second, the time of one request), Theatarr's upload returns its magnet,
which then looks like Theatarr's own new one; if it is uncached, Theatarr deletes it. The two-minute ledger window, one use per
add, the gate and the job lease narrow it no further than that second. Real-Debrid has no such hole: every
`addMagnet` creates a new torrent id. Turn the switch on only for an AllDebrid account nothing else writes to.

**The adder's own fence.** `DeleteOwnUncachedAdd` takes the `*Added` its own `AddMagnet` returned (pointer identity,
kept in a per-adder ledger), once, within two minutes, and only with the router's proof (`OwnAdd{NotOnAccountBefore,
NewEntry}`). Anything else is `ErrNotOwnAdd` before a request. Requests: RD `DELETE /torrents/delete/{id}` (204), AD
`POST /v4/magnet/delete` `id=` (the docs' path; v4.1 changed only upload and status). TorBox has no delete: `add_only_if_cached` refuses before anything exists.

**Dry run.** The plan's `would_add` message says what cached-only would do: "deleted (uncached_deleted) and the next
provider tried" (AD with the switch on: unless the list shows the hash or the id), "stays on AllDebrid
(uncached_kept …) and the next provider is tried" (AD with the switch off), or "stays (uncached_kept_shared …)" when
the registry (or, on AllDebrid, Theatarr's downloads) place the hash on the account. A dry run reads no list.

With `cached_only: false` nothing here runs: the add, one read and the selection, as before.

Tests (`internal/router/uncached_test.go`, httptest RD and AD through a real gateway; `adder_test.go`):
`TestCachedOnlyCachedAdd`, `TestCachedOnlyUncachedAddDeleted` (RD deleted, AD takes it; with the default RD deleted
and AD kept (`uncached_kept`, no AD delete request), TorBox unchanged; with `alldebrid_delete_uncached` on both
deleted; every provider misses), `TestCachedOnlyNeverDeletesExistingADMagnet` (list shows the hash: reused; upload
returns an id the list had: kept; list unreadable: kept, even with the opt-in on),
`TestAllDebridUncachedDeleteOptIn` (on/off and list proof with in-memory providers),
`TestAllDebridUncachedDeleteUnprovable` (registry hash or id, download id, delete failure: kept), `TestCachedOnlyNeverDeletesKnownHash` (registry: kept; an old
Theatarr download that failed: on RD the new add is deleted and the old torrent untouched, on AD kept), `TestCachedOnlyOffUnchanged`, `TestCachedOnlyGate` (shadow and kill switch: no request at all, the poller
deletes nothing), `TestAllDebridUncachedKeptOncePerRoute`, `TestUncachedCapOnRealDebridAndAllDebrid`, `TestDeleteNeedsTheAddersOwnAdd`, `TestDeleteOwnUncachedAdd`, `TestAllDebridOwnUncachedDelete` (in-memory gateway), `TestNoDeletes`. The gate tests
(`TestShadowVersionNeverWrites`, `TestKillSwitchBlocksWrites`) count deletes as writes.

## Stalled downloads (`router.stalled_after`)

decypharr's `remove_stalled_after: 10m` with queue cleanup `blacklist_research`. Code: `internal/router/stalled.go`.

A download still `added` (accepted; not ready, not fetching: RD converting or waiting for a selection, AD queued)
`stalled_after` (default 10 min, AllDebrid 25 min; 1 min to 24 h; `0s` off; per provider below) after `added_at`,
read by the poller:
- fails as `stalled: not ready 10m0s after the add (provider status …)`, and its hash goes on `router_blocklist` for
  the version, source `stalled`, in one transaction;
- Theatarr's add is deleted (`adder.StalledDeleter.DeleteOwnStalledAdd`) only on Real-Debrid, only when the add's guard
  proved it a new entry of Theatarr's own (`download.own_add` = 1, migration 00022: the guard runs for every RD add while
  `stalled_after` is on), the entry still shows the hash, no other download or registry content knows the id, and
  the gate allows a write. Otherwise it stays and the error says why ("kept on rd: …"). Never on AllDebrid: every
  upload of a hash returns the same magnet, so ten minutes later nothing proves no other client uses it;
- the title is searched again (next section).

**Per provider (`providers.<key>.stalled_after`).** Each provider may set its own timeout (`0s` off, else 1 m to
24 h); unset, a provider takes `router.stalled_after`, except **AllDebrid, which waits at least 25 min** while the
router's timeout is on. AllDebrid reports healthy uncached jobs with size 0 and every counter at zero for about ten
minutes (622 s and 580 s measured, both finished), and "a ten minute no-movement timeout would abandon healthy
AllDebrid jobs" ([notes: torrent lifecycle](https://notes.debridmediamanager.com/zurg/internals/torrent-lifecycle/)).
The arr views' queue warning uses the same per-provider value. Tests: `TestProviderStalledAfter` (policy),
`TestProviderStalledAfterOverride`, `TestADQueuedStillStalls` (now at AD's 25 min, and not at the router's 10).

The RD stalled delete is safe because RD ids are unique per add,
so an id the add returned that no one else knows is Theatarr's. A comment in `stalled.go` points here.

A provider's queued state is not fetching: a download stays `added` while its provider reports it queued (AllDebrid's
queue, RD converting, TorBox queued or stalled), so the stall timeout applies to it however often it was polled
before (`TestADQueuedStillStalls`).

**No progress while downloading (`router.downloading_timeout`).** A download in `downloading` whose
provider progress has not risen for `downloading_timeout` (default 24 h; 1 h to 7 d; `0s` off) fails the same way:
`stalled: no progress for 24h0m0s while downloading (provider status …, 40.0%)`, blocklisted (source `stalled`), Theatarr's
own Real-Debrid add deleted with the same proof, and the title searched again. `download.progress_at` (migration
00076) is when the poller last saw the progress rise; the clock starts when the poller first sees the download
downloading, so a download from before 00076 gets the whole timeout. A dead swarm, an uncached add kept because it
could not be proven Theatarr's (`uncached_kept_shared`) or a reused torrent another tool fetches no longer hold the title
in flight forever (`TestDownloadingWithoutProgressTimesOut`).

**Theatarr's own RD add that fails before a selection.** A Real-Debrid add of Theatarr's own refused for a dangerous
file or for holding no video waits in `waiting_files_selection` forever. It is deleted with the stalled delete's
proof (`own_add` = 1, the entry shows the hash, no other row knows the id, the gate open); the failure's reason stays
the release's, and the log says when it was kept. Reused torrents and other providers are never deleted
(`TestRDOwnAddFailedBeforeSelectionDeleted`).

Tests: `TestStalledDownload`, `TestStalledNeverDeletesShared` (reused, not provably new, id known elsewhere,
AllDebrid, kill switch, off).

## Torrent no longer on the provider

One read that does not find a download's torrent is not proof that it is gone (a listing lag, a short outage that
answers 404). The poller counts consecutive not-found reads (`download.missing_polls`, `missing_since`, 00061) and
fails and blocklists the release (`the torrent is no longer on <provider>`, source `provider`) only after at least two
of them spanning five minutes; a read that finds the torrent clears the count. Within the window after a provider
settings change the old rule stands: the download fails without a blocklist. A **reused** torrent (never Theatarr's add)
that stays missing says nothing against the release: the download goes back to `queued` with no provider, the
torrent's registry row is marked `absent`, and a `router.route` job resumes it with its own release (a fresh add, or
the reuse of another entry that holds the hash); nothing is blocklisted. Without a route queue (a CLI poll) it fails
unblocklisted. Tests: `TestNotFoundNeedsRepeatedPolls`, `TestNoVideoReusedOrFinished`, `TestStaleDownloadIDNotReused`,
`TestStaleRegistryIDNotReused`, `TestReusedTorrentGoneRequeues`.

## Ready deadline (`router.ready_timeout`)

A download still `ready` after `router.ready_timeout` (default 6h, 1m to 7d) fails, its hash is blocklisted for the
version (source `ready_timeout`), and a re-search is queued. The deadline starts at `ready_at` (legacy rows without
it use `added_at`, then `created_at`); import retries do not extend it. While the mount is down (the library's mount
check, `Router.SetMountCheck` with `Library.MountListed`: the release root answers and its listing is plausible, the
check relink uses, so an empty or half-loaded `__all__` counts as down) no download expires: a zurg outage longer than the deadline must not blocklist every
release that was only waiting to appear. The deadline counts only time the mount was healthy: the
poller checks the mount while any download is ready and remembers the outages it saw (in memory, 30 days; a restart
forgets the earlier ones), so downloads that became ready during a long outage get their full `ready_timeout` after
it ends instead of failing the moment it does (`TestReadyDeadlineSkipsMountOutage`). Otherwise it is enforced
without reading or deleting any provider torrent. A ready download whose provider has no client is left unpolled
with that provider (`TestPollLeavesUnavailableProvidersUnpolled`). A held import never expires: one held for review
(`library_import` `rejected`), for its episode title, or recorded as a `dry_run` (the library gate refuses the
version); a later pass imports it once that changes (`TestDryRunImportNeverExpires`). A download waiting because the
mount and zurg or decypharr disagree about its files (`mount mismatch: …`) fails at the deadline without blocklist and
without a re-search: the disagreement is no verdict on the release; this applies too when an earlier partial import
left the download `importing` (`TestReadyDeadlineMismatchNotBlocklisted`, `TestReadyDeadlineMismatchAfterPartial`).

Expiry checks the state and library claim in one transaction: live import claims defer it, and `importing` or
`imported` downloads are left alone. Expired claims are invalidated, and an existing `waiting_mount` import records
the failure too. Tests: `TestReadyDeadline`, `TestReadyDeadlineKeepsOtherStates`, `TestReadyDeadlineWaitsForMount`,
`TestFailureAndBlocklistAtomic`.

## The search schedule

Every automatic search starts from one place: the search schedule (`internal/acquire/schedule.go`, migrations 00126
and 00127). A wanted item is one row of the Wanted page: one library's missing or cutoff-unmet part of a film, or of
one series season. Its next search is a row of `search_request`, keyed `(title_id, version_key, reason, season)`
(`season` -1 for a film):

- `next_at`: from when the item may be searched;
- `tries`: empty searches of new content in a row (or searches no indexer answered, for other rows);
- `why`: NULL on the regular schedule, `asked` for Search now, `added` for a title a list sync or the timed
  collection sync added, `new` for a retry of new content, else the failure the router reported (`exhausted`,
  `stalled`, `dead_link`, ...).

An item with no row is unscheduled. Rows go with the title and with the library.

The settings are `router.wanted`: `per_hour` (searches in any hour, default 10), `again_after` (when an older item is
searched again, default 24 h), and the two backlog switches, `missing` ("Search for missing items", default on) and
`cutoff_unmet` ("Search for upgrades": `quality`, `score` or `off`, default `quality`). There is no interval, no
separate retry switch and no on/off switch for the whole schedule: Pause stops it.

**Rounds.** `Round` starts what is due. A full round runs every 15 minutes (job `acquire.wanted`, worker `wanted`
while a library is Live and Pause is off; the first 5 minutes after start, or at once when Pause is turned off).
Search now and a failure run a round of their own titles only. A round:

- does nothing while Pause is on or no library is Live;
- (full round) marks a series wanted when a monitored missing episode passes its stored air time;
- takes the due rows whose item still has something to search: a row with a `why` whatever the switches say, a
  regular row only while its switch is on. A due row with nothing left to search, or a regular row whose switch is
  off, is deleted;
- (full round, per switch that is on) takes the unscheduled items. One whose latest run of any source (a search by
  name, an add's search, an arr command) is recent enough gets a row with the time the rule below gives that run,
  and is not searched now; the rest are due (`TestRoundCountsManualSearch`);
- orders what it took: `asked`, then the other rows with a `why`, then the backlog (unscheduled items newest added
  first, then regular rows); inside each, the earliest due first (`TestRoundOrder`, `TestRoundBacklogOrder`);
- groups the targets that share one search, skips an item with a download in flight or with a search the schedule
  started still queued or running (its row stays due, `TestQueuedSearchNotDuplicated`), and starts groups while
  `router.wanted.per_hour` allows. What the hour does not allow waits for a later round.

So a due time says from when an item may be searched, not when it starts. Each search a round starts (a loop
search) is a run with source `wanted` and `acquire_run.research` set to its row's `why`. Rounds hold the acquirer's
`drainMu`, so two of them never spend the same slot. With both switches off a full round reads only the due rows, and nothing on the
backlog is searched (`TestRoundBacklogOffStartsNothing`).

**After a search.** When a search of an item ends, whatever started it, its row is rescheduled (`nextSearch`,
`TestNextSearchRule`): a loop search reschedules its own row, any other search (title page, add, arr command) the
item's regular and `new` rows. A search by hand is therefore not followed by a scheduled one minutes later.

| Outcome | Next search | `tries`, `why` |
|---|---|---|
| No indexer answered (`infra = 1`), a `new` row | after 1 hour | unchanged |
| No indexer answered, another row, fewer than three times in a row | after 1 hour | `tries` + 1, `why` kept |
| A missing item whose content is new (below) | after 1, 2, 4, 8 and 16 hours, then `again_after` (never longer than `again_after`) | `tries` + 1, `new` |
| Otherwise, its switch off | the row is deleted: not scheduled | |
| A partial answer (`infra = 2`) | after 6 hours (`again_after` when shorter) | 0, regular |
| Otherwise | after `again_after` | 0, regular |

A search no indexer answered (a VPN refusal, `ErrVPNRequired`, or `Searched.NoAnswer`) does not count as a search of
the item. A search where some indexer answered and some request failed (an error, a back-off or a spent budget, a
page that failed, a failed Cardigann keyword query, a plain Cardigann series query without episode criteria:
`Searched.Incomplete`) counts, routes what it accepted, and is due again sooner. Theatarr's own `page_cap` stopping
the paging (DMM's 200) is not a failure (`indexer.Result.Capped`). Both spend the hourly limit. Tests:
`TestPartialSearchCountsAsSearchedSooner`, `router.TestSearchPartialAnswerIsIncomplete`,
`router.TestPlainKeywordAnswerDoesNotCountAsSeasonSearch`.

**New content.** Debrid caches pick a new release up within minutes to hours, but RSS lists it once and the first
search usually runs before any release is cached. Content is new for 7 days after its date (`availability.Fresh`): an
episode's air date; a movie's digital or physical date, either one (a later disc release makes it new again); never a
theatrical date (nothing worth caching exists yet) nor a torrent's date. Removed episodes are never new, and a
route's freshness is its latest episode aired by now. A missing item on the schedule whose content is new is retried
by the first rows of the table, then every `again_after` while it stays new (seven days from its date). With its
switch off, this holds for an item that Search now or a failure put on the schedule: its row is kept while the
content is new. Upgrades never use the fast retries. For a route of
new content the uncached add (`router:add`) and the miss on every provider (`router:miss`) last 1 hour instead of
`miss_ttl` (at most `miss_ttl`; a 451 keeps `miss_ttl`), so a retry can add a release once it is cached. Each retry
whose release is still uncached costs one add and delete on RD or AD, counted in `uncached_adds_per_hour`. Tests:
`TestFresh`, `TestFreshContentMissLivesAnHour`, `TestFreshEpisodes`, `TestFailureThenLadder`.

**Pause.** Rounds do nothing and Search now answers 409. Rows already due wait. Turning Pause off starts a round at
once: due items start as the hourly limit allows, and the rest stay due for later rounds. The router does not ask for re-searches while paused (its gate is closed). A loop search whose
job runs after Pause was turned on ends skipped before it asks any indexer (`acquire_run.infra` 3, gated): it counts
in no hourly limit and its row stays due (`TestPauseHoldsDueItems`).

**Search now** (`internal/acquire/searchnow.go`). The Wanted page's buttons (one row, the selected rows, or every row
of the tab with its library and waiting filter, without paging and without a cap) go through `POST /wanted/search`. A
request names rows of the list: `title_id`, `version` and, for a series, `season` (0 or more; left out for a movie,
and for a series it asks for every season with something wanted). `RequestSearchNow` skips, with the reason, an item whose
library is not active, that has nothing wanted now, or that a loop search is already queued or running for; it
refuses everything while Pause is on (409). Each other row is made due now with `why = 'asked'` (asking again for a row
already asked and due keeps its time), and a round of those titles runs at once. The answer says how many started
and how many asked rows still wait (`started`, `pending`). An asked row is searched whatever the switches say and
whatever `again_after` says (`TestSearchNowTakesDueRegularRow`). `DELETE /wanted/search` ("Cancel queued searches")
puts the asked rows that are due back on the regular schedule (`again_after` from now); searches already started
stay started. The list reports what stops a search by hand apart from the row's waiting state
(`WantedRow.search_blocked`: `downloads_off` (Pause), `in_flight`), the page's controls follow it, and "all" asks only
for the rows without it (`searchable`). `GET /wanted` counts the asked rows that are due (`search_now_pending`).
Tests: `TestWantedListSearchNowQueue`, `TestWantedListNextSearch`, `web.TestWantedSearchAPI`.

**Searches by name.** The title page's Search, the search after an add made in Theatarr and arr-view commands keep
their own path (`Submit`, source `command`): they start at once, outside `per_hour`, and are not scheduled. They only
reschedule an item's existing row (above), and the next full round counts them for an unscheduled item. So does a
person adding or refreshing one collection with Search on add.

**Adds by lists.** A list sync (timed or Sync now) and the timed collection sync do not search at once: with Search
on add, `RequestSearchLater` makes the missing rows of the libraries the title was added to due now with
`why = 'added'` and starts no round; the loop searches them under `per_hour`, after the asked rows, whatever the
switches say, and reschedules them by the ordinary rule (old content with "Search for missing items" off loses its
row). It writes nothing while Pause is on, never turns a due row into `added` (an asked row stays asked), and leaves
out items with a download in flight or a loop search queued or running. A new series has no episodes until its first
metadata refresh: its request waits in `series_search_wait` marked later, and `ReleaseWaiting` then makes one
`added` row per season with something wanted. Sync now while Pause is on is refused (409) before anything is added.
History calls such a search "added by a list". Tests: `TestAddedRowOnTheSchedule`, `TestAddedSeriesWaitsForMetadata`,
`TestAddedRunDoesNotBlockFailure`, `TestManualSearchTakesAddedRow`.

**Upgrading from the wanted loop.** Migration 00126 adds the columns; Search now requests still queued become due
`asked` rows; queued `acquire.searchnow` jobs and the queued retry chains of new content (`acquire.research` with
reason `fresh`) are deleted. Migration 00127 removes `router.wanted.enabled`, `interval` and `retry_new` from the
stored settings; a stored `enabled: false` first becomes `missing: false` and `cutoff_unmet: off` (a stored null
changes nothing). A policy file with those keys loads the same way, and each key is named in `Policy.Ignored`
("ignored: removed setting (one search schedule)"). At the first full round every item is unscheduled, and those
searched recently get their time from their last run, so the upgrade starts no burst. Tests:
`TestMigrationSearchSchedule`, `TestMigrationWantedSettings`, `policy.TestLegacyWantedKeys`.

### Re-search after a failure

When a download fails for a reason the arrs and decypharr answer with "blocklist and search again", the router asks
for a search at once instead of waiting for the item's next search. Code: `internal/router/research.go`,
`internal/acquire/research.go`.

| Failure | Found by |
|---|---|
| Every candidate of a route missed on every provider (`exhausted`) | `Route`, active versions only |
| Stalled, dangerous files (`torrent_files`), the library's sample, runtime or mislabel guard (`library`: a sample, or a mislabelled remux or 2160p) | the poller: `router_blocklist` rows of those sources with `researched_at` NULL (00022; older rows count as done), so the library needs no hook |
| Provider error status, torrent no longer on the provider, no video to select (`provider`); ready deadline (`ready_timeout`; not a `mount mismatch` wait, which is not blocklisted) | the poller: failure and blocklist are recorded together, then the same blocklist scan queues the re-search |

This follows Radarr and Sonarr v4's `FailedDownloadService`: their `DownloadFailedEvent` feeds both
`BlocklistService` and `RedownloadFailedDownloadService`. Theatarr blocklists per version and keeps the shared torrent;
provider errors and torrents without video take precedence over the stalled timeout so they cannot trigger
stalled-torrent cleanup.

The router enqueues job `acquire.research` (`router.ResearchRequest`: title, version, the failed download's
episodes, reason) only when its gate allows writes for the version (so not while paused). The acquirer (`Research`):

- skips a version that is not Live;
- finds the item's rows: a movie's (missing while the movie is wanted, else cutoff), or for a series one per season
  of the failed episodes (missing when one of them is wanted). The search covers that season's whole missing (or
  cutoff) set, not only the failed episodes;
- skips a row whose library and season had a search after a failure within `router.wanted.again_after` (6 hours after
  a partial answer), so a release that keeps failing cannot loop. Search now and retries of new content do not count
  here (`TestLegacyRunsDoNotBlockFailure`);
- makes the row due now with the reason as its `why` and runs a round of the title. It is searched under
  `router.wanted.per_hour`, ahead of the backlog, whatever the backlog switches say; afterwards the row follows the
  table above (a missing item of new content goes on to the new-content retries).

Tests: `TestExhaustedRouteResearches`, `TestBlocklistedFailuresResearch`, `TestStalledDownload` (router);
`TestProviderFailuresBlocklistAndResearch`, `TestReadyDeadline`, `TestFailureAndBlocklistAtomic` (router);
`TestResearchAfterFailure`, `TestResearchInfraFailureAskedAgain`, `TestNoAnswerResearchGivesUp`,
`TestFailureThenLadder` (acquire).

Blocklisted hashes are skipped by the new route, and known misses don't count against `max_candidates`, so the new
search reaches releases the first one never tried.

### A dead link is searched once

When relink marks an active version's link dead (`docs/design/library.md` "Relink and verify": two misses at least
30 min apart, the mount healthy, not within zurg's repair grace, and no provider folder holds the hash, since relink
repoints by hash to any provider first), the library queues one `acquire.research` job (reason `dead_link`) in the
dead mark's transaction for each of the link's versions the library gate lets write (a version the link shares that
is shadow or not listed gets none): the movie, or the dead link's own episodes. The acquirer makes the item's row due
as above, without the `again_after` skip, and only while the library still writes for the version.

It stays one search: the run and its route carry the reason (`acquire_run.research`, read by the router through the
route's `acquire:<run>` source). A route of it adds only while its target is still missing: the movie still
`wanted`, or a candidate that covers one of the dead link's episodes still `wanted`; filled again before the route
runs, the route is cancelled (`not_wanted`), even for a release that would upgrade the new file. A route of it that
misses everywhere queues no `exhausted` re-search. If it finds nothing, the item stays wanted for its next search,
RSS and hand searches. The dead release is not blocklisted: found cached again, re-adding it is the repair; uncached,
the cached-only check rejects it. Gated versions (shadow, library disabled) only record the dead mark as a dry run
and queue nothing. The dead mark waits only through zurg's existing repair grace; a repair zurg still reports after
the grace does not hold the search back. Tests: `TestRelinkDeadQueuesOneSearch`,
`TestRelinkDeadSeriesQueuesItsEpisodes`, `TestRelinkDeadSharedLinkQueuesActiveVersionOnly`,
`TestRelinkDeadWhileGated`.

## Pacing (`router_pacing`, persisted)

Each provider's `pacing` in the policy:
- **`adds_per_minute`:** counted from the first add of the minute. When the last one is used, the provider enters
  `cooldown` if it has one (the burst rule, "RD pauses adds"); otherwise it waits for the minute to end.
- **`min_gap`:** the least time between two adds to the provider, counted from the last add sent, accepted or
  refused (`router_pacing.last_add_at`, migration 00082). Default **20 s on Real-Debrid**, none on AllDebrid and
  TorBox; `0s` turns it off, at most 10 m. Source: "Two adds on one account need 20 seconds between them." Six clean release names added ten seconds apart
  were refused six times out of six; the same six twenty seconds apart were accepted every time
  ([notes: Real-Debrid](https://notes.debridmediamanager.com/zurg/providers/realdebrid/); also
  [torrent lifecycle](https://notes.debridmediamanager.com/zurg/internals/torrent-lifecycle/): "Nine adds at 20 s
  spacing produced zero 451s"). A provider inside its gap is paced like any other (`deferred`/`paced`): the release
  goes to the next provider of the version when one takes it, otherwise the route waits and its continuation runs
  when the gap is over. A season route that places many single-episode releases on an RD-only version therefore
  places the first at once and the rest one by one, 20 s apart; none is failed or dropped
  (`TestMinGapSpacesSeasonBatch`). The restore of the torrent manager shares the rows and waits the same way.
  A route reserves its add in one transaction right before sending it and records the outcome in another, as the
  restore does (`ReserveAdd`, `RecordAddOutcome`), so neither overwrites the other's add (`TestRouteKeepsConcurrentRestoreReservation`). A request the gateway refused before sending it (budget, its own
  back-off, VPN guard, host) takes its reservation back and does not back the provider off: the route waits for the
  refusal's `RetryAt` (else 1 minute) (`TestGatewayRefusalIsNotAnAdd`).
- **`cooldown`:** also follows RD's account hold (451s on hashes of different titles within 10 minutes; one hash's
  451, or 451s on releases of one title, are verdicts on those hashes), which **escalates**: a 451 the name rule does not explain while the
  last hold runs or within an hour after it ended takes the next step of 5 min → 15 min → 60 min (60 stays the top);
  a 451 after a quiet hour starts at 5 min again. `cooldown` is the floor of every step (a `cooldown: 10m` gives 10,
  15, 60), and a later `Retry-After` wins. Why: RD's throttle "has memory. Once tripped it keeps refusing through
  minutes of quiet" and "clears with quiet and not with retries"; a sweep "spent four hours adding nothing because
  three adds landed within thirteen seconds of each other"
  ([notes: Real-Debrid](https://notes.debridmediamanager.com/zurg/providers/realdebrid/)). The step is
  `router_pacing.hold_level` (0 to 2) and the last hold's end `hold_until`; Admin › Settings › Download clients shows both
  (`hold_level`, `last_add_at`). Tests: `TestAccountHoldEscalates`, `TestAccountHoldEscalatesInRoute`.
- **`uncached_adds_per_hour`:** counts per clock hour. It counts adds that turned out uncached (the first status read
  after the add shows downloading, or the cached-only read deleted or kept them) and TorBox's not-cached refusals.
  zurg's maintainers measured that these cost the same quota. Only TorBox has a known limit (60/h). Leave it unset
  on RD and AD: they have no cache-check API, so every cached-only check is an add, and a reached cap blocks every
  add to that provider, cached ones too, until the hour ends. AD deletes Theatarr's uncached adds with
  `router.alldebrid_delete_uncached`, and the once-per-route skip bounds the kept ones.
- **Limits:** apart from RD's 20 s spacing above, none of the providers limits adds separately; decypharr and zurg only pace all API calls
  (RD 250/min, zurg 75/min for the torrent list; AD 600/min; TorBox 300/min, its download-link endpoint about 100
  before a 5-minute lockout). The example's `adds_per_minute` is 0 (off) and the gateway budgets cap the requests.
- **`backoff_max`:** caps the exponential back-off, 1 h by default.
- **Back-off kind** (`router_pacing.backoff_down`, migration 00095): a back-off is marked *down* when the
  failure says the provider is down: no answer (transport error), a 5xx, its own unavailable code (RD 6 and 25, AD
  `MAINTENANCE`, TorBox server errors; not AD's `GENERIC` or `MAGNET_FILE_UPLOAD_FAILED`), or downloads that
  stalled while it was down (provider unavailable, which also marks a back-off already running). A rate limit, too
  many active torrents, a traffic or auth refusal and a bare 4xx back off without it. Any answered add clears it;
  a refusal that extends a down back-off keeps it down. Only a down back-off stops a route reusing a torrent the
  provider holds (`TestAddBackoffDoesNotBlockReuse`, `TestBackoffKind`); the torrent manager's restore passes
  the kind through `AddOutcome.Down`. Migration 00097 marks a back-off still running at the deploy as down when its
  stored reason is one an outage writes (`error`, `unavailable`, `unknown`, "provider unavailable: ..."), so an outage
  under way while the column arrived blocks reuse too; the few non-outage refusals with such a reason then wait out
  their back-off, the safe side (`TestBackoffDownBackfillMigration`).

## Polling

`Router.Run` polls every `router.poll_interval` (1 m). `Poll` reads every `added` or `downloading` download with the
read-only `Provider.Torrent`, least recently tried first (`download.poll_tried_at`: every read attempt, answered or
not; `polled_at` stays the last successful observation; migration 00084). One pass runs at a time. The sweeps (interrupted adds,
stray queued rows, deleted versions, ready deadlines) and every write run under the router lock; the status reads do
not, so routes and settings changes are not held up behind slow providers, and a pass stops reading after
`pollPassLimit` (2 m), leaving the rest (`postponed` in the summary) for the next pass. A download that moved on while
its read was out is left alone (`TestPollReleasesLockAndIsBounded`). The provider writes recording a
status may make under the lock, Real-Debrid's file selection and the delete of Theatarr's own stalled or failed add, each
have `providerWriteLimit` (30 s): a selection cut by it is an error retried at the next read, and a delete cut by it
keeps the torrent ("the delete failed"), while the download still fails as decided. Their ownership checks are
unchanged (`TestPollProviderWritesAreBounded`). A selection that failed on Theatarr's side may still have reached
Real-Debrid: when a later read shows a non-reused add whose selection was sent, past
`waiting_files_selection` and downloading or downloaded, it is recorded as selected, with a log line saying it was
inferred, instead of stalling at added (`TestSelectionInferredForOwnAdd`). Selection inference is independent of
`own_add`, which remains the deletion proof: cached-only and `stalled_after` can both be off, leaving it unset.
The inferred download enters `downloading`, so the no-progress timeout still applies.

**Failed status reads**. A read the provider did not answer (timeout, transport error, 5xx) counts as tried,
so the next pass starts with the downloads behind it and failing reads cannot take the start of every pass. The
download is backed off: read again after the poll interval doubled per failed read in a row (`poll_errors`), at most
15 minutes. A read cut by the pass deadline only records the attempt: it neither counts as failed nor clears the
failed reads in a row (`TestPollCutKeepsFailedStreak`). Read errors do not keep a download out of the stall
timeouts: a failed read is not movement, so the stall clock runs on from the add (`stalled_after`, still `added`) or
the last progress (`downloading_timeout`, `downloading`). Once it is past the limit with at least 3 failed reads in
a row (they need not span the limit), the download fails as stalled and is blocklisted for
re-search, unless its provider is down (below); nothing is deleted, since the torrent was not seen.
`TestPollFailedReadsRotateAndStall`.

**Provider outages**. Failed reads are recorded after the pass's other reads, and per provider the pass
decides whether the provider is answering:
- **Most of its downloads failing at once is its outage**, whatever its account says (RD's torrent
  endpoint can fail while `/user` answers): at least 3 failing, and more than half of the provider's downloads in
  the pass. Failing counts the reads that failed (5xx, 429, transport errors, gateway refusals) and the downloads
  not read this pass because their reads keep failing (the per-download back-off below); answered reads (a status,
  not found, a 4xx) and reads the gateway held back count only toward the total.
- **Otherwise a few failing reads are the torrents' own** once the provider shows it answers: another of its reads
  answered, or, when only 5xx answered, one account read. That read goes through the gateway's back-off for
  failures in a row (`gateway.HealthCheck`, one at a time per provider, still budgeted; a rate limit or Retry-After
  still refuses it): three failing torrent reads in a row back the provider off in the gateway, and the check must
  still run. When the account answers, its success ends that back-off and the reads it held back are
  read again (up to 3 rounds per provider within the pass deadline), so the decision rests on what the provider
  said about them. Those downloads stall and are blocklisted as above; one or two broken torrents cannot stop
  their stall clocks forever (`TestTorrent5xxThroughRealGateway`, real gateway and RD client on loopback;
  `TestTorrent5xxDoesNotFreezeStallClock`).
- **An account that fails too** (or is refused: a rate limit, a spent budget) means the provider is down.

The account check is only there to tell those apart; it does not pause anything (as in
zurg). An outage read is backed off like any failed
read and the stall clock keeps running. A download that reaches its stall limit while its provider is down ends as
**provider unavailable**: the provider gave up, not the release, so nothing is blocklisted. The download goes back
to `queued` with its provider fields cleared and a route job is queued (`error` says why); the provider's adds back
off through router pacing (unless a back-off already runs), so the route tries the next provider in the version's
order or waits for this one. The back-off excludes the provider from reuse as well: the registry or another download
may still name the torrent, and reusing it would restart its stall clock on the provider that is down. After the
back-off a known id is reused only when a read verifies it; one the provider still cannot read is `held_unread`
(`TestOutageContinuationUsesHealthyFallback`, `TestUnreadableHeldIDNotReused`,
`TestHealthyReuseTakesNoPacingSlot`). Theatarr's own add is left on the provider, as in any blind stall: the torrent was not seen,
so its ownership cannot be proved on it. Without a route queue (a CLI poll) the download fails, still not
blocklisted. A short outage therefore does not blocklist and orphan the downloads added just before it,
and a long one does not hold them forever (`TestProviderOutageEndsUnavailable`). A read held back by the gateway is
recorded as a failed read when the provider is down, otherwise only as tried. A provider's error answer for the
torrent (a 4xx) still counts as an answer; the provider saying it is unavailable does not, whatever the HTTP
status: AllDebrid's `MAINTENANCE` and `GENERIC` and TorBox's `DATABASE_ERROR`, `SERVER_ERROR` and `UNKNOWN_ERROR`
come in an error envelope on HTTP 200 and are read as a 5xx (`provider.ErrUnavailable`,
`TestAllDebridMaintenanceIsOutage`). Ready downloads are not read from the provider; their `ready_timeout` waits out mount outages instead
(`failure.go`).

**Selection behind the write gate**. A non-reused Real-Debrid add that waits for its file selection
while the router may not write for its version (Pause on, or the version Watch only) cannot be
selected; the wait is the gate's, not the torrent's. This is the only pause of the stall clock, since it is Theatarr's own
switch: the read clears the failed reads in a row and starts `download.poll_outage_since` (migration 00086; the
column now records only this pause) until a read that is not gated; then the selection goes out and the gated time
is added to `download.stall_offset`, which `stalled_after` does not count, and to `progress_at`. Turning Pause on does not blocklist such adds as stalled
(`TestGatedSelectionPausesStallClock`).

**Dangerous and executable files** (Sonarr v5, PRs #8775 `2b7906724` and #9093 `1c01b720a`;
`internal/router/extensions.go`). The first read that lists the torrent's files (any provider; on Real-Debrid
before anything is selected) checks every file, as v5's `TorrentClientBase.ValidateFileNames` does with its default
`FailDownloads = [Executables, PotentiallyDangerous]`:
- a potentially dangerous file (`.arj .lnk .lzh .ps1 .scr .vbs .zipx`) or an executable (`.bat .cmd .exe .sh`)
  anywhere in the torrent refuses the **whole release**, whatever else it holds (v5 rejects the release; it doesn't
  skip the file);
- the download goes `failed` with v5's message (`Caution:` then `Found potentially dangerous files with extensions:
  .lnk` and/or `Found executables with extensions: .exe`, one per line) and its hash goes on `router_blocklist` for
  the version, source `torrent_files`, in one transaction. Route and Plan skip it from then on (`blocklisted`);
- nothing is selected, and nothing is deleted on the provider.

Deviations: v5 checks a `.torrent` file before the add and never a magnet; Theatarr adds magnets only, so the check runs on
the provider's file list after the add. It is always on (v5's default for torznab, newznab, nyaa and torrent RSS
indexers); there is no per-indexer setting and no user-defined extension list. Tests: `TestTorrentFileRejection`
(v5's fixture cases), `TestPollRefusesDangerousTorrents` (Real-Debrid before selection, AllDebrid already
downloaded, the blocklisted hash not routed again).

**Real-Debrid `waiting_files_selection`.** Files are selected, through the gate, as "File selection on
Real-Debrid" below says: every playable file, for a movie and a series alike. For a non-reused torrent, no video at
all fails and blocklists the download for re-search, with nothing selected.

### File selection on Real-Debrid (`internal/router/playable.go`)

Real-Debrid caches a torrent per selected file set. Its API documentation describes the (since removed)
`/torrents/instantAvailability` answer as, per hash, "all file IDs variants", with "you must ask all file IDs from
this array on /selectFiles to get instant downloading" (https://api.real-debrid.com/). One set of a torrent can be
cached while another is not, so the set to select is the one most often selected by everyone else, whose
downloads filled the cache:

- Debrid Media Manager adds a magnet and selects every file its `isVideo` accepts, movie or series alike, and
  every file only when none is a video (`src/utils/addMagnet.ts` `handleSelectFilesInRd`,
  `src/utils/selectable.ts` `isVideo`, github.com/debridmediamanager/debrid-media-manager). Its cache index stores
  the files of such downloads.
- decypharr selects every file its `ValidateFileAllowed` accepts (video and audio extensions, samples, trailers and
  extras skipped by a regex, optional size bounds), movie or series alike
  (`pkg/debrid/providers/realdebrid/realdebrid.go` `CheckStatus`, `internal/config/misc.go`).
- zurg's `force_select_playable_files` selects the playable files.

**The rule (`Playable`, used by the router and the torrent manager's restore):**
- every video that is not a sample or trailer, plus the audio files beside them; samples and trailers only when
  nothing else is a video; nothing when no file is a video (an audio-only torrent too);
- the extensions are DMM's `isVideo` list as shipped: `.avi .flv .m2ts .m4v .mkv .mov .mp4 .mpg .mpeg .ts .webm
  .wmv`, and `.mp3 .flac .m4a` with a video. DMM's list, not decypharr's wider one, because the set has to match
  what the cache was filled with. `.vob` and `.iso` were dropped (DMM ships `.vob` commented out as "not yet
  confirmed working" on RD and has no `.iso`; decypharr has no `.iso`; RD cannot stream a disc image);
- a sample or trailer is DMM's rule: the word `sample` or `trailer` anywhere in the path (`\b`, so `_sample_` is
  not one); a file at the torrent's root named `rarbg*` (the RARBG promo video and text) is left out as DMM does;
- subtitles are never selected: neither DMM nor decypharr selects them, a set with them is one the cache was not
  filled with, and the library imports no subtitle file from a release. nfo, text, images and executables are never
  selected: with them in the set Real-Debrid can pack the torrent into one RAR, which cannot be streamed.

Movies get every playable file too, not only the largest video (a set few tools select). The
library still links only the movie's largest non-sample video (`planMovie` takes the first of the release folder's
videos, largest first, past the sample and runtime guard); the extras are on the mount beside it and never linked.
A cached-only movie whose playable set is uncached gets one more add with the largest video alone ("Cached-only on
Real-Debrid and AllDebrid", "One second selection"). Tests: `TestPlayable`, `TestChooseFiles`, `TestRouteAddsAndPolls`.

A reused torrent is never selected by Theatarr. If a reused RD torrent remains waiting, the poller leaves it for its owner
until `router.stalled_after`; then it fails and blocklists the download while keeping the shared torrent on RD.

**Status:**
- `downloaded` → `ready` (`ready_at`, the provider's name, progress 100). `cached` = 1 when the first read already
  showed it downloaded. Once that write commits, the router calls its ready notifier (`SetReadyNotify`, also after a
  download client's ready write), which never blocks: `theatarr serve` wakes the library's import pass with it, so
  the import does not wait for the next `library.mount.poll_interval` (`docs/design/library.md`, "Import of a ready
  download"). Every settings generation's router gets the same notifier (`TestRouteAddsAndPolls`,
  `TestClientPollMapsStates`).
- downloading → `downloading` with progress. Queued on the provider leaves the state as it is: `added` stays
  `added` (RD converting or queued, AD's queue, TorBox queued), `downloading` stays `downloading`.
- `downloading` with no progress for `router.downloading_timeout` → `failed` as stalled ("Stalled downloads").
- the provider's error statuses → `failed` with the status, blocklisted for re-search (source `provider`).
- still `added` `router.stalled_after` past the add → `failed` as stalled ("Stalled downloads").
- a torrent gone from the provider (`ErrNotFound`) → `failed`, blocklisted for re-search (source `provider`).
  These failures delete nothing on the provider.
- still `ready` past `router.ready_timeout` → `failed`, blocklisted for re-search ("Ready deadline").
- `adding` for more than a minute (a crash between the add and its record, or an answer that did not say) is settled
  from the account first ("Crash between add and record"), unless a route job for it is queued or running (the
  route settles it, and resumes it when the add never landed); one it cannot settle stays `adding`, however long
  that takes (never failed by time).
- `queued` for 10 minutes with no route job for it → a route job resumes it (step 8 of "One route").
- a download of a version deleted from settings (its key no longer in `versions`), `queued`, `added`,
  `downloading` or `ready` → `failed` with the reason, on the next poll before any provider read
  (`endDeletedVersions`). Nothing is blocklisted and nothing is read or deleted on the provider: the torrent stays on
  the account. `adding` rows are settled first, and `importing` rows and ready downloads a live import claim holds
  are left alone. Removing the last version counts too: with no versions left, every version-bound download is
  ended. Tests: `TestDeletedVersionDownloadsEnd`, `TestLastVersionRemovedEndsDownloads`.

### Crash between add and record

Route jobs run at least once, and the add and its record are two steps (`reconcile.go`). Hard rule: a torrent is
never added twice for one download, and an add whose outcome is unknown is settled from the provider before anything
is added again; time alone never turns "unknown" into "add again".

Before the add the router calls `jobs.Fence` (a route job that lost its lease stops there and sends nothing) and sets
the download `adding`, from `queued` only, with the provider and a fresh attempt token (`download.add_attempt`,
migration 00032); `updated_at` is when the add was sent. Every write that settles that add matches the token: the
record of an accepted add, the return to `queued` after a refusal, an adoption from the account, a resume, a failure.
So a route that lost the row to another process (which settled the add, or resumed and added it) writes nothing, and
a late answer cannot overwrite a newer result (`errAddTaken`; the torrent it added stays on the account, nothing is
deleted, and its id is logged). When the download was removed while the add was out (Dismiss), the route stops there
(`download_moved_on`) instead of failing: a job retry would find nothing in flight and add the removed release again
under a new row (`TestDismissDuringAddIsNotAddedAgainByTheRetry`). `failDownload` is a guarded move
from explicit states (`queued` for a route's own download, `added`/`downloading` for the poller), so a route
cancelled after its download moved on (imported, ready, added) leaves it alone (`TestCancelledRouteDoesNotFailAnImportedDownload`); neither it nor `queueDownload` touches a row in `adding`.

### Guarded transitions and queue removal

Every state change a route or the poller makes on a download goes through one primitive (`transition` and `move` in
`db.go`): the write applies only while the row is still in the states it expects, holding the add attempt (and, after
an add, the provider and torrent id) it expects; writes that start something (the add, a deferral) also need the
route job's lease (`jobs.Fence`). What must commit with the change runs in the same transaction: the provider-epoch
fence, the blocklist row, the follow-up route job. A write that finds the row elsewhere changes nothing
(`errAddTaken`), and a route whose own download was ended or taken stops (`cancelled`, step `download_moved_on`).

Removing a queue item (`Dismiss`: the arr queue, Activity, copy removal) ends the download in one transaction:
`failed` with the reason, the blocklist entry when asked, and every queued `router.route` job that would resume it
marked done ("cancelled: download N was ended"). A route already running finds it ended at its next write:
- a deferred route resuming it stops instead of opening a new download for the same release;
- the cached-only settle returns a deleted or kept uncached add to `queued` for the next provider only while the
  download still holds that add, so a download removed or blocklisted during the settle wait stays ended and nothing
  is added elsewhere. A blocklist entry for the release made during the wait (without a removal) ends the
  release before the next provider too;
- after an uncached add was deleted, the settle is recorded even if the route's context ends right then: the
  download is `queued` again, not left `added` with a deleted torrent.

Tests: `TestDismissCancelsDeferredRoute`, `TestDismissDuringSettleStaysEnded`, `TestUncachedDeleteRecordedAfterCancel`.

After the add, what the provider accepted is recorded even if the context ends right then. The row stays in `adding`
when the outcome is unknown:

- the call was interrupted (context ended, crash, lost lease);
- the answer does not say (`addUncertain`): a transport error, a 5xx, a body cut short or over the size limit, a
  success answer without a torrent id or that does not parse. Then no other provider is tried for the download, the
  attempt is recorded as `error` / `add_unsettled`, and the route is deferred past `addSettle`.

A provider's own refusal (an error answer with a code, any 4xx) and a request the gateway never sent (back-off,
budget, VPN) are definite: the row returns to `queued` and the next provider is tried. AllDebrid and
TorBox answer errors with HTTP 200: their error envelope with a code (`adder.Error.Refused`) is a refusal even for a
code it does not know (it backs the provider off, as `unknown` does); only TorBox's own server errors
(`DATABASE_ERROR`, `SERVER_ERROR`, `UNKNOWN_ERROR`) and envelopes without a code stay uncertain (`TestErrorEnvelopeIsRefusal`).

A row in `adding` is settled from the provider (`reconcileAdding`), by a route that reaches its hash and by the
poller (rows older than a minute, every poll). The poller's recovery is bounded (`TestPollRecoveryIsBounded`):
at most 25 rows a pass (selected in SQL: rows naming a provider with a read-only client, or none), least recently
checked first (`poll_tried_at`); their provider reads (`lookupAdding`) run without the router lock, within one minute
(`reconcilePassLimit`), with one account listing per provider per pass, each listing limited to an equal share of the
pass. A lookup cut short (by its listing's share or the pass) settles and notes nothing, but its row is marked tried,
so a provider that never answers in time goes to the back of the line instead of starving the other providers'
rows (`TestPollRecoveryDoesNotStarveProviders`). The results are applied under the lock, each write fenced
by the add attempt it read, so a row a route settled meanwhile is left alone:

- the hash is held there already (registry, a download of Theatarr's own; a known id is taken only when a read of it verifies
  it: one the provider does not answer for leaves the account's list to decide,
  `TestReconcileDoesNotAdoptUnreadableID`) or the account's torrent list shows it: the download
  points at that torrent (`added`, attempt code `reconciled`), nothing is added. When another download of Theatarr's own holds
  that torrent it is a reuse (`reused` 1), as at the add. Otherwise it is the entry Theatarr's journaled add (the `adding`
  row and its attempt) most likely made, and it is adopted as Theatarr's add (`reused` 0, `own_add` unset): the poller
  selects its Real-Debrid files, where a reuse would sit in `waiting_files_selection` until the stall timeout
  blocklisted a good release (`TestReconciledAddGetsFilesSelected`);
- the list does not show it and the add was sent more than `addSettle` (5 min: past the gateway's 60 s whole-request
  timeout and its 60 s budget wait) ago: the add never landed. A route resumes the same download and adds it; the
  poller fails it ("the add was interrupted and never reached …"). After 3 such adds to one provider that never
  landed (`maxUnlanded`, counted from the download's `reconciled` attempts), the resumed route skips that provider
  and tries the version's next one (or the next release), so an answer it cannot read cannot loop the same add on
  every resume (`TestUnlandedAddsCapped`);
- otherwise (sent less than `addSettle` ago, so a request still in flight in another process could land; the list
  cannot be read; no read-only client): it stays `adding`, unsettled, with the reason in `download.error` ("add not
  settled: …"), and a route reports it in flight (`add_unsettled`, `in_flight`) and queues the route that settles it
  (after `addSettle` from the add, at least a minute on), so the download is resumed once the account can tell
  rather than failed by the poller (`TestUnsettledContinuationQueuesAnother`). There is no timeout: Activity shows
  "Adding to X: not confirmed yet" and the `UnsettledAddsCheck` health check warns past 10 minutes.

A row from a build before 00032 that names no provider is checked on every provider of its version. Reconciliation
only reads. The adopted torrent is never provably Theatarr's (`own_add` unset), so neither the cached-only settle nor the
stall timeout can delete it. Nothing Theatarr knows after a crash proves its own request created the entry: timing (dated after the add
was sent), being the only entry with the hash and no other row knowing the id all hold just as well when another
client added the same hash while Theatarr's add never landed, so ownership is never inferred. Such a torrent stays on the account if it stalls: safe clutter, never a delete of someone else's torrent
(`TestCrashedAddTorrentNeverDeletedAsStalled`). Tests: `TestCrashAfterAddIsReconciledNotAddedAgain`,
`TestCancelRightAfterAddStillRecords`, `TestCrashBeforeAddLandedResumes`, `TestUnreadableAccountAddsNothing`,
`TestPollReconcilesInterruptedAdds`, `TestLostLeaseBeforeAddThenTakeover`, `TestUncertainAddIsSettledNotAddedAgain`,
`TestUnreadableAccountNeverTimesOutIntoASecondAdd`, `TestTakeoverDuringAnAddInFlight`.

## Download states (migration 00014): shared with the library

00001's `download`, `download_version` and `download_attempt` were never written. They are replaced, and the down
migration restores them.

| State | Meaning | Set by |
|---|---|---|
| `queued` | Release chosen, every provider paced; retried later | router |
| `adding` | The add is being sent to `provider` (set already, with `add_attempt`); left by a crash or an answer that does not say, settled from the account, never by time | router |
| `added` | The provider accepted it (RD: files maybe not selected yet) | router |
| `downloading` | The provider is fetching it; `progress` 0–100 | router (poll) |
| `ready` | The provider holds every selected file. **The library imports these** | router (poll) |
| `importing` | Import in progress (optional) | library |
| `imported` | Linked into the version's library | library |
| `failed` | `error` says why | router or library |

`download` columns the library needs:
- `title_id`, `version_id`, `version_key`, `info_hash`, `release_title`;
- `provider`, `provider_torrent_id`, `name` (the provider's torrent name, the mount folder), `original_name`
  (migration 00109: the name the torrent declared, `provider.Torrent.OriginalName`, which a mount such as zurg names
  the folder after; the poll's status record stores it when the read gives one and keeps it otherwise, as for
  `name`; `TestRouteAddsAndPolls`), `episodes` (JSON episode ids for series);
- `ready_at`, `imported_at`.

Also new: `router_pacing` and `router_plan`. `provider_cache` (00001) holds the `miss` and `uncached` rows with
`source` `router:*`, and (cache evidence) `cached` rows from `feed:<indexer>` and `cached`/`uncached` rows from
`check:torbox`, and `cached` rows from a router add the provider had at once (`add:<provider>`). Its CHECK already
allowed `cached`, so no migration. Migration 00022 adds `download.own_add`, `router_blocklist.researched_at` and
`acquire_run.research`.

## The arr views' queue

`internal/arrview/queue.go` fills `GET queue`, `queue/status` and `queue/details` from `download`. It lists the
states from `queued` to `importing`, for the view's versions.

| Download | `status` | `trackedDownloadState` |
|---|---|---|
| `queued`, `adding`, `added` | `queued` | `downloading` |
| `downloading` | `downloading` | `downloading` |
| `ready` | `completed` | `importPending` |
| `importing` | `completed` | `importing` |

- `downloadId` is the upper-case info hash, as for the arrs' qBittorrent clients.
- `downloadClient` is `theatarr router (<provider>)`, `protocol` is `torrent`, and `sizeleft` is computed from progress.
  `timeleft` is left out.
- Radarr lists one item per download, filtered by `movieIds` (view ids or aliases). Sonarr lists one per episode,
  with `episode` when `includeEpisode=true` (Seerr asks for it), filtered by `seriesIds`, and in `queue/details` by
  `seriesId` and `episodeIds`.
- `queue/status` counts the items.
- The empty goldens are unchanged. `TestQueueFromDownloads` covers the filled queue.

## Copies to other providers

"Add to other providers" (the owner's decision of 2026-11-13, phase H part 2) adds a torrent the library holds to every
configured provider that does not hold it yet, so a provider outage leaves the file streamable from another. It adds
the torrent; an add that is not cached is deleted on every provider; it is a manual action (managers: a movie's file
on the title page, an episode's in the episode window; admins: a Torrents row) plus `router.copy_to_providers`
(off by default), which queues the same work after each import of a download that came from a provider. New imports
only: the existing library is not swept. Code: `internal/router/copy.go`; table `provider_copy` (migration 00128).

A copy is not a download: no `download` row, import, offer, blocklist, failure re-search or History entry. One row per
hash and provider (`unique (info_hash, provider)`); a new request for a pair replaces a finished row and returns an
unfinished one as it is (a double click, a season pack's episodes). Rows are made for the providers with a key whose
registry row does not hold the hash complete ("Cached on"); one job (`router.copy`) works each row:

1. **Stop early.** Paused (or the router off): `skipped` ("Skipped: paused"). Held complete meanwhile: `added`. The
   account at `TorrentLimitWarnPercent` of its limit (the last complete sync, as the health check counts it):
   `skipped`. A running back-off or Real-Debrid hold: `skipped` with its end. Real-Debrid and
   `indexer.RealDebridRefuses(name, loose)`: `refused`, never sent.
2. **Read-only evidence** (`checkHash`): a live `provider_cache` row; else DMM's availability for that one hash on
   Real-Debrid and AllDebrid (`indexer.Service.CheckAvailabilityHash`, handed to the router as
   `Config.AvailabilityHash`; a listed hash is stored like a search's, an unlisted one is unknown, never uncached);
   on TorBox its own cache check. Uncached: `not_cached`, nothing added.
3. **The add**, under the add lock only for this step, as the torrent manager's restore holds it. A route's add of
   the hash to that provider still unsettled (`download` in `adding`): the copy waits. Every provider's account is
   listed first; a failed listing sends nothing and the row waits. The hash on the list: `added`, "Already on AD";
   Cached on (`content_provider` present/100) is written only when that entry is complete, otherwise "Already on AD,
   still downloading (12%)". The account at `TorrentLimitWarnPercent` of its limit by that fresh listing: `skipped`.
   The pacing reservation is the route's (`updatePace`); a provider paced out hands the job back (`jobs.Later`).
   The gate is read again right before the add. `adding` is written before it, then `checking` with the torrent id.
   Outcomes go to pacing as the route's `classify` records them; a 451 is the route's hold handling (`note451`) and
   ends `failed`, never retried; TorBox's not-cached refusal ends `not_cached`.
4. **Settle** with the route's read-back (`readBack`, shared with `readAfterAdd`; Real-Debrid selects every playable
   file, the gate read again before the selection, then the grace). Complete: Cached on written, `added`. Not
   complete (or dead): the uncached verdict is recorded (`provider_cache` uncached for `miss_ttl`) and counted in
   `uncached_adds_per_hour` once, then our add is deleted when it is ours. Copies delete regardless of
   `alldebrid_delete_uncached`; the delete is `adder.StalledDeleter` on all three providers (AllDebrid and TorBox
   implement it for copies only), so it needs no in-memory ledger and survives a restart.

**The rules** (the fix round of 2026-11-13; `copy_rules_test.go`):
- **Ownership** (`own_add`): true only when a successful listing read just before the add showed neither the hash nor
  the id the add returned, no registry row shows the hash present on that provider, and the returned id is not a
  registry entry of the hash (an absent registry row, an old torrent since removed, does not count). Otherwise the
  entry is never deleted. Known limit: after a lost add answer, an entry found by hash is treated as not ours and kept.
- **Before every delete** (the first, each retry, after a restart; an owed delete included): the torrent is read
  again. Gone (confirmed by a fresh listing that shows neither id nor hash when the read failed: TorBox answers a
  missing id with a 500): `not_cached`. Complete meanwhile: Cached on, `added`. Another hash, or in use on this
  provider (a download of ours naming the id, or the hash there in any state but failed; a registry entry giving the
  id another hash; another copy with the id): `failed`, "left in place". The use check, the gate and the delete run
  under the add lock, so no route adopts the torrent in between. Complete at that read: cached evidence replaces the
  uncached verdict, as on a settle.
- **Real-Debrid with nothing playable to select** (an .iso, a DVD's .vob): `failed`, "Real-Debrid cannot serve this
  file type", and our trial add is deleted under the same checks.
- **Pause**: the gate is read before every provider write (add, file selection, delete). A queued copy is
  `skipped`; one past `queued` only waits (an `adding` row is not rewound while paused).
- **Never stranded**: anything an unfinished copy cannot do now hands the job back (`jobs.Later`) after the time it
  has waited since its last change, 1 to 30 minutes; the job's attempts are never used up. A new request for an
  unfinished row whose job is gone queues one, and so does every poller pass (`ResumeCopies`). The uncached verdict is
  counted in the transaction that records the owed delete.
- **Routes and copies wait for each other**: a route defers (`unsettled`, a minute) while a copy of the hash to that
  provider is `adding` or `checking`, before it would reuse a registry entry of the hash there; a copy waits while a
  route's add there is unsettled, checked under the add lock just before its listing.
- **Every known name**, once per request: Real-Debrid's name rule is applied to the request's names (the file's
  release and scene names, a provider entry's display and original names), its downloads' release and original
  names, the release names of the links and arr files tied to it by content, download or an arr file's download id,
  and the registry's names; every read is by hash. Any refused name makes the copy `refused`, never sent; a queued
  row a later request finds refused becomes `refused`.
- **Hold times** are `provider_copy.until_at`, sent as `until`; the browser shows them in its own zone.

**Recovery.** At start (`ResumeCopies`, every unfinished row without a queued job) and on each job run, the row's
state says what is left. `adding` without a torrent id waits `addSettle` from its last update, then the account's
list is read: a torrent of the hash there is taken up (`own_add` false: it may be an older one, so it is never
deleted); none: `queued` again. `checking` is settled from the account, an owed delete through the checks above.

**API.** `POST /titles/{id}/copies {version, episode_id?}` (manage) takes the hash and names of the file the page
shows (the movie's current file, the episode window's file). One resolution gives that file's hash to the action, the
Torrent link, `copies` and Cached on (`linkHashSQL`/`fileHashSQL`, `heldByHash`): a link's content, else the hash it
records (download, file metadata, adopted source) with or without a content row; an arr file's content, else its
download id when that is a v1 hash. `POST /torrents/copy {provider, id}` (admin) takes the hash and name from the
registry, else the display and original names from a read of the provider. Both
answer the rows; 409 with the Pause words while paused; 422 "Cannot add to other providers: Theatarr has no torrent
hash on record for this file." `GET /torrents/copies?hash=` (admin) reads one torrent's copies for the Torrents page
to poll. The movie's current file, the episode file and the Torrents row carry `copies` (`provider`, `state`,
`message`, `updated_at`, `until`), see files only.

Tests: `TestCopyTorBox`, `TestCopyRealDebridTrial`, `TestCopyAllDebridList`, `TestCopyRefusedAndPaused`,
`TestCopyWaitsForMinGap`, `TestCopyRestartInChecking`, `TestCopyLostDeleteAnswer`, `TestCopyRecoversAdding`,
`TestCopyDedupe`, `TestCopyRule*`, `TestCopyRound2*`, `TestProviderCopiesOneHash`, `TestProviderCopiesSameHashAndPoll`, `TestCopyAfterImport`, `TestProviderCopiesAPI`, `TestRouterSectionCopyToProviders`.

## Policy (`router`, `examples/policy/router.yaml`)

```yaml
router:
  enabled: false       # the kill switch; default false
  cached_only: true    # skip known-uncached providers; TorBox adds only if cached; RD/AD: add, read, delete Theatarr's own
  alldebrid_delete_uncached: true   # default; delete Theatarr's own uncached AD add (see the accepted race above); false keeps it
  copy_to_providers: false  # default; after each import add the torrent to the other providers ("Copies to other providers")
  cached_grace: 2s     # RD/AD: wait before the one re-read of a fresh add; 1s to 30s
  stalled_after: 10m   # fail, blocklist and re-search a download still added this long; 1m to 24h; 0s = off
                       # (providers.<key>.stalled_after overrides it; AllDebrid defaults to at least 25m)
  downloading_timeout: 24h  # the same for a download whose progress has not risen this long; 1h to 7d; 0s = off
  ready_timeout: 6h    # fail, blocklist and re-search a download still ready this long; 1m to 7d
  miss_ttl: 6h         # 10m to 7d
  poll_interval: 1m    # 15s to 1h
  max_candidates: 5    # 1 to 50
  trackers: []         # public announce URLs added to magnets; none by default
  cache_evidence:      # see "Cache evidence"
    ttl: 24h           # positive evidence kept; 1h to 7d
    torbox_check:
      enabled: true    # nothing without a TorBox key
      top: 10          # hashes of one search, best first; 1 to 100
      per_hour: 60     # checkcached requests in any hour; 1 to 1000
  wanted:              # the search schedule ("The search schedule"); nothing is searched while paused
    per_hour: 10       # searches in any hour; 1 to 200
    again_after: 24h   # older items are searched again after this; 1h to 30d
    missing: true      # search for missing items by themselves
    cutoff_unmet: quality  # quality (default), score or off; true = quality, false = off
```

Each tracker must:
- use udp, http or https;
- have no credentials, query or fragment;
- have a path of `""` or `/announce`.

The provider order per version is the existing `versions[*].providers` (validated against `providers[*].order`).
Pacing is the existing `providers[*].pacing` (with `min_gap`, "Pacing"); `providers[*].stalled_after` overrides the
stall timeout per provider ("Stalled downloads").

## Commands

**`theatarr router dry-run -title ID|-tmdb N|-tvdb N -version KEY [-season N [-episode N]] [-json]`.**
- It searches like `theatarr decide -search`, through the gateway and its VPN check, then decides and ranks.
- It prints the plan for every candidate and provider, then "would add X on P".
- It and `theatarr decide -search` rank with the evidence the releases carry and count every provider of the version
  in the cache tie-break: no stored `provider_cache` rows and no keys stored in settings, so the order of equally
  scored releases can differ from a live search's.
- Nothing is sent to a provider or recorded.

**`theatarr router status [-json]`.** It reads only:
- the switch, `cached_only`, and the active versions;
- per version: may it write;
- per provider: key set, adds this minute, uncached this hour, cooldown, back-off;
- downloads by state, recent downloads and plans, live misses;
- the blocklist: hashes a version never routes again, with the reason and who added them.

**`theatarr router reconcile [-db FILE] [-apply] [-json]`.** See "Reconciling legacy queued routes" below. Dry run by
default; `-apply` writes only to the database (no provider call).

## Deviations

- **Direct adds.** An arr setup routes adds through decypharr with a per-add `debrid` field. The router adds to the
  providers directly, through the gateway: one outbound path, with budgets and the VPN guard enforced in one place.
  decypharr stays a client of the arr views.
- **Cached-only on RD and AD deletes, with a stricter guard than decypharr.** Neither has a cache check, so cached-only
  is decypharr's add, read, delete-and-try-next. decypharr deletes whatever id the add returned; Theatarr deletes only an
  entry it can prove is its own new one, and keeps (and downloads) any other. The proof has a hole on AllDebrid:
  another client uploading the same hash between Theatarr's list read and Theatarr's upload (about a second) gets the same magnet,
  which Theatarr would take for its own. The owner accepted the race: `router.alldebrid_delete_uncached` is on by
  default (2026-11-13); with it off an uncached AD add of Theatarr's is kept (`uncached_kept`) and the next provider
  tried.
- **Cache tie-break.** The arrs' comparer never looks at cache evidence. Theatarr ranks a debrid release cached on
  more providers ahead of an otherwise equal one, because it has fallbacks: when one provider fails, the route tries
  the next that holds it. One comparer step (`compareCached`), for Radarr right after the format score, for Sonarr after the lowest
  episode number (a season pack still wins at an equal score), before protocol and indexer priority:
  1. cached beats not cached;
  2. with `provider_preference: ordered` (the default), the earliest position in the version's provider list among
     the providers holding it: cached on the first beats cached only on the second, and so on;
  3. then more providers holding it wins.

  `versions[*].provider_preference` is `ordered` or `none` (YAML, the settings API, Admin › Settings › Libraries,
  **Provider preference**); a change on an active version needs confirmation, as any unjudged version field.

  With providers `rd, ad, tb`: rd+ad+tb > rd > ad+tb > ad > tb > not cached. With `provider_preference: none`
  step 2 is skipped: rd+ad+tb, then any two, then any one, then none. Releases without evidence keep the arr order
  among themselves, and a better quality or format score still wins; SeaDex standing (`prefer_seadex`) still comes
  first. Only providers the release may go to count: the version's providers as the indexer's `providers`
  allowlist narrows them (`policy.ReleaseProviders`), enabled and with a key (`decide.Engine.SetUsableProviders`;
  without it, as in `theatarr decide` and `theatarr router dry-run`, every provider of the version counts). The
  evidence is the route's (see "One route"). The route's provider order does not change: list order with cached
  providers first, in both modes. A search across several libraries checks each target in turn: evidence a later
  target's TorBox check gains is not applied to the order of targets already ranked; the next search sees it, since
  the check stores its answers.
  Tests: `TestRankCachedOrdered`, `TestRankCachedNoPreference`, `TestRankCachedAfterQualityAndScore`,
  `TestRankCachedUsableProvidersOnly`, `TestRankCachedOnExtraOnly`, `TestSonarrCachedAfterEpisodeSteps`,
  `TestCheckTopEvidenceReranks`, `TestRSSStoredCacheBreaksTie`, `TestRedecideReadsStoredCacheEvidence`,
  `TestRSSKeepsAccountOrder`, `TestRedecideKeepsAccountOrder`, `TestShadowGrabArrReleaseCacheEvidence`,
  `TestShadowGrabPlanKeepsAccountOrder`, `TestShadowGrabArrStandingIndexerAllowlist`.
- **The miss rule.** A miss needs a verdict from every provider that has a key. A provider that is only paced defers
  the route.
- **Reuse.** A hash the provider already holds is reused, not added or selected again: the registry is read before
  every add, and the torrent id it names is read back from the provider before it is reused. A reused RD torrent waiting for its owner's file selection is polled until it downloads or reaches the
  stalled deadline; if it stalls, Theatarr fails and blocklists its download while keeping the shared torrent on RD. A
  reused or finished torrent whose files are known and hold no video fails at once instead (`no video file in the
  torrent; ... kept on <provider>`), blocklisted on Theatarr's side only; Theatarr never selects or deletes on it. "No video"
  is decided by path alone (`HasVideo`): any video extension of the selection below plus `.vob` and `.iso`, samples
  included. It needs no file ids (AllDebrid and TorBox files have none) and does not apply Real-Debrid's selection
  limits: AllDebrid and TorBox take every file, so a DVD's `.vob` is a video there (`TestAllDebridFinishedIsReady`, `TestOtherProvidersKeepVOB`).

## Triggers (migrations 00016 and 00033)

What hands work to the router in `theatarr serve`. Code: `internal/acquire`, `internal/acquire/rss`,
`internal/arrview/command.go`, `internal/router/hooks.go`.

Movie searches (including search on add, wanted, re-search and version fanout), RSS and release grabs enforce minimum
availability before acquisition. `announced`/`tba` is immediate, `inCinemas` uses the cinema date when known, and
`released` uses the earlier digital/physical date, falling back to cinema plus 90 days only when both home dates
are absent; unknown dates wait, with Radarr's default availability delay of zero. Wanted skips unavailable movies
before its hourly budget is spent, and queued searches recheck the gate. The latest explicit setting on a view
serving the version wins over its imported arr setting; without one, the default is `released`.

| Trigger | Versions | What reaches the router |
|---|---|---|
| Arr view search commands and an add's search option | **active only** | `Router.Enqueue` per search, through `internal/acquire` |
| The search schedule | **active only** | `Router.Enqueue` per search, through `internal/acquire` |
| RSS read, items of active versions | **active only** | `Router.Enqueue` per title and version, its items ranked |

**The acquirer (`internal/acquire`).**
- A search group is one root `acquire_run`, one `acquire.search` job, and a row for every version-target it serves.
  Child rows point to the root with `search_run` (00033), so command status and each version's route stay distinct
  while the indexer query runs once. Identical `(indexer, query)` pairs are sent once; each version is decided only
  against the queries in its own indexer list (`router.SearchRequests`).
- Each accepted version gets its own `router.route` job with `Source` `acquire:<run id>`, committed with that target
  row's `routed` state. The router reuses a provider torrent already added for another version of the same title.
- The job claims the run for its lease (`acquire_run.claim`, migration 00027) and has three attempts. A run left
  `searching` by a crash or a shutdown is taken over by the job's next lease and searched again; the outcome is
  recorded only under the claim that holds it, so a run is never routed twice (`internal/acquire/recover_test.go`).
- A target is a movie version, a season or an episode (`target` `movie`, `s01`, `s01e02`).
- `Submit` drops every version that is not active. The job checks the mode again before it searches, and skips a
  target whose title (movie) or episodes (series) have a download in flight (`router.InFlight`).
- A command that names titles (or episodes) also serves the other active versions of those titles (the fanout), but
  only where the title (or episode) is monitored in that version, the version was not removed (Seerr delete, "Remove
  copy") and the movie is available, as an automatic search would take it. The user-invoked semantics (unmonitored
  too, before minimum availability) apply to the versions the caller named only. A run for a version that joined
  this way is stored with `acquire_run.requested = 0` (migration 00084), and its route runs the freshness check on
  its first pass like an automatic route; a named version's route skips it until resumed.
  `TestTitleSearchFanoutSkipsUnmonitoredAndRemovedVersions`, `TestJoinedCommandRouteChecksFreshness`.
- A command that names none (`MissingMoviesSearch`, `CutOffUnmetMoviesSearch`, `MissingEpisodeSearch`,
  `CutoffUnmetEpisodeSearch`) searches only the versions it asked for, the view's active ones. That is what that
  arr would search: `/v/K` searches K only, `/radarr` its movie versions, `/sonarr` its series versions, never
  another kind. `TestBulkCommandSearchesOnlyItsVersions`, `TestCutoffUnmetCommandsQualityOnly`.
- Series:
  - an episode search per named episode;
  - otherwise a season search per season with selected episodes;
  - a missing or cutoff search that finds one episode in a season searches that episode;
  - specials only when named; missing and cutoff searches take aired episodes only.
  - **upgrades off** (Sonarr v5 `b94cf9b3b`): a whole-series search (`SeriesSearch`) of a version whose profile
    has `upgrades: false` (or an arr profile with `upgradeAllowed` false) takes only the episodes without a file, so a
    season whose episodes all have one is not searched. As in v5 a season, episode, missing or cutoff search is
    unchanged; the search schedule's missing pass never searched such a season anyway, and with upgrades off the cutoff is
    the first allowed quality, so files are rarely below it. `TestSeriesSearchSkipsFilledSeasonsWithoutUpgrades`.
- A command over a whole view (`MissingMoviesSearch` and the like) starts at most 100 searches (`acquire.MaxBulk`),
  the title-target groups searched longest ago first.

**What the search schedule searches.** When it searches is "The search schedule" above; this is what it takes. With
no `router.wanted` keys it searches the whole backlog at `per_hour` from the first Live start; `missing: false` and
`cutoff_unmet: off` leave the backlog to RSS, searches on add, searches by hand and the re-searches after a failure.
The schedule paces by `per_hour` and the router by provider pacing only; there is no separate per-season bulk pacing.
- It takes the active versions' missing (`wanted`) and cutoff-unmet (`filled`) movies and episodes; cutoff-unmet
  ones only of versions whose `upgrades` is not `off` ("Upgrades").
- `router.wanted.cutoff_unmet` says which cutoff-unmet files it searches:
  - `quality` (the default) searches only files whose quality is below the profile's quality cutoff. With
    upgrades off, the cutoff is the first allowed quality. This is the arrs' Wanted > Cutoff Unmet list and its
    search: Radarr `MovieCutoffService.MoviesWhereCutoffUnmet` and Sonarr
    `EpisodeCutoffService.EpisodesWhereCutoffUnmet`, used by `CutoffUnmetMoviesSearch` and
    `CutoffUnmetEpisodeSearch`. They select by quality only (`BuildQualityCutoffWhereClause`).
  - `score` searches every `filled` file, also one at the cutoff quality but below `cutoff_score`.
  - `off` searches no file for an upgrade.
  - `true` and `false`, the earlier schema, still load as `quality` and `off`; anything else fails validation.
- Only the schedule's selection changes. Version and episode states (`filled` while below the quality or the score
  cutoff), feeds and every upgrade decision keep the full cutoff, as the arrs' RSS sync and searches do
  (`UpgradableSpecification.CutoffNotMet`). A file at the cutoff quality but below the score is still upgraded when
  a feed or another search brings a better-scored release.
- The file judged is the one decide compares against: the newest active link, else the arr's file. Without a
  decision engine the state alone decides, as with `score`. `TestWantedCutoffMode`, `TestQualityCutoffSelection`.
- The arr views always use the quality cutoff, whatever this setting: `GET wanted/cutoff` and the
  `CutOffUnmetMoviesSearch` / `CutoffUnmetEpisodeSearch` commands list and search what the arr would. The setting
  governs Theatarr's own schedule only. `TestWantedCutoffQualityOnly`, `TestCutoffUnmetCommandsQualityOnly`.
- Why: imported arr profiles often set a `cutoff_score` (10000, say) that no real file reaches. With
  `score`, the schedule would search every title with a file again every `again_after`. The arrs never search those.
- At most `per_hour` shared searches start in any hour, counted from root `acquire_run` rows of source `wanted`;
  child version rows do not spend another search, and gated runs (Pause) spend none.
- It does nothing while Pause is on (`router.enabled` false), and searches only Live versions
  (`Policy.SearchVersions`). Test: `catalog.TestSearchable`.

**Command status.** A command's status follows its runs (`acquire.LoadCommandStatus`), the route job, and what the
router did with the run's source:

| Runs | Command |
|---|---|
| All waiting | `queued` |
| Any searching, routing, or its download `queued` for a provider | `started` |
| All ended | `completed`, with "added X on P", "no accepted release", "a download is in flight", or "dry run (disabled): would add X on P" while the kill switch is off |
| All failed | `failed` |

**Imported-release offers.** `router.offer` offers an imported movie or series release to the title's other versions
of the same kind. Series decisions use the imported download's episode ids as Sonarr search criteria; accepted offers
route one release, so the router reuses the provider torrent and the library links the season pack or episodes.
The six-hour sweep also covers adopted links, from their stored source (below); `-sweep -dry-run` lists decisions, unknown hashes are remembered as `no_hash`,
and neither the source version nor the arr's own library receive its files. Pending offers retry every
15 minutes for six hours, then daily for at most seven further attempts; readiness changes can retry early within
that cap, and the final `gave_up` carries `retry_limit`. Recorded replacements retire sources and cancel their
pending offers as `declined` with reason `superseded`; every accepted offer still passes the existing router,
mode, version-indexer, blocklist and library gates.

An adopted link is offered from the source stored on it (`link.adopted_source`: hash, release, the arr's indexer
names and the library the arr filled, with the `adopted_from` it was read for), resolving the names with
`IndexerByName` and keeping the arr's library out of the offer (`TestAdoptedLinkOffersWithoutFollowing`). The arr
history is not read: an adopted link without a stored source, or whose `adopted_from` has changed since, has no
indexer and is not offered (`TestAdoptedLinkWithoutStoredSourceNotOffered`), as unknown indexers already are. Imported
arr files (`version_file`) are not offered; an open offer (pending or retry memory) whose source was one is deleted
at the next sweep, not declined, so the same file from an adopted link is decided afresh (`TestArrFileOfferForgotten`).

Arr history names an indexer by the arr's own name ("TheRARBG (Prowlarr)", "DMM RD (Prowlarr)"), never a policy key.
`Policy.IndexerByName` matches it to a configured indexer: the key itself, a name an indexer's `arr_names` lists
(a plain alias list, without regard to case: "DMM RD (Prowlarr)" for `dmm`), or the name without a trailing " (…)" compared, letters and digits only and without regard to case,
with the key or the key without its `prowlarr-` prefix; a name two indexers match is no match. A name that matches
none keeps the offer `declined` `unknown_indexer`. That decline (and an old `private_indexer` one) is decided again on
the next offer or sweep once the source's indexer resolves, and the target's own rules decide (`IndexerNotInVersion`
when the version does not list it). Other declines stay remembered.

An accepted offer to a target the router gate refuses (`router.enabled` off, or the target in shadow) is recorded
`planned` (its route is a dry-run plan), not `routed`. A `planned` offer is offered again, and routed, by the
next offer or six-hour sweep once the gate is open for the target; while it stays shut nothing is decided or queued
again. After the flip the planned offers are paced: one offer pass routes at most
`OfferPlannedPerPass` (20) planned offers, the rest wait for the next pass, and a planned offer not offered again for
`OfferPlannedExpiry` (7 days) is declined `planned_expired` instead of routed (`TestPlannedOffersPacedPerSweep`,
`TestPlannedOffersExpire`). Migration 00069 turns `routed` rows that made no download of the target for that hash (accepted while the
router was off) into `planned`. Tests: `TestOfferArrIndexerMatchedByName`, `policy.TestIndexerByName`,
`TestOfferWhileGateShutIsRetried`, `TestOfferGateRetryMigration`.

**Active RSS.** RSS reads the feeds of shadow and active versions. Accepted items of an active version are not
shadow picks:
- they are collected per title and version;
- each group becomes one `router.route` job, `Source` `rss`, unless a download is in flight for it. The job's dedupe
  key is a hash of the whole request (target, version, candidates with their episodes, policy revision), so a second
  RSS read before the worker opens a download queues no second route, while other episode coverage or changed
  candidates get their own job (`TestRSSRouteJobsDeduplicated`, `TestRSSPendingRouteNotDuplicated`).
- A read marks its items seen (`shadow_rss_seen`) only after its picks are stored and its routes enqueued, in one
  transaction; a read that fails before then leaves its items to the next read (`TestRSSSeenOnlyAfterRouting`).
- A seen mark is per policy revision: after a settings save changes the revision, items still in the
  feed are decided again under the new policy, so an item rejected before a profile or rule change can be picked
  (`TestRSSSeenPerPolicyRevision`).

**Proof (tests).** The fake adders (`internal/router/routertest`) log every call.
- `TestActiveRSSRoutes` (`internal/acquire/rss`): movie-web active, movie-remux shadow. The active picks are added,
  and there are no downloads for the shadow version.
- `TestSearchCommandsShadowNeverWrite` (arrview): every search command and the add option on shadow views. No search,
  no run, zero writes.
- `TestSearchCommandActiveVersion` (arrview): searches and adds for the active versions only, and the command status
  follows them.
- `TestShadowVersionsNeverSearchedOrWritten` (acquire): Submit, a stale run for a now-shadow version, and the search
  schedule.
- `TestCommandFanoutSearchesTitleOnceAndRecordsServedVersions` (acquire) and `TestSearchRequestsSharesQueriesAcrossVersions`
  (router): a shared query records both version targets and runs the upstream request once.
- `TestKillSwitchDryRun` (acquire): an active version with the switch off. A dry-run plan and zero writes, and the
  schedule searches nothing.

## Decision formats and the blocklist (00016)

Blocklist entries the former periodic arr import mirrored stay. Hashless entries match source titles
(ignoring case and surrounding whitespace); either a hash or title match prevents routing. Title-only entries use a
`title:` key in the blocklist's `info_hash` field; an unblock is reset by the next import while the arr still lists
the entry.

- **Formats.** `router.Candidate.Formats` carries the decision's matched custom formats with their scores
  (`router.Candidates` fills it). The download stores them in `download.formats` (JSON `[{name, score}]`) beside
  `quality` and `score`. The library names files from them (`library.Options.Matched`: the profile's
  `includeCustomFormatWhenRenaming` formats, in profile order). A download without them (NULL) still decides its
  release title again.
- **Blocklist.** When the library's sample, runtime or mislabel guard rejects a download, it fails the download and calls
  `router.Blocklist` in the same transaction: `router_blocklist` (version, hash, title, release, reason, source
  `library`, download).
  - Route and Plan skip a blocklisted hash for that version: step `blocklisted`, a content verdict, so the next
    candidate is tried.
  - `theatarr router status` lists the blocklist.
  - Nothing is deleted on any provider.

## Upgrades

`versions[*].upgrades` says what a route does with a release that would **replace a file the version already has**:

| Rule | What a route does with an upgrade | Arr equivalent |
|---|---|---|
| `auto` (default) | routes it like any other release | profile `upgradeAllowed` on |
| `off` | skips it (`upgrades_off`); the file stays whatever is found. Cutoff-unmet searches (the search schedule and commands) skip the version | `upgradeAllowed` off (`UpgradeAllowedSpecification`), for the version rather than the profile |

The profile's own `upgrades: false` (arr `upgradeAllowed`) still applies inside the decision engine; the version rule
sits on top of it in the router. `Router.Route` reads the version's current files for the release's items before any
candidate is tried (`upgrades.go` `currentFiles`: active links, then the arr's `version_file` rows of the owning
instance). Releases for items without a file are never refused: in a series route a pack that would replace episode
1's file is skipped under `off` while a release that only fills episode 2 is tried as usual (`TestUpgradesOff`).

`approve` (hold an upgrade until a manager approved it) was removed in 2026-11: migration 00118 drops `upgrade_hold`,
and a stored or submitted `approve` reads as `auto` (policy defaults and the stored library rows). A route job an older
build queued for an approved upgrade (source `upgrade:N`) still runs as an ordinary route and counts as a person's
grab (`TestQueuedApprovedRouteRunsAfterUpgrade`).

## Client-routed versions (`add_via: client`, migration 00108)

Spec: `docs/design/external-clients.md`. A version with `add_via: client` sends its grabs to its
`client` (a `kind: decypharr` client; qBittorrent Web API, `internal/qbit`) instead of adding to the providers.
The default, `add_via: providers`, is unchanged. A client-routed version lists no providers; the client picks its
own debrid account, so `router.cached_only`, the providers' pacing and `stalled_after` do not apply to it.

- **Rows.** The download row sets `download.client` (migration 00108) and keeps `provider` NULL, so every provider
  path (`pollSweeps`, `checkStuck`, provider deletes, the unsettled-adds check, cap accounting) passes it over.
  `provider_torrent_id` is the info hash. Attempts record `provider = client:<key>`.
- **Before the add.** Releases from an indexer with a `providers` allowlist are skipped (`indexer_providers`): the
  allowlist names debrid accounts, and the client chooses its own. A `client:<key>` miss in `provider_cache` within
  `miss_ttl` skips the release (`known_miss`). A version whose client is not built (another kind, bad URL) gets
  `no_client`. A dry run (`theatarr router dry-run`, the shadow plans) reports `would_add` with the client and makes
  no call.
- **The add.** One `torrents/info?hashes=<hash>` read first, with no category filter: a hash the client already
  holds, under any category (an arr's from before the handover), is adopted as it is (`client_adopted`,
  `reused = 1`), not added again. Then the row goes to `adding` and `torrents/add` sends the magnet with the client's `category`.
  decypharr's JSON errors (`code`, `retryable`, `permanent`) decide the verdict (`qbit.Classify`):
  - `torrent_not_cached` (404): a miss. A `client:<key>` miss row is cached and the route moves to the next release
    (`client_not_cached`); nothing is blocklisted. It counts as a miss everywhere (`missedEverywhere`), so an
    exhausted route searches again; a `known_miss` skip of a cached miss row does not.
  - a permanent error, `torrent_blocked`, `invalid_magnet`, or a refused login (`Fails.`): the release is
    blocklisted with source `client` and the route moves on (`client_refused`).
  - anything else (traffic, too many active downloads, 5xx, no answer): the route stays queued and is retried after
    `BackoffBase` (`client_deferred`), as when every provider is paced, until the route gives up.
- **Polling.** `pollClients` reads each client's rows (`added`, `downloading`, and `adding` rows older than a
  minute) with one `torrents/info?hashes=<hash>` call per row, under any category: decypharr 2.7.1 reads each
  `hashes` value as one hash, not qBittorrent's `h1|h2`. Only the completed states (`pausedUP`, `stoppedUP`,
  `uploading`, `stalledUP`, `queuedUP`, `forcedUP`, `checkingUP`) set `ready`, with the client's torrent name;
  progress 1 in a downloading state is still downloading (decypharr reports it before its links exist). With
  `import_from: download_folder` ready also sets `source_path` to the content path mapped from `remote_path` to
  `local_path` (an empty content path waits as `downloading`). `error` and `missingFiles` fail the download, as a
  dead provider torrent does: blocklist and re-search. A hash the client no longer lists goes through the same
  missing-read check as a provider torrent before it fails. An `adding` row is settled from the client's list
  (listed: `added`; not listed: back to `queued`).
- **Outage.** Each read has its own deadline and is applied as it arrives; the first failed read ends that client's
  pass, and the downloads not read are left for the next pass, never counted missing. A client that does not answer
  fails nothing: its downloads wait, the reads back off, and one error
  line per outage is logged ("download client does not answer; its downloads wait"), with an info line when it
  answers again.
- **Safety.** Nothing deletes from a client: Dismiss, failure, expiry and replacement only end Theatarr's row.
  `router.ready_timeout` still applies. The gateway counts the client as a `client` target, and its password is
  redacted from logs. The router switch's confirmation names the client of a client-routed version. Changing `add_via` on an active version needs confirmation in the settings.
- **Tests.** `internal/router/client_test.go` (fake API: `internal/qbit/qbittest`), `internal/qbit/qbit_test.go`,
  and `internal/e2e` `TestClientRoutedMovie`: one grab through the fake API to an imported link on the fake mount.

## Limits

- **The acquirer has no planner budget checks of its own.** The gateway enforces the budgets and back-offs, and the
  search schedule's `per_hour` paces it.
- **Hash-less releases.** Releases with only a `.torrent` link (nyaa through SeaDex) are skipped (`no_hash`):
  `.torrent` files are not fetched and converted to their hash.
- **Usenet and seedbox routing** are not part of the router.

## Turning the router on

  1. Run `theatarr router dry-run` for a few titles.
  2. Check `theatarr router status`, and the router plans in **Activity › Decision journal** (`plan` entries).
  3. Set one library Live and turn **Pause** off. The search schedule then starts at its default pace (10 searches
     an hour).
  4. Watch the log line and the first adds.

## Release identity

A release's torrent identity comes from its info hash and magnet only; SeaDex titles and record ids never supply a
routable hash. A release whose reported hash conflicts with its magnet is rejected: the indexer service drops it, a
release push rejects it before deciding, and the router checks the identities still available on every route (step
`hash_conflict`) and refuses to queue it. A release reporting no hash is routed by its magnet's.

The checks decode magnet queries and compare every v1 `xt` (hex or base32), including separate download and magnet
links, enclosure magnets and repeated Torznab identities. Magnets are parsed leniently (any-case `xt`/`xt.N` keys,
split on `&` and `#`, control characters tolerated), and a magnet naming no v1 identity beside another reported
identity is a conflict. A SeaDex entry without a relation list (`trs` absent or null) offers no release. Links are
kept in memory only, so the router cannot detect a conflict in an older row from its stored hash alone: rows queued
by a build before these checks are listed and ended by `theatarr router reconcile` (below).

For versions with `prefer_seadex` on, automatic re-decision keeps RSS or search semantics and the original search
criteria, which are stored with the download (`release_data` route fields) so requeued, stray and retried downloads
keep them. A prefer-on searched route stored without criteria, or a candidate without identity version 1 and an
origin, is not re-decided: it is skipped and its episodes are searched again, one re-search per route; no criteria
are reconstructed. Invalid stored release data carries unknown seeder evidence, like NULL data. The fresh check's
same-torrent guard applies to prefer-on versions only. With `prefer_seadex` off, a route is decided again only on a
policy revision change. The offer CLI supplies both movie and series deciders. Curator comparisons share one pinned
stored snapshot per operation (one route, one offer sweep, one cutoff list); cached search results cannot replace
it. How SeaDex standing is kept and used: [decide.md](decide.md#seadex-standing).

### Candidate identity version and origin

A producer's new request (`Route`, `Enqueue`, `EnqueueTx`, with no resumed download) stamps
each candidate with `identity` = `RouteIdentityVersion` (1: the identity checks above ran on it while its links were in
memory) and `origin`: `rss` (feeds, release pushes, movie offers, grabs by hand) or `search` (wanted, re-search,
`acquire:` runs, series offers, or a series search candidate with its `criteria`). The marks travel through
route job payloads, `download.release_data` (`route_identity`, `route_origin`, beside `route_search` and
`route_criteria`) and hold candidates; a request rebuilt from a stored row keeps what was stored. Re-decision uses
the explicit origin only. A prefer-on re-decision does not trust a candidate without identity
version 1 and an origin (a row an older build queued), nor a series search candidate without its criteria: it is
skipped (`search_criteria_unknown`) and its episodes are searched again (reason `legacy_criteria`). Nothing guesses
an origin or recovers criteria.

### Reconciling legacy queued routes (`theatarr router reconcile`)

Rows queued by a build before the identity version cannot be proved: their links are gone and their origin is
unknown. A prefer-on route ends such a row when it runs (above); `theatarr router reconcile` lists every such row still
waiting, whatever the version, and ends the automatic ones up front:

| Row | Listed when | `-apply`, automatic source | Manual source (`ui:grab:`, `ui:retry:`, `arrview:release:`, `upgrade:`) |
|---|---|---|---|
| route job (`router.route`, queued) | a candidate lacks identity or origin | failed and dismissed (`dismissed_by = reconcile`) | listed only |
| route job (running), and the queued downloads of its title version (an initial route's download is not linked to its job) | same | left alone, listed (run reconcile again later) | left alone |
| route job continuing a download whose add is in flight (`adding` and later) | same | left alone, listed | left alone |
| download (`queued`, no add sent) | `release_data` lacks `route_identity`/`route_origin` | failed (`error` names reconcile) | listed only |

With `-apply`, each title version with an ended automatic row gets one re-search (`acquire.research`, reason
`reconcile`, the union of the rows' episodes; dedupe key `acquire.research/reconcile/<version>/<title>`, so a second
run queues none while the first waits). The acquirer runs it only when `router.enabled` is true and the version is
active and grabs; until then it is not consumed but queued again an hour later (each time a new job, no attempt
counted), so the replacement searches retry hourly and run once grabbing is on; a search it already started that
finds grabbing off by then waits the same way (`TestGatedResearchRunNotASearch`), subject to the normal search budget
(`router.wanted.per_hour` defers them like any re-search). Other re-searches are
dropped while off. Nothing is deleted: rows keep their history, `content_provider`
ownership is untouched, and nothing is sent to any provider. Manual rows need a fresh selection by a person. Rows
past `queued` (adding, added, downloading) are not touched. Runbook: [migration.md](../migration.md#before-the-trial), step 5
(before `router.enabled: true`).
