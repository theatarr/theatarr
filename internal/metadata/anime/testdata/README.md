# Test data for internal/metadata/anime

## pyset.json

Iteration orders of CPython 3.10 sets of random ints (added one by one, then copied), generated with Python 3.10.12
on 2026-10-26. `TestPySetOrder` checks the set emulation (`pyset.go`) against them. No outside data.

## offline/

An offline cache of the open-data responses (recorded 2026-10-25) for three franchises: Re:Zero, Mushoku Tensei and
Made in Abyss, with their golden outputs. `TestGoldenFixture` builds them from the cache (package `offlinecache`) and
compares with the golden outputs.

Only the responses and list records these three builds read are kept, and only the fields the pipeline uses.

To see what a build produces, run `THEATARR_ANIME_DUMP=<dir> go test ./internal/metadata/anime -run TestGoldenFixture`:
it writes each franchise's JSON and Markdown to `<dir>`. The Markdown is in the form of `out/*.md`; the JSON also
carries the fields the comparison ignores (such as `mapping_decision` and `stages`), so it is not a drop-in copy of
`out/*.json`.

| Path | Source | Terms |
|---|---|---|
| `offline/out/*.json`, `offline/out/*.md` | The golden outputs, derived from the sources below | as below |
| `offline/overrides/*.json` | Curated moves (ours), citing MyAnimeList stacks | ours |
| `offline/community/*.json` | Community orders: entry keys transcribed from MyAnimeList series-guide stacks (URLs in each file) | short lists of our keys; evaluation only |
| `offline/franchises.json` | The franchise list (ours) | ours |
| `offline/cache/anilist/` | AniList GraphQL (`graphql.anilist.co`) media batches | AniList terms: no mirroring or hoarding. A few dozen records, for tests only |
| `offline/cache/kitsu/` | Kitsu API (`kitsu.app/api/edge`), reduced to relationships and titles | keyless public API, no published terms |
| `offline/cache/skyhook/` | Sonarr SkyHook (`skyhook.sonarr.tv`), TVDB episode data, reduced to numbers, dates, titles, overviews, airs fields | unofficial endpoint; TVDB data |
| `offline/cache/xem/` | TheXEM `map/all` | no published terms |
| `offline/cache/arr/` | GETs against a Sonarr (series ids, episode numbers) and a Radarr (movie lookups proxied from TMDb), reduced to ids, titles, years and dates | recorded responses; no URLs, keys or GUIDs |
| `offline/cache/static/scudlee-anime-list-master.xml` | Anime-Lists/anime-lists `anime-list-master.xml`: only the `<anime>` records looked up (22 of about 14,000) | no licence file published; 22 records (ids, default TVDB season, offsets, name, episode mappings), credited in `NOTICE` |
| `offline/cache/static/fribb-anime-list-full.json` | Fribb/anime-lists `anime-list-full.json`: only the looked-up records, three fields | no licence file published; 34 records (`anilist_id`, `anidb_id`, `themoviedb_id`), credited in `NOTICE` |
| `offline/cache/static/anibridge-mappings.json` | anibridge/anibridge-mappings v3 `mappings.json`: the looked-up AniList keys (27) | MIT, credited in `NOTICE` |
| `offline/cache/static/manami-aod-minified.json` | manami-project anime-offline-database: the 32 looked-up records, reduced to the fields the tests use | ODbL 1.0 + DbCL 1.0; the excerpt stays under ODbL 1.0, credited in `NOTICE` |
