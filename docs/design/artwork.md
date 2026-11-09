# Artwork and metadata

This implements [`ui.md`](ui.md) §5 and the art rows of §6. The code is in `internal/metadata/art`
(the job, the cache and the image routes), `internal/metadata/tmdb/details.go` (the details calls), `internal/web/art.go`
(the routes and API fields), `internal/policy/art.go` and migration `00023_title_meta`.

## Data

`title_meta` has one row per title. It holds the overview, tagline, poster, backdrop and logo paths, genres, runtime,
rating and votes, US certification, status, network or studio, original language, release date (or first air date),
last and next air dates, `art_hash`, `refreshed_at`, `attempted_at`, `refresh_after` and `error`. A failed try keeps
the art from the last good fetch. `tmdb_id` is the id the row was fetched with. Sonarr adds carry only a TVDB id, so for
those it is found by the TVDB id, or else by the IMDb id.

Movie decisions read runtime and original language from `title_meta`, falling back to historical imported TMDb
cache rows (then the title's imported fields). Every successful metadata refresh also replaces TMDb's alternative
titles and translations in `title_alias`; source `tmdb-original` holds the original title as an alternative name.
Arr-sourced aliases are preserved, and a failed refresh keeps the last good metadata and names.

## The `meta.art` job

- **Requests.** One details request per title through the gateway and the `tmdb` budget. Movies append
  `external_ids,release_dates,alternative_titles,translations,images` and series append `external_ids,content_ratings,images`, both with
  `include_image_language=en,null`. These calls skip the response cache, because `title_meta` is their cache. The
  client paces them at 4 per second.
- **What a run takes.** Titles with no row come first, then due rows, up to `per_run`.
- **Movie release dates.** The metadata transaction replaces TMDb's country details and canonical cinema/digital/physical
  dates (US first, earliest worldwide fallback per type; wide theatrical before limited, no premiere/TV date, primary
  movie date as a last cinema fallback). Canonical dates have source `tmdb-art`, so arr enrichment cannot remove them;
  country details keep source `tmdb`. Date-derived status and the refresh marker let acquisition, Radarr and the
  calendar use corrected dates; fetch errors preserve the last successful dates and imported sources stay intact.
- **When it runs.** About a minute after start, then every `interval`. It also runs within about 2 minutes of a title
  appearing with no row, so new titles from the arr views, imports and the UI fill on their own.
- **Refresh age:**

  | Title | Refreshed after |
  |---|---|
  | Movie released in the last 90 days, or not yet released | 3 days |
  | Series airing: next episode, returning or in production, or aired in the last 90 days | 3 days |
  | Released or last aired within a year | 14 days |
  | Older | 90 days |
  | TMDb answered 404 | 30 days |
  | No TMDb, TVDB or IMDb id to find it by | 7 days |

  Each age gets up to 11 hours of spread by title id, so titles filled in the same run don't all come due together.
- **Errors.** Any error that isn't about one title stops the run: a gateway refusal (back-off, budget), a network error,
  or a 5xx. The failing title is pushed back an hour and the job fails, so the worker retries it with its usual
  exponential back-off.
- **Poster pre-warm.** After each filled title, the run fetches its poster at `prewarm` (w342) into the image cache. A
  pre-warm failure only stops pre-warming for the rest of that run.
- **Manual refresh.** `POST /api/theatarr/v1/titles/{id}/refresh` queues that title at once, ahead of other work, and answers
  202 with the job id. Without a TMDb key it answers 503.

## Images

- **Routes:**
  - `GET /img/t/{id}/{kind}/{size}?v={art_hash}`, where `kind` is `poster`, `backdrop` or `logo`.
  - `GET /img/tmdb/{size}/{file}`, for titles not in the library.
- **Access.** Both routes need a sign-in (cookie or key), like every other route.
- **What can be fetched.** Sizes are `w92 w185 w342 w500 w780 w1280`, plus `original` only with `ui.images.original`.
  A file must match `^[A-Za-z0-9]{10,40}\.(jpg|png)$`. Anything else gets a 404 and nothing is fetched.
- **Fetching.** Images come through the gateway only, as source `tmdb-images` with its own budget. Each file is fetched
  once, however many requests ask for it at the same time. The answer must be JPEG or PNG and at most 20 MiB.
- **Cache layout.** Files are stored at `{dir}/{size}/{first two chars}/{file}`. A cache hit refreshes the file's
  modification time at most once an hour. When the directory goes over `max_bytes`, the least recently used files are
  evicted until it is back to 90%.
- **Cache-Control:**

  | Response | Cache-Control |
  |---|---|
  | `/img/tmdb/…`, or title art whose `?v=` matches its `art_hash` | `private, max-age=31536000, immutable` |
  | Title art without `?v=`, or with an old one | `private, max-age=3600` |
  | Generated SVG | `private, max-age=300` |
- **Generated SVG.** It shows the title and year on a gradient picked by the title id. It is served when there is no
  art yet, when TMDb fails, or when there is no image cache configured.
- **CSP.** The policy is unchanged (`img-src 'self' data:`). The browser never talks to TMDb.
- **API.** `GET /titles` items gain `poster`, `backdrop` and a short `overview`. `GET /titles/{id}` also gains `meta`
  (the full row, plus a `logo` URL), which is `null` before the first try.

## Policy

```yaml
budgets:
  tmdb-images: {requests: 20, per: 1s, burst: 40, daily: 20000}
metadata:
  art: {per_run: 200, interval: 6h, prewarm: w342}   # all optional; these are the defaults
  sources:
    tmdb-images: {url: https://image.tmdb.org/t/p, budget: tmdb-images}   # no api_key_env allowed
ui:
  images: {dir: /data/images, max_bytes: 2GiB, original: false}   # optional; dir defaults to images/ beside the database
```

- **Turning parts off.** Without `metadata.sources.tmdb-images`, the cache is off and `/img` serves generated art only.
  Without the TMDb key, the job is off.
- **Validation rules:**
  - `per_run` is at most 5000.
  - `interval` is at least 10m.
  - `prewarm` is a listed size or `off`.
  - `ui.images` needs the `tmdb-images` source.
  - `dir` is absolute, and overlaps no library root, `library.trash.dir` or the mount (`library.mount.path`,
    `link_path`), as written or resolved (symlinks, bind mounts): the cache removes files under its dir.
  - `max_bytes` is at least 16MiB.
