# Moving from Sonarr and Radarr

Coming from the arrs? See the [arr-to-Theatarr page map](arr-map.md) for routes and menu paths.

The one-time [import](#import-from-sonarrradarr) brings a Sonarr or Radarr instance's titles, their monitoring and
the files already on the debrid mount into a library you made in Setup. The [staged path](#who-this-is-for) below,
with Watch-only libraries at the arrs' own folders and Live libraries of Theatarr's own beside them, describes
installs that already run that way.

## Import from Sonarr/Radarr

**Admin › Import from Sonarr/Radarr** (`/admin/arr-import`, linked from **Setup** and **Settings › Libraries**)
brings one instance into one library, once. Code: `internal/arrimport`; API under
`/api/theatarr/v1/arr-import` ([web.md](design/web.md#setup-and-preflight)).

1. **Instance**: Radarr or Sonarr, its address and API key, then **Check**: it reads the version, the quality
   profiles, the root folders and the title count. The key is sent with each request only; Theatarr never stores it
   and the page keeps it in memory only.
2. **Library**: one library of the matching kind (a movie library for Radarr, a series library for Sonarr). It keeps
   its own quality profile and naming; the instance's are not copied. If you want the same names, paste the
   instance's naming formats into the library's naming editor. The library's folder must be separate from the
   instance's folders (an overlap is refused: if Theatarr sees the instance's folder at another path, enter it
   under Folders).
3. **Quality profiles**: the titles that use the ticked profiles come in (at least one). To send titles to different
   libraries, import once per library with its profiles ticked.
4. **Folders**: Theatarr reads the instance's files to see where their links point. When it cannot read a root folder
   at the instance's path, give the path where Theatarr sees it (mount it into Theatarr's container if needed;
   read-only is enough). A folder Theatarr can read can be mapped too, under "Theatarr sees the instance's folder
   somewhere else", for example when Theatarr's own library uses the same path.
5. **Preview** writes nothing and says what Import would add and how many files it can link. **Import** opens once a
   Preview of the same choices has finished.

What is read: `system/status`, `rootfolder`, `qualityprofile`, then `movie` and `moviefile` (Radarr) or `series` and,
per series, its episodes and episode files (Sonarr). Nothing else (no tags, custom formats, naming, history,
blocklist or lists), and nothing is ever written to the instance.

What comes in:

- Each title not in the library is added with the instance's monitoring: a movie's monitored flag and minimum
  availability; a series' monitored flag, seasons, each episode's flag and "monitor new seasons". The import never
  starts a search.
- Files are linked into the library under its own naming only when they are symlinks into the debrid mount. Ordinary
  files (Usenet, local disk) are not linked, nor are links whose file is gone from the mount: their titles come in as
  missing and, if monitored, Theatarr looks for them like any other missing title. The Preview says so before
  anything is written.
- A Sonarr series has its monitoring and files brought over only when its episode list comes from TVDB, so episodes
  match Sonarr's numbering. Without a TVDB key (**Settings › Metadata & artwork**) series are added, but their
  monitoring and files are not; the Preview says so first.
- A library that is Watch only, or without **Write library links**, can be previewed but not imported into.

Running it again adds only titles that are new and links files that are still missing; it never changes a title
already in the library or monitoring you set in Theatarr, with one exception: a series whose episode list the run
switches from TMDb to TVDB numbering takes Sonarr's season and episode monitoring. So after adding a TVDB key,
running it again brings series' monitoring and files over. To keep adding titles that are added to the instance later, add it as a list under
**Collections › Lists** ([arrview.md](design/arrview.md#lists)).

If the instance keeps running and upgrades a file after the import, Theatarr's link to the old file stops working
and Theatarr looks for that title again.

### The periodic import is gone

Theatarr no longer keeps a library in step with a Sonarr/Radarr instance: the `import` settings section,
`theatarr import-arr` and the Instances page are gone. A library an instance filled keeps its titles, monitoring and
links, but nothing new arrives from the instance; use a list sync or the one-time import. A quality profile or
naming taken from an instance (`from_arr`) becomes a stored copy on upgrade, with a message on **System › Errors**
asking you to check it; one that cannot be copied leaves its library Watch only, with a message saying so. Views no
longer answer the old instances' ids (`alias_from`): a client that kept them (Seerr) gets 404 until its Radarr or
Sonarr scan stores Theatarr's ids. Policy files with `import:`, `from_arr` or `alias_from` are refused.

## Who this is for

The staged path below, with Watch-only libraries beside the arrs, is the older one; most installs only need the
import above. It goes from an existing Sonarr/Radarr setup (with Prowlarr, Seerr and Recyclarr) to Theatarr in
stages. The old services are never changed, nothing here turns grabbing on by itself, and every stage can be undone.
Go one library at a time: every step is a setting you change and confirm.

Anyone with a running arr whose libraries should move to Theatarr. A fresh library with no arr behind it does not
need this guide: it only needs the check and the switches ([quickstart.md](quickstart.md#9-go-live-deliberately)), or
starts live from Setup: **Create library and start downloading**
creates the library **Live** with its downloads on and turns **Pause** off (**Allow new downloads** and **Write
library links** on) in one confirmed save ([quickstart.md](quickstart.md#6-a-first-library)).

| Stage | What you do | Where | How to undo |
|---|---|---|---|
| [2. Shadow](#2-shadow-compare-with-the-arr) | Let the migrated library decide without grabbing, and compare its decisions with the arr's grabs | **Advanced › Sonarr/Radarr migration › Comparison**, **Activity › Decision journal** | Nothing to undo: a shadow library adds and links nothing |
| [3. A Live library](#3-a-live-library-beside-the-arr) | Add a Live library with a root and a Plex library of its own, import the arr's titles and files once, run a trial | **Settings › Libraries**, **Settings › Plex & playback**, **Admin › Import from Sonarr/Radarr** | Turn the library's downloads off or set it to Watch only; the arr never stopped ([stage 6](#6-stop-switches-and-rollback)) |
| [5. Hand over](#5-hand-over-clients-and-plex-retire-the-arr) | Move Seerr, the apps and Plex to the new library, then retire the arr | **Settings › Connected apps**, **Users & access**, each client | Restore the client settings and Plex sharing you saved; keep the arr's database and roots through the rollback window |
| [6. Stop and roll back](#6-stop-switches-and-rollback) | Stop one library or everything, without double downloads | **Settings › Libraries**, **Settings › Download clients**, `THEATARR_FORCE_OFF` | Turn the switches back on deliberately, one side at a time, never both as a catch-up |

[Reference](#7-reference) at the end has states and the cutoff, and known limits. Run `theatarr preflight` (or
**Setup**'s checks) before the trial, during it and before the handover.

## 2. Shadow: compare with the arr

A migrated library starts in shadow with its downloads off, and its arr keeps running. The shadow runner decides on
the arr's grabs and on the RSS feeds and records what Theatarr would have done; nothing is added or linked.

Let the library decide in shadow for a while and compare its decisions with the arr's grabs in **Advanced ›
Sonarr/Radarr migration › Comparison** and **Activity › Decision journal** ([journal.md](journal.md)). Explain each
difference before you go on.

Once the new library grabs ([stage 3](#3-a-live-library-beside-the-arr)), compare its decisions with the arr's grabs in the
same places.

## 3. A Live library beside the arr

A library migrated from an arr, **F**, stays Watch only and its arr keeps running. You add a Live library **K** of
the same kind with its own root and its own Plex section, and bring F's titles, monitoring and files into it with the
[one-time import](#import-from-sonarrradarr). New titles reach K through a list sync (the arr as a list under
**Collections › Lists**) or by running the import again. F's roots stay read-only to Theatarr throughout. Going live
means proving K, moving clients and Plex to K, then retiring F's arr. Theatarr never takes over F's folders in place.

Below, `movie-remux` is F and `movie-remux-theatarr` is K. Use your own keys.

### Libraries that followed another

Following (`follows:` on a library, `naming.from_version`, `theatarr library adopt` and `theatarr parallel`) is
gone. A library that followed another keeps its titles, monitoring, links, profile and naming, and stops receiving
titles and file changes from the followed library: new titles arrive through a list sync or the one-time import, and
files the arr upgrades are no longer mirrored. Migration 00121 copies what it inherited to its own rows: the followed
library's naming (templates, empty templates and `from_arr` as they were), and each movie's minimum availability where
the arr's value differs from the library's default (as a choice older than any you made). Policy files must not
contain `follows:` or `from_version:`: the strict reader refuses them. An add that names no library now also adds to
former followers.

### Before the trial

1. **Read the shadow evidence** ([stage 2](#2-shadow-compare-with-the-arr)). Explain each difference between F's
   decisions and the arr's grabs before you go on.
2. **Create K.** In **Settings › Libraries**, add a Live library of F's kind, with a root of its own (for example
   `/media/library/movies-remux`), the profile and naming you want, its indexers and providers, and **Don't download
   for this library** (under **Advanced**) ticked. Check the mount, the trash and the roots.
3. **Give K a Plex library.** Create one Plex library on K's root, shared with no one yet, and set its section for K
   under **Settings › Plex & playback**. Your users keep watching F's sections.
4. **Review legacy queued work.** `theatarr router reconcile` lists queued work left from before. Review it, then
   `theatarr router reconcile -apply` settles the automatic rows; manual rows need a new selection. Work in flight
   is left alone ([router.md (internals)](design/router.md)).
5. **Import F's arr into K** (**Admin › Import from Sonarr/Radarr**), with **Write library links** on, right before
   the trial, then:

   ```sh
   docker compose exec theatarr theatarr preflight
   docker compose exec theatarr theatarr backup now
   ```

   Resolve every FAIL. The import skips missing or off-mount files; it cannot repair them. Keep the backup set.
   Scan K's Plex library once after the import.

### The trial

Write down the settings and the success criteria before you turn anything on.

1. **Settings › Libraries:** K is Live and the only new library whose downloads will be on (**Don't download for
   this library** unticked). F stays Watch only. **Write library links** stays on for K's imports.
2. **Settings › Download clients › Behaviour:** start conservatively. Keep cached-only adds on, keep the wanted loop's
   missing and cutoff-unmet searches off, and use a low searches-per-hour limit. Set the providers' pacing to what
   their API limits allow. Don't cap Real-Debrid or AllDebrid with an uncached-adds-per-hour limit: on those
   providers the cache check is itself an add. Keep **Send notifications** off, and review the request budgets under
   **Advanced › Request limits**.
3. Untick K's **Don't download for this library**, then turn **Allow new downloads** on (or **Pause** off). Review each confirmation's
   effects. Check `theatarr router status` and run `theatarr preflight` again.
4. **Test the stop switch in the first minutes:** tick K's **Don't download for this library** and check that
   no new provider adds start. A request already sent may finish, and ready downloads can still import. Turn it back
   on once the behaviour is clear.
5. Run the trial for about a week. Try at least three different cached releases (a new episode, a season pack, an
   upgrade, or their movie equivalents) and play each in K's Plex library.

**Success criteria**

- Every download imports within the stall timeout plus one poll, or ends with an explained failure; none stays stuck
  adding, added or ready, and the unsettled-adds health check stays PASS.
- No uncached add remains on the provider. Automatic cleanup deletes only Theatarr's own adds, never reused or
  foreign torrents.
- Links land only in K's root, replaced links go to the trash, and Plex shows new files within a minute. Every trial
  release plays.
- No repeat add of the same hash for the same title and library; a release imported before is recognised.
- K's dangling-link count stays at zero, no unexplained errors appear, and the stop-switch test passes.
- After a week, compare K's routes with the arr's grabs ([stage 2](#2-shadow-compare-with-the-arr)). Explain the differences and check that nothing was
  added twice. A good overall percentage does not excuse an unexplained choice.

### What to watch

**First hour:** the container logs, **Activity › Queue**, **System › Status**, **System › Errors** and **Activity ›
Decision journal**. Follow each download from add to poll, import, link and Plex; watch provider refusals and their
reasons, mount and VPN health, and playback.

**First day and week:** cached-only behaviour in **Admin › Torrents**, imports and upgrades confined to K's root and
trash, dangling links, repeated hashes, comparison differences and new errors. Check **System › Backup** for a
complete nightly set. Test requests through K's own view before you switch your users' clients.

## 5. Hand over clients and Plex, retire the arr

For a Watch-only library F, the handover means switching clients and Plex libraries to the Live library K's roots. F's arr roots stay read-only throughout; after clients and Plex move, retire F's arr and roots. Theatarr
never takes over F's folders in place.

### Before retiring the arr

List what still depends on the arr and decide what replaces each piece:

- every client of the arr: Seerr or Overseerr servers (default, 4K, anime), phone apps, Homepage, autobrr and any
  scripts or webhooks;
- the arr's import lists and Connect targets;
- indexers that only Prowlarr reaches, if you plan to retire Prowlarr too;
- usenet downloads, which Theatarr does not handle.

Keep Prowlarr while any library still uses one of its feeds.

### Hand over clients and Plex, then retire F's arr

1. Back up each client's settings and pick a quiet time for requests. Prepare K's views under **Settings ›
   Connected apps** and client API keys under **Users & access**. K's own test view is
   `/v/movie-remux-theatarr/radarr` (`/sonarr` for series).
2. Repoint **Seerr or Overseerr** server by server: pick the view, profile and K's root again. Test an existing request
   and a new one against the intended titles and libraries.
3. Repoint the phone apps, autobrr and Homepage to K's view and a client key. Test browsing, requests, release
   pushes and widgets. A pushed release must name an indexer K lists.
4. Switch your users' Plex access to K's library after checking playback and sharing. Keep F's Plex libraries and
   roots available for a rollback; don't point K at F's folders.
5. When every client has moved, stop new arr searches, drain its queue, disable its download clients and stop the
   arr. Keep its database, configuration and roots through the rollback window.
6. Check that K's monitoring and requests stand on their own. Run `theatarr preflight` and a request and playback test again.

## 6. Stop switches and rollback

**During the trial (the arr never stopped):** tick K's **Don't download for this library** in **Settings ›
Libraries** (under **Advanced**), then turn **Allow new downloads** off if no other library should grab. This stops new adds; a provider request
already sent can finish. Ready downloads can still import while **Write library links** is on. Review them in
**Activity** and remove unwanted queued or ready work there (ending Theatarr's record does not delete the provider
torrent).

The arr keeps doing its job, so there is no arr catch-up search to cause duplicates. To stop all work for K, set it
to Watch only as well: that stops new links too. Existing links stay available in Plex.

**After the handover:** stop K's downloads first, account for every download in flight, and restore the client
settings and Plex sharing you saved. If the arr was stopped, keep its searches off while you compare its catalogue
and files with what K acquired. Add new requests deliberately and reconcile missing content before you allow arr
searches; the arr does not adopt K's roots. Never turn both sides back on at once as a catch-up. Keep the original
roots; don't move K's links into F's root.

**Emergency stop:** turn **Pause** on at the top of any admin page (it turns **Allow new downloads** and **Write
library links** off; when it reads **Custom**, turn the one still on off on its Settings page), and **Send notifications** off on its Settings page; they affect every library. `THEATARR_FORCE_OFF=router,library,notify` forces them off at startup
whatever the saved settings say; it needs a restart and can never turn anything on. A database or image rollback
uses a backup set ([restore.md](restore.md)); it cannot undo provider adds or filesystem changes.

### Stopping or removing a library

With DB settings, edit mode, root and view bindings in **Admin › Settings › Libraries**. Watch only stops new work;
removing a library with links requires the specific confirmation shown there.
Removal only stops management: operational link rows, filesystem links, mount content and trash remain intact.

Set K to `mode: shadow` (Theatarr stops searching, adding and linking for it; its links stay and Plex keeps showing them)
or remove K from `versions` and `library.versions` (its links stay on disk until removed by hand; the Plex sections
can be deleted). Nothing to undo on the arr side.

### Changing a root with links

The library editor refuses to change a root with active links. Keep the old root and library, create a new library
with a separate existing root, configure its naming in the naming editor, then run `theatarr preflight` before
switching Plex and clients. Retire the old library only after
reviewing its removal confirmation; Theatarr never migrates or deletes the old links as part of a settings edit.

## 7. Reference

### States and the cutoff

The two libraries' states use different cutoffs and cannot be compared one to one:
- **The arr-fed library F** takes its state from the arr's `qualityCutoffNotMet` (`arr.import`). That flag checks
  the quality cutoff only (Radarr `MovieFileResource`, Sonarr `EpisodeFileResource`).
- **K's imported links** (the one-time import) are judged as a Theatarr import is (`importedState`, `decide.Engine.FileCutoffNotMet`): the
  quality cutoff and the profile's `cutoff_score`, with K's own profile.
- **What that means:** with a profile whose `cutoff_score` no arr file reaches, K holds every adopted file as
  `filled` while F shows most as `cutoff_met`. Both are right by their own definition.
- **What the wanted loop searches** follows the arrs: with `router.wanted.cutoff_unmet: quality` (the default) it
  searches only files below the quality cutoff, as the arrs' Cutoff Unmet search does (docs/design/router.md "The
  wanted loop"). Files at the cutoff quality but below the score are upgraded only when a feed or another search
  brings a better-scored release, as the arrs' RSS sync does. Adopted links need no repair.

### Known limits

- Imported links keep neither the arr's score nor its custom format list. That changes no decision: the cutoff and
  every upgrade comparison recalculate them from the file's inputs.
