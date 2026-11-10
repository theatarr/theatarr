# Troubleshooting

The places new installs most often get stuck, and what to do about each. For anything else, start with
**Admin › System › Status**, **System › Errors** and **System › Logs**, and run the preflight
(**Setup › Run preflight**, or `docker compose exec theatarr theatarr preflight`): each line says why it failed and
what to do next.

## The container keeps restarting

**Symptom:** `docker compose ps` shows Theatarr restarting, and `docker compose logs theatarr` shows an error about
opening or writing a file under `/data`.

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

**Cause:** metadata sources have no default address. Theatarr looks for sources with these exact names.

**Fix:** under **Settings › Metadata & artwork**, add:

| Name | Source address | Key |
|---|---|---|
| `tmdb` | `https://api.themoviedb.org/3` | your TMDb API key |
| `tmdb-images` | `https://image.tmdb.org/t/p` | none |
| `tvdb` (optional) | `https://api4.thetvdb.com/v4` | your TVDB key |

The names must match exactly. Save; no restart is needed.

## There is no profile to pick for a library

**Symptom:** the first-library step in Setup has no profile to choose, and the TRaSH presets say they are off.

**Cause:** the presets come from a TRaSH-Guides checkout mounted at `THEATARR_TRASH`, and no profile ships built in.

**Fix:** either clone the checkout next to your compose file and restart:

```sh
git clone --depth 1 https://github.com/TRaSH-Guides/Guides trash-guides
docker compose restart theatarr
```

The example compose file mounts `./trash-guides` at `/config/trash` and sets `THEATARR_TRASH` to it. If Docker
already created an empty, root-owned `trash-guides` directory, remove it first (`sudo rmdir trash-guides`).

Or create a profile by hand under **Settings › Profiles** in the raw profile editor
(`/admin/settings/profiles?raw=1`, **Add profiles**), then pick it in Setup.

## A library exists but nothing happens

**Symptom:** you created a library and added titles, but nothing is downloaded and nothing appears in the library
folder.

**Cause:** a new library starts in **Shadow** mode with **Allow downloads for this library** off. It decides what it
would grab and records that, but adds nothing to debrid and writes no links. A shadow library is also not searched
on a schedule: it only sees releases from the RSS feeds of its indexers, so with indexers that have no feed
(Torrentio, Zilean) it records nothing at all.

**Fix:**

- To see how it judges a release, paste a release name into **Settings › Profiles › Check a release**. Decisions it
  has recorded are in **Admin › Advanced › Decision journal** and on the title's **Releases** page; a title added a
  moment ago may have none yet. To search the indexers for one title now, run
  `docker compose exec theatarr theatarr decide -tmdb <id> -version <library> -search` (it records nothing).
- Give the library at least one indexer with an RSS feed (a Torznab feed, DMM, SeaDex or a catalog indexer) if you
  want it to keep recording decisions while in shadow.
- To start downloading, follow [quickstart.md](quickstart.md#9-go-live-deliberately): set the library to
  **Active**, turn on **Allow downloads for this library** and **Write library links** under **Settings › Libraries**,
  and **Allow new downloads** under **Settings › Download clients**. **Wanted** says why each title is still waiting.

## `golive check` skips or fails the agreement line

**Symptom:** `theatarr golive check -version <library>` shows the shadow **window** and **agreement** lines as INFO,
"no arr feeds <library>", or fails them with "the shadow runner has not run" or "no arr grabs".

**Cause:** the agreement line compares the library's decisions with the grabs of a Sonarr or Radarr that feeds it.
A library with no arr behind it has nothing to compare with, so both lines are skipped (INFO) and do not fail the
check: there is no evidence either way. A library that an arr feeds fails them until the shadow runner has run and
has arr grabs to compare.

**Fix:** with no arr, nothing is broken. Read the library's decisions in the decision journal instead, make sure
every other line passes, and go live once the decisions look right. If you are moving from an arr, add it under
migration so the check has grabs to compare, and let it run for the window ([golive.md](golive.md)).
