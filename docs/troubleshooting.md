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

**Cause:** a new library starts in **Shadow** mode with **Allow downloads for this library** off. It decides what it
would grab and records that, but adds nothing to debrid and writes no links. A shadow library is also not searched
on a schedule: it only sees releases from the RSS feeds of its indexers, so with indexers that have no feed
(Torrentio, Zilean) it records nothing at all.

**Fix:**

- To see how it judges a release, paste a release name into **Settings › Profiles › Check a release**. Decisions it
  has recorded are in **Activity › Decision journal** and on the title's **Releases** page; a title added a
  moment ago may have none yet. To search the indexers for one title now, run
  `docker compose exec theatarr theatarr decide -tmdb <id> -library <library> -search` (it records nothing).
- Give the library at least one indexer with an RSS feed (a Torznab feed, DMM, SeaDex or a catalog indexer) if you
  want it to keep recording decisions while in shadow.
- To start downloading, follow [quickstart.md](quickstart.md#9-go-live-deliberately): set the library to
  **Active**, turn on **Allow downloads for this library** and **Write library links** under **Settings › Libraries**,
  and **Allow new downloads** under **Settings › Download clients**. **Wanted** says why each title is still waiting.

## `golive check` skips or fails the agreement line

**Symptom:** `theatarr golive check -library <library>` shows the shadow **window** and **agreement** lines as INFO,
"no arr feeds <library>", or fails them with "the shadow runner has not run" or "no arr grabs".

**Cause:** the agreement line compares the library's decisions with the grabs of a Sonarr or Radarr that feeds it.
A library with no arr behind it has nothing to compare with, so both lines are skipped (INFO) and do not fail the
check: there is no evidence either way. A library that an arr feeds fails them until the shadow runner has run and
has arr grabs to compare.

**Fix:** with no arr, nothing is broken. Read the library's decisions in the decision journal instead, make sure
every other line passes, and go live once the decisions look right. If you are moving from an arr, add it under
migration so the check has grabs to compare, and let it run for the window ([migration.md](migration.md#2-shadow-compare-with-the-arr)).
