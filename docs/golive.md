# Going live

How a library starts grabbing, and how an existing arr library is handed over to Theatarr without a gap and without
double downloads. A fresh library with no arr behind it only needs the check and the switches
([quickstart.md](quickstart.md#9-go-live-deliberately)); the rest of this page is about libraries that come from an
arr.

Go live one library at a time. Nothing here changes on its own: every step is a setting you change and confirm.

## The follower model

A library migrated from an arr, **F**, stays in shadow and its arr keeps running. You add a **follower** library
**K** (`follows: F`) with its own roots and its own Plex section. K takes F's titles and monitoring from the arr,
starts with links to the arr's current files, and then grabs for itself. F's roots stay read-only to Theatarr
throughout. Going live means proving K, moving clients and Plex to K, then retiring F's arr. Theatarr never takes
over F's folders in place. [parallel.md](parallel.md) describes adoption, refresh and what K inherits.

Below, `movie-remux` is F and `movie-remux-theatarr` is K. Use your own keys.

## Before the trial

1. **Read the shadow evidence.** Let F decide in shadow for a while and compare its decisions with the arr's grabs
   in **Advanced › Sonarr/Radarr migration › Comparison** and **Advanced › Decision journal**. Explain each
   difference before you go on. The go-live check's default bar is 95% agreement over 14 days, with at least 20
   arr grabs.
2. **Create K.** In **Settings › Libraries**, add a library that follows F, with a root of its own (for example
   `/media/library/movies-remux`), the profile and naming you want, its indexers and providers. Keep every other
   follower's **Allow downloads for this library** off. Check the mount, the trash and the roots.
3. **Give K a Plex library.** Create one Plex library on K's root, shared with no one yet, and set its section for K
   under **Settings › Plex & playback**. Your users keep watching F's sections.
4. **Review legacy queued work.** `theatarr router reconcile` lists queued work left from before. Review it, then
   `theatarr router reconcile -apply` settles the automatic rows; manual rows need a new selection. Work in flight
   is left alone ([router.md](design/router.md)).
5. **Adopt and refresh K**, with K active, downloads off and **Write library links** on, right before the trial:

   ```sh
   docker compose exec theatarr theatarr library adopt -version movie-remux-theatarr
   docker compose exec theatarr theatarr library adopt -version movie-remux-theatarr -refresh
   docker compose exec theatarr theatarr golive check -version movie-remux-theatarr
   docker compose exec theatarr theatarr backup now
   ```

   Resolve every FAIL and explain the coverage, dangling-link and agreement WARNs. Adoption skips missing or
   off-mount source files; it cannot repair them. Once K grabs, adoption and refresh stop for K, and new gaps are
   left to RSS or a manual search. Keep the backup set.

## The trial

Write down the settings and the success criteria before you turn anything on.

1. **Settings › Libraries:** K is Active and is the only follower whose **Allow downloads for this library** will be
   on. F stays in shadow. **Write library links** stays on for K's imports.
2. **Settings › Download clients › Behaviour:** start conservatively. Keep cached-only adds on, keep the wanted loop's
   missing and cutoff-unmet searches off, and use a low searches-per-hour limit. Set the providers' pacing to what
   their API limits allow. Don't cap Real-Debrid or AllDebrid with an uncached-adds-per-hour limit: on those
   providers the cache check is itself an add. Keep **Send notifications** off, and review the request budgets under
   **Advanced › Request limits**.
3. Turn K's **Allow downloads for this library** on, then **Allow new downloads**. Review each confirmation's
   effects. Check `theatarr router status` and run the go-live check again.
4. **Test the stop switch in the first minutes:** turn K's **Allow downloads for this library** off and check that
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
- After a week, compare K's routes with the arr's grabs (below). Explain the differences and check that nothing was
  added twice. A good overall percentage does not excuse an unexplained choice.

## `theatarr golive check`

```sh
docker compose exec theatarr theatarr golive check
docker compose exec theatarr theatarr golive check -version movie-remux-theatarr
docker compose exec theatarr theatarr golive check -version movie-remux-theatarr -days 7 -json
```

The check reports PASS, INFO, WARN or FAIL with a fix for each, and exits 1 on a failure. It only reads; it never
activates a library. Without `-version` it checks every library; a followed library reports that it is cut over
through its follower.

It checks the profile build, the mode and the download and global switches, provider order and credentials, indexer
budgets, the mount, writable roots and trash, the Plex section, metadata, health and the backlog. A followed
library's roots are read-only by design: the check makes sure they exist and are readable, not that Theatarr can
write there. A configured VPN check must pass for its required traffic; without one the check warns. Credentials
are named by variable or settings path, never shown.

For a follower, a parallel section replaces the ordinary shadow section: it names F and its arr, checks that the
roots are separate, the adoption coverage (every episode of a pack counts), whether adopt and refresh are fresh, and
the agreement with the arr. The arr still grabbing is INFO: expected during the trial. The follower sample counts
the arr's grabs: same-hash matches, different-hash matches and arr-only rows. A PASS does not replace the playback
and stop-switch evidence.

A library with no arr to compare with (a fresh install, no Sonarr/Radarr migration) always gets a FAIL on
`agreement`: "no import.arr instance feeds K". That is by design: there is no evidence that its decisions match
anything. Read its decisions in the journal instead, and accept going live without that evidence; the exit code stays
1 while the FAIL is there.

A new library can also get a FAIL on `window`: "the shadow runner has not run". The shadow runner records a run each
time it reads the RSS feeds (every 15 minutes, starting a few minutes after `serve` starts) or imports an arr's grabs,
and no run is recorded yet. The RSS job runs only while a shadow library has an indexer with a feed (see
[quickstart.md](quickstart.md#6-a-first-library)), so check that first. Leave `serve` running and run the check again
later: the FAIL turns into a WARN that counts the days the runner has run, until the window (`-days`) is covered.

The check names settings by their YAML keys. Where each one is in the UI:

| Key | Settings page |
|---|---|
| `versions.K.*` (`grab`, `library`, `providers`, ...) | Settings › Libraries, the library K |
| `library.versions.K` roots | Settings › Libraries, the library's write roots |
| `library.versions.K.plex_section`, `library.plex` | Settings › Plex & playback (the Plex library for each Theatarr library) |
| `library.mount.path`, `library.trash.dir` | Settings › Libraries › File management |
| `metadata.sources.tmdb`, `metadata.sources.tvdb` | Settings › Metadata & artwork |
| `router.enabled` (**Allow new downloads**) | Settings › Download clients, the switch above the tabs |
| `router.cached_only` | Settings › Download clients › Behaviour (What to grab) |
| `router.wanted.enabled` | Settings › Download clients › Behaviour (Automatic searches) |
| `library.enabled` (**Write library links**) | Settings › Libraries |
| `import.arr` | Admin › Advanced › Sonarr/Radarr migration (the Sonarr and Radarr instances) |
| `egress` | Settings › Indexers › VPN |
| `views` (the views section) | Settings › Connected apps |

`-provider-expiry key=YYYY-MM-DD` warns when a provider plan ends within 30 days, and `-public-url` (or
`THEATARR_PUBLIC_URL`) puts Theatarr's address into the client steps it prints.

## Comparing with the arr

```sh
docker compose exec theatarr theatarr parallel compare -version movie-remux-theatarr -since 7d
docker compose exec theatarr theatarr parallel compare -version movie-remux-theatarr -since 7d -json
```

This reads each arr feeding F and compares its recorded grabs with K's routes, without changing who owns the
imports. It reports same hashes, different releases with quality and score, unmatched choices and routes already
covered by an arr file. `golive check` uses the same measurement. Keep F's arr running during the trial: after it is
retired there are no new arr grabs to compare. The arr and K can pick the same release while they run side by side;
that is not K adding content twice.

## What to watch

**First hour:** the container logs, **Activity › Queue**, **System › Status**, **System › Errors** and **Advanced ›
Decision journal**. Follow each download from add to poll, import, link and Plex; watch provider refusals and their
reasons, mount and VPN health, and playback.

**First day and week:** cached-only behaviour in **Admin › Torrents**, imports and upgrades confined to K's root and
trash, dangling links, repeated hashes, comparison differences and new errors. Check **System › Backup** for a
complete nightly set. Test requests through K's own view before you switch your users' clients.

## Before retiring the arr

List what still depends on the arr and decide what replaces each piece:

- every client of the arr: Seerr or Overseerr servers (default, 4K, anime), phone apps, Homepage, autobrr and any
  scripts or webhooks;
- the arr's import lists and Connect targets (`theatarr migrate inspect` lists them);
- indexers that only Prowlarr reaches, if you plan to retire Prowlarr too;
- usenet downloads, which Theatarr does not handle.

Keep Prowlarr while any library still uses one of its feeds.

## Hand over clients and Plex, then retire F's arr

1. Back up each client's settings and pick a quiet time for requests. Prepare K's views under **Settings ›
   Connected apps** and client API keys under **Users & access**. K's own test view is
   `/v/movie-remux-theatarr/radarr` (`/sonarr` for series).
2. Repoint **Seerr or Overseerr** server by server: pick the view, profile and K's root again. Check the stored ids
   first with `theatarr migrate seerr-ids` ([migration.md](migration.md#seerr-ids)), then test an existing request
   and a new one against the intended titles and libraries.
3. Repoint the phone apps, autobrr and Homepage to K's view and a client key. Test browsing, requests, release
   pushes and widgets. A pushed release must name an indexer K lists. After checking Homepage, add `homepage` to
   K's `golive_done` list (an acknowledgement for the go-live check, not an automatic test).
4. Switch your users' Plex access to K's library after checking playback and sharing. Keep F's Plex libraries and
   roots available for a rollback; don't point K at F's folders.
5. When every client has moved, stop new arr searches, drain its queue, disable its download clients and stop the
   arr. Keep its database, configuration and roots through the rollback window.
6. End K's mirroring deliberately: set K's naming explicitly before you remove the inherited naming, review the
   confirmed removal of `follows` in **Settings › Libraries**, and remove the arr import entry under **Advanced ›
   Sonarr/Radarr migration**. Check that K's monitoring and requests now stand on their own. Run the go-live check
   and a request and playback test again.

## Stop switches and rollback without double downloads

**During the trial (the arr never stopped):** turn K's **Allow downloads for this library** off in **Settings ›
Libraries**, then **Allow new downloads** off if no other library should grab. This stops new adds; a provider request
already sent can finish. Ready downloads can still import while **Write library links** is on. Review them in
**Activity** and remove unwanted queued or ready work there (ending Theatarr's record does not delete the provider
torrent).

With K still active and following F, turning its downloads off resumes adoption and refresh at later arr imports;
refresh never replaces K's own imported links. The arr keeps doing its job, so there is no arr catch-up search to
cause duplicates. To stop all work for K, set it to shadow as well: that stops new links and adoption too. Existing
links stay available in Plex.

**After the handover:** stop K's downloads first, account for every download in flight, and restore the client
settings and Plex sharing you saved. If the arr was stopped, keep its searches off while you compare its catalogue
and files with what K acquired. Add new requests deliberately and reconcile missing content before you allow arr
searches; the arr does not adopt K's roots. Never turn both sides back on at once as a catch-up. Keep the original
roots; don't move K's links into F's root.

**Emergency stop:** turn **Allow new downloads**, **Write library links** and **Send notifications** off on their
Settings pages; they affect every library. `THEATARR_FORCE_OFF=router,library,notify` forces them off at startup
whatever the saved settings say; it needs a restart and can never turn anything on. A database or image rollback
uses a backup set ([restore.md](restore.md)); it cannot undo provider adds or filesystem changes.

## Upgrading the image

1. Write a backup set while the old image still runs, and note its name and the old image tag:

   ```sh
   docker compose exec theatarr theatarr backup now
   docker compose exec theatarr theatarr backup list
   ```

   Keep that set and the old image for the rollback window.
2. Rebuild and restart with the new source (`git pull`, then `docker compose up -d --build`), keeping the data
   volume and binds. Wait until the container is healthy (`/readyz`), then check:

   ```sh
   docker compose exec theatarr theatarr migrate status
   docker compose exec theatarr theatarr errors list -status open
   docker compose exec theatarr theatarr golive check
   ```

   Read the startup logs and **System › Status**. A ready image is no reason to turn grabbing on.
3. To roll back, follow [restore.md](restore.md#upgrades-and-image-rollback): restore the pre-upgrade set with the
   old image, then start the old image with downloads forced off until you have reconciled.

Never run an older image against a newer database: startup and restore refuse that downgrade, and a backup written
after the upgrade may use a format the old image cannot read. Restoring the old set loses the records and settings
written since; debrid content and library links stay and need the reconciliation in
[restore.md](restore.md#restore-from-the-command-line).
