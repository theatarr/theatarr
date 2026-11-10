# Quickstart

From nothing to a first library: live at once, or deciding in shadow mode first. Theatarr replaces Sonarr, Radarr, Prowlarr,
Recyclarr and a debrid bridge (decypharr or similar) with one service. Seerr, Overseerr, Homepage and the arr phone
apps keep working: they talk to Theatarr's arr-compatible views.

Every path, address and key below is a placeholder. Replace them with yours.

Coming from a running arr stack? **Admin › Advanced › Sonarr/Radarr migration** (also linked from **Setup**) walks
through [migration.md](migration.md): it reads the stack and adds its indexers, providers, profiles and libraries for
you, disabled or in shadow. The steps below still apply for checking and going live.

## 1. Run the container

Pull the released image with [`examples/compose.image.yaml`](../examples/compose.image.yaml), copied as
`compose.yaml` into any directory; no checkout of the repository is needed. Each release publishes an image for
linux/amd64 and linux/arm64 to `ghcr.io/theatarr/theatarr`, and the
[Releases page](https://github.com/theatarr/theatarr/releases) lists the versions. The arm64 image is
cross-built and started once under emulation in CI; it is untested on real hardware. The image tag is the version
without the leading `v`: release `v0.1.5` is the image `0.1.5`. Put the version in a `.env` file beside
`compose.yaml`, where every `docker compose` command reads it:

```sh
echo THEATARR_VERSION=0.1.5 > .env
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
      - ./data:/data                   # database, backups, logs and the first admin password
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

The first start creates the `admin` account and writes its password to `data/initial-admin-password` (mode 0600). It
is never logged.

```sh
docker compose up -d                      # built from source: docker compose up -d --build
sudo cat data/initial-admin-password      # mode 0600, owned by 65532
```

Open `http://<host>:7700` and sign in as `admin`. The file is deleted at the first login. If you lose the password,
run `docker compose exec theatarr theatarr admin reset-password`.

After the first login an admin lands on **Home**, with a **Finish setting up Theatarr: 0 of 6** card. **Continue
setup** opens **Setup** (`/admin/setup`), which walks through the steps below and ends with the preflight. Set your
own admin password there first.

## 3. Follow Setup, then edit Settings forms

Open **Setup** (`/admin/setup`). Its six steps: secure the admin account, connect downloads, add metadata (TMDb),
connect the library, check readiness, then review activation. A fresh install needs a writable data directory, nothing else. Add the debrid mount,
writable library roots and trash before checking readiness or importing. Saving the mount and the trash each asks
you to confirm what the change enables. Steps 4–7 below fill in the connections
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
until you say so: the first-library flow turns on **Allow downloads for this library** and the global switches only
when you choose **Create library and start downloading** and confirm the list it shows (step 6).

**TMDb.** Setup's **Add metadata (TMDb)** step has one field: your TMDb API key (the short v3 "API Key" from your
TMDb account's API settings, not the long "API Read Access Token"). Saving it creates the two sources below with
TMDb's public addresses and stores the key, after you confirm the two hosts. The step also counts as done when the
`tmdb` source reads its key from an environment variable (`api_key_env`).

By hand, on **Metadata & artwork**: metadata sources are a list of named sources, and the server has no default
address. Add a source named exactly `tmdb` with the source address `https://api.themoviedb.org/3` and your TMDb API
key. Add a second named `tmdb-images` with the address `https://image.tmdb.org/t/p` and no key: it serves posters and
backdrops through Theatarr's image cache, and without it the UI shows generated art only. For TVDB, a source named
`tvdb` with the address `https://api4.thetvdb.com/v4` and your TVDB key (a subscriber key also needs the PIN in
`THEATARR_TVDB_PIN`). [`examples/policy/metadata.yaml`](../examples/policy/metadata.yaml) has the same sources.

**Optional import from YAML.** Startup does not read a policy directory. To seed a new database from YAML files,
stop the service, run `theatarr policy validate DIR`, then `theatarr settings import -policy DIR` to review the
preview, and repeat with `-confirm` to save it. Importing never turns on the global switches. A running install
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

Both are first-class, and they mix. **Settings › Indexers › Add indexer** has three tabs: Catalog, Torznab & built-in
sources, and Migrate from Prowlarr. It opens on Catalog, or on Torznab & built-in sources while the catalog is off.

- **Keep Prowlarr.** Add each Prowlarr indexer's Torznab feed (`http://prowlarr:9696/<id>/api` with Prowlarr's API
  key) as a Torznab indexer (**Torznab & built-in sources**, then **Torznab / Newznab**). Or use **Migrate from Prowlarr** once: it proposes one native
  indexer per Prowlarr indexer, using the catalog definition where one exists and the Torznab feed where none does.
  Jackett feeds work the same way.
- **Native catalog.** Pick indexers from the Cardigann definitions (the catalog Prowlarr uses). Turn the catalog on
  under **Settings › Indexers › Catalog** ([cardigann.md (internals)](design/cardigann.md)). Sites behind a Cloudflare check
  need a FlareSolverr-compatible service, set in the same catalog settings. The built-in sources (DMM, Torrentio, Zilean,
  SeaDex) need no catalog, and none of them is required: use the ones you want.

A new indexer is off until you turn it on. Each form tests the values before it saves them (a probe query through
the configured VPN check), and a passed test offers to enable the indexer in the same save:

- **Torznab & built-in sources** (and a disabled indexer you edit later): **Test these values**, then **Enable and save**
  beside the result. **Save** alone stores the current **Enabled** value.
- **Catalog**: **Test search**, then **Add and enable**. **Add (disabled)** is always there, and is the only
  button until a test has passed.
- **Migrate from Prowlarr** adds its indexers disabled; open each one under **Settings › Indexers**, test it and
  enable it there.

A new indexer is in no library yet: tick it in the libraries that should search it (**Settings ›
Libraries**, or Setup's library step). Catalog and Prowlarr-migration additions get a request budget of their own;
built-in sources get none automatically (assign one if you want it). Setup counts an indexer as done once it is enabled; a Torznab
or DMM indexer also needs its API key, while Torrentio, Zilean, SeaDex and catalog sites need none. Setup's steps,
its **Off until set up** list and `golive check`'s SetupCheck all apply this rule.

A built-in source needs its **Service URL**. The form for a new Torrentio or SeaDex indexer starts with the public
address filled in (change it if you use another instance); DMM and Zilean start empty. Nothing is stored until you
save, and the server has no default of its own. The public services are at:

| Source | Service URL |
|---|---|
| Torrentio | `https://torrentio.strem.fun` (prefilled) |
| DMM | `https://debridmediamanager.com/api/torznab/rd/cached` (DMM's Torznab feed: `/api/torznab/cached`, or `rd`, `ad` or `tb` in the path for one provider's cache), with your DMM API key |
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
   uid 65532 (step 1). **Check root** shows where it resolves and refuses a root that is missing, inside the mount
   or outside the library area. The first library's root sets that area (its parent directory) unless
   **File management › Broken link checks › Folders to check** names it;
2. a quality profile: a TRaSH default builds a new profile with its formats, or pick an existing profile. Every
   TRaSH quality profile is offered, here and in the library editor under **Settings › Libraries**;
3. the indexers and providers;
4. one of two buttons on a fresh install:
   - **Create library and start downloading** creates the library **Active** with **Allow downloads for this
     library** on, and turns on the global switches **Allow new downloads** and **Write library links**, in one
     save. A dialog lists each of these first; nothing is saved until you confirm. The button is disabled, with the
     reason, until the library picks at least one enabled indexer and one enabled debrid provider with its key, and
     the debrid mount and the trash folder (**Settings › Libraries › File management**) are set. If
     `THEATARR_FORCE_OFF` names a switch, the library is still created and Setup says which switch stays forced off.
   - **Create in shadow (decide later)**, then **Create library**: the library starts in **Shadow** mode with
     **Allow downloads for this library** off, and no global switch changes.

   An install that migrates from Sonarr/Radarr (its settings hold an arr instance, `import.arr`) has no start
   button: there it is **Review**, then **Create library**, always in shadow, and the library goes live as
   [migration.md](migration.md#3-a-follower-library-beside-the-arr) describes.

A library in **Shadow** mode with **Allow downloads for this library** off reads **Preview only**. It decides what
it would grab and records that in the database for comparison. It adds nothing to debrid and writes nothing to the
library. A debrid library needs no download client: Theatarr adds to the providers itself. Edit libraries later
under **Settings › Libraries**.

The editor there has two things Setup does not ask for:

- **Name** (optional, at most 40 characters). Theatarr's own pages and the request-ready notifications show it
  in place of the label guessed from the key and profile, such as "4K WEB". Connected apps never see it: the key,
  the root folder and the profile names of the arr-compatible views stay as they are, so renaming a library breaks
  nothing in Seerr or an arr client. A release from before this field cannot load settings that hold a name; clear
  the names before you go back to one.
- **Advanced**, at the end of **Where**: **Write roots**, **Send grabs to**, **Client** and
  **Follows**. A debrid library that links into its own root needs none of them. The section starts open when the
  library already uses one of them.

**Where a shadow library's decisions come from.** The scheduled search for wanted titles runs only for a library
that grabs (Active, with **Allow downloads for this library** and **Allow new downloads** on), so a shadow library
is not searched on a schedule. It gets decisions two ways:

- **RSS.** Every 15 minutes Theatarr reads the RSS feeds of the library's indexers that have one: Torznab feeds
  (Prowlarr, Jackett), DMM's cached feed, SeaDex's recent feed and catalog indexers. Torrentio and Zilean have no
  feed; they answer per title. A standalone shadow library needs at least one indexer with a feed.
- **Comparison with an arr.** A library fed by a Sonarr or Radarr instance (migration) decides each arr grab and
  runs its own search for it.

To see how the library judges releases, start in the UI:

- **Settings › Profiles › Check a release**: paste a release name and pick a title and library. It shows the parsed
  quality, the matched formats, the score, any rejection and whether it would upgrade the file. It searches nothing
  and records nothing.
- The title's **Releases** page (from the title page): for an Active library it can search the indexers now; for a
  shadow library it shows the last stored decision (from RSS or from an arr grab it compared), so a title added a
  moment ago may show nothing yet.

To search a shadow library's indexers for a title right away, run the decision engine from the shell. It prints the
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

A library created with **Create library and start downloading** (step 6) is already live: it is Active, its
downloads are on and so are the two global switches. Watch it as in item 3 below. The rest of this step is for a
library created in shadow.

For a fresh library, read its decisions first: **Activity › Decision journal** (**Open the decision journal** in Setup)
shows what it would have grabbed and why ([journal.md](journal.md)). Then run preflight and the go-live check:

```sh
docker compose exec theatarr theatarr golive check -library movies
```

The check exits 1 when a line fails. On a library that no arr feeds, the shadow **window** and **agreement** lines
read INFO ("no arr feeds movies: nothing to compare, skipped"): there are no arr grabs to compare with, so they do
not fail the check. Read the decision journal instead, and go live deliberately once the decisions look right.
[migration.md](migration.md#4-the-go-live-check) says which settings page each key the check names is on.

1. **Settings › Libraries:** set the library to **Active** and turn **Allow downloads for this library** on,
   reviewing each confirmation.
2. Turn **Write library links** on there, then **Allow new downloads** under **Settings › Download clients**.
   Review the affected libraries, roots and providers. A save applies without a restart.
3. Watch **Activity**, **System › Status** and **System › Errors**, and Plex; check that a cached release imports and
   plays. Run the check again.

Downloads need Active mode, **Allow downloads for this library** and **Allow new downloads**. Library writes need
Active mode and **Write library links**, and stay inside the library's roots and trash. Turning downloads off stops
new adds; ready downloads can still import. Turn library writes off too if imports must stop. Settings changes do
not cancel work already in flight. `THEATARR_FORCE_OFF=router,library,notify` forces the switches off at startup
whatever the saved settings say; it needs a restart and can never turn anything on.

Moving an existing arr library? Use a follower library instead, as [migration.md](migration.md#3-a-follower-library-beside-the-arr) describes: the arr keeps
running while Theatarr builds its own copy beside it.

## 10. A first download

With a live library (Active, **Allow downloads for this library**, **Allow new downloads** and **Write library
links** all on), add a title and watch it arrive:

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

1. **Secure the admin account:** replace the generated password.
2. **Connect downloads:** configure a debrid provider and an indexer; each has its own completion badge.
3. **Add metadata (TMDb):** enter your TMDb API key; Setup creates the `tmdb` and `tmdb-images` sources.
4. **Connect your library:** configure the mount under **Settings › Libraries › File management** and Plex under
   **Settings › Plex & playback**, then create your first library: started at once, or in shadow
   ([above](#6-a-first-library)).
5. **Check readiness:** run preflight and look at **Activity › Decision journal** (**Open the decision journal**).
6. **Review activation:** one card per library (its key, label, root, state, indexers and providers, and **Why?**
   listing every closed gate), then the three global switches. This review is read-only; each item links to its
   editor.

The first four steps are complete with a changed password, both download sources, a `tmdb` source whose key
resolves (stored in settings, or in the environment variable it names), and at least one library with a root and a
mount. Readiness is complete when the latest full preflight for the current settings has no failures
(warnings stay visible). The server remembers the latest run until it restarts, so a page reload keeps the step;
after a restart, or once the settings change, run the preflight again. Activation is complete only when a
library runs as Active with **Allow downloads for this library** and **Allow new downloads** on; otherwise it says
**Not yet**.

Theatarr turns these on for you only through **Create library and start downloading** in step 4, on an install no
arr feeds, after you confirm the list. Setup stays available from Settings after the reminder disappears.

## More

- [migration.md](migration.md): moving an existing Sonarr/Radarr, Prowlarr, Seerr and Recyclarr setup into Theatarr:
  the plan, shadow, follower libraries, the go-live check, client and Plex handover, rollback
- [restore.md](restore.md): backups, restore and upgrades
- [mcp.md](mcp.md): set up and run Theatarr from an MCP client
- [design/settings.md (internals)](design/settings.md): the settings database and API, presets and profiles
- [design/web.md (internals)](design/web.md): accounts, API keys, the native API
- [design/library.md (internals)](design/library.md): how imports link into the roots
