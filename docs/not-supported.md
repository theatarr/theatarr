# Not supported yet

Theatarr is built around one setup: a debrid account, a debrid mount (zurg or rclone), symlinks into your library
folders, and Plex. This page lists what it does not do yet, so you can decide before you install. Where there is a
workaround, it is here too.

## Downloading

- **Native usenet.** Theatarr has no NZB downloader and does not talk to SABnzbd or NZBGet. A usenet-to-debrid
  helper can still send grabs through a library's arr-compatible view (`/v/<library>/radarr` or `/sonarr`), as it
  would with a Radarr or Sonarr; see [design/clients.md (internals)](design/clients.md).
- **Real torrent clients.** Grabs go to Real-Debrid, AllDebrid or TorBox directly, or to a qBittorrent-API debrid
  bridge such as decypharr, which adds them to its own debrid account. A qBittorrent on a seedbox or at home that
  downloads real files is not supported, and neither are hardlink or copy imports of those files. The library
  editor shows **Storage** and **Import mode** (under **Advanced**) only when a saved library already uses the
  `seedbox` storage or the `hardlink` or `copy` import mode, labelled "not supported yet"; nothing sends grabs to
  such a library.
- **Delay profiles and release profiles.** There is no delay before a grab and no release profile. Migration from
  an arr reads them only to warn that they are not carried over. Quality profiles and custom formats cover most of
  what release profiles are used for.

## Media servers

- **Jellyfin and Emby.** Theatarr only talks to Plex (library scans, sign-in, now playing). Jellyfin and Emby can
  still serve the library: they see the symlinks in the library folders, as long as they can also read the debrid
  mount at the path the links point to. Set them to scan on a schedule or watch the folders.

## Sign-in

- **OIDC, LDAP and two-factor sign-in.** Users sign in with a local password or with Plex. A reverse proxy with its
  own sign-in can sit in front of the web UI, but Theatarr still asks for its own login behind it, and the
  arr-compatible views and API keys must stay reachable without the proxy's sign-in for Seerr and the arr apps.

## Notifications

Theatarr sends notifications as webhooks: a generic JSON webhook (arr-shaped payloads, or Theatarr's own
`theatarr.*` events), a Discord format and an ntfy format (not yet tested against an ntfy server). There is no
built-in email, Telegram, Gotify or Pushover target.

A Discord or ntfy target gets a message when a health check starts failing and when it stops, for new errors,
the daily digest and a request that is ready. Health warnings, such as a mount that stopped answering, are not
sent at once unless you turn **Also send warnings** on for that target (**Settings › Notifications ›
Destinations**; off by default): it then also gets a
check that starts to warn and a warning that eases or clears. Notices are never sent on their own; warnings and
notices are both in the daily digest. A JSON webhook target has no such option: it is filtered by its events.

To reach one of those services, send the webhook to something that turns JSON into a message:

- **A small relay** (n8n, Node-RED or a short script) that reads the JSON and posts a message to your service. This
  works with any service and lets you choose the wording.
- **[Apprise API](https://github.com/caronc/apprise-api)**, which forwards one message to many services (email,
  Telegram, ntfy and so on). Apprise API expects a message `body` (and an optional `title`), which Theatarr's JSON
  does not have. It can rename fields of an incoming JSON body with query parameters that start with a colon; use
  them in the target URL to map fields of the payload to `title` and `body`. The exact syntax is in the Apprise API
  documentation. This mapping has not been tested with Theatarr, so send a test event and check that the message
  arrives.

Either way, add a target under **Settings › Notifications › Destinations** with the relay's or Apprise API's
address as its URL and the format left empty (JSON). A target needs something to send: list the arr views whose
events it gets, or turn on the `native` option for Theatarr's own events. Then turn **Send notifications** on.

## Arr-compatible API

The Radarr- and Sonarr-compatible views do not answer every call of the arr APIs. Known gaps:

- **Sonarr's "add import list exclusion" on a series delete** (`addImportListExclusion`). LunaSea and Ruddarr can
  send it. The series is still removed, but the switch does nothing: Theatarr has no series lists and no series
  exclusion list for it to act on. Radarr's `addImportExclusion` on a movie delete is supported.
- **`PUT episode/{id}`**, **`DELETE queue/bulk`**, **`DELETE blocklist/bulk`** and the **iCal calendar feed**. No
  client we checked calls them: they set episode monitoring with `PUT episode/monitor`, remove queue and blocklist
  entries one at a time, and read the calendar as JSON, all of which work.

## Other gaps

- **ARM images.** Released images are for linux/amd64 only. The image builds for linux/arm64, and every CI run
  builds it and starts the arm64 binary once under emulation, but no arm64 image is published yet and it has not
  been tested on real hardware (Raspberry Pi, ARM NAS). On an arm64 host, build it from source
  ([quickstart.md](quickstart.md#1-run-the-container)); CI cross-builds on an amd64 machine, so a build on an
  arm64 host is untested too. Reports are welcome. 32-bit ARM is not built.
- **No update notice.** Theatarr does not check for new versions; `theatarr version` prints the build you run.
- **Import lists.** Only TMDb movie lists (**Collections › Lists**, or the Radarr-compatible API). There are no
  series lists and no other list providers yet.
- **Tags.** There is no tag-based routing; each library lists its indexers and providers instead
  ([arr-map.md](arr-map.md)).
