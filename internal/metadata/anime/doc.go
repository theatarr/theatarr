// Package anime is the anime metadata engine (docs/design/anime.md): it builds a franchise from open data, maps every entry to
// what Sonarr (TVDB season/episode, including Season 0) and Radarr (TMDb movie id) use, and lists the entries in
// three orders (release, automatic watch, story).
//
// Golden outputs are the reference: on the offline fixture in testdata/offline, [Build] reproduces out/<slug>.json
// and out/<slug>.md for each franchise there (TestGoldenFixture).
//
// # Stages
//
// Each stage is a function; its output is kept in [Result.Stages] and stored per franchise (package animestore).
//
//  1. crawlAniList: the typed crawl (line, branch, side, leaf, with the series-stem check). crawl.go
//  2. mapper.resolveIDs: AniList to AniDB (Fribb, else manami) and Kitsu (manami). mapper.go
//  3. mapper.mapEntry and Lists.AnibridgeFor: the candidate mappings from ScudLee (applied as HAMA does) and anibridge.
//  4. builder.arbitrate: the score (date fit ×2 + episodes exist + coverage + title similarity), its keep and
//     reject rules, plus the design's status (auto, arbitrated, conflict) and the r2 anchor signal (ScudLee <before>
//     against TVDB airsBefore). arbitrate.go
//  5. secondaryEntries (Kitsu, AniDB-only) and tvdbOnlyEntries (TVDB Season 0 nobody claims), with the heuristic
//     match and the fix-ups (merge same title, merge duplicates, split far-apart releases, drop duplicates).
//  6. setFlags and target. flags.go
//  7. releaseOrder, watchOrder (watch and story mode). orders.go
//  8. storyOrder: the story-mode order plus the overrides' moves. story.go. Mapping overrides and exclusions apply
//     before validation and in the orders.
//
// # Sources
//
// Everything outside comes through [Sources]: AniList, Kitsu, TVDB, TheXEM, a movie lookup, the Sonarr library and
// the static [Lists]. Backends: offlinecache (an offline cache of the responses), live (through internal/gateway, cached in the
// database), and animestore.SonarrTVDB (our imported Sonarr episodes as TVDB, until a TVDB v4 client exists).
//
// # Faithfulness
//
// The golden outputs were produced by a Python implementation of the same pipeline; the Python behaviour they
// depend on is reproduced, not approximated:
//   - CPython's set iteration order for ints (pyset.go): the crawl and the manami walk iterate sets, and the order
//     decides who reaches a node first. Checked against CPython 3.10 orders in testdata/pyset.json.
//   - Python's Unicode-aware \b (pyre.go): titles include Japanese, where Go's ASCII \b would match differently.
//   - date.fromisoformat's strictness, sort_date's padding, round(x, 2), repr() of floats, lists and strings in notes,
//     urllib.parse.quote for the Radarr search keys, list.index/remove by equality in the story order.
//
// # Deviations
//
//   - topo: successors are walked in edge order, not as a Python set of strings (whose order changes with
//     PYTHONHASHSEED). Ready nodes are re-sorted by date before each pick, so only ties could differ;
//     none do in the twelve example franchises (examples/policy/overrides/franchises.yaml).
//   - mergeDuplicates: Radarr's inCinemas timestamp is read by its date part (as the TMDb check reads it); read
//     whole it never parses, and the "TMDb picked from grouped AniDB ids" step would never run. No output of the
//     twelve example franchises changes.
//   - Added, not in the golden outputs: [MappingOutcome] per entry ("mapping_decision" in the JSON), the stage outputs
//     ("stages"), mapping overrides and exclusions in the overrides file, routes, line position and alternate cuts
//     (routes.go: "route", "alt_cut", "alt_cut_tvdb" on rows, "routes" on the story order, "alt_cuts" on the
//     result), and a "Mapping decisions" section that `theatarr anime build` prints after the Markdown. The golden
//     comparison ignores these; none appears without those overrides.
//   - Live, the movie lookup and title search are our TMDb client (search/movie) instead of Radarr's lookup: dates
//     are TMDb's primary release dates.
//   - Live, TVDB is our imported Sonarr: no airsBefore/After (Sonarr's API lacks them) and no overviews, so
//     TVDB-only specials are placed by date and the overview recap hint never fires. A TVDB v4 client lifts both.
package anime
