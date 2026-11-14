# The library

The library API and the SPA's Home, grids and title page ([`ui.md`](ui.md) §2 and §7). Code: `internal/web/{library,facets,episodes,discover,titlepage,perms}.go`, `web/src/pages/{home,
catalog,title}.tsx`, `web/src/components/{library,title}/`.

## API

- **`GET titles`** takes `sort` (added: the newest current file; created: when the title was added; name, year,
  release, rating, runtime, certification, network, size, completion, next, quality, activity), `order`, and the
  filters `anime`, `monitored`, `availability`, `genre` (any of, comma-separated), `quality`, `provider` (see files
  only; a cache code `rd`, `ad`, `tb` or a provider key), `year_from` and `year_to`. Rows carry genres, rating,
  certification, runtime, release date, `availability` (the poster badge), `added_at` (newest current file),
  `title_added_at`, `status`, `original_language`, a movie's `in_cinemas`, `digital_release` and `physical_release`,
  a series' `series_type`, `season_count` and `previous_airing`, and `size_bytes`; each version carries `label` ("4K
  WEB"), `quality`, `files`, `added_at`, `size_bytes`, `providers` (cache codes of the providers that hold a current
  file complete now), and, see files only, `profile`, `minimum_availability` (movies) and `folder`. A copy removed
  from the library is in no row, filter, sort or facet. A current file is the owning instance's, or any file when no instance
  owns the version; active links count too.
- **`availability`:** the title's status word (docs/design/ui.md, Status words; `titleAvailabilitySQL`): available (a
  file is held and no monitored library has a gap, whatever the cutoff), partial (a file is held and a monitored
  library lacks one, or an aired monitored episode), missing (no file, monitored, out), upcoming (no file, monitored,
  not out yet), unmonitored (no file, no library monitored), none (no versions). A gap in a library that is not
  monitored is no gap, for a series as for a movie. The row, the `availability=` filter, the facet counts and the
  title page share this one SQL rule. `upgrade=found|best_known` filters by the quality word (an upgrade found; a file
  below its cutoff with nothing better found).
- **`GET titles/facets?kind=&anime=`** counts per availability (adding up to the grid), quality filter (`upgrades`,
  see files), monitoring, version key, quality, genre,
  certification and (see files) provider cache code, over one grid; the grid's other filters are ignored.
- **`GET titles/{id}`** adds per version `label`, `current` (the current file in brief, with `formats`), `providers`,
  `cached_on` (cache codes), `removed` (a copy removed from the library, counted in none of the rollups), `episodes`
  (series counts without specials) and `history` (Theatarr's downloads and searches, newest first, at most 50; a movie's
  second import is an upgrade), and `franchises` for the anime order panel.
- **`GET titles/{id}/episodes?season=`**: seasons (specials last) with have/missing per version, and episodes with a
  cell per version.
- **`GET discover`**: recent, upcoming (next 30 days), missing (released and wanted; a movie is released on its
  `home_release`; without one, on its earliest US or worldwide digital or disc date, else TMDb's release date plus 90
  days, else the end of its year) and upgrades, 20 items each, with a note, a date, the total and a "more" link.
  The Getting ready row is `GET activity/continue`. Missing and upgrades are copy shortfalls: without `see_files` both rows are
  empty, in `GET discover` and `GET discover/{row}` alike.
- **`GET discover/{row}?limit=&offset=`**: one row in full, paged, same picks and order. The rows mix movies and
  series, so each "more" link is the SPA's `/discover/{row}` (every kind, the row's own predicate), not a Movies grid
  filtered by `availability=`, which means something else (a movie missing one of two versions is `partial` there but
  in the Missing row).
- **See files.** `perms.go` holds the field filter: without `see_files` there are no paths, hashes, release names,
  sizes, scores, providers, profiles, minimum availability, folders, arr records or registry rows in any of these answers, and `provider=` is refused (403).
  `availability` is watchability for these callers (`plainAvailability`): a movie with a file
  in any version is available (one missing copy does not make it partial); a series is available when some version
  is complete, partial ("Some episodes available") when versions have files but none is complete, missing ("Not
  available") when nothing has a file; never upgrade. The `availability=` filter and the facet counts use the same
  rule (`watch_*` in `availabilitySQL`).
  Qualities are resolutions only (1080p, 2160p), with no file counts, version or file dates, cutoff, upgrade state or
  history; below cutoff reads as available, and the cutoff filters and a source `quality=` are refused (403)
  (`docs/design/users.md` "Users see availability only").
  `Config.Perms` decides. `GET auth/me` lists the permissions.

## Screens

- **Home:** Getting ready, the requests row, Recently added, Upcoming, Missing, Below
  cutoff. Family accounts (no `see_files`, no `manage_requests`): Getting ready, My requests, Recently available,
  Upcoming. Managers' Pending requests posters open the request in the queue (`/requests?request=<id>`).
- **Poster cards:** the title (two lines at most) and the year always under the poster, one restrained availability
  badge (a dot with a word; the word moves under the card when the card is narrower than 160 px), the overview on
  hover. Family wording: Available, Some episodes, Not available, and Getting ready for a missing title with a
  download in hand (`acquiring`). A missing movie reads Upcoming until its `home_release` (the earlier of digital and
  disc, else cinema plus 90 days), not only until its cinema date.
- **Movies, Series, Anime:** Movies and Series leave anime out. Full width. Poster grid paged by 60 as you scroll,
  off-screen cards skipped (content-visibility); a one-line table with columns chosen per list; filters in a sheet
  with the active ones as chips; filters and sort in the URL; view, columns and poster options kept per list. Family accounts' Movies filters leave out Some episodes (a movie has none).
- **Title page:** a compact hero, then for callers who see files the action bar and the Libraries table, then tabs (docs/design/ui.md §2, Title page).
- **Redirects:** `/library` goes to `/movies` (or `/series` for `kind=series`), keeping its `q`, `with`, `without`
  and `state` filters; `/titles/{id}` goes to `/title/{id}` (`internal/web/spa.go`).

## Performance

Version detail hash inference first selects targets used by the requested versions, then checks every active
symlinked version file on those targets. Evidence from another title or version still suppresses an ambiguous
hash. Migration 00087 indexes the target and file-path lookups; unrelated library targets are not grouped.

List rows (including discover/Home) batch stored ceilings and their evidence in one query and load the referenced
quality profiles in one more, decoding each shared profile once per page; larger batches are chunked below SQLite's
variable limit. Availability, filters, facets and permission redaction retain their existing rules.
