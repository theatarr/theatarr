// Package gateway is the single outbound HTTP path (docs/design/gateway.md). Every request the service sends to anything it
// configures (indexers, debrid providers, metadata sources, download clients, the egress check) goes through
// [Gateway.Do]. Nothing else in the codebase uses http.DefaultClient or builds its own transport for these calls;
// TestNoOtherOutboundClients enforces that.
//
// # What a request passes
//
// [Gateway.Do] takes a [Target]: who the request is for (an indexer, provider, metadata source or client key from the
// policy). In order, it:
//
//  1. resolves the target in the policy (an unknown target is refused: [ErrUnknownTarget]);
//  2. applies the VPN egress guard for the classes in egress.require_vpn_for (indexers by default): the latest egress
//     check for the target's egress path must be fresh and have seen an address in the VPN set ([ErrVPNRequired]);
//  3. checks the host against the allowlist, which is the policy's outbound hosts plus egress.check_url
//     ([ErrHostNotAllowed]). This check runs again on every redirect hop, before dialing. A hop into the host of a
//     VPN-required target is followed only by a request already VPN-guarded on that target's egress path
//     ([ErrVPNRequired]), and the egress check follows redirects only on its check_url endpoint;
//  4. refuses a target that is backing off after repeated failures ([ErrBackoff]), except one health check at a time
//     ([HealthCheck]) through the failures-in-a-row back-off only, so a caller can tell a failing resource from a
//     failing target;
//  5. takes a token from the target's budget, waiting up to Options.MaxWait, and counts the request against the
//     budget's hourly and daily caps ([ErrBudget]);
//  6. sends it with timeouts, the User-Agent theatarr/<version>, environment proxies ignored, and a response size limit
//     ([ErrResponseTooLarge] from the body's Read).
//
// Every refusal is a *[Refused] that matches one of the sentinel errors with errors.Is and carries RetryAt when
// waiting would help.
//
// # Budgets and back-off
//
// A budget is a token bucket (requests per period, burst default 1) plus optional hourly and daily caps counted per
// UTC clock hour and day. The caps' counts and every back-off live in SQLite (migration 00002), so a restart resets
// neither; the bucket itself is in memory (a restart grants at most one burst). A 429, or a 503 with Retry-After,
// backs off the target's budget (or the target, when it has none) until Retry-After (capped at 1h), or
// exponentially from 30s to 15m when the header is missing. Three consecutive failures (network errors or 5xx) back
// off the target from 1m, doubling, to 1h (a provider's pacing.backoff_max when set). A success clears both. A
// health check ([HealthCheck]) may go through that failure back-off, never through a rate limit, Retry-After or an
// out-of-band [Gateway.BackOff], and it is still charged to the budget.
//
// # Egress guard
//
// [Gateway.RunEgress] checks each egress path at start and every egress.interval: the direct path, plus one per
// distinct indexer proxy. A check asks egress.check_url through that path's own transport, so it measures the same
// route the guarded requests take. The state is logged on every change, exported as theatarr_egress_* metrics and shown
// in /readyz by [Gateway.EgressSummary]. A request still pinned to a retired generation passes only from an exit
// address the current generation's settings also trust (same check_url, address in its VPN set), so a trust change
// applies at once; it also follows the current require_vpn_for and the shorter max_age at every request, redirect
// and dial, proven by the current check of its own route (vpnRule); of two checks of one route the newer observation
// decides, and its answer also governs the connections dialled on the older one. A guarded direct dial goes only to the addresses the check proved for it, tried in order 250 ms
// apart (the first to connect wins); a source probe resolves and checks them the same way.
//
// # Logs
//
// Logged URLs never carry a query string, path segments holding '=' are masked (torrentio-style configs), and the
// values of every secret env var the policy names are masked wherever they appear.
package gateway
