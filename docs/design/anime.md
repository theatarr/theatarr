# The anime metadata engine (`internal/metadata/anime`)

The anime engine builds franchises from AniList, AniDB, Kitsu and the community mapping lists, maps each entry onto
TVDB and TMDb, and derives release, watch and story orders. Decisions and imports use those mappings, and Plex gets
watch-order playlists. Where it sits: [overview.md](overview.md); TVDB itself: [tvdb.md](tvdb.md). Its outputs match a set
of golden outputs exactly ([Golden outputs](#golden-outputs)).

Code: `internal/metadata/anime` (pipeline), `.../offlinecache` (an offline cache of the source responses), `.../live`
(sources through the gateway), `.../animestore` (database, review queue, the Sonarr-episode TVDB backend),
`.../animemap` (mappings for decisions and imports), `.../playlists` (Plex watch-order playlists), migrations
`00017_anime.sql` and `00018_anime2.sql`, `cmd/theatarr/anime.go`, `cmd/theatarr/animeplaylists.go`,
`examples/policy/overrides/`, `examples/policy/playlists.yaml`.

## What it does

`anime.Build` takes a franchise (slug, name, AniList seed, optional `scope: stem`) and returns the result:
entries with their TVDB and TMDb mappings, the TVDB series, excluded neighbours, unclaimed regular episodes, and the
release, watch and story orders. Each stage is a function; its output goes into `Result.Stages` and the database.

| # | Stage | Where |
|---|---|---|
| 1 | Typed AniList crawl: line, branch, side, leaf; series-stem check; `scope: stem` | `crawl.go` |
| 2 | Id joins: AniList → AniDB (Fribb, else manami), Kitsu (manami) | `mapper.go` |
| 3 | Candidate mappings: ScudLee (season, offset, mapping lists, `a` absolute; applied as HAMA does) and anibridge | `mapper.go`, `lists.go` |
| 4 | Arbitration: the score and keep/reject rules; status; the anchor signal; mappings to missing TVDB episodes dropped | `arbitrate.go` |
| 5 | Secondary entries (ScudLee by TVDB id, manami two-way links, Kitsu), heuristic match, fix-ups, TVDB-only specials | `secondary.go`, `flags.go`, `merge.go`, `tmdb.go` |
| 6 | Flags (recap, alternate, extra, short, unaired, ...) and target (sonarr, sonarr S00, radarr, both) | `flags.go` |
| 7 | Release order; automatic watch order (spine, alternates and route blocks, TVDB airs, air-date interleave) | `orders.go` |
| 8 | Story order: story-mode order plus the overrides' moves (slices, mid-season splits, `start`) | `story.go` |

### Arbitration and the review queue

The arbitration's decision is kept as it is (that is what the golden outputs check). On top, every mapped entry gets a status:

| Status | When | Mapping used to import |
|---|---|---|
| `auto` | the two lists agree, or one list maps it and the TVDB air dates fit (or there are no dates to check) | yes |
| `arbitrated` | the lists disagree and the winner leads by ≥ 0.5 (score range 0–5) or its placement matches TVDB airsBefore; or one list kept although the dates don't fit (the decision's "lists agree / title matches / direct AniDB join") | yes |
| `conflict` | the lists disagree within 0.5, or the winner's placement contradicts TVDB airsBefore | **no** (TVDB's numbering), review item |
| `heuristic` | no list maps it; matched by air date and title | yes, review item |
| `override` | set by an overrides file, or accepted in review | yes |
| `rejected` | every candidate dropped (TVDB lacks the episodes, dates don't fit, no date), or rejected in review | no (TVDB's numbering) |

**Anchor signal**: ScudLee's `<before>;1-13;</before>` says AniDB special 1 goes before episode 13. The special
is found through the record's season-0 mapping list (or as the candidate's n-th episode for a record whose default
TVDB season is 0) and compared with TVDB's `airedBeforeEpisodeNumber` for that special. In the twelve franchises it
never fires: only Bakemonogatari has a `<before>`, and it maps its specials to regular episodes, which have no airs
fields. Unit tests cover match and mismatch.

Across the twelve franchises: 183 auto, 23 arbitrated, 7 conflicts, 18 heuristic, 3 rejected, 40 entries without a
mapping (Radarr-only films, unaired entries). The 7 conflicts: Hanamonogatari, Tsukimonogatari, Owarimonogatari II
and Zoku Owarimonogatari (lossy ScudLee lists), Mushoku Tensei S2 (episode 0), DBZ Kai: The Final Chapters
(international vs Japanese numbering) and Dragon Ball Super: Beerus (2026 re-edit, one vs two episodes). Most are
known hard cases; the arbitration's pick (anibridge each time) wins, but by less than 0.5 (mostly through
coverage), so a person confirms each once.

**Review queue** (`anime_review`, `theatarr anime review`): `conflict` and `heuristic` items per entry; `refresh_shrink`
and `refresh_moves_linked` items when a refresh is held. A decision sticks: a later build proposing the same mapping
(same entry, TVDB series and episodes) takes the accepted one as `override` (or keeps a rejected one out) instead of asking again. Items a new build does
not raise again are `superseded`.

**Refresh hold**: `Save` holds a build, keeping the applied one, when it would map more than 10% fewer of
a TVDB series' episodes, or would move a mapping whose episodes have files (an active `link_episode`, or an imported
`version_file_episode`). Both checks compare the build with saved review decisions applied, so a refresh that proposes
what review already decided does not ask again. The held build is the franchise's `pending_result`; accepting its
item applies it, rejecting drops it.

### Overrides

One file per franchise in the policy's `metadata.overrides_dir` (`<slug>.yaml`; JSON loads too). Format in
`examples/policy/overrides/README.yaml`; five override files are there in YAML, with `franchises.yaml` (the
twelve franchises) and `fate-stay-night.yaml` (routes). All of it is applied (`overrides.go`, `story.go`,
`routes.go`):

| Key | Effect |
|---|---|
| `moves` | story order: entries or slices moved after a key, a mid-season point, a slice, or `start` |
| `mappings` | pins an entry's TVDB range: status `override` |
| `exclude` | out of every order |
| `routes` + `line_position` | route order (Fate). A route key is an alternate's root (the block is the alternate and every row the watch order routed to it: its specials, recaps, sequels) or a main-order key (the block runs from that row to the next route key). The blocks become one sequence in the watch and story orders (role `route`, not optional); the rest of the line goes `first` (default) or `last`. Unlisted alternates stay alternates; the release order keeps its dates and marks each row's `route`. Unknown keys are listed in `story_order.routes.failed` |
| `alt_cuts` | `{name, tvdb, entry?, match?, map: {DC1: S01E01, DC2: [S01E02, S01E03]}}`: a cut no source numbers. Keys end in the cut's episode number; values are TVDB episodes of `tvdb`. Stored in `anime_alt_cut`; a release whose title matches `match` (a case-insensitive regex) or every word of `name` maps its numbers through the table (decisions, below). The cut's own `entry` is marked in the orders with the cut's TVDB range |

Validation: keys, slices, ranges, routes listed twice, `line_position` without `routes`, alt cuts without `tvdb`, map
keys without a number, values that are not `SxxEyy`, a bad `match` regex, two cuts with one name.

With `fate-stay-night.yaml` (`routes: [al:356, al:19603, al:20791]`, `line_position: last`) the watch order is the
2006 series with its specials and the TV Reproduction recap, Unlimited Blade Works (with the El-Melloi and strange
Fake spin-offs that air inside it), Heaven's Feel I-III, then Fate/Zero and its side stories; the UBW 2010 film and
the sunny day special stay alternates.

### Import gating (`internal/decide`, `internal/library`, `.../animemap`)

`animemap.Load` reads every franchise mapping into a TVDB series (`anime_mapping`, entry names from `anime_entry`,
`anime_alt_cut`). Importable: `auto`, `arbitrated`, `override`, `heuristic` (a heuristic rejected in review becomes
`rejected`). A mapping that is not importable (`conflict`, `rejected`) is never used, and nothing waits for it: its
episodes import through TVDB's numbering, exactly like an episode no entry maps (so specials land in Season 00 the
way Sonarr puts them). The best-scoring candidate of a conflict is never imported under. The review item is still
created, and Admin › Reviews shows it; playlists leave such an entry out ("missing") until it is reviewed.

**Decisions** (`decide/anime.go`; `Title.AnimeMap`, loaded by `LoadSeriesVersion` for any series with mappings):
1. An alternate cut the release title names: its numbers map through the cut's table (`anime_mapping: alt_cut:<name>`);
   a number the table lacks is `UnknownEpisode`.
2. A franchise entry whose name (Sonarr's clean title) is the release's series title, with absolute numbers: the
   entry's own episode n is the n-th TVDB episode of its range outside season 0 (`franchise`). A franchise name or an
   alternate cut also makes the series match when Sonarr's own lookup does not (`MatchAlias`). An entry that cannot
   hold every number, or two entries that both can, leave it to Sonarr; so does an entry whose mapping is not
   importable. Whenever the franchise gives such a release no placement, the series is known, so Sonarr's episode
   resolution runs as it would for the series (series-wide absolute numbers, scene and XEM mappings), exactly as the
   import's file resolution places it: "2nd Season - 02" of a rejected entry is absolute 2, S01E02; "2nd Season -
   05" of a two-episode entry is S01E05, whatever the entry's status; `UnknownEpisode` only where Sonarr cannot place
   it. An alternate cut's missing number stays `UnknownEpisode`.
3. Otherwise Sonarr's mapping (`sonarr`).

SeaDex season packs carry their AniList entry id. When the franchise maps that entry into only part of a TVDB
season (one cour), the decision covers only those regular episodes, keeping the entry mapping for import (for an
entry split into parts, `al:N#1..k`, the AniList entry `al:N` they share: files never map through one part). A pack
for one cour cannot take the rest of the season's episode slots (`TestSeaDexEntryCoversOnlyItsCour`). An entry whose
mapping (or a split part's) is not importable does not narrow the pack: TVDB's numbering takes the season
(`TestSeaDexUnreviewedEntryUsesSonarrNumbering`). When SeaDex lists the same info hash under several entries (a
whole-season batch listed under each of its cours), the release carries all of them (`seadex_entries`, collected by
the seadex indexer before it keeps one listing per hash, and from the stored standing with Prefer SeaDex). The union is
made only on evidence: at least two of them are importable and map into the pack's season, their regular episodes there
are exactly the season's episodes 1..N, and the torrent's episode files (SeaDex's file list; extras such as OVAs, specials
and NCOP/NCED left out) are numbered exactly 1..N, each once, as the import parses them (`seadex_numbered`, set by the
indexer). Then the decision covers the union, the size limits apply to it (`anime_mapping: season:<n>`, no entry), and
each file takes that season's numbering before any entry its name matches: a file numbered 13 (or 13v2) is SnnE13
(`TestSeaDexBatchUnderSeveralCoursCoversTheSeason`, `TestSeaDexBatchEntriesFromStanding`). In every other shape
(numbers repeat per cour, start elsewhere, are series-wide absolute, cannot be read, or there is no file list), and for a
hash listed under one entry, the listing is decided as one cour, as before (`TestSeaDexBatchWithoutSeasonNumberingAsBefore`).
Known limit: with Prefer SeaDex off, memberships that exist only in the stored standing, or that the router's
de-duplication across queries drops, are not seen, so such a batch is decided as one cour.

Then every episode only non-importable ranges cover, and every episode of a release that named a non-importable
entry (step 2 or a SeaDex pack), is listed in the decision's `anime_fallback` with the entry and its status
(`TestAnimeFranchiseMapping`, `TestAnimeUnreviewedEntryUsesSonarrNumbering`). The decision JSON gains
`anime_mapping`, `anime_entry`, `anime_fallback`. Nothing is rejected for it. A release that named a non-importable
entry (step 2) keeps that entry, also when the entry's candidate range cannot hold the number (the one entry of that
name: review may pick another): `anime_entry` beside `anime_mapping: sonarr`, and on the download
`episode_map {"how":"franchise","entry":…,"fallback":true}`, so an accept can move its episodes later
(`TestAnimeFallbackMatchesFileResolution`).

**Library**: files resolve through the same rules (`decide.ResolveFile`); a decision's stored entry that is no longer
importable at import time falls back to TVDB's numbering too, and a fallback's stored entry maps files named only
"01" once it is importable. Nothing is held (`TestAnimeUnreviewedImports`). When the mapping of the entry a download
stored changed between the grab and the import, the download's episodes no longer match where its files resolve, so
the import first reconciles them (`library/resolve.go` `rescope`), in two cases:

- the stored franchise entry (or one of its split parts) is no longer importable (`EpisodeMap.Stale`): its files now
  map through TVDB's numbering. The episodes become the ones the decision now gives the release as the download
  stored it (`release_data`: its TVDB id, kind and extras, so a pack matched by id keeps Sonarr's season:
  `TestAnimeRejectedMappingKeepsReleaseIdentity`), or its files' when the release places nothing (a pack named by
  the entry) (`TestAnimeRejectedMappingImports`);
- the download was grabbed through TVDB's numbering for an entry that was not importable (a fallback) and review
  has accepted the entry, or another candidate for it, since (`TestAnimeAcceptBeforeImport`,
  `TestAnimeCandidateBeforeImport`). Each file the entry now places, whose Sonarr placement is among the download's
  episodes, swaps those episodes for the entry's. The release title is not decided again, and episodes no such file
  holds stay: a pack the router narrowed is not widened (`TestAnimeAcceptKeepsNarrowedPack`), a file named for
  another entry or by an explicit SxxEyy moves nothing, and neither does one a client or an admin chose. A candidate
  that cannot hold the number leaves the download where Sonarr placed it.

Three rules keep this safe (`internal/library/animerescope_test.go`):

- Only a stored entry re-scopes. A download with no stored mapping (grabbed through TVDB's numbering for a release
  that named no entry) keeps its episodes whatever the franchise mappings do; so does a mapping still importable, and
  one stored without an entry key (a split-parts pack grabbed before the key was kept: `TestAnimeSplitPackScope`).
- A file that cannot pass the import guards (a sample) moves nothing (`TestAnimeSampleDoesNotRescope`).
- A re-scope never replaces a file the grab did not cover. When a new episode already has a file of the version
  (Theatarr's link, a dead link or the arr's file), the download is not re-scoped at all: its files are held as out
  of scope, the existing file stays, nothing goes to the trash (`TestAnimeRescopeKeepsOtherEpisodesLink`).

A real import writes the new episodes to `download.episodes`; `download.episode_map` stays, for a later re-link.
Whenever the episodes are not reconciled, a file that now maps outside them is held: the plan's `unresolved` says
`out of scope`, the download stays `ready`, nothing is linked or moved aside, and Change episodes (or a manual
import) places it. This is the counterpart, before the import, of the re-link below.

**Re-link after review** (`library/animerelink.go`). When review accepts a mapping (`animestore.Resolve` or
`ResolveCandidate`), or a saved build turns an entry's mapping importable (it was `conflict` or `rejected`), or review
accepts a held refresh that moves an importable mapping, an `anime_relink` job is queued in the same transaction, one per TVDB series (dedupe
`anime_relink/tvdb:<id>`, run a minute later so accepts in a row join it). A reject queues nothing, and neither
does a franchise's first build (no earlier mapping). The job re-runs the import's resolver (`decide.ResolveFile`
with the current mappings, the download's release, folder and stored mapping) for every active link Theatarr made
for the series, and moves a link only when the resolver places the file through the franchise (`+franchise:` or
`+alt_cut:`) on other episodes than it holds. The `anime_fallback` markers are not read. Left alone: an arr's file,
an adopted link (no download), a link several versions share, a file whose episodes a client or an admin chose
(`download_file` `manual`), and a file the resolver places by its own season and episode (Sonarr's meaning stands).

A move is the change-of-episodes move (`library/reassign.go`, docs/design/library.md "Change episodes"): the
version's naming for the new episodes, the gated compare-and-swap rename, the link's episodes and the episode states,
the download's recorded file identity (with the resolver's `how`, not `manual`), and the Plex scans of both folders.
Nothing is replaced, so nothing goes to the trash. Each move is recorded in `library_action` (kind `link`, with the
download, detail `relinked after review: S00E26 → S01E12`), which the journal lists as a link entry related to the
download's decision. Writes need the gate: for a version it refuses (Pause on, or a Watch-only version) the
job records the same action as `dry_run` (the reason appended) and changes nothing; a root that does not answer
retries the job. Passes repeat while a move succeeds, so a shifted range (E01-E03 to E02-E04) frees its own targets
in turn. A target episode that still has another file afterwards (Theatarr's link or the arr's; a swap), or a new name
another file already has on disk, is skipped: both files stay, nothing is overwritten, and one error-inbox entry
(source `log`, component `anime-relink`, one per link) names the two paths. Tests:
`internal/library/animerelink_test.go`, `TestRelinkQueued`.

Known limit: a file named by another alias (neither the entry's name nor a cut) moves only through the decision's
stored entry. A download whose decision stored none (grabbed through TVDB's numbering for a release that named no
entry, or before fallbacks kept their entry) keeps Sonarr's placement; Change episodes moves it by hand. The resolver
does not infer the entry from the release title, which would change the placement of every anime grab.

Known limit: a file named after another entry outranks the stored identity. A fallback pack whose release names
entry 2 ("2nd Season") but whose files carry the franchise's base title (entry 1's name) resolves through entry 1:
accepted before the import it imports at entry 1's episodes, accepted after it the re-link moves nothing
(`TestAnimeFallbackFilesNameOtherEntry`); Change episodes moves it by hand. Letting the stored fallback outrank the
file name would place files by the release title.

Known limit: a split-parts pack (`al:N#1..k` stored as `al:N`) stays current while any part or the unsplit entry is
importable. A refresh that replaces the parts with one unsplit entry mapping other episodes before the import leaves
the files held as out of scope (visible on the download, files intact); Change episodes or a re-grab places them.

Known limit: a download grabbed through a franchise entry that stays importable is not re-scoped when the entry's
range moves before the import (a refresh or another candidate): files outside its episodes are held as out of scope,
files intact; Change episodes places them.

Known limit: a grab through an entry that review then rejects is decided again whole, so a pack the router had
narrowed (an episode turned unmonitored while it waited) gets back every episode TVDB's numbering gives its
release: a narrowed two-episode pack imports both files. Nothing is replaced (a new episode that already has a file
stops the re-scope). The download does not record which episodes the router dropped, and the same path rightly grows
a split-parts pack to its season (`TestAnimeSplitPackScope`).

Known limit: a download with no stored entry is not re-scoped. One grabbed through TVDB's numbering before the
series had a franchise mapping, whose file the series' first build then maps to another episode before the import,
is held as out of scope, files intact (`TestAnimeFirstBuildBeforeImport`, `TestAnimeFirstBuildKeepsOtherEpisodesLink`,
`TestAnimeFirstBuildKeepsIdMatchedPack`; a build that agrees with Sonarr changes nothing:
`TestAnimeFirstBuildKeepsNarrowedPack`). So is one whose release named no entry when review accepts an entry that
places its files elsewhere. Change episodes places them. The build is not deferred while downloads wait: a download
already held would defer it for good, and a franchise build maps other series than the one that queued it.

Known limit: a download whose main file is held as out of scope while an explicitly numbered sample stays in scope
ends failed and blocklisted, not held. The general import rule decides it (`internal/library/import.go`: no link
planned and a file the sample guard rejected is a rejected release); it does not know the held file was the real one.

Playlists need nothing: a sync reads the active link of each episode, so a re-linked file joins its playlist at the
next sync.

### Review screen and API (`internal/web/anime.go`)

- **`/admin/reviews/anime` (Anime mappings):** items filtered by state (Waiting, Accepted, Rejected, Replaced, All) and
  franchise, one card each (`web/src/components/anime/review-card.tsx`, words in `web/src/lib/anime-review.ts`). A card
  shows the series (linked to its title page when it is a Theatarr title), the kind in words, a link to the franchise,
  and one plain question per kind:

  | Kind | Question | Import today | Accept does | Reject does |
  |---|---|---|---|---|
  | `conflict` ("Lists disagree") | "Which {series} episodes are “{entry}”? The mapping lists disagree, so until you choose its episodes keep TVDB's numbering." | not used: TVDB's numbering | the mapping is used (override), relink of the series queued | stays out (rejected); nothing moves |
  | `heuristic` ("Matched by date and title") | "Is “{entry}” {series} {range}? No mapping list covers it; Theatarr matched it by air date and title and uses this match now." | used | kept as override; stops asking | not used from now on; new files take TVDB's numbering; linked files stay (no relink) |
  | `refresh_shrink` ("Refresh would unmap episodes") | "Apply the refresh of {franchise}? It would map {after} of the {before} {series} episodes mapped now. Nothing changes until you choose." | current mapping kept, rebuild held | applies the whole held rebuild | drops it; the next rebuild may ask again |
  | `refresh_moves_linked` ("Refresh changes linked episodes") | "…The refresh maps “{entry}” to {to} instead of {from}. {n} of these episodes have files; files already linked for them may be moved to match." When the held build maps the entry to nothing importable (a conflict counts as nothing): "…The refresh stops mapping “{entry}” ({from}), so new files take TVDB's numbering." plus the same files sentence. The card never promises that files move: the server cannot preview which links a relink changes. | as above | as above; linked files may be relinked to match | as above |

  Beside the question: the AniList entry (title, English title, format, episode count, dates, AniList and AniDB links)
  and, for conflict and heuristic, one TVDB box per option: the proposed mapping (labelled "In use now" on an open
  heuristic item, "Theatarr's pick" otherwise) and each other candidate, with the range in words (no separate count), the source ("AniBridge list", "ScudLee's anime-lists", "Air date and title
  match", "Set by hand"), sample episodes (first two and last, with names and air dates from Theatarr's episode list
  when the series is a Theatarr title), a TVDB link and **Use this mapping** (a dropped candidate says why it can't be
  used). A held refresh shows "Now" (the entry's importable mapping in use) and "After the refresh" (the held
  build's, or the episode counts and up to five episodes no longer mapped); these are filled only while the item is
  open, so a resolved one shows its summary and what was chosen.
  On an open conflict the TVDB column heading reads "TVDB's numbering · in use now".
  Buttons, no browser confirm: conflict **Edit season/offset…** and **Keep TVDB's numbering** (reject); heuristic
  **Edit season/offset…** and **Don't use this match** (reject), with the note "Don't use this match: new files take
  TVDB's numbering; files already in the library stay where they are."; held refresh **Apply the refresh** (accept) and
  **Keep current mapping** (reject), with "This answers all {n} changes of this refresh." when one answer settles
  several items. A resolved item asks its question in the past tense and says what was decided ("Rejected: the match
  is not used; new files follow TVDB's numbering."). **Edit season/offset…** sets one contiguous run of one season of one of the options' series by hand
  (accepted like a candidate: status `override`, winner `manual`; the choice sticks across rebuilds while the same
  mapping is proposed: `anime_review.choice`); a split or multi-season mapping still goes through the overrides
  file. **Show the raw rule** opens the summary, kind, entry key, the candidate table with score parts (exists, date
  fit, cover, title similarity, score, anchor, why dropped), the **override snippet** (a `mappings:` entry per
  candidate, to paste into `overrides/<slug>.yaml`) and an optional note sent with the next choice.
  Below, the franchises with mapping counts by status. Items raised from this version store `detail.after` of a
  `refresh_moves_linked` item properly (`{"tvdb", "episodes"}`); older ones have it empty.
- **`/admin/reviews/anime/{slug}`:** open items, the **order panel** (release, watch, story; each item with its source: release
  date, TVDB airs, the AniList chain, or `override` for moves, routes, alternate cuts and mappings decided in
  review; a series split by an interleaved special shows the part to stop at and to resume from, and each item
  its TVDB and TMDb ids), the mappings, the **numbering view** (each mapped episode: TVDB, absolute as the imported Sonarr has it, the
  AniList entry and the entry's own number, season/episode, mapping status, import eligibility and source).
  Alternate orders include their sources; alternate cuts show episode-to-TVDB mappings. Playlist management
  remains on Settings › Plex & playback, with playlist settings and the Playlists Theatarr keeps list.
- **Title page:** the watch-order panel links admins to franchise details for orders, mappings and episode numbering.
- **API** (`openapi.json`): `GET /anime/franchises`, `GET /anime/franchises/{slug}`, `GET /anime/review`,
  `GET /anime/review/{id}/snippet`, `POST /anime/review/{id}/accept` (no body: the proposed mapping; `{"candidate": n}`; or `{"mapping": {"tvdb", "season",
  "first", "count"}}`, a hand-set run checked against Theatarr's episode list; each with an optional `"note"`),
  `POST /anime/review/{id}/reject`, `GET /anime/playlists`, and the build endpoints under "Automatic builds".
  Session auth with CSRF for session POSTs; API keys as for the rest. Accept, reject and the build requests are the
  only writes. `theatarr anime review` takes `-candidate N` (with
  `-accept`) and `-snippet ID`.

### Watch-order playlists in Plex (`.../playlists`, `internal/plex/playlists.go`)

One admin-owned regular video playlist per franchise and order kind (`playlists.orders`, default watch and story),
named `{franchise} ({order})` ("Re:Zero - Starting Life in Another World (story order)"), plus one per alternate route
left in the watch order (a root with routed rows; `playlists.routes`).

- **Items:** each order row's TVDB episodes come from the applied mapping (importable ranges only; a conflict row is
  listed as missing), narrowed to the row's part (`anime.RowEpisodes`: "up to before S01E13", story slices); an entry
  without episodes but with TMDb ids is a movie. Each item is the file Plex sees: Theatarr's active link, else the arr's file
  (`version_file`) while the arr owns the version. `playlists.skip_optional` leaves out recaps and side stories.
- **Sync** (gate open): the server's machine id, one listing per section of `library.versions[*].plex_section` and
  type (episodes, movies) to find rating keys by file path, then `plex.SyncPlaylist` per playlist: find it by the
  stored rating key or by title, create it, or remove unwanted entries and duplicates, append missing ones and move
  entries into order. A playlist already in order gets no write. Nothing deletes a playlist or a library item.
- **Gate:** Plex is written only when `library.enabled` **and** `playlists.enabled` are true (both default false)
  and `library.plex`'s token is set. Otherwise, or with `-dry-run`, a sync plans from the database alone, records the
  plan in `plex_playlist` (mode `dry_run`, with the reason) and makes **no Plex call at all**, not even a read.
  `TestGated` checks each closed combination with a fake Plex that records every call (none), and `TestPlanAndSync`
  that an open gate creates the playlists and a second sync writes nothing.
- **Command:** `theatarr anime playlists [-franchise SLUG] [-dry-run] [-items] [-json] -db ... -env-file ...`.
  `theatarr serve` runs the same sync, behind the same gates.

Policy (`examples/policy/playlists.yaml`, validated in `internal/policy/playlists.go`):

```yaml
playlists:
  enabled: false
  orders: [watch, story]   # release, watch, story
  routes: true
  skip_optional: false
  name: "{franchise} ({order})"
  interval: 6h             # for theatarr serve, minimum 1h
```

## Automatic builds (`.../anime/autobuild`)

`theatarr serve` builds franchises on its own while `metadata.anime.auto_build` is on (default true) and the policy
has the anime sources (`anilist`, `kitsu`, `thexem`, `anime-lists`, `fribb`, `anibridge`, `manami`; the gateway
refuses any other target). Building only writes mappings; episodes whose mapping is not settled import through
TVDB's numbering, and their conflicts show in Admin › Reviews. With a source missing, the "Anime builds" health
check says which, and nothing builds; manual builds are refused the same way.

**Discovery.** Every anime series (`title.anime`, kind series, with a TVDB id) that no franchise maps into gets a
discovery pass, one at a time:

1. Covered when a franchise has a mapping into the TVDB series, or an entry for one of its AniList ids (a franchise
   whose mapping was rejected still covers it), applied or in a refresh held for review (`pending_result`).
2. The series' AniList ids: ScudLee's records for the TVDB id, through Fribb (else manami) from AniDB to AniList.
   None is `no_mapping`.
3. A franchise in the shipped list (`<overrides_dir>/franchises.yaml`) seeded by one of them is built under its own
   slug and scope (origin `list`); covered once a franchise built from that seed is stored (another franchise that
   merely has the slug does not cover it).
4. Otherwise an ad-hoc franchise (origin `auto`). The seed is the record ScudLee puts at TVDB season 1 (or absolute)
   with no episode offset, lowest AniDB id first; else the earliest AniList start date (ties: lowest id). The crawl
   walks prequels and sequels from any seed, so a later season still yields the whole line. The slug comes from the
   series name (`Re:Zero - Starting Life in Another World` → `re-zero-starting-life-in-another-world`), with
   `-<tvdb id>` appended when another franchise has it or the shipped list reserves it (built or not), or
   `tvdb-<id>` for a name with no ASCII letters. When the finished crawl reaches a shipped franchise's seed, that
   franchise is built instead, under its own slug (the series is covered by it when it is stored already).

Before a new franchise is saved, the job checks ownership again (builds run one at a time, so the check holds until
the save): a franchise built from the same seed meanwhile, or one that has the seed as an entry (applied, or in a
build held for review: a seed queued beside another of its line), covers it and the job finishes without a
franchise, and a slug a franchise of another seed took (stored, or reserved by the shipped list) fails the build.

The outcome per title is kept in `anime_discovery` (`queued`, `built`, `covered`, `no_mapping`, `failed`, with the
franchise and the error). A title is tried again after `refresh_interval` (an hour when its job was lost while
`queued`), so a series the lists learn later is picked up.

**Refresh.** A franchise built more than `metadata.anime.refresh_interval` ago (default 168h, minimum 24h) is rebuilt;
one at most every interval / franchises, the one attempted longest ago first, so the rebuilds spread over the
interval. Each rebuild records `last_attempt`, `last_outcome` (`ok`, `failed`) and `last_error` in the franchise's
config; one that failed is not tried again for a day, so a franchise that keeps failing does not take every slot. A
franchise with a refresh held for review (`pending_result`) waits for the review. The origin (`list`, `auto`, `manual`) and the
TVDB id are kept in the franchise's config, so a rebuild uses the same seed and scope; a shipped franchise takes its
current list entry.

**The job.** Builds run as `anime_build` jobs (dedupe by TVDB id, slug or seed; two attempts), one at a time in the
process: a second job that finds a build running is put back a minute later without counting an attempt. The loop
(every minute, first after two minutes) queues only when no `anime_build` job is queued or running. A job builds
through the same live sources as the CLI and saves like `-save`: a rebuild that drops episodes or moves linked
mappings is held for review. The lists (about 90 MB) are parsed once per process, kept a day, and released after
30 idle minutes.

**Screen and API** (`internal/web/animebuild.go`, admin only). Admin › Reviews › Anime mappings shows each franchise's
last build, its next refresh (built_at + interval while automatic builds are on and no refresh waits for review), the
latest build job while it is queued or running (or its error when it failed) and a **Rebuild** button; the franchise
page has the same. Below, **Anime series without a franchise** lists the discovery candidates with the last outcome,
its detail and time, and a **Build** button, with a note when builds cannot run (the missing source) or
`auto_build` is off (manual builds still work). The lists and the franchise page poll every 5 s while a job is queued
or running, else every 30 s while the page is open (so builds the loop starts show up); when a build job a list showed ends, the franchises,
the franchise page, the review items and the uncovered list are all fetched again.

- `POST /anime/franchises/{slug}/build`: queue a rebuild; 202 `{"job_id", "slug"}` (the same job while it is queued).
- `POST /anime/build`: `{"tvdb_id": n}` queues a discovery for an anime series (404 when it is not one);
  `{"anilist_id": n, "slug": "...", "name": "..."}` builds from an AniList seed (origin `manual`; slug from the name
  when empty). The seed decides first: a seed a stored franchise was built from or has as an entry queues a rebuild
  of that franchise, a shipped seed builds the shipped franchise under its slug, and a seed already queued keeps that
  job; the response's `slug` is the one the build uses. 409 when a franchise of another seed has the slug, the shipped
  list reserves it, or a queued build of another seed claims it. Both ids, neither, or a slug/name with `tvdb_id` is a 400.
- `GET /anime/uncovered`: `{"builds": {ready, off, auto_build, refresh_interval}, "items": [...]}`.
- A missing source (or no running builder) answers 503 and queues nothing. `GET /anime/franchises` adds
  `next_refresh` and `job` to each franchise.

## Golden outputs

The golden outputs (three franchises in `internal/metadata/anime/testdata/offline`) were produced by a Python
implementation of the same pipeline. Each build matches its `out/<slug>.json` (every field: entries, notes,
mappings, TMDb checks, excluded neighbours, gaps, the three orders, the community comparison) and `out/<slug>.md` (byte for byte).
The comparison treats a missing key and `null` alike and ignores Theatarr's additions (`mapping_decision`, `stages`).

| Franchise | Entries | Release / watch / story rows | Moves | auto | arbitrated | conflict | heuristic | rejected | JSON | Markdown |
|---|---|---|---|---|---|---|---|---|---|---|
| Made in Abyss | 14 | 10 / 11 / 10 | 0 | 12 | 0 | 0 | 1 | 0 | same | same |
| Re:Zero | 16 | 14 / 14 / 14 | 1 | 14 | 0 | 0 | 0 | 0 | same | same |
| Tensei Slime | 18 | 18 / 19 / 19 | 0 | 9 | 0 | 0 | 5 | 0 | same | same |
| Demon Slayer | 24 | 24 / 24 / 23 | 0 | 16 | 0 | 0 | 2 | 1 | same | same |
| Monogatari | 23 | 22 / 23 / 32 | 9 | 12 | 0 | 4 | 1 | 0 | same | same |
| Fate (Stay Night) | 24 | 24 / 24 / 15 | 0 | 18 | 1 | 0 | 0 | 1 | same | same |
| Attack on Titan | 51 | 48 / 50 / 49 | 1 | 41 | 0 | 0 | 2 | 0 | same | same |
| Jujutsu Kaisen | 12 | 12 / 15 / 15 | 1 | 8 | 0 | 0 | 2 | 1 | same | same |
| Mushoku Tensei | 9 | 9 / 10 / 10 | 0 | 7 | 0 | 1 | 0 | 0 | same | same |
| Dragon Ball Z | 42 | 42 / 60 / 45 | 0 | 18 | 19 | 2 | 2 | 0 | same | same |
| Haikyuu!! | 18 | 18 / 19 / 19 | 0 | 14 | 2 | 0 | 0 | 0 | same | same |
| Sword Art Online | 23 | 23 / 25 / 25 | 2 | 14 | 1 | 0 | 3 | 0 | same | same |

`TestGoldenFixture` runs Re:Zero, Mushoku Tensei and Made in Abyss on every `go test` from an offline cache
(`internal/metadata/anime/testdata/offline`, 690 KB: only the responses and list records these builds read, only the
fields used; sources and licences in its README) and compares with their golden outputs.

What it took to be exact, rather than close:
- **CPython set order.** The crawl and the manami walk iterate Python sets of ints; the order decides which path
  reaches a node first (its kind, direction and "found via"). `pyset.go` emulates CPython 3.10's set table (linear
  probing, perturbation, resizing) and is checked against orders recorded from CPython.
- **Python's `\b`** is Unicode-aware; titles include Japanese. `pyre.go` checks boundaries Python's way.
- `date.fromisoformat`'s strictness, `sort_date` padding, `round(x, 2)`, `repr()` of floats, lists, tuples and
  strings inside notes, `urllib.parse.quote` in the Radarr search keys, `list.index`/`remove` by equality in the story
  order, shallow `dict(e, ...)` copies on split.

**Deviations from the Python behaviour** (also in the package doc):
- `topo`: the golden outputs walk successors as a Python set of strings (order varies with `PYTHONHASHSEED`); the
  engine walks them in edge order, which is the same on every run. Ready nodes are re-sorted by date before each
  pick, so only ties could differ; none do in the twelve example franchises.
- `mergeDuplicates`: read whole, Radarr's `inCinemas` (`2019-01-04T00:00:00Z`) never parses, so "TMDb picked from
  grouped AniDB ids" would never run. The engine uses the date part, as `checkTMDb` does. No output of the twelve
  example franchises changes.

## Sources and caching

| Source | Offline (`offlinecache`) | Live (`live`, through the gateway) | Fresh for | Pace |
|---|---|---|---|---|
| AniList GraphQL | `cache/anilist` by the request body | POST, batched 50 ids, per franchise on demand (no mirroring) | a day if any media in the batch is airing or announced, else 30 days | 2.1 s (30/min) |
| Kitsu | `cache/kitsu` | GET, the same request | 7 days | 1/s |
| TVDB | `cache/skyhook` (SkyHook, TVDB-shaped) | the TVDB client ([tvdb.md](tvdb.md)), else **an imported Sonarr** (`animestore.SonarrTVDB`); `-tvdb-cache` adds a SkyHook-shaped cache behind it | — | — |
| ScudLee, Fribb, anibridge, manami | `cache/static` | GET daily, conditional (ETag/Last-Modified) | 1 day | — |
| TheXEM | `cache/xem` | GET | 7 days | 1/s |
| AniDB titles dump | — | Reserved fetch/cache policy; no active titles-dump caller | 1 day | — |
| SeaDex | — | `live.SeaDex` per AniList id | 1 day | 2 s |
| Movie lookup | `cache/arr` (Radarr's lookup) | the TMDb client: by id, and `search/movie` for entries without an id (`tmdb.SearchMovie`, adult titles left out) | movies per the client's TTLs; searches a week | TMDb client's |
| Sonarr library | `cache/arr` | offline cache only; live builds have no "in Sonarr" check | — | — |

Responses go into `metadata_cache` (gzip, keyed by method + URL (+ body hash for POSTs), with `etag` and
`last_modified` from 00017). A stale entry is revalidated with `If-None-Match`/`If-Modified-Since`; a 304 extends it.
404s are cached for a day. **Breaker per source:** three consecutive failures skip the source for 5 minutes,
doubling to an hour; meanwhile stale cache is served, and with nothing cached the call fails (`ErrSourceDown`).
AniList GraphQL errors in a 200 are not cached. The gateway's own budgets and back-offs apply on top.

**TVDB from an imported Sonarr**, when no TVDB key is set or TVDB fails: only series some imported Sonarr has are known (a mapping to
another series is dropped as "TVDB does not have"); Sonarr's API has no airsBefore/After, so TVDB-only specials are
placed by date; no overviews, so the overview recap hint is silent. The TVDB v4 client sits behind the same `TVDB`
interface; `TVDBChain` combines backends ([tvdb.md](tvdb.md)).

## Storage (migration 00017)

| Table | Holds |
|---|---|
| `anime_franchise` | slug, seed, scope, the applied build (gzip JSON), a held `pending_result` and its source |
| `anime_stage` | each stage's output of the applied build (crawl, joins, mappings, flags, orders) |
| `anime_node` | the franchise graph: every AniList work met, as line/branch/side/leaf or excluded (with why) |
| `anime_entry` | entries with ids, flags, target, TMDb ids and the entry JSON |
| `anime_mapping` | per entry: status, winner, TVDB series and episodes, reason, candidates with scores |
| `anime_order` | release, watch and story rows (alternates after the main order) |
| `anime_review` | the review queue (one open item per franchise, entry and kind) |
| `metadata_cache` | + `etag`, `last_modified` |

Mappings used to import: `anime_mapping.status IN ('auto', 'arbitrated', 'override', 'heuristic')` (see "Import
gating").

## Storage (migration 00018)

| Table | Holds |
|---|---|
| `anime_alt_cut` | an overrides file's alternate cuts: name, TVDB series, entry, match, the number → episodes table |
| `anime_review.choice` | the candidate accepted instead of the proposed mapping |
| `plex_playlist` | per franchise, order and route: name, Plex rating key, items synced, the last plan, mode (`dry_run`, `applied`, `failed`) and why |

## Storage (migration 00110)

| Table | Holds |
|---|---|
| `anime_discovery` | per anime series (title): its TVDB id, the last discovery outcome, the franchise built or covering it, the error, when |

## Commands

```sh
# offline, from an offline cache: Markdown like out/<slug>.md plus a "Mapping decisions" section
theatarr anime build -franchise re-zero -cache <dir>/cache [-json] [-out DIR]
# store it (held for review if it drops episodes or moves linked mappings)
theatarr anime build -franchise re-zero -cache ... -db /data/theatarr.db -save
# live
theatarr anime build -franchise re-zero -env-file .../theatarr.env -db /data/theatarr.db -save
# ad hoc
theatarr anime build -franchise one-piece -seed 21 -db ...
# the queue
theatarr anime review [-franchise re-zero] [-state open|accepted|rejected|superseded|all] [-json]
theatarr anime review -accept 12 -note "anibridge is right"      # or -reject 12
theatarr anime review -accept 12 -candidate 1                     # accept another candidate
theatarr anime review -snippet 12                                 # the overrides YAML that pins it
# playlists (a dry run unless library.enabled and playlists.enabled)
theatarr anime playlists -db ... [-franchise re-zero] [-dry-run] [-items]
```

The franchise list is `-franchises FILE`, else `<cache>/../franchises.json`, else
`<overrides_dir>/franchises.yaml` (the stored `metadata.overrides_dir`; a relative one resolves under the policy
directory, `THEATARR_POLICY`, default `/config/policy`). Overrides: `-overrides DIR`, else `<cache>/../overrides`, else
the stored `overrides_dir`. Commands read their settings from the `-db` database; they take no `-policy`.

## Live sources

What a live build needs:

1. **Policy** (`metadata.yaml` and `budgets.yaml`; the examples carry these):

   ```yaml
   metadata:
     sources:
       anilist:        {url: https://graphql.anilist.co, budget: anilist}
       kitsu:          {url: https://kitsu.app/api/edge, budget: kitsu}
       thexem:         {url: https://thexem.info, budget: thexem}
       anime-lists:    {url: https://raw.githubusercontent.com/Anime-Lists/anime-lists/master/anime-list-master.xml}
       fribb:          {url: https://raw.githubusercontent.com/Fribb/anime-lists/master/anime-list-full.json}
       anibridge:      {url: https://github.com/anibridge/anibridge-mappings/releases/download/v3/mappings.json}
       manami:         {url: https://github.com/manami-project/anime-offline-database/releases/latest/download/anime-offline-database-minified.json}
       github-assets:  {url: https://release-assets.githubusercontent.com}   # GitHub release downloads redirect here
       github-objects: {url: https://objects.githubusercontent.com}          # ... or here
       anidb:          {url: https://anidb.net/api/anime-titles.xml.gz}
       seadex:         {url: https://releases.moe, budget: seadex}
   budgets:
     anilist: {requests: 30, per: 1m}
     kitsu:   {requests: 60, per: 1m}
     thexem:  {requests: 60, per: 1m}
   ```

   The lists' URLs are taken from the policy; AniList's endpoint too. manami's database is archived; its last
   release still downloads, and a continuation URL can replace it here.
2. Optionally the TMDb key (`metadata.sources.tmdb`) for the TMDb checks.
3. Run, for example, `theatarr anime build -franchise re-zero -env-file ... -db ... -out <dir> -save`.
   The first run downloads about 90 MB of lists (cached a day); each franchise then takes AniList batches 2.1 s
   apart (5–15 requests) and Kitsu at 1/s.

## Limits

- Tie-break hints in the overrides and route playlists for the story order's alternates are not supported.
- Anime versions are not searched by scene numbering or per episode (indexer adapters ask TVDB seasons).
- There are no same-name Plex collections in custom order.
