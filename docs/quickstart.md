# Quickstart

From nothing to a first library: live at once, or Watch only first. Theatarr replaces Sonarr, Radarr, Prowlarr,
Recyclarr and a debrid bridge (decypharr or similar) with one service. Seerr, Overseerr, Homepage and the arr phone
apps keep working: they talk to Theatarr's arr-compatible views.

Every path, address and key below is a placeholder. Replace them with yours.

Coming from a running Sonarr or Radarr? Set up Theatarr with the steps below, then **Admin › Import from
Sonarr/Radarr** (linked from **Setup** and **Settings › Libraries**) brings an instance's titles, what is monitored and
the files already on your debrid mount into your library, once ([migration.md](migration.md#import-from-sonarrradarr)).

## 1. Run the container

Pull the released image with [`examples/compose.image.yaml`](../examples/compose.image.yaml), copied as
`compose.yaml` into any directory; no checkout of the repository is needed. Each release publishes an image for
linux/amd64 and linux/arm64 to `ghcr.io/theatarr/theatarr`, and the
[Releases page](https://github.com/theatarr/theatarr/releases) lists the versions. The arm64 image is
cross-built and started once under emulation in CI; it is untested on real hardware. The image tag is the version
without the leading `v`: release `v0.2.0` is the image `0.2.0`. Put the version in a `.env` file beside
`compose.yaml`, where every `docker compose` command reads it:

```sh
echo THEATARR_VERSION=0.2.0 > .env
```

```yaml
# compose.yaml
services:
  theatarr:
    image: ghcr.io/theatarr/theatarr:${THEATARR_VERSION:?set THEATARR_VERSION to a released version, for example 0.1.5}
    container_name: theatarr
    restart: unless-stopped
    ports:
      - "7700:7700"
    volumes:
      - ./data:/data                   # database, backups and logs
      - /mnt/debrid:/mnt/debrid:ro     # the debrid mount (zurg, rclone, ...): Theatarr only reads it
      - /srv/media:/media              # library roots (/media/movies, /media/tv, /media/anime) and the link trash
                                       # (/media/.trash, outside every root); mount them where the media server sees them
      # - ./trash-guides:/config/trash:ro  # optional: a TRaSH-Guides checkout (clone it first), see below
    environment:
      TZ: Etc/UTC                      # the nightly backup runs at 03:00 in this zone
      # THEATARR_TRASH: /config/trash  # the optional TRaSH-Guides checkout above
```

**Building from source instead.** In a checkout of the repository, copy
[`examples/compose.yaml`](../examples/compose.yaml) to the repository root (`compose.yaml`, `data/` and
`trash-guides/` there are ignored by git and left out of the image build). It is the same service with `build: .`
and `image: theatarr:local` in place of the released image, needs no `THEATARR_VERSION`, and starts with
`docker compose up -d --build`.

Theatarr starts with an empty data directory and no configuration. It serves the UI and shows what is missing
instead of exiting. Each missing piece turns one feature off, and the Setup screen and `theatarr preflight` say which.

The image runs as uid:gid `65532:65532`. `./data`, the library roots and the trash directory must exist and be
writable by it; the debrid mount only readable. Docker creates a missing bind-mount directory owned by root, and
the service then cannot write `/data`: it exits with one line that names the directory, the uid and the `chown` to
run ([troubleshooting.md](troubleshooting.md#data-directory)), and restarts in a loop. Create them before the first
`up`. With the host paths above, in the directory that holds `compose.yaml`:

```sh
mkdir -p data
sudo chown 65532:65532 data
sudo mkdir -p /srv/media/movies /srv/media/tv /srv/media/.trash      # library roots and the trash, as you need them
sudo chown 65532:65532 /srv/media/movies /srv/media/tv /srv/media/.trash
```

The trash directory (`/media/.trash` in the container) is outside every library root. Only chown the directories
Theatarr writes; it needs no other access to `/srv/media`.

Mount the library roots at the same path the media server sees them, and the debrid mount at the path the symlinks
should point at (or set the link path under **Settings › Libraries › File management** to the path the media server
sees). Plex follows the links, so Plex must see the debrid mount too.

The TRaSH-Guides quality profiles are built in: each release bundles a snapshot of them, so the TRaSH defaults are
listed under **Settings › Profiles** and in every profile picker with no setup. A TRaSH checkout is optional: it
follows upstream between releases and is what quality sync from a local tree reads. To use one, run
`git clone --depth 1 https://github.com/TRaSH-Guides/Guides trash-guides` and uncomment the two TRaSH lines.
`THEATARR_TRASH` is this checkout; it is not the trash directory for replaced links, which you set in Settings.

## 2. First login

A new install has no account. Start it, open `http://<host>:7700`, and the first visit shows **Create your admin
account**: pick a username (`admin` is filled in) and a password of at least 12 characters. You are signed in at once.
The page is there only until the first account exists.

```sh
docker compose up -d                      # built from source: docker compose up -d --build
```

If you lose the password, run `docker compose exec theatarr theatarr admin reset-password --username <yours>` (the
default is `admin`). On 0.1.5 and older there is no create page: the first admin password is in
`data/initial-admin-password`. An install from such a version whose password was never used keeps it there; the
sign-in page says so.

Creating the account opens **Setup** (`/admin/setup`), which walks through the steps below and ends with the
preflight. Until setup is finished, **Home** also shows a **Finish setting up Theatarr** card whose **Continue setup**
leads back to it.

## 3. Follow Setup, then edit Settings forms

Open **Setup** (`/admin/setup`). Its six steps: secure the admin account, connect downloads, add metadata (TMDb),
connect the library, check readiness, then review activation. A fresh install needs a writable data directory, nothing else. Add the debrid mount,
writable library roots and trash before checking readiness or importing. Settings save at once; only a few changes
ask first (making a library Live, turning **Pause** off, removing a library, and moving a
provider or indexer to a new address while keeping its stored key). Steps 4–7 below fill in the connections
and the first library; Setup links to each editor.

**Settings** (`/admin/settings`) uses forms. Credentials are stored write-only in the database:

| Where | What to enter |
|---|---|
| **Libraries › File management** | **Mount folder** (`/mnt/debrid`), **Library link destination** (the mount as Plex sees it; empty uses the Mount folder), **All torrents folder** (a folder name under the mount, `__all__` on zurg and mounts laid out like it; not a path) and **Trash › Folder** (`/media/.trash`) |
| **Plex & playback** | Plex URL and token, and the Plex section for each library |
| **Metadata & artwork** | TMDb (required: adding titles, search, art and release dates need it) and its image host; TVDB for Sonarr-style series numbering and for anime. See below |
| **Indexers › VPN** | Optional VPN exit check and the traffic classes that require it |
| **Download clients** | Debrid providers, other clients, and download behaviour |
| **Libraries** | Library roots, profiles, indexers and provider order |

Save validates and applies settings without a restart; new work and background loops use the saved generation.
Use **Show advanced** for less common fields, or **Edit as YAML** for a section's raw editor. Nothing downloads
until you say so: the first-library flow sets the library to **Live** and turns **Pause** off only
when you choose **Create library and start downloading** and confirm the list it shows (step 6).

**TMDb.** Setup's **Add metadata (TMDb)** step has one field: your TMDb API key (the short v3 "API Key" from your
TMDb account's API settings, not the long "API Read Access Token"). Saving it creates the two sources below with
TMDb's public addresses and stores the key. The step also counts as done when the
`tmdb` source reads its key from an environment variable (`api_key_env`).

By hand, on **Metadata & artwork**: metadata sources are a list of named sources, and the server has no default
address. Add a source named exactly `tmdb` with the source address `https://api.themoviedb.org/3` and your TMDb API
key. Add a second named `tmdb-images` with the address `https://image.tmdb.org/t/p` and no key: it serves posters and
backdrops through Theatarr's image cache, and without it the UI shows generated art only. For TVDB, a source named
`tvdb` with the address `https://api4.thetvdb.com/v4` and your TVDB key (a subscriber key also needs the PIN in
`THEATARR_TVDB_PIN`). [`examples/policy/metadata.yaml`](../examples/policy/metadata.yaml) has the same sources.

**Optional import from YAML.** Startup does not read a policy directory. To seed a new database from YAML files,
stop the service, run `theatarr policy validate DIR`, then `theatarr settings import -policy DIR` to review the
preview, and repeat with `-confirm` to save it. Importing never turns Pause off. A running install
imports and exports under **Advanced › YAML settings › Import & export**. See
[the settings reference (internals)](design/settings.md).

The database and listen paths, `THEATARR_FORCE_OFF`, `THEATARR_TRASH`, the log and backup paths and credentials read
from environment variables need a restart. A credential stored in Settings wins over an environment variable.

**VPN egress (optional).** With the VPN check configured (**Settings › Indexers › VPN**), Theatarr compares its exit
address with the expected VPN addresses and refuses requests of the required traffic classes (by default indexers)
while the check fails; an empty list of required classes only monitors. Without it the guard is off: Theatarr warns
in health and the UI, and searches from whatever address the host uses. To use it, run the container in the VPN's
network namespace, or on a host whose only route out is the VPN.

**The debrid mount.** zurg (Real-Debrid) or rclone with a debrid WebDAV remote (AllDebrid, TorBox) on the host,
bound read-only into the container. Theatarr only reads it and links into the library roots. It never writes into it.

## 4. Debrid providers

**Settings › Download clients › Add provider**: Real-Debrid, AllDebrid or TorBox, with its API key. A new
source is off until you turn it on, and one save does it: fill in the form, press **Test these values**, and when
the test passes choose **Enable and save** beside the result. (**Save** alone stores the form as it is, so a new source stays disabled; change a value
after the test and it must pass again before **Enable and save** comes back.) The name and URL default from the
service; budget, pacing, stall timeout and the rest stay under **Show advanced**. The key is stored in the
database, write-only. The test makes one read-only account call, with the values in the form and before anything
is saved, and the result shows whether the plan is premium and when it ends. Several providers form a fallback order. Setup counts a provider as done once it is enabled and has an API
key (stored, or in the environment variable it names).

To keep decypharr or another qBittorrent-API client, add it under **Settings › Download clients › Other clients**
and let the library add through it ([external-clients.md (internals)](design/external-clients.md)).

**Admin › Torrents** lists each account's torrents with what uses them, and backs them up or restores them
([torrents.md (internals)](design/torrents.md)).

## 5. Indexers: keep Prowlarr, or go native

Both are first-class, and they mix. **Settings › Indexers › Add indexer** has four tabs and opens on Catalog:

- **Catalog.** The tracker sites Theatarr searches itself (the Cardigann definitions Prowlarr uses,
  [cardigann.md (internals)](design/cardigann.md)). A new install starts with the catalog on; when it is off, the tab
  has **Turn on the catalog**, which downloads the list from GitHub at once and checks it for updates once a day. No
  tracker is contacted until you add one. Sites behind a Cloudflare check need a FlareSolverr-compatible service, set
  under **Settings › Indexers › Catalog**.
- **Torznab & built-in sources.** Any Torznab or Newznab feed (Jackett, a usenet indexer), and the built-in sources:
  DMM, Torrentio, Zilean and SeaDex. None of them is required: use the ones you want. DMM's API key comes only with a
  paid GitHub sponsorship of DMM.
- **Link Prowlarr.** Enter Prowlarr's address and API key once; Theatarr lists Prowlarr's indexers and adds the
  ticked ones as Torznab feeds. A new address needs the key typed again. The trackers, their logins and their VPN
  stay in Prowlarr: run Prowlarr behind your VPN, as Theatarr's VPN check does not cover the requests Prowlarr
  sends. Prowlarr must be on your local network. Indexers added to Prowlarr later are linked by opening the tab
  again. Keep DMM and SeaDex added directly: through Prowlarr they lose which debrid service has a release cached.
- **Move from Prowlarr (once).** Each Prowlarr indexer that has a catalog definition becomes an indexer Theatarr
  runs itself, with its own request budget, in the libraries its categories fit; the others are left out (link them
  instead). Logins are not copied, so a tracker that needs one is added off: enter its login, then turn it on.
  Prowlarr's feeds stay as they are: take a feed out of a library once its tracker runs here, or the tracker is
  searched twice.

A new indexer is on, and the same form has a **Libraries** list: the ticked libraries search it from that save. Every
library that suits it starts ticked (a catalog site by its categories, SeaDex for anime libraries, every other
source for all libraries). Each form can test the values before saving (a probe query through the configured VPN
check); a test is not required. Catalog and moved indexers get a request budget of their own, linked Prowlarr feeds
share one (`prowlarr`, 60 a minute); built-in sources get none automatically (assign one if you want it). Setup
counts an indexer as done once it is enabled; a Torznab or DMM indexer also needs its API key, while Torrentio,
Zilean, SeaDex and catalog sites need none. Setup's steps, its **Off until set up** list and the
SetupCheck all apply this rule.

A built-in source needs its **Service URL**. The form for a new Torrentio or SeaDex indexer starts with the public
address filled in (change it if you use another instance); DMM and Zilean start empty. Nothing is stored until you
save, and the server has no default of its own. The public services are at:

| Source | Service URL |
|---|---|
| Torrentio | `https://torrentio.strem.fun` (prefilled) |
| DMM | `https://debridmediamanager.com/api/torznab/rd/cached` (DMM's Torznab feed: `/api/torznab/cached`, or `rd`, `ad` or `tb` in the path for one provider's cache), with your DMM API key (paid: DMM gives keys only to its GitHub sponsors) |
| Zilean | your own Zilean instance, for example `http://zilean:8181` |
| SeaDex | `https://seadex.moe` (prefilled; `releases.moe` redirects there) |

[`examples/policy/indexers.yaml`](../examples/policy/indexers.yaml) uses the same addresses.

Privacy is a label for badges, filters and sorting, nothing else. An indexer without one counts as private,
except the built-in public sources (Torrentio, DMM, Zilean, SeaDex), which count as public. Releases from every
indexer a debrid library lists, whatever its label, go to that library's providers (optionally limited per
indexer). When a DMM indexer is configured, and only then, DMM's availability check also runs before the grab. It
covers Real-Debrid and AllDebrid only: releases a library may send to one of them are asked about, and those DMM
lists as cached there get that provider's "cached on" mark. A library whose providers are neither (TorBox only,
for example) asks DMM nothing.

DMM is optional. Cached-only grabbing (**Only add cached releases**) works without it: the debrid provider's
own answer decides whether a release is cached. What happens to an uncached one depends on the provider and on
the account. TorBox is asked before the add and an uncached release is not added. Real-Debrid and AllDebrid are
read back after the add: an uncached add of Theatarr's own is removed again and the next provider is tried (on
AllDebrid it is removed only when that is turned on, off by default), while a torrent that was on the
account before the add stays, and the download waits for it. Without DMM fewer releases carry a cached mark before
the grab, so the provider order and the release list have less to go on.

## 6. A first library

A library groups a set of titles with a kind (movies or series), a quality profile, a library root it links into,
the indexers it searches and the providers it adds to. **Setup** builds the first one:

1. a key (`movies`), the kind, and the library root (`/media/movies`). The root must exist and be writable by
   uid 65532 (step 1). It can be any folder: **Check root** shows where it resolves and refuses only a root that is
   missing, the filesystem root, inside the debrid mount or a folder holding it, or overlapping the trash or another
   library's root;
2. a quality profile: a TRaSH default builds a new profile with its formats, or pick an existing profile. Every
   TRaSH quality profile is offered, here and in the library editor under **Settings › Libraries**;
3. the indexers and providers;
4. one of two buttons on a fresh install:
   - **Create library and start downloading** creates the library **Live** and turns **Pause** off, in one
     save. A dialog lists each of these first; nothing is saved until you confirm. The button is disabled, with the
     reason, until the library picks at least one enabled indexer and one enabled debrid provider with its key, and
     the debrid mount and the trash folder (**Settings › Libraries › File management**) are set. If
     `THEATARR_FORCE_OFF` forces Pause, the library is still created and Setup says Pause stays on.
   - **Create as Watch only (decide later)**, then **Create library**: the library starts as **Watch only**
     and Pause does not change.


A **Watch only** library: Theatarr searches, downloads and links nothing for it. A debrid library needs no download client: Theatarr adds to the providers itself. Edit libraries later
under **Settings › Libraries**.

The editor there has two things Setup does not ask for:

- **Name** (optional, at most 40 characters). Theatarr's own pages and the request-ready notifications show it
  in place of the label guessed from the key and profile, such as "4K WEB". Connected apps never see it: the key,
  the root folder and the profile names of the arr-compatible views stay as they are, so renaming a library breaks
  nothing in Seerr or an arr client. A release from before this field cannot load settings that hold a name; clear
  the names before you go back to one.
- **Advanced**, at the end of **Where**: **Write roots**, **Send grabs to** and **Client**. A debrid library that links into its own root needs none of
  them. The section starts open when the library already uses one of them.

Each library is **Watch only** or **Live**, chosen in its row on **Settings › Libraries** or at the top of its
editor. Going **Live** asks for a confirmation first; going back to Watch only saves at once. Watch only: Theatarr searches,
downloads and links nothing for this library. Live searches, downloads and links. Beside the control, the row says
when Pause holds a Live library back: **Live, paused**.

**Pause**, at the top of every admin page, stops searches, new downloads, list syncs and changes in your library
folders, for every library. Downloads already at a provider finish there and are imported when you turn Pause off. Turning it off
asks for a confirmation that names the Live libraries; turning it on does not. **Send notifications** is a field on
**Settings › Notifications**.

**A Watch only library makes no decisions of its own.** The scheduled search for wanted titles and the RSS sync
run only for libraries that are Live while Pause is off. To see how a library judges releases, start in the UI:

- **Settings › Profiles › Check a release**: paste a release name and pick a title and library. It shows the parsed
  quality, the matched formats, the score, any rejection and whether it would upgrade the file. It searches nothing
  and records nothing.
- The title's **Releases** page (from the title page): for a Live library it can search the indexers now; for a
  Watch only library it shows the last stored decision, if there is one.

To search a Watch only library's indexers for a title right away, run the decision engine from the shell. It prints the
ranked decisions with their score breakdown and records nothing:

```sh
docker compose exec theatarr theatarr decide -tmdb 603 -library movies -search                  # a movie, by TMDb id
docker compose exec theatarr theatarr decide -tvdb 81189 -library tv -season 1 -search          # a series season
```

## 7. Preflight

**Setup › Run preflight**, or from the shell:

```sh
docker compose exec theatarr theatarr preflight          # text
docker compose exec theatarr theatarr preflight -json    # the same report as JSON, for scripts
```

Both forms exit 1 when a check fails, and 0 otherwise.

It checks the mount (visible, readable, not empty), each writable library root and the trash (they exist and are
writable, are not inside the mount, and are on one filesystem with the downloads when a library imports with
hardlinks), provider keys (a read-only account call), indexers (reachable, a probe query through the VPN check, the
query types they answer), the TMDb and TVDB keys, the arr views, API keys for clients, Plex, and the VPN egress.
Each line is OK, INFO, WARN or FAIL, with why and the next step. `-sources=false` skips the outbound calls. The
preflight never writes anything.

Invalid settings show as FAIL lines while the local checks continue; outbound tests are skipped until the settings
validate. Credentials stored in Settings count; the report marks them without showing their values. A credential
read from the environment is named by its variable.

The VPN result covers Theatarr's own HTTP traffic. It does not cover DNS or the outbound traffic of Prowlarr, Zilean
and other services; those need their own VPN routing.

The health checks (**System › Status**, `GET /api/theatarr/v1/health`) carry a **SetupCheck** notice while
libraries, indexers, providers, TMDb or the mount are missing.

## 8. Seerr, Overseerr and the other arr clients

Theatarr serves Radarr- and Sonarr-compatible APIs (`<view>/api/v3/...`) on port 7700:

| View | Serves |
|---|---|
| `/v/<library>/radarr` or `/v/<library>/sonarr` | One library, by its kind. Exists for every library, no setup needed. Its one quality profile is named after the library's key |
| `/radarr`, `/sonarr` (or your own) | Several libraries behind one server. Created and edited under **Settings › Connected apps** (path, flavour, libraries, profiles, aliases and the view's own API key); applies without a restart |

Clients sign in with an API key. Create one per client under **Users & access** (API keys). In Seerr or Overseerr,
go to **Settings → Services → Add Radarr server** (Sonarr is the same):

- **Hostname or IP**: the Theatarr host. **Port**: `7700`. **Use SSL**: off, unless a proxy terminates TLS in front.
- **URL Base**: the view path, for example `/v/movies/radarr`.
- **API Key**: the key you created.
- **Test**, then pick the **quality profile** (the library's key on a `/v/` view) and the **root folder** (the
  library's root). Mark it the default server.

Seerr adds and removes titles through the view. Its requests become wanted titles for that view's libraries.
Homepage widgets, LunaSea and Ruddarr use the same URL and key ([arrview.md (internals)](design/arrview.md)).

Two details for clients and monitors:

- **`<view>/ping`** (for example `/v/movies/radarr/ping`) answers `{"status":"OK"}` without an API key, as
  Sonarr and Radarr do. Ruddarr uses it to find a reachable address, and an uptime monitor can too. It only says
  that Theatarr answers at that path; everything under `<view>/api/v3` still needs the key.
- **Deleting a movie with "add import exclusion"** (a switch in LunaSea and Ruddarr, off by default) also puts
  the movie on Theatarr's exclusion list, so TMDb lists and collections do not add it again. There is one
  exclusion list for the whole install, not one per library or view, and it does not stop you or a request from
  adding the movie by hand. Theatarr has no page for the list yet: a Radarr client reads and edits it through the
  view (`/api/v3/exclusions`). The same switch on a series delete does nothing
  ([not-supported.md](not-supported.md#arr-compatible-api)).

## 9. Go live deliberately

A library created with **Create library and start downloading** (step 6) is already live: it is Live and
Pause is off. Watch it as in item 3 below. The rest of this step is for a
library created as Watch only.

For a fresh library, check how it judges releases first: **Settings › Profiles › Check a release**, or
`theatarr decide -search` for a title ([step 6](#6-a-first-library)). Then run preflight:

```sh
docker compose exec theatarr theatarr preflight
```

It exits 1 when a line fails; each line names the settings page to fix it on. Go live deliberately once the
decisions look right.

1. **Settings › Libraries:** set the library to **Live**, reviewing the confirmation.
2. Turn **Pause** off at the top of the page. Review the Live libraries it names. A save applies without a restart.
3. Watch **Activity**, **System › Status** and **System › Errors**, and Plex; check that a cached release imports and
   plays. Run the check again.

Downloads and library writes need a **Live** library and **Pause** off; writes stay inside the library's roots and
trash. Pause stops searches, new downloads and library changes; downloads already at a provider finish there and are
imported when Pause is turned off. Settings changes do not cancel work already in flight.
`THEATARR_FORCE_OFF=pause,notify` forces Pause on and notifications off at startup whatever the saved settings say
(`router` and `library` also force Pause); it needs a restart and can never turn anything on.

Moving an existing arr library? Once the library is Live and Pause is off, **Admin › Import from
Sonarr/Radarr** brings in its titles, their monitoring and the files on the debrid mount
([migration.md](migration.md#import-from-sonarrradarr)); Theatarr only reads the instance.

## 10. A first download

With a live library (**Live**, and **Pause** off), add a title and watch it arrive:

1. Type a film or series into the search in the top bar (or open **Search**) and choose **Add** on the result.
   Tick the library and turn **Search now** on: it is off by default, and without it the title waits for the
   automatic search, which runs every 15 minutes and starts at most 10 searches an hour. Once you turn it on, this
   browser keeps it on for the next add, until you turn it off again.

   A series that is not in the library yet has two more choices, and leaving them alone adds it as before:
   - **Monitor**: the seasons you tick (**Seasons I pick below**; **Every season** while the episodes are not
     known yet), or one of **All episodes**, **Future episodes**, **Missing episodes**, **First season**,
     **Latest season**, **Pilot only** and **None**.
   - **Monitor seasons that appear later**, on by default. Off: a season announced after the add stays
     unmonitored.

   Neither is offered for a series that is already in a library.
2. Open **Activity › Queue**. A release that a provider has cached shows there as a download and is then linked
   into the library root; with Plex connected, Theatarr asks Plex to scan that folder.
3. If nothing shows up, open **Wanted**: each row says what the title is waiting for, and **Search now** on the
   title page searches it again at once. The title's **Releases** page lists what the indexers returned and why
   each release was accepted or rejected.

By default only releases a provider already has cached are added (**Settings › Download clients › Behaviour**,
**What to grab › Only add cached releases**), so a title with no cached release stays wanted and is searched again
later. One exception: on AllDebrid an add that turns out uncached stays on the account and finishes downloading
there. [troubleshooting.md](troubleshooting.md#a-live-library-downloads-nothing) explains it and lists the other
reasons a live library stays quiet.

## Readiness-led setup

Home and Settings show admins **Finish setting up Theatarr: N of 6** and **Continue setup** while the install has no
libraries, Setup reports missing pieces, or no library is Active yet and the readiness check (step 5) has not passed
for the current settings (a restart or a settings change opens it again; run preflight to close it). Once a library is Active and nothing is missing, the card and the sidebar entry go away;
the **Setup checklist** link stays on Settings and System › Status. Other users see no setup card. The
[arr-to-Theatarr map](arr-map.md) has the page names and menu paths for familiar arr tasks.

Setup (`/admin/setup`) has six steps:

1. **Secure the admin account:** done when you created the account yourself; an install from an older version
   replaces its generated password here.
2. **Connect downloads:** configure a debrid provider and an indexer; each has its own completion badge.
3. **Add metadata (TMDb):** enter your TMDb API key; Setup creates the `tmdb` and `tmdb-images` sources.
4. **Connect your library:** configure the mount under **Settings › Libraries › File management** and Plex under
   **Settings › Plex & playback**, then create your first library: started at once, or as Watch only
   ([above](#6-a-first-library)).
5. **Check readiness:** run preflight and look at **Activity › Decision journal** (**Open the decision journal**).
6. **Review activation:** one card per library (its key, label, root, state, indexers and providers, and **Why?**
   listing every closed gate), then Pause. This review is read-only; each item links to its
   editor.

The first four steps are complete with a changed password, both download sources, a `tmdb` source whose key
resolves (stored in settings, or in the environment variable it names), and at least one library with a root and a
mount. Readiness is complete when the latest full preflight for the current settings has no failures
(warnings stay visible). The server remembers the latest run until it restarts, so a page reload keeps the step;
after a restart, or once the settings change, run the preflight again. Activation is complete only when a
library runs as **Live** and Pause is off; otherwise it says
**Not yet**.

Theatarr turns these on for you only through **Create library and start downloading** in step 4, after you confirm
the list. Setup stays available from Settings after the reminder disappears.

## More

- [migration.md](migration.md): moving an existing Sonarr/Radarr, Prowlarr, Seerr and Recyclarr setup into Theatarr:
  the one-time import, a trial, client and Plex handover, rollback, and what an upgrade from the older tools changes
- [restore.md](restore.md): backups, restore and upgrades
- [mcp.md](mcp.md): set up and run Theatarr from an MCP client
- [design/settings.md (internals)](design/settings.md): the settings database and API, presets and profiles
- [design/web.md (internals)](design/web.md): accounts, API keys, the native API
- [design/library.md (internals)](design/library.md): how imports link into the roots
