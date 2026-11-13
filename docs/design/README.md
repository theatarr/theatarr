# Design docs

Written for contributors. They describe how Theatarr is built and why, including history; you do not need them to
run it. The user guides are indexed in [../README.md](../README.md).

How Theatarr works inside, one part per page. Start with the overview.

- [overview.md](overview.md): what Theatarr is, the domain model, the pipeline, the safety model and settings.

## Decisions

- [decide.md](decide.md): the decision engine, history parity, SeaDex standing.
- [profiles.md](profiles.md): profiles, the quality half of a decision.
- [formats.md](formats.md): the custom-format engine and TRaSH formats.
- [series.md](series.md): Sonarr import, series decisions and series RSS.
- [rss.md](rss.md): the RSS sync for Live libraries.

## Acquisition

- [indexers.md](indexers.md): search sources, single-flight, the planner.
- [cardigann.md](cardigann.md): native Cardigann indexers, the definition catalog and trawl.
- [gateway.md](gateway.md): the single outbound HTTP path, budgets and the VPN guard.
- [router.md](router.md): the download router: provider order, cached-only adds, pacing, recovery.
- [providers.md](providers.md): debrid provider reads and the content registry.
- [external-clients.md](external-clients.md): sending grabs to a qBittorrent-API client.
- [torrents.md](torrents.md): the provider torrent manager: usage, backups, restores, deletes.
- [ceilings.md](ceilings.md): source ceilings.

## Library

- [library.md](library.md): import, links, naming, probes, Plex scans, the mount guard.
- [import-arr.md](import-arr.md): importing arr instances and seeding the registry.
- [manage.md](manage.md): season and episode management, upgrade explanations.

## APIs and clients

- [arrview.md](arrview.md): the Radarr- and Sonarr-shaped views.
- [devices.md](devices.md): devices and integrations on the views and the native API.
- [clients.md](clients.md): client compatibility.
- [web.md](web.md): auth, the native API's base routes, setup and logs.
- [users.md](users.md): users, Plex sign-in and requests.

## Metadata

- [tv-metadata.md](tv-metadata.md): series metadata refresh.
- [tvdb.md](tvdb.md): the TVDB v4 client.
- [anime.md](anime.md): the anime metadata engine.
- [artwork.md](artwork.md): artwork and title metadata.

## UI

- [ui.md](ui.md): the web UI: surfaces, requests, metadata, the UI's API, product rules.
- [library-ui.md](library-ui.md): the library pages.
- [settings.md](settings.md): database-backed settings, the settings API and pages.
- [now-playing.md](now-playing.md): the now playing page.

## Operations

- [readiness.md](readiness.md): health checks, backups, policy revisions, job priorities.
- [jobs.md](jobs.md): failed jobs: resolve, retry, dismiss.
- [errors.md](errors.md): the error inbox.
