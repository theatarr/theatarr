// Package cardigann runs Prowlarr's Cardigann indexer definitions natively (docs/design/cardigann.md): the engine that
// logs in to a site, searches it, scrapes the result rows and resolves downloads, the catalog that describes the
// definitions, and the definitions sync source (Store).
//
// The engine is a port of Mediarium's (github.com/rdborg/Mediarium, internal/indexers @ f7d1e26), which is
// AGPL-3.0, Copyright (C) Ryan Borg and the Mediarium contributors. Each ported file says which Mediarium file it
// comes from; NOTICE at the repository root lists it. The ported code stays under AGPL-3.0, and so does Theatarr
// when it is published (docs/dev.md).
//
// Ported: the definition model and validator, templates, selectors (CSS and JSON), filters, dates, categories,
// login (post, form, cookie, get), search paths and row parsing, downloads, the FlareSolverr exchange, the catalog
// summaries and setting kinds, the local-site check, the versioned-archive extraction, and the tests of all of
// these.
//
// Left out: Mediarium's Newznab/Torznab client and feeds (Theatarr has its own torznab adapter), its built-in Go
// indexers and custom-definition folder, its definition repository and database tables, the link-reference store,
// its DNS-checking dialer (netguard), its own budgets, daily limits and back-off (the gateway does that), usenet,
// and its stats and RSS budgets.
//
// Deviations, each with the reason:
//   - Nothing is sent by the engine. Each Instance carries the gateway as Transport and Solver; the engine follows
//     redirects itself, one hop per gateway request (Target.NoFollow), so every hop gets the VPN check, the
//     allowlist, the indexer's budget and back-off. A missing Transport is refused.
//   - Hosts: Mediarium refused private addresses by resolving names in its dialer. Here a site may only reach its
//     own address and the hosts listed in the indexer's hosts (the gateway's allowlist holds the same list); page
//     links to the home network are refused by address. The hosts and foreign tests were rewritten for that.
//   - Link references: Mediarium kept download links in a store and handed out references. Result carries Link and
//     Magnet; package indexer keeps them in memory only (indexer.Link), never in JSON or logs.
//   - Schemas: Mediarium fell back to the oldest version in an archive with only newer schemas. ExtractDefinitions
//     refuses it, since a newer schema may use features the engine does not know.
//   - Sessions (cookie jar, FlareSolverr user agent, login state) are saved through SessionStore (the database,
//     kept like the settings secrets), keyed by the indexer's key, so a restart does not sign in again; Mediarium
//     kept them in memory only.
//   - Usenet definitions and CAPTCHA logins are refused by the validator; the catalog marks them unsupported.
//   - Messages name Theatarr and its settings (cardigann.flaresolverr_url, trawl), logging is log/slog, and the
//     package's own errors carry no URL (it may hold a passkey).
//   - Status errors carry no Retry-After: the gateway backs the indexer off.
//   - The corpus test reads the definitions only from a folder given in THEATARR_CARDIGANN_DEFS. Prowlarr/Indexers has no
//     licence, so no snapshot of it is committed and the test never fetches; without the variable it runs on the
//     hand-made definitions in testdata (Mediarium's own fixtures).
//   - Added: RSSProblem (rss.go) says whether a definition can feed the RSS sync; the catalog summary carries it.
//   - Regular expressions: Mediarium refused every pattern Go's regexp can't compile. A pattern RE2 refuses for its
//     syntax (look-around, back-references) runs on regexp2 (internal/netregex) with a match timeout, as Prowlarr
//     runs it on .NET; Go's regexp keeps every pattern it accepts.
//   - Added: query variables beyond the keywords (ids, season, episode, year) and the result fields Mediarium dropped
//     (volume factors, ratio and seed time, grabs, files, TMDb and TVDB, the Internal flag), as Prowlarr's Cardigann
//     generator and parser use them.
//   - Added: a custom folder of the operator's own definitions (Store, custom/), listed with the synced ones and
//     winning over them on the same id; Mediarium's own custom folder was not ported.
package cardigann
