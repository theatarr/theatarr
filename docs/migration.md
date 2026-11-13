# Moving from Sonarr and Radarr

Coming from the arrs? See the [arr-to-Theatarr page map](arr-map.md) for routes and menu paths.

Moving to Theatarr means one thing now: the one-time [import](#import-from-sonarrradarr) brings a Sonarr or Radarr
instance's titles, their monitoring and the files already on the debrid mount into a Live library you made in Setup.
New titles then arrive through a list sync or by running the import again. [Moving off the arr](#moving-off-the-arr)
below is the order to do it in, and [Upgrading to this release](#upgrading-to-this-release) says what an install
that used the older tools (following, the comparison, the periodic import) sees after the upgrade.

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
- A library that is Watch only, or while **Pause** is on, can be previewed but not imported into.

Running it again adds only titles that are new and links files that are still missing; it never changes a title
already in the library or monitoring you set in Theatarr, with one exception: a series whose episode list the run
switches from TMDb to TVDB numbering takes Sonarr's season and episode monitoring. So after adding a TVDB key,
running it again brings series' monitoring and files over. To keep adding titles that are added to the instance
later, add it as a list under **Collections › Lists** ([arrview.md](design/arrview.md#lists)).

If the instance keeps running and upgrades a file after the import, Theatarr's link to the old file stops working
and Theatarr looks for that title again.

## Moving off the arr

Go one library at a time. The arr is never changed by Theatarr and keeps running until you retire it, so every step
can be undone.

### Before the trial

1. **Create the library.** In **Settings › Libraries**, add a library of the arr's kind with a root of its own (for
   example `/media/library/movies`), the profile and naming you want, its indexers and providers. Keep it **Watch
   only** for now. Check the mount, the trash and the roots.
2. **Give it a Plex library.** Create one Plex library on its root, shared with no one yet, and set its section
   under **Settings › Plex & playback**. Your users keep watching the arr's sections.
3. **Check how it judges releases.** A Watch-only library searches nothing on its own. **Settings › Profiles › Check
   a release** judges one release name for a title and library, and `theatarr decide -search` searches its indexers
   for one title and prints the ranked decisions ([quickstart.md](quickstart.md#6-a-first-library)); neither records
   anything.
4. **Review legacy queued work.** `theatarr router reconcile` lists queued work left from before. Review it, then
   `theatarr router reconcile -apply` settles the automatic rows; manual rows need a new selection. Work in flight
   is left alone ([router.md (internals)](design/router.md)).
5. **Start conservatively.** Write down the settings and the success criteria below before you turn anything on.
   In **Settings › Download clients › Behaviour**, keep cached-only adds on, keep the wanted loop's missing and
   cutoff-unmet searches off, and use a low searches-per-hour limit. Set the providers' pacing to what their API
   limits allow. Don't cap Real-Debrid or AllDebrid with an uncached-adds-per-hour limit: on those providers the
   cache check is itself an add. Keep **Send notifications** (**Settings › Notifications**) off, and review the
   request budgets under **Advanced › Request limits**.
6. **Set the library Live and turn Pause off.** Set it **Live** in **Settings › Libraries**, then turn **Pause** off
   at the top of any admin page. Pause is one switch for every library: its confirmation names all the Live
   libraries that start searching and downloading, so review that list. The import needs both: while the library is
   Watch only or Pause is on, Preview says so and Import is refused.
7. **Import** (**Admin › Import from Sonarr/Radarr**), then:

   ```sh
   docker compose exec theatarr theatarr preflight
   docker compose exec theatarr theatarr backup now
   ```

   Resolve every FAIL. The import skips missing or off-mount files; it cannot repair them. Keep the backup set.
   Scan the new Plex library once after the import.

### The trial

1. Check `theatarr router status`.
2. **Test Pause in the first minutes:** turn it on and check that no new provider adds start. A request already sent
   may finish; downloads already at a provider are imported when Pause is turned off again. Turn it off again.
3. Run the trial for about a week. Try at least three different cached releases (a new episode, a season pack, an
   upgrade, or their movie equivalents) and play each in the new Plex library.

**Success criteria**

- Every download imports within the stall timeout plus one poll, or ends with an explained failure; none stays stuck
  adding, added or ready, and the unsettled-adds health check stays PASS.
- No uncached add remains on the provider. Automatic cleanup deletes only Theatarr's own adds, never reused or
  foreign torrents.
- Links land only in the library's root, replaced links go to the trash, and Plex shows new files within a minute.
  Every trial release plays.
- No repeat add of the same hash for the same title and library; a release imported before is recognised.
- The dangling-link count stays at zero, no unexplained errors appear, and the Pause test passes.

### What to watch

**First hour:** the container logs, **Activity › Queue**, **System › Status**, **System › Errors** and **Activity ›
Decision journal**. Follow each download from add to poll, import, link and Plex; watch provider refusals and their
reasons, mount and VPN health, and playback.

**First day and week:** cached-only behaviour in **Admin › Torrents**, imports and upgrades confined to the library's
root and trash, dangling links, repeated hashes and new errors. Check **System › Backup** for a complete nightly set.
Test requests through the library's own view before you switch your users' clients.

### Hand over clients and Plex, then retire the arr

List what still depends on the arr and decide what replaces each piece:

- every client of the arr: Seerr or Overseerr servers (default, 4K, anime), phone apps, Homepage, autobrr and any
  scripts or webhooks;
- the arr's import lists and Connect targets;
- indexers that only Prowlarr reaches, if you plan to retire Prowlarr too;
- usenet downloads, which Theatarr does not handle.

Keep Prowlarr while any library still uses one of its feeds. Then, for a library `movies` (use your own key):

1. Back up each client's settings and pick a quiet time for requests. Prepare the views under **Settings ›
   Connected apps** and client API keys under **Users & access**. The library's own test view is
   `/v/movies/radarr` (`/sonarr` for series).
2. Repoint **Seerr or Overseerr** server by server: pick the view, profile and root again, and let Seerr scan the
   view so it stores Theatarr's ids. Test an existing request and a new one.
3. Repoint the phone apps, autobrr and Homepage to the view and a client key. Test browsing, requests, release
   pushes and widgets. A pushed release must name an indexer the library lists.
4. Switch your users' Plex access to the new library after checking playback and sharing. Keep the arr's Plex
   libraries and roots available for a rollback; don't point Theatarr at the arr's folders.
5. When every client has moved, stop new arr searches, drain its queue, disable its download clients and stop the
   arr. Keep its database, configuration and roots through the rollback window.
6. Check that the library's monitoring and requests stand on their own. Run `theatarr preflight` and a request and
   playback test again.

### Stopping and rolling back

**During the trial (the arr never stopped):** set the library to **Watch only** in **Settings › Libraries**, or turn
**Pause** on to stop every library. A provider request already sent can finish. Review queued or ready work in
**Activity** and remove what you don't want there (ending Theatarr's record does not delete the provider torrent).
Existing links stay available in Plex. The arr kept doing its job, so there is no arr catch-up search to cause
duplicates.

**After the handover:** turn Pause on first, account for every download in flight, and restore the client settings
and Plex sharing you saved. If the arr was stopped, keep its searches off while you compare its catalogue and files
with what Theatarr acquired. Add new requests deliberately and reconcile missing content before you allow arr
searches; the arr does not adopt Theatarr's roots. Never turn both sides back on at once as a catch-up.

**Emergency stop:** turn **Pause** on at the top of any admin page, and **Send notifications** off on **Settings ›
Notifications**. `THEATARR_FORCE_OFF=pause,notify` forces both at startup whatever the saved settings say; it needs a
restart and can never turn anything on. A database or image rollback uses a backup set ([restore.md](restore.md)); it
cannot undo provider adds or filesystem changes.

### Stopping or removing a library

With DB settings, edit mode, root and view bindings in **Admin › Settings › Libraries**. Watch only stops new work;
**Stop managing library** removes the library after a confirmation that names what it leaves behind.

A Watch-only library keeps its links, and Plex keeps showing them. A removed library's entries on every title go
(versions, monitoring, wanted state, downloads, the link rows it alone holds, its own view rows), while its symlinks
stay on disk, untracked, until removed by hand, and downloads at the provider stay there; its Plex sections can be
deleted.

### Changing a root with links

The library editor refuses to change a root with active links. Keep the old root and library, create a new library
with a separate existing root, configure its naming in the naming editor, then run `theatarr preflight` before
switching Plex and clients. Retire the old library only after
reviewing its removal confirmation; Theatarr never migrates or deletes the old links as part of a settings edit.

### States and the cutoff

Imported links are judged as a Theatarr import is (`importedState`, `decide.Engine.FileCutoffNotMet`): the quality
cutoff and the profile's `cutoff_score`, with the library's own profile. With a profile whose `cutoff_score` no arr
file reaches, the library holds every imported file as `filled` where the arr showed `cutoff_met`; both are right by
their own definition. With `router.wanted.cutoff_unmet: quality` (the default) the wanted loop searches only files
below the quality cutoff, as the arrs' Cutoff Unmet search does ([router.md](design/router.md), "The wanted loop").
Files at the cutoff quality but below the score are upgraded only when a feed or another search brings a
better-scored release, as the arrs' RSS sync does.

Imported links keep neither the arr's score nor its custom format list. That changes no decision: the cutoff and
every upgrade comparison recalculate them from the file's inputs.

## Upgrading to this release

The database migrations run at start; nothing needs doing first. What an install that used the older tools sees:

- **Libraries that followed another** (`follows:`) are ordinary libraries now. Each keeps its titles, monitoring,
  links, profile and naming; migration 00121 copies the naming it inherited and each movie's minimum availability
  where it differed from the library's default. They no longer receive titles or file changes from the library they
  followed: new titles arrive through a list sync or the one-time import, and files the arr upgrades are no longer
  mirrored. An add that names no library now also adds to former followers.
- **Watch-only libraries that copied a Sonarr/Radarr instance** (through the old periodic import) stay, with their
  titles, as ordinary Watch-only libraries. They are no longer updated. Each gets one **System › Errors** card:
  "Library K copied a Sonarr/Radarr instance. That copy is no longer updated. Delete the library when you no longer
  need it." Remove it with **Stop managing library** when you no longer need it; that also resolves the card.
- **A quality profile or naming taken from an instance** (`from_arr`) becomes a stored copy of what the library used,
  keeping its key. Each library concerned gets one **System › Errors** message asking you to check the copy in
  **Settings › Profiles** or **Settings › Libraries**. If no copy could be made (no stored snapshot of the instance),
  that library alone is set Watch only, with a message saying so; check its profile and naming, then set it Live.
- **Old arr ids in the views no longer answer** (`alias_from` is gone). A client that kept a title's Radarr or Sonarr
  id from before (Seerr, for example) gets 404 for it until its next scan of the view stores Theatarr's ids. Point
  the client at the view and let it sync.
- **One Pause.** Pause is on after the upgrade if the router or the library switch was off. A Live library whose
  downloads were off is now Watch only. **Send notifications** is a setting on **Settings › Notifications**, kept as it
  was.
- **One parser.** Every library uses the same release parser and pack rule. Picks can differ from before on installs
  that used the old defaults.

### Removed in this release

- **Commands:** `theatarr golive`, `theatarr parallel`, `theatarr import-arr`, `theatarr shadow` (`run-once`,
  `status`, `reclassify`), `theatarr decide shadow`, `theatarr library adopt` and `library naming-sync`, and the guided
  move in `theatarr migrate` (`inspect`, `plan`, `apply`, `verify`, `seerr-ids`); `theatarr migrate` keeps `up` and
  `status`. `theatarr sonarr-parse` lost `-sonarr`.
- **Pages:** **Advanced › Sonarr/Radarr migration** (the comparison, the arr imports and the guided migration).
- **Settings and policy fields:** the `import` section (`import.arr`, the periodic arr import), `from_arr` on
  profiles and naming, `alias_from` on views, `follows` on libraries and `naming.from_version`, `grab` on libraries,
  the separate router, library and notification switches (`router_enabled`, `library_enabled`, `notify_enabled`; use
  `paused`), the parser and pack fields (`sonarr`, `packs`, `packs_threshold`), and the comparison fields of the
  `shadow` section (`import_interval`, `search`, `searches_per_hour`, `search_reuse`, `keep_results`,
  `link_window`; `rss` and `rss_interval` stay). Policy files that still contain them are refused.
- **The comparison data** (arr grabs decided again, the comparison report and its journal entries) is dropped.
- **SeaDex quality reading:** a SeaDex file name with WEB and EAC3 and no other source tag now reads as WEBDL (it was
  Unknown). With **Prefer SeaDex** and a profile that allows WEB-1080p, a SeaDex-listed 1080p WEB entry that used to
  wait as "quality pending" can now be picked ahead of a Blu-ray alternative.
- **Already-imported guard:** with the arr history gone, a release an arr imported whose name overstated its quality
  can be grabbed again until a matching Theatarr import exists (tracked per library and episode, not once per title).
