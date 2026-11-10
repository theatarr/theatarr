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
  editor still shows the `seedbox` storage and the `hardlink` and `copy` import modes when a saved library already
  uses them, labelled "not supported yet"; nothing sends grabs to such a library.
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

## Other gaps

- **No published image yet.** Build the image from the repository ([quickstart.md](quickstart.md)).
- **No update notice.** Theatarr does not check for new versions; `theatarr version` prints the build you run.
- **Import lists.** Only TMDb movie lists, created through the Radarr-compatible API. There is no settings page for
  lists and no series lists.
- **Tags.** There is no tag-based routing; each library lists its indexers and providers instead
  ([arr-map.md](arr-map.md)).
