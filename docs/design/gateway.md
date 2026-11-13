# Gateway: the single outbound HTTP path

Every outbound request goes through one package, which applies the allowlist, the VPN guard, budgets and back-off.
Where this sits: [overview.md](overview.md).

## What it does

`internal/gateway` sends every request the service makes to an indexer, debrid provider, metadata source, download
client or the egress check. Callers use `Gateway.Do(ctx, Target, *http.Request)` (or `Get`). A `Target` names the
policy entity (`{Class: indexer, Key: "dmm"}`). The gateway has no privacy guard: privacy is a
label, and listing an indexer in a debrid version is the user's choice.

Each request passes these guards in order. A refusal is a `*gateway.Refused` that matches a sentinel with
`errors.Is` and carries `RetryAt` when waiting would help.

| Step | Refuses with | Rule |
|---|---|---|
| Target | `ErrUnknownTarget` | The class and key must exist in the policy |
| VPN egress guard | `ErrVPNRequired` | For classes in `egress.require_vpn_for` (indexers by default): the target's egress path needs a check that is fresh (`max_age`), succeeded, and saw an address in the VPN set |
| Host allowlist | `ErrHostNotAllowed` | Host and port must be in the policy's outbound hosts plus `egress.check_url`. Checked again on every redirect hop, before dialing. http(s) only |
| Target back-off | `ErrBackoff` | 3 failures in a row (network error or 5xx) back the target off: 1 m, doubling, up to 1 h or the provider's `pacing.backoff_max`. A health check (`gateway.HealthCheck(ctx)`, the poller's account read) goes through this back-off only, one at a time per target, still charged to the budget; its success ends the back-off (`TestHealthCheckGoesThroughFailureBackoff`) |
| Budget | `ErrBudget`, `ErrBackoff` | Token bucket, hourly and daily caps (checked and counted in one step, so concurrent requests cannot overshoot), 429 back-off. Waits up to 60 s in all (`Options.MaxWait`, one deadline from the first try, or the context deadline), otherwise refuses |
| Send | `ErrResponseTooLarge` | 60 s total timeout, 10 s dial and TLS, 30 s to headers (a target with `Wait` set, the Cardigann solver leg, gets that whole wait instead and its timeouts don't count as failures), max 5 redirects, 32 MiB body (per-request `Target.MaxBytes`). `User-Agent: theatarr/<version>`. `HTTP_PROXY` and friends are ignored |

Responses: 429, or 503 with `Retry-After`, set a back-off until `Retry-After` (capped at 1 h), or 30 s doubling to
15 m when the header is missing. A success clears it, but only a back-off (or failure count) that was last set before
that request was sent: a slow request's 200 never clears a 429 another request got while it was in flight. Back-off
rows are written in order, each as memory holds it at the time. Who is backed off is decided per target when the gateway starts:

| The limited target | Backs off | Why |
|---|---|---|
| Has no budget | the target | |
| Is the budget's only user | the budget | Same thing |
| Is a provider | the budget | RD, AD and TorBox count limits per account or key; TorBox's 429 locks the account |
| Shares the budget, and every user calls the same endpoint (URL without query) | the budget | The server can't tell the users apart, so the limit is the key's |
| Shares the budget with targets on other endpoints | the target only | Prowlarr's per-indexer feeds (`/7/api`, `/8/api`): a 429 is that indexer's limit. One Prowlarr indexer answering 429 with `Retry-After: 300` must not refuse the other indexers behind the shared `prowlarr` budget |

The back-off row's subject is `budget:<key>` or the target (`indexer:<key>`, …); a target back-off refuses at once
with `ErrBackoff` instead of waiting. Metrics: `theatarr_gateway_budget_backoffs_total{budget}` or
`theatarr_gateway_target_backoffs_total{target}`, and `theatarr_gateway_backoff_seconds{subject}` for either.
`Gateway.BackOff(ctx, target, until, reason)` lets a caller back a target off itself, for example a provider on
`too_many_active_downloads` or `traffic_exceeded`.

`Gateway.State()` is a read-only snapshot from memory: the egress summary and paths, every budget (rate, burst,
caps, tokens now, this hour's and today's use, users, its own back-off) and every back-off row (active or not, with
failures and reason). The web dashboard and `/api/theatarr/v1/gateway` read it.

No package outside `internal/gateway` may build an HTTP client. `TestNoOtherOutboundClients` scans the tree. Its
exceptions are the loopback healthcheck in `cmd/theatarr/serve.go`, `internal/arr/` (the LAN arrs and Prowlarr's
indexer list, the import tooling and the cardigann migration reader), and `internal/indexer/cardigann/engine.go`, whose
clients (a cookie jar, no redirects) only wrap the indexer's gateway transport and refuse to run without one.

**Cardigann indexers** ([cardigann.md](cardigann.md)). `Target.NoFollow` returns a redirect as the response: the
engine follows it itself, each hop a new `Do` with every guard again. `Target.UserAgent` replaces `theatarr/<version>` for
that request (the engine's browser User-Agent, or the one trawl passed a Cloudflare check with). A cardigann
indexer's `hosts` join the outbound hosts (and a probe's allowlist; a private address must already be on the live
one), as does `cardigann.flaresolverr_url`. The definitions fetch (`CardigannCommit`, `CardigannArchive`) has its
own `cardigann-definitions` budget, like the quality sync's.

### Indexer requests by purpose

The gateway counts every request it sends to an indexer (each redirect hop sent is one, the hop before a refused
redirect included; a refused request is not sent and not counted) per indexer key, purpose and UTC day.
The code that starts a search tags its context with `gateway.WithPurpose`; an untagged request counts as `other`.
Accounting only: a purpose changes no limit, budget or share.

| Purpose | Set by |
|---|---|
| `interactive` | `Acquirer.InteractiveMany`: the releases list and the arr views' interactive search |
| `wanted` | An acquire run (`run`, `runFanout`) for something missing: the search schedule's runs, search commands and re-searches |
| `upgrade` | An acquire run with reason `cutoff`, or a run for a movie that has a file or for episodes that each have one (`searchPurpose`, `held`); a shared search is `upgrade` only when every target it serves is one |
| `rss` | The RSS sync (`internal/acquire/rss` `rssPages`) |
| `test` | A source test from the settings pages (`testIndexerSource`). A source probe counts only tagged requests, so the quality sync and the cardigann definitions fetch (indexer-class probe targets) are not counted |
| `other` | Anything untagged; the `theatarr search`, `decide` and `router` command-line searches |

`TestPurposeCallSites` lists every `WithPurpose` call by file, so a new search path is added to the table on purpose
instead of counting as `other`. Single-flight ([indexers.md](indexers.md)): concurrent identical searches share one
upstream search, whose requests count under the first caller's purpose; a cached answer sends nothing.

Counts are kept 30 UTC days (today included) in memory and in `gateway_indexer_request`. A request only counts in
memory; `theatarr serve` writes the new counts every minute and when it stops (`RunRequestFlush`; the last write comes after the HTTP server
and the workers have stopped, so a search finishing during shutdown keeps its count; a search still running when the HTTP server's shutdown grace ends, or one cancelled mid-request by the shutdown, may not be counted: by choice, no more shutdown machinery for accounting), keeping counts whose write
failed for the next try, and `theatarr search` writes its own as they exit. A crash loses at most the
last minute's counts. The first count of a new day drops older days in memory, and the next write prunes their rows. `Gateway.State().Requests` sums today and the last 7 UTC days (today and the 6 before) per indexer.
The settings overview (`GET /settings`) gives each indexer a `requests` object (`today`, `week`, and `purposes`: the
purposes with a request in the 7 days, in the order above, each with `today` and `week`), and the Indexers page shows
it in each row beside the indexer's budget and that budget's use today (when it has a daily cap).

## Policy keys

### `egress` (optional)

No `egress` section turns the guard off: requests still pass the allowlist, budgets and other gateway checks,
startup logs one warning, and health/readiness and the UI show “VPN guard off” as a warning. A configured section
with `require_vpn_for: []` is monitor-only: checks still run and are shown, but refuse no requests; otherwise any
subset of `indexers`, `providers`, `metadata` requires a fresh VPN check for those classes, defaulting to `[indexers]`
when `require_vpn_for` is omitted. Configured sections still need `check_url` and VPN addresses in every mode.

```yaml
egress:
  check_url: https://api.ipify.org   # IP echo: plain-text address, or JSON with "ip". Host allowlisted automatically
  vpn_ips: [203.0.113.10]            # expected exit addresses of the VPN
  vpn_cidrs: []                      # or prefixes; at least one entry across both. 0.0.0.0/0 is refused
  interval: 2m                       # default 2m, minimum 10s
  max_age: 6m                        # default 3 × interval; must be ≥ interval
  timeout: 10s                       # one check; default 10s
  require_vpn_for: [indexers]        # omitted defaults to [indexers]; [] monitors only; any subset is allowed
```

### `indexers[*].proxy` (optional)

`http://`, `https://`, `socks5://` or `socks5h://` host and port only, no credentials, for example a gluetun sidecar.
That indexer's requests go through it, and the egress guard checks that path separately. `socks5h` also resolves DNS
at the proxy.

### `budgets[*]`: `hourly`, `daily` (optional)

```yaml
budgets:
  dmm-key:   {requests: 16, per: 1m}                            # burst defaults to 1: evenly paced
  torrentio: {requests: 30, per: 1m, burst: 5, daily: 5000}
```

- The bucket refills at `requests` per `per` and holds `burst` tokens. The default burst is 1. With a burst of N,
  one window can see N extra requests, so DMM's 16/min stays under its 20/min limit only while its burst stays ≤ 4.
  DMM's 20 requests a minute are per key: when other tools use the same DMM key, they share that limit, and
  `dmm-key` should be set to Theatarr's part of it.
- `hourly` and `daily` count requests per UTC clock hour and day.

### Persistence (`00002_gateway.sql`)

| Table | Holds |
|---|---|
| `gateway_budget_window` | Count per budget, period (`hour`, `day`) and window start. Only for capped budgets; rows older than 48 h are pruned |
| `gateway_backoff` | Back-offs and consecutive failures per subject (`budget:<key>`, `indexer:<key>`, `provider:<key>`, …) |
| `gateway_indexer_request` (`00106`) | Indexer requests per indexer, purpose and UTC day start. Rows older than 30 days are pruned |

A restart keeps caps and back-offs. The token bucket is in memory, so a restart allows at most one burst early.

## The VPN egress guard

- **Startup:** `theatarr serve` builds the gateway after migrating and starts the egress worker
  (`cmd/theatarr/serveliveworkers.go`). It checks every egress path at once, then every `interval`, through the
  gateway of the settings generation in force. The worker follows the settings: an `egress` section added later
  starts it, a changed `interval` restarts it, and a removed section stops it. Each published generation also gets
  one immediate check.
- **Paths:** `direct`, plus one per distinct indexer proxy. Each check goes through that path's own transport and
  the allowlist, the same route as the requests it guards.
- **Before the first check**, guarded requests are refused. They are also refused when the check fails, is stale,
  or sees an address outside the set.
- **Settings changes:** a new gateway generation keeps a path's last check only when `check_url` and the VPN set
  (`vpn_ips`, `vpn_cidrs`) are unchanged, as is the path's proxy. Otherwise the path starts unchecked (guarded
  requests wait for the generation's own check), and its check state is separate from the retired generation's.
  Every guarded request also checks the recorded exit address against the generation's own VPN set and, for an
  operation still pinned to a retired generation, against the current generation's: a trust change applies at once,
  not when the retired verdict passes `max_age`. The retired verdict holds only for an exit the current settings also
  accept from the same `check_url` (`TestRetiredGenerationFollowsTrustChange`).
  A pinned operation also follows the current enforcement and freshness, at every request, redirect hop and dial: its target needs the VPN when either its own or the current settings' `require_vpn_for` says so
  (guard turned on, or monitor-only made required, mid-search), the proof may be no older than the shorter of the two
  `max_age`s, and the exit must be in both VPN sets. The request keeps its pinned transport (proxy, credentials,
  address); the proof for that route is the current generation's check of the same path (same name and proxy), or
  the pinned path's own check when it echoes the current `check_url`; with neither the request is refused. When both
  exist, the one that observed the path last decides: a newer check that failed or saw an exit outside
  the VPN set refuses the pinned request even while the older good one is still fresh, and its answer also closes the
  guarded connections dialled on the older check's proof, busy ones too, within that check's own VPN set
  (`TestNewerWANCheckRevokesRetiredProof`, `TestNewerWANCheckClosesRetiredConnections`). The
  runtime keeps every generation's check of a path (same name and proxy; held weakly, so a check goes with the last
  generation or connection using it) and every observation counts across them, whatever generation made it: checks of other generations (an intermediate one, an older one a pinned operation runs a check on, a
  prepared one) never prove a route, but an observation of theirs newer than the proving check's refuses unless it
  saw a fresh exit every VPN set of the rule trusts, narrows the proven address families to its own, and closes the
  guarded connections of every generation's check that its answer no longer proves. A dial registers its connection
  on the proving check and then judges the path again under the rule in force at that moment (not the one it started
  with), so an observation published while it connected is either seen by that judgement or revokes the registered
  connection; a dial through a proxy (not tracked by family) is judged again once it is open
  (`TestIntermediateFailedProofSurvivesNextGeneration`, `TestDialRechecksGenerationPublishedWhileConnecting`,
  `TestOlderGenerationWANObservationGovernsCurrent`). A request
  already sent without the guard is refused at its next redirect, hop or dial once its target needs the VPN, and a
  pinned request is not redirected into a host the current settings protect (`TestRetiredGenerationFollowsEnforcement`,
  `TestRetiredGenerationFollowsShorterMaxAge`, `TestPlainRequestStoppedWhenEnforcementStarts`,
  `TestRetiredRedirectIntoNewlyProtectedHost`, `TestRetiredProxyPathProof`).
- **Address families:** an echo proves the exit of the family it went out on. On the `direct` path the check echoes
  twice: once as any request would, then pinned to the other family (`tcp6` after an IPv4 answer, `tcp4` after an
  IPv6 one, on a connection of its own). The other family counts as proven only while its echo answers with an
  address of that family inside the VPN set; an echo endpoint of one family (`https://api.ipify.org` answers over
  IPv4 only), no route, or an exit outside the set (logged as a warning) leaves it unproven. The path's state and
  health still follow the first echo only. Every echo, first and second, goes on a new connection: a kept-alive
  one could keep the route it was opened on (connmark routing) and report an exit the path no longer has.
  A guarded request on the `direct` path resolves its host once and dials those addresses, never the name again,
  so a DNS answer that changes between the check and the dial cannot bring in an unchecked address; the request
  keeps its URL, so the `Host` header and the TLS server name are the host's (`TestGuardedDialUsesCheckedAddresses`). An address is dialled when (`TestGuardedDialFamilies`):
  - it is private, loopback or link-local, so it cannot leave the LAN over either family (a Prowlarr feed, trawl, a
    LAN Zilean); this is judged on each address actually dialled, not on the name;
  - or its family is proven (both families when both are proven), so a host with native IPv6 and a VPN that routes
    only IPv4 cannot reach an indexer over IPv6 outside the tunnel.

  A host with no such address is refused with a VPN refusal (no back-off), not a transport error. A host that does
  not resolve is dialled by name, pinned to the first echo's family, and the dial fails as it would anyway. The
  checked addresses are tried in order, the next one when the one before fails or has not connected within 250 ms;
  the first connection wins and the rest are cancelled or closed, so an unreachable leading address does not hold
  back a reachable one for the whole 10 s dial timeout (`TestDialAddrsStaggers`). A source probe resolves
  names the same way (its dialer wraps the live one), so its guarded dials get the checked addresses, both proven
  families and the local exception too (`TestProbeResolvesForGuardedDials`).

  Guarded `direct` connections are tracked on the path's check, which every settings generation that keeps the check
  shares. A check that answers decides which may stay: a good one closes those of a family it no longer proves, one
  that sees an exit outside the VPN set closes all but the local ones; idle or busy, in the current generation or a
  retired one, so neither a kept-alive HTTP/1 connection nor an HTTP/2 one taking new streams outlives the proof of
  its family (`TestRevokeReachesRetiredGenerations`, `TestRevokeClosesBusyConnections`). A failed check
  closes nothing: guarded requests are refused until a good one. Guarded requests on `direct` have a connection pool
  of their own, so they never reuse a connection an unguarded request opened to the same host without any family
  check (`TestGuardedPoolSeparate`). Idle connections also close when an observed
  address changes. Through an indexer `proxy` the dial goes to the proxy, which chooses the onward family itself:
  keep its IPv6 inside the tunnel or off.
- **Observability:**
  - Log: one line per state change: `egress changed` (error level when not OK) with path, `vpn_ok`, ip and error.
  - Metrics: `theatarr_egress_vpn_ok{path}` (1 only when fresh and OK), `theatarr_egress_check_age_seconds{path}`,
    `theatarr_egress_observed_ip_info{path,ip}` and `theatarr_egress_checks_total{path,result}`.
  - `/readyz` and `/gateway` have an `egress` object: `mode` (`off`, `monitor-only`, `required`), `vpn_ok`,
    `require_vpn_for`, and per path `ok`, `ip`, `checked_at`, `age_s`, `stale` and `error`. Off has no check paths
    and includes `type: warning` and `message: VPN guard off`; it does not fail readiness.
- **`/readyz` stays 200 when the VPN is down.** The guard already refuses every request that needs the VPN.
  - The API, the arr views (Seerr), imports and jobs that need no VPN keep working.
  - A 503 would make Docker or the orchestrator restart a container that can't fix the tunnel.
  - Alert on `theatarr_egress_vpn_ok == 0` instead.
- **`theatarr egress-check [-db file] [-v]`** runs one check per path and prints one line per path: the observed IP and
  `VPN OK` or `NOT VPN: <why>`. It exits 1 if any path is not on the VPN and a class requires VPN; off prints a
  warning and succeeds, and monitor-only prints its observations and succeeds.

### Verifying a deployment

1. The host's traffic is routed through the VPN, with a kill switch that drops it when the tunnel is down.
2. `theatarr egress-check` prints `VPN OK` and the VPN exit on every path.
3. Negative test, choose one:
   - Temporarily take the host off the VPN route (the kill switch then drops its traffic). The check must fail with a
     dial or timeout error.
   - Or run `theatarr egress-check` from a WAN host with the same policy. It must print `NOT VPN: exit address <WAN> is not
     in the VPN set` and exit 1.
4. On the service:
   - `/readyz` shows `"egress":{"vpn_ok":true,…}` and `/metrics` shows `theatarr_egress_vpn_ok{path="direct"} 1`.
   - After the negative test, the log shows `egress changed` with `vpn_ok:false`, and indexer requests count under
     `theatarr_gateway_refused_total{reason="vpn"}`.

### Limits of the guard

- **DNS** for indexer host names is resolved by the host's resolver. If that resolver goes out over the WAN, the
  names you look up leak to the ISP, although the traffic does not. Route the host's DNS through the tunnel, or give
  it a resolver reached through the VPN. A `socks5h` proxy resolves at
  the proxy.
- **Prowlarr** (`kind: torznab`) is on the LAN. The guard checks that Theatarr reaches Prowlarr from a VPN-routed host. The
  call to the tracker itself leaves from Prowlarr's host. Prowlarr's own egress must be on the VPN too, or Prowlarr
  must use the same proxy (Prowlarr supports indexer proxies). The same holds for zilean.
- **The address check** proves the exit address, not the route. A VPN exit that changes address needs `vpn_cidrs`
  covering the provider's range, or a policy update.

## Design notes

| Choice | Why |
|---|---|
| Caps and back-offs are persisted; the token bucket is in memory | A bucket refills in seconds to minutes, so persisting it buys at most one burst. Persisting it would cost a write per request on every budget |
| No request priorities: waits are first come, first served | Priorities belong in the search planner and job queue (they decide what to send). The gateway only paces |
| Single-flight and the result cache are not in the gateway | They key on normalised queries, which the indexer adapters know and the HTTP layer doesn't ([indexers.md](indexers.md)) |
| Paging caps (`page_cap`) are not enforced here | The adapter decides how many pages to request |
| `hourly`/`daily` caps | For daily-limited APIs |
| No source-indexer check in the debrid adapter | Privacy is a label; the version's indexer list and `indexers.<key>.providers` decide where releases go |

## Tests

`go test` never touches the network. Host names under `.test` are mapped to local `httptest` servers through
`Options.DialContext`, and time is a fake clock. The tests in `internal/gateway`, `internal/policy` and
`cmd/theatarr` cover:
- the VPN guard: before the first check, a WAN exit, a VPN exit, a stale check, a broken echo, provider opt-in, no
  egress section;
- an egress path through a fake forward proxy;
- the allowlist: an unlisted host is never dialed, other ports, a redirect away and a redirect to a listed host;
- pacing and long-wait refusal;
- a daily cap across a restart and into the next UTC day;
- 429 with Retry-After persisted across a restart, and without it (30 s wait);
- failure back-off and caller back-off;
- the size limit, with and without Content-Length;
- redaction of logs and errors (query strings, `=` path segments, secret env values);
- metrics and the `/readyz` summary;
- `theatarr egress-check` end to end against a local echo.
