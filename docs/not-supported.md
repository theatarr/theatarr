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
- **Delay profiles and release profiles.** There is no delay before a grab and no release profile. The import
  from Sonarr or Radarr does not bring them over. Quality profiles and custom formats cover most of
  what release profiles are used for.
- **Some anime packs of several seasons.** Every series takes a pack of several seasons, which Sonarr does not, and
  searches two or more wanted seasons together with one query without a season (docs/design/series.md); an anime
  pack is proved file by file at import. Still not taken for anime: "Complete Series" names; named packs of
  scene-numbered seasons; one download for a SeaDex torrent shared by two seasons (the second season takes it with
  its next search). A pack whose seasons are the tracker's and not the library's usually fails the proof (its files
  do not match the library's episodes) and the seasons are then fetched one by one; but where a tracker's season has
  as many episodes as the library's, equal numbers are not proof of equal content (another cour can sit there) and
  such a pack is taken as named. Any pack whose name does not prove the seasons between ("S01.S03") is rejected.
  A pack whose files are bare numbers ("Season 1/01.mkv", "001.mkv") is rejected at import and its seasons are
  searched one by one: a bare number carries no proof of what it numbers. So is a pack with an episode whose file
  name holds an extra's marker word ("S01E05 - Special Training", "- 07 - Opening Night"): the file is set aside as
  an extra, that episode has no file, and the seasons are searched one by one. Under a search of several seasons an
  anime batch named "2nd Season 01-12" or "S01-10" is read as a span of seasons and not taken; the season's own
  search takes it.

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
`theatarr.*` events), a Discord format, an ntfy format (not yet tested against an ntfy server) and a Telegram
format (built from Telegram's Bot API documentation, not yet tested against Telegram's servers). There is no
built-in email, Gotify or Pushover target.

A Discord, ntfy or Telegram target gets a message when a health check starts failing and when it stops, for
the daily digest, a request that is ready, and a file that broke while someone was playing it (replaced, no other
copy, or the earlier copy put back). Health warnings, such as a mount that stopped answering, are not
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

- **`PUT episode/{id}`**. No client we checked calls it: they set episode monitoring with `PUT episode/monitor`,
  which works.
- **The iCal calendar feed.** Clients read the calendar as JSON (`GET calendar`), which works.

## Other gaps

- **ARM images.** Releases from 0.1.5 publish a linux/arm64 image beside linux/amd64 under the same tag (Docker
  picks the right one). Every CI run builds it and starts the arm64 binary once under emulation, but it has not
  been tested on real hardware (an ARM VPS, Raspberry Pi, ARM NAS), and neither has a build from source on an
  arm64 host. Reports are welcome. 32-bit ARM is not built.
- **No update notice.** Theatarr does not check for new versions; `theatarr version` prints the build you run.
- **IMDb lists.** IMDb answers a plain HTTP client with an empty page (a bot challenge), and Sonarr and Radarr read
  IMDb lists through their own hosted service. Use a **JSON list** made from the IMDb list instead (**Settings ›
  Import lists**). Trakt, Plex watchlist and the other arr list providers are not offered either.
- **Tags.** Compatible clients such as Seerr and Ruddarr can set tags on a movie or series, per library,
  and they are kept: the client sees them again and a Connect webhook with tags gets only the events of titles
  carrying one of them. Theatarr's own pages show them (About's **Tags**, a Tags column and a Tag filter on Movies,
  Series and Anime, for accounts that see files) and managers edit them there (About's **Tags** › Edit, the selection
  bar's **Tags…**); nothing is routed by tag, and **Import from Sonarr/Radarr** does not bring them. Each library lists its indexers and providers instead
  ([arr-map.md](arr-map.md)).
