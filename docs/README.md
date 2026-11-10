# Documentation

Start with the task you have. A **library** is what settings keys, API fields and CLI flags call a `version`
(`versions.movies`, `-version movies`).

## User guides

| Guide | Use it for |
|---|---|
| [Quickstart](quickstart.md) | Build and run the container, first login, Setup, a first library in shadow, going live |
| [Not supported yet](not-supported.md) | What Theatarr does not do yet (usenet, real torrent clients, Jellyfin and Emby, ...) and the workarounds |
| [Troubleshooting](troubleshooting.md) | A restarting container, TMDb off, no profile, a library that does nothing, the go-live agreement line |
| [Arr-to-Theatarr map](arr-map.md) | Where each familiar Sonarr, Radarr, Prowlarr and Recyclarr page lives |
| [Migration](migration.md) | Bring an existing arr stack in, disabled and in shadow |
| [Parallel libraries](parallel.md) | Follower libraries that run beside an arr on roots of their own |
| [Going live](golive.md) | The go-live check, a follower trial, client and Plex handover, stop switches and rollback |
| [Decision journal](journal.md) | Reading what Theatarr decided and why, and correcting it |
| [Backup and restore](restore.md) | Backup sets, restore, upgrades and image rollback |
| [MCP](mcp.md) | Connect an MCP client to set up, migrate and check an install |
| [Demo data](demo.md) | An invented library for trying the UI offline |
| [Development](dev.md) | Toolchain, make targets, code layout and test rules |

## Design and reference

[design/README.md](design/README.md) indexes the design and reference docs: settings, indexers, providers, the
router, library imports, the arr-compatible views, Plex and the UI. [design/overview.md](design/overview.md)
describes the architecture.
