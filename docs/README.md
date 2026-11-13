# Documentation

Start with the task you have. A **library** is what settings keys and API fields call a `version`
(`versions.movies`). The CLI takes `-library movies`; the older `-version movies` still works.

## User guides

| Guide | Use it for |
|---|---|
| [Quickstart](quickstart.md) | Run the container, first login, Setup, a first library (Live at once or Watch only), going Live, a first download |
| [Not supported yet](not-supported.md) | What Theatarr does not do yet (usenet, real torrent clients, Jellyfin and Emby, ...) and the workarounds |
| [Troubleshooting](troubleshooting.md) | A restarting container, TMDb off, no profile, a Watch only library that does nothing, a Live library that downloads nothing |
| [Arr-to-Theatarr map](arr-map.md) | Where each familiar Sonarr, Radarr, Prowlarr and Recyclarr page lives |
| [Moving from Sonarr and Radarr](migration.md) | The one-time import from an arr, a trial, client and Plex handover, rollback, and what upgrading from the older tools changes |
| [Decision journal](journal.md) | Reading what Theatarr decided and why, and correcting it |
| [Backup and restore](restore.md) | Backup sets, restore, upgrades and image rollback |
| [MCP](mcp.md) | Connect an MCP client to set up and check an install |
| [Demo data](demo.md) | An invented library for trying the UI offline |
| [Development](dev.md) | Toolchain, make targets, code layout and test rules |

## Internals (design notes)

Written for contributors. They describe how Theatarr is built and why, including history; you do not need them to
run it. [design/README.md](design/README.md) indexes them: settings, indexers, providers, the router, library
imports, the arr-compatible views, Plex and the UI. [design/overview.md](design/overview.md) describes the
architecture.
