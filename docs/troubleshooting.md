# Troubleshooting

The places new installs most often get stuck, and what to do about each. For anything else, start with
**Admin › System › Status**, **System › Errors** and **System › Logs**, and run the preflight
(**Setup › Run preflight**, or `docker compose exec theatarr theatarr preflight`): each line says why it failed and
what to do next.

<a id="data-directory"></a>

## The container keeps restarting

**Symptom:** `docker compose ps` shows Theatarr restarting, and `docker compose logs theatarr` repeats one line:

```text
theatarr serve: data directory /data is not writable by uid 65532:65532 (owner 0:0, mode 0755): open /data/.write-test-123: permission denied. Fix on the host: chown -R 65532:65532 the directory mounted at /data, or run the container as a user that owns it. See docs/troubleshooting.md#data-directory
```

`serve` checks the data directory (the database's directory) before it writes anything and exits with status 1.

**Cause:** the image runs as uid:gid `65532:65532`. If `./data` did not exist before the first `docker compose up`,
Docker created it owned by root, and Theatarr cannot write its database there.

**Fix:** stop the container, give the directory to that user, and start again:

```sh
docker compose down
sudo chown -R 65532:65532 data
docker compose up -d
```

The library roots and the trash directory need the same owner. The debrid mount only needs to be readable. See
[quickstart.md](quickstart.md#1-run-the-container).

## Search says TMDb is off, or posters are missing

**Symptom:** search shows "TMDb search is off", you cannot add titles, or every title shows generated art.

**Cause:** the server has no default metadata address. Theatarr looks for sources with these exact names.

**Fix:** enter your TMDb API key in **Setup › Add metadata (TMDb)**: it creates `tmdb` and `tmdb-images` with the
addresses below. Or add them by hand under **Settings › Metadata & artwork**:

| Name | Source address | Key |
|---|---|---|
| `tmdb` | `https://api.themoviedb.org/3` | your TMDb API key |
| `tmdb-images` | `https://image.tmdb.org/t/p` | none |
| `tvdb` (optional) | `https://api4.thetvdb.com/v4` | your TVDB key |

The names must match exactly. Save; no restart is needed.

## There is no profile to pick for a library

**Symptom:** a profile picker lists no TRaSH default, or the presets say they are unavailable.

**Cause:** the TRaSH quality profiles are built in (a snapshot bundled with each release), so this only happens
when `THEATARR_TRASH` names a TRaSH-Guides checkout that loads for one arr flavour and not the other: a checkout that
loads is used alone. A `THEATARR_TRASH` directory with no checkout in it does not turn the defaults off; the bundled
ones are used and **Settings › Profiles** says so.

**Fix:** complete the checkout (`git -C trash-guides pull`, or clone it again), or remove `THEATARR_TRASH` and its
mount to use the bundled defaults, then restart. A profile can also be made by hand under **Settings › Profiles** in
the raw profile editor (`/admin/settings/profiles?raw=1`, **Add profiles**).

## A library exists but nothing happens

**Symptom:** you created a library and added titles, but nothing is downloaded and nothing appears in the library
folder.

**Cause:** a new library starts as **Watch only**: Theatarr searches, downloads and links nothing for it. Or
**Pause** is on (a fresh install starts paused).

**Fix:**

- To see how it judges a release, paste a release name into **Settings › Profiles › Check a release**. Decisions it
  has recorded are in **Activity › Decision journal** and on the title's **Last search** page; a title added a
  moment ago may have none yet. To search the indexers for one title now, run
  `docker compose exec theatarr theatarr decide -tmdb <id> -library <library> -search` (it records nothing).
- To start downloading, follow [quickstart.md](quickstart.md#9-go-live-deliberately): set the library to
  **Live** on **Settings › Libraries** and turn **Pause** off at the top of the page. **Wanted** says why each title
  is still waiting.

## A live library downloads nothing

**Symptom:** the library is **Live**, **Pause** is off,
you added a title, and nothing is downloaded or nothing reaches Plex.

Start with **Wanted** (it says what each title is waiting for), the title's **Releases** page (what the indexers
returned and why each release was accepted or rejected) and **System › Status**. The usual causes:

- **The title has not been searched yet.** The add dialog's **Add** does not search; **Add and search**
  does. A title added with **Add**
  waits for the automatic search: a round every 15 minutes, at most 10 searches an hour, and an older item again
  only after 24 hours (**Settings › Download clients › Behaviour**, **Automatic search**). The **Wanted** page's
  **Next search** column says when each item may be searched; **Not scheduled** means **Search for missing items**
  (or **Search for upgrades**) is off, so it is searched only when you ask or after a failed download. **Search
  now** on the title page, or at the end of the title's row in a list's table view, searches it at once. **Search
  now** on the **Wanted** page makes the item due at once;
  searches start as the hourly limit allows, so a long list takes hours. To search many titles of one library, a
  manager or admin can also choose **Select** on **Library › Movies** or **Series**, tick the titles, pick the library in the
  bar (not **Every library**) and choose **Search now**: they are searched the same way under the same limit, a series season by season, and a
  title with nothing missing in that library is skipped. The bar then says how many searches were queued and
  links to **Wanted**.
- **A list added it.** Titles a list sync adds (timed or **Sync now**), and movies a timed collection refresh
  adds, are not searched at once: with the list's **Search on add** they become due on the same automatic search,
  under its hourly limit, and **Wanted** says "Added by a list". **Sync now** is refused while **Pause** is on.
  Adding or refreshing one collection yourself with **Search on add** still searches at once.
- **The movie is not available yet.** A movie is searched automatically only once it has reached its **Minimum
  availability**: Released (the default: a digital or physical release date has passed, or 90 days since the
  cinema date), In cinemas, or Announced. The title page's **Libraries** table shows the value in force
  for each library (the **Minimum availability** column, or the row's **Details**) and lets a manager change it; a movie library can set its own default and an **Availability
  delay** in days (**Settings › Libraries**, **Which releases**). **Search now** on the title page searches
  regardless.
- **An episode aired a few hours ago.** The automatic search usually runs before any release exists. A new
  episode or film that is not found is tried again after 1, 2, 4, 8 and 16 hours, then every **Search older items
  again after** (24 hours by default; no wait is longer than it), until seven days after its release date, within
  the same hourly limit. That happens while **Search for missing items** is on, and also with it off after a
  **Search now** or a failed download that found nothing. The **Wanted** page shows the next try ("New release, try
  3"). There is no separate setting for it.
- **An anime series sits in a library that does not take anime (or the reverse).** The title page says so next
  to **Series type**. Add the series to a fitting library or change its type.
- **No release is cached.** **Only add cached releases** (**Settings › Download clients › Behaviour**, **What to grab**) is on by
  default: a release that no provider has cached is not taken, and the title stays wanted. An uncached add is
  removed again on Real-Debrid (when it is Theatarr's own) and never made on TorBox. AllDebrid is the exception: with the default settings
  the uncached add stays on the account and finishes downloading there while Theatarr tries the next provider
  (**Delete uncached AllDebrid adds** is off, because Theatarr cannot always prove the add is its own). Turn
  **Only add cached releases** off to let the providers download uncached releases.
- **A source is disabled.** A new provider is off until you turn it on: open it, press **Test these values**, and
  choose **Enable and save** when the test passes (**Settings › Download clients**). A new indexer starts on; one you
  turned off, or a tracker moved from Prowlarr that needs a login, is turned on the same way (**Settings ›
  Indexers**). The library must also list the indexer and the provider (**Settings › Libraries**).
- **The provider refuses the key.** A wrong key, or a plan that has ended, fails every add. **Test** on the provider
  shows whether the plan is premium and when it ends; **System › Status** reports an account that is not premium.
- **The debrid mount is empty inside the container.** **System › Status** then says "The mount at /mnt/debrid is
  empty: is it mounted?", and finished downloads cannot be linked. This happens when the mount (zurg, rclone) came
  up or was remounted on the host after the container started: the container keeps the directory it bound at start.
  Restart the container (`docker compose restart theatarr`). On a Linux host, the container can follow a remount
  when you bind the mount's parent directory with `rslave` propagation instead of the mount itself, for example
  `/mnt:/mnt:ro,rslave`, keeping the same path inside the container. This needs the parent to be a shared mount
  on the host, and Docker Desktop does not support it; see
  [Docker's notes on bind propagation](https://docs.docker.com/engine/storage/bind-mounts/#configure-bind-propagation).
- **Plex cannot follow the links.** The links in the library root point at the mount path as Theatarr sees it
  unless **Library link destination** (**Settings › Libraries › File management**) names the path Plex sees. Plex
  must have the debrid mount too, at the path the links point at; a title that is linked but missing in Plex
  usually means the two paths differ.
- **The library root is not writable.** The root and the trash folder must be writable by uid `65532`
  ([quickstart.md](quickstart.md#1-run-the-container)); the preflight names a root it cannot write.

## Search does not find a release you know exists

An admin can add it by hand. Open the title, open a library row's **Details** and choose **Last search results**; **Add by magnet
or hash** is under the list: paste the magnet link or the 40-character info hash and give the release name (a magnet's
own name is filled in). For a series the name must carry the season or episode, for example `S02` for a season
pack or `S02E05` for one episode. The release is checked against the library's profile like any other; a rejection
lists its reasons and offers **Add anyway**. A release with no known size is accepted. If the add fails, for
example because no provider has it cached on a cached-only library, Theatarr does not start a search for
something else.

## Status says links are dangling

**Symptom:** **System › Status** shows a notice such as "3 of 1200 links into the mount are dangling", or a title
that was in Plex no longer plays.

**Cause:** a link in a library folder points at a file that is no longer on the debrid mount: the provider dropped
or renamed the torrent, or the torrent lost its video file.

**What to look at:** admins get **Show the N broken links** in that check's row on **System › Status**. It opens
the links of the latest report, oldest first (at most 500; `theatarr library dangling` prints all of them). Each
line has the title (or the release folder, for a link Theatarr has no title for), the library, how long the link
has been broken, "torrent has no video upstream" where that is the cause, the link's path, and what happens next:

| Line | Meaning |
|---|---|
| Waiting for the next repair sweep | Theatarr made this link and will try to point it at the torrent's new folder. A renamed torrent is fixed this way without a new download. |
| Link marked missing | The sweep found nothing to point the link at and recorded the file as gone. The title page and **Wanted** show whether that copy is searched again. |
| A link Theatarr let go of | Theatarr made the link and no longer uses it (the copy was replaced or removed). |
| Not a link Theatarr made | Theatarr has no record of this link: it comes from another tool, such as an arr, under one of the checked folders. |

The list only shows the report: opening it scans nothing and repairs nothing. The repair sweep needs **Write
library links** on and an Active library. To run it by hand, see
[restore.md](restore.md#restore-from-the-command-line) (`library dangling -scan`, then `library relink -dry-run`).
