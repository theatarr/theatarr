# Not supported yet

Theatarr is built around one setup: a debrid account, a debrid mount (zurg or rclone), symlinks into your library
folders, and Plex. This page lists what it does not do yet, so you can decide before you install. Where there is a
workaround, it is here too.

## Downloading

- **Native usenet.** Theatarr has no NZB downloader and does not talk to SABnzbd or NZBGet. A usenet-to-debrid
  helper can still send grabs through a library's arr-compatible view (`/v/<library>/radarr` or `/sonarr`), as it
  would with a Radarr or Sonarr; see [design/clients.md](design/clients.md).
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

- **OIDC, LDAP and two-factor sign-in.** Users sign in with a local password or with Plex. If you need single
  sign-on, put Theatarr behind a reverse proxy that does it, and keep the local accounts for the arr-compatible API
  keys.

## Notifications

Theatarr sends notifications as webhooks: a generic JSON webhook (arr-shaped payloads, or Theatarr's own
`theatarr.*` events) and a Discord format. There is no built-in email, Telegram, ntfy, Gotify or Pushover target.

To reach one of those services, put [Apprise API](https://github.com/caronc/apprise-api) between Theatarr and the
service:

1. Run Apprise API and give it a configuration with the services you want (email, Telegram, ntfy and so on).
2. In Theatarr, add a target under **Settings › Notifications › Destinations** whose URL is that configuration's
   notify address on Apprise API (leave the format empty for JSON; the `native` option adds Theatarr's own events),
   and turn **Send notifications** on.
3. Apprise API expects a message `body` (and an optional `title`), which Theatarr's JSON does not have. Apprise API
   can rename fields of an incoming JSON body with query parameters that start with a colon; use them in the target
   URL to map a field of the payload (for example the event type and the title name) to `title` and `body`. The
   exact syntax is in the Apprise API documentation. Send a test event and check that the message arrives.

If you would rather not depend on that mapping, a small relay (n8n, Node-RED or a short script) that turns the JSON
into a message works with any service.

## Other gaps

- **No published image yet.** Build the image from the repository ([quickstart.md](quickstart.md)).
- **No update notice.** Theatarr does not check for new versions; `theatarr version` prints the build you run.
- **Import lists.** Only TMDb movie lists, created through the Radarr-compatible API. There is no settings page for
  lists and no series lists.
- **Tags.** There is no tag-based routing; each library lists its indexers and providers instead
  ([arr-map.md](arr-map.md)).
