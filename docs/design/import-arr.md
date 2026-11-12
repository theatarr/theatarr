# `theatarr import-arr`: arr import with registry seeding

The 15-minute `import-arr` feeds the Watch-only libraries of the [staged path](../migration.md#who-this-is-for); the
one-time import from Sonarr/Radarr is `internal/arrimport` ([migration.md](../migration.md#import-from-sonarrradarr)).

The importer reads Radarr and Sonarr instances, read-only, into Theatarr's database and seeds the content registry.
This page covers movies; series follow the same shape ([Sonarr](#sonarr)). Where this sits: [overview.md](overview.md).

## What it does

`theatarr import-arr [--db file] [--env-file file] [--enrich] [--json] [instance ...]` (instances from the stored
`import.arr` settings in the database)

- **Reads each instance with GET only:** system/status, rootfolder, tag, qualityprofile, delayprofile,
  releaseprofile, movie, moviefile (in batches of 50 movie ids) and the full history (250 per page, newest first).
  - Delay and release profiles are not imported: Theatarr has neither.
    Each one that would change what the arr grabs (an enabled release profile with terms; a delay profile that
    delays torrents or turns them off) is a warning in the summary.
  - Requests are sequential, at most 2.5 per second.
  - On a re-run, history stops at the first page whose records are all stored.
  - Blocklists (`GET blocklist`, 250 per page) are read in full on every run.
- **Writes Theatarr's database** (migration `00003_import.sql`), in one transaction per instance:

| Table | Holds |
|---|---|
| `title` | One per TMDb id, whichever instances hold it |
| `version` | One per title and policy version, with `monitored` and `state` |
| `version_source` | The instance that owns a version's state |
| `arr_instance`, `arr_tag`, `arr_quality_profile` | Reference data per instance |
| `arr_title` | The arr's record of each movie: monitored, profile, path, root, tag labels, the labels already copied to the views (`tags_copied`, 00105), file id, added |
| `arrview_tag`, `arrview_title_tag` | The arr's tag labels (requester tags as `1-alice`, every other label as it is), copied to the views' tags of the source version; edge-triggered on `arr_title.tags_copied` (a label is copied once, when it appears; a version that newly gets the title takes all current labels), never removed (`TestImportCopiesTagsToVersions`). See docs/design/arrview.md "Tags from the arrs" |
| `arr_alias` | Seerr ids: the arr's movie id maps to Theatarr's title, its moviefile id to the version_file |
| `version_file` | The arr's current file per version: path, size, quality, revision, release group, scene name, edition, languages, mediaInfo, custom format score, date added, original path, cutoff flag; tied to its download id, the grab's release title and the registry content |
| `arr_history` | Every history event, sanitised. `data` keeps an allowlist of keys (indexer, size, protocol, hash, release group, fileId, paths, message, ...). Values that look like a URL or a credential are dropped, so there are no download URLs, GUIDs or passkeys |
| `content`, `content_version` | Registry seeding: one content per torrent info hash; per hash and version, the release title and the first grab, import and failure |
| `title_release_date`, `title_external_id` | Release dates (from the arr, and from TMDb with `--enrich`); TMDb collection and Wikidata ids |
| `title_alias`, `title.secondary_year` | Migration `00009`. The names releases are mapped by: the arr's `alternateTitles` (source = the instance), TMDb alternative titles and translated titles with `--enrich` (source `tmdb`, with country and language). The secondary year is the arr's `secondaryYear` (TMDb has none) |
| `metadata_cache` | TMDb responses, gzip, with a TTL; nightly retention prunes expired TMDb and TVDB search rows in batches, preserving detail responses for offline reads |
| `import_run` | One row per run, with the JSON summary |

Radarr's movie runtime is imported immediately and refreshed on subsequent imports, as Sonarr's series runtime
is. A zero/unknown response preserves the last known positive runtime. Decision loading also preserves that
fallback when a cached TMDb response omits runtime, avoiding a race with asynchronous metadata enrichment.

- **Prints a summary** per instance, a table per version and a list of conflicts.
  - Per instance: titles imported, unmapped (and why), shared or removed; wanted, filled and cutoff met; file
    linkage; history by event and by protocol; registry counts; warnings; rows changed per table.
  - Conflicts: the same TMDb id with different files across instances that map to one version.
- **`--enrich`** asks TMDb for movies that lack an IMDb id, a year, TMDb release dates or TMDb names (alternative
  titles and translations).
  - Requests go through the gateway (`internal/gateway`, target `metadata:tmdb`): host allowlist, the `tmdb`
    budget, back-off and metrics apply.
  - The key comes from the variable that `metadata.sources.tmdb.api_key_env` names (`THEATARR_TMDB_KEY` in the example).
  - One request per movie (`append_to_response=external_ids,release_dates,alternative_titles,translations`), at 10
    per second, cached in the database. Answers cached under the older, shorter key are asked again by the next `--enrich`;
    decide reads either key.
  - Names are replaced per source on every run; a translation that keeps the title (empty `data.title`) is left out.
    A movie TMDb gives no other names stays a candidate and is answered from the cache.
  - It fills empty fields only. Differences from the stored values are reported, not applied.
  - It also resolves arr movies that have an IMDb id but no TMDb id (`/find`), before the import writes.

## Policy

The `import` settings section (`import.arr`) names the instances. The example is `examples/policy/import.yaml`:

```yaml
import:
  arr:
    radarr_4k:     {flavour: radarr, url: http://radarr:7878,     api_key_env: THEATARR_ARR_RADARR_4K_KEY,     version: movie-remux}
    radarr_4k_web: {flavour: radarr, url: http://radarr-web:7878, api_key_env: THEATARR_ARR_RADARR_4K_WEB_KEY, version: movie-web}
```

Validation:
- the flavour is radarr or sonarr, and must match the version's kind;
- the version must exist;
- the URL's host must be (or resolve only to) a LAN address: loopback, RFC 1918 or ULA. It is listed in
  `OutboundHosts`, and the client checks the address again before every connection;
- the key variable is required (and listed in `SecretEnvs`).

Several instances may map to one version.

## Mapping rules

- **Title:** matched on (kind, TMDb id).
  - A later import fills empty fields and never overwrites. A different IMDb id is a warning.
  - The secondary year follows the arr when it has one. The arr's alternative titles are replaced per instance on
    every run, and dropped with the instance's record when the arr no longer has the movie.
  - `title.monitored` is set when any of its versions is monitored.
- **Version state:**
  - no file: `wanted`;
  - a file with `qualityCutoffNotMet`: `filled`;
  - any other file: `cutoff_met`.
- **Ownership:** the first instance to import a version owns its monitored flag and state.
  - Another instance on the same version still gets its `arr_title` and `version_file` rows, but never changes the
    version.
  - Files that differ are reported as conflicts. Files count as the same when their sizes match and they share a
    content, release title, scene name or file name.
- **Info hash:** `data.torrentInfoHash`, else a 40-hex download id (qBittorrent-type clients and decypharr use the
  hash).
  - Never for a usenet grab (protocol 1). Usenet client ids (SABnzbd and the like) are kept only as download ids.
- **A file's release title:**
  - the source title of the first grab with the file's download id;
  - for a manual import with no download id (the arr's ManualImport), the import event's source title, counted
    separately.
- **Unmapped rows:**
  - a movie with no TMDb id: not imported, listed with its reason;
  - history whose movie is gone from the arr: stored with no title, counted by reason.
- **Idempotent:**
  - every write compares first, so a re-run with nothing new changes no row (besides its `import_run` row);
  - a re-run after new history adds only those rows plus whatever they change;
  - registry rows merge (earliest dates, first title), so instances converge in any order;
  - a movie gone from the arr loses its `arr_title`, `version_file` and `version_source` rows. Theatarr's title and
    version stay.

## What arr data looks like

Patterns the importer is built for:

- **History mixed with usenet.** An instance that also grabs from usenet has grabs with UUID download ids and no
  hash; their `downloadFailed` events carry no hash either. They are stored, but seed no content.
- **ManualImport files** have no download id, so the arr gives no hash for them. When their content is on debrid,
  the provider lists (the registry sync) supply the hash.
- **Files with no import event in history** (older than the history kept, or found by a rescan) have no release
  title. The shadow re-scoring falls back to the scene name or the file name for them.
- **Shared content.** Instances that share a debrid account often hold the same torrent: one hash can back
  versions in several libraries.
- **Tag variants.** The same requester can appear as `1 - alice` and `1-alice`.

## Design notes

- **Files are `version_file` rows, not Links.** A Link needs the symlink target, which only the library scan
  (`readlink`) can give. `version_file.path` is what that scan joins on.
- **Grabs are not stored as Release, Download or Decision rows.** Those need indexer GUIDs, which are dropped as
  possible secrets.
  - `arr_history` keeps what the shadow re-scoring needs: title, indexer, size, quality and the arr's score.
- **Contents seeded from history get state `unknown`,** since no provider list has been read yet.
- **The arr client is `internal/arr`,** GET only, with `movie`, `moviefile` and `rootfolder` on its allowlist.
- **Custom formats are not fetched.** History and moviefile already carry the arr's names and scores. Format
  definitions come from the formats sync ([formats.md](formats.md)).

## Sonarr

Series import (migration `00011`) adds series, episodes, episode files, scene mappings and per-episode
state. See [`series.md`](series.md).
