// Package arrview serves Radarr- and Sonarr-shaped APIs over our titles and versions (docs/design/arrview.md):
// what Seerr, Homepage and decypharr call on an arr, answered from our database and metadata clients.
//
// # Views
//
// A view is a set of versions, a flavour and a base path. The policy's views section defines /radarr and /sonarr
// (or others); every version also gets /v/<version>/radarr or /sonarr without configuration. Each serves
// <base>/api/v3/... Routing below the base is case-insensitive, as ASP.NET's is (Seerr asks for /qualityProfile).
//
// Inside a view, hasFile, movieFile, statistics and the season counts describe only the view's versions: hasFile is
// true when any of them has a file, movieFile is the file of the first version in the view's order that has one.
// The "quality profiles" a view lists are the version sets of its policy profiles (a profile id is stored the first
// time its name is seen, so it stays put). Root folders are the libraries of its versions.
//
// # Auth
//
// The view's own key (policy views[*].api_key_env), or any active API key of internal/auth, sent the way the arrs
// accept it: the apikey query parameter, the X-Api-Key header or a bearer token. A request without a valid key gets
// the arrs' bare 401 (no body). Every API response carries X-Application-Version.
//
// # Ids
//
// A movie or series id is IDBase (1,000,000) plus our title id, the same in every view. A smaller id is a replaced
// arr instance's id: it is looked up in the alias table (arr_alias, written by the import) for the view's alias_from
// instances, in order, so the ids Seerr stored (externalServiceId) keep answering after cutover (docs/migration.md). The
// offset keeps the two id spaces apart: without it, title 7 and radarr_4k_debrid's movie 7 would be the same id.
// Episode, movie file, tag and command ids are ours and global.
//
// # Writes
//
// Writes go to our database, except through the two gated paths: grabs (search commands, POST release,
// release/push) go to the router, which writes to a provider only for active versions while router.enabled is
// true; imports and file unlinks go to the library, which writes only through its gate. A search command whose
// versions are all in shadow mode is stored completed with a message saying so. POST movie|series creates the title if needed and turns on the versions the chosen profile
// (or, with none, the view's default-enabled versions) calls for; anime-only or non-anime versions are matched to
// the title, with Sonarr's series type and the root folder as hints for a new title. A second add of a title turns
// on the extra versions instead of failing. PUT changes only the view's versions. DELETE unlinks only the view's
// versions: they are unmonitored and listed in arrview_removed_version, which every view hides; deleteFiles=true
// first checks and moves Theatarr's links to the library trash through the gate.
//
// # Deviations from the arrs
//
//   - TVDB id lookups for uncatalogued series read TVDB/TMDb through the metadata gateway clients, briefly cached
//     and bounded to five seconds. Unmatched names search TVDB through the same client. Failure keeps the local answer.
//   - Lookup results set hasFile and monitored from the view (Radarr leaves hasFile out of lookups); id is set only
//     when the view has the title, so Seerr adds it otherwise.
//   - A second POST of a title is a 201, not Radarr's "This movie has already been added" 400.
//   - DELETE keeps version rows and imported arr files; deleteFiles=true moves only Theatarr's links to trash.
//   - The queue lists the router's downloads in hand (download table, queue.go): downloadId is the info hash,
//     downloadClient "theatarr router (<provider>)", ETA from recent progress or zero/now when complete, omitted when
//     unknown; Sonarr lists a pack once per episode, with stable episode-specific ids. Removing any member
//     removes the download or pending approval release from our queue, retaining provider content. Held imports
//     and failures awaiting resolution carry warnings/errors.
//   - Quality profiles list every quality as allowed and no formats: they are version sets.
//   - Movie metadata the database does not hold (overview, images, genres, ratings, runtime) is empty.
//   - History is Theatarr's downloads; ids are download event ids (HistoryBase + download id × 10 + event,
//     plus 2^40 × the episode's position for a Sonarr pack's later episodes).
//     Plain-array routes remain complete lists. Negative page sizes return 400 instead of SQLite's unlimited LIMIT;
//     paging uses checked 64-bit offsets instead of the reference repository's overflowing Int32 multiplication.
//   - Sonarr episode files: an episode has a file when a view version's per-episode state is filled or cutoff_met, an
//     imported arr file holds it, or an active link of a view version does. File ids are version_file ids, or
//     LinkFileBase + link id for our links (a link of ours wins over the arr's file: it is newer).
//   - The decision endpoints (release, release/push, parse, manualimport), file deletes, notifications and the
//     outbound webhooks are described, with their deviations, in docs/design/devices.md.
//   - Languages are mapped by name through the view flavour's language list: Sonarr's for Sonarr views, Radarr's for
//     Radarr views.
package arrview
