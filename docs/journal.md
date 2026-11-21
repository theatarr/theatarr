# The grab journal

For an admin, or an agent working for one, checking what Theatarr grabbed and why, and finding what looks wrong. The
journal answers that without reading the code or the raw database. Code: `internal/journal`. Migrations
`00024_journal.sql` (decisions) and `00144_journal_flags.sql` (stored flags).

## Reading it

| How | Use |
|---|---|
| **System › Decision journal** (`/admin/system/journal`, admins) | The list in the UI, newest first, one line per thing that happened (below, "The list"), with **Flagged** for the entries a check found suspect (no badge or Status notice counts them: they ask for no action). An entry opens from its title link. The title page's Journal block (History tab) shows the same lines for the title. |
| `GET /api/theatarr/v1/journal` | List entries, newest first. Header `X-Api-Key: <key>`. |
| `GET /api/theatarr/v1/journal/{id}` | Get one entry. |
| `GET /api/theatarr/v1/journal/flagged` | `{"count": n}`: how many entries carry a `warn` or `error` flag (a count over the stored flags). The UI does not read it. |
| `theatarr journal [flags]` | The same list on a database file. Output is JSONL (one entry per line, then `{"next_cursor": ...}` when there are more). `-format text` gives a few lines per entry. `-id ID` prints one entry. `-all` follows the cursor to the end. |

Filters work the same way in the API and the CLI (`-flag` or `?param=`):

| Filter | Value |
|---|---|
| `since` | RFC 3339, `YYYY-MM-DD`, or a duration back from now (`72h`; the CLI also takes `30d`) |
| `title` | What a person typed (the list's "Title or id" box, `theatarr journal -title`): part of a title's name (case-insensitive); a number also matches the title with that id, so "2001" finds title 2001 and "2001: A Space Odyssey" ("1923" finds "1923" and "Hollywood in 1923"; the id counts even when its title is gone). Activity › History's typed box does the same. A title's own History, the episode window and **Open in the journal** pass `title_id` and show that title only |
| `title_id` | One title exactly, by its id: the title page's journal and its **Open in the journal** link (the list then says "Only <title>" with **Show every title**; typing a title replaces it); History by title uses it too |
| `version` | A version key (`movie-remux`) |
| `kind` | A comma list of kinds (table below) |
| `suspect` | `true`: only entries with a `warn` or `error` flag |
| `severity` | `info`, `warn` or `error`: only entries with a flag at least that high |
| `cursor` | The `next_cursor` of the previous page |
| `limit` | 1–500, default 50 |

`suspect` and `severity` read the stored flags (below), so a flagged page is as quick as any other. The CLI reads the
database only: it shows the flags `theatarr serve` stored and works out none itself.

### The list

The list (UI, API and CLI alike) shows one line per thing that happened. An entry another entry's line stands for is
left out of the list and named in that entry's `after` (What followed), with its own `ref`; it still opens by its id
(History's Journal links open it as before):

| Line | Stands for |
|---|---|
| A download's grab (`dl:<n>`, grab or upgrade) | Its failure (`dl:<n>:fail`), its import (`dl:<n>:import`) and its link and replace actions (`lib:<n>` with the download). The summary says what came of it: "Grabbed <release> on RD · imported, replaced <file>", "… · imported 8 files", "… · failed: <reason>", "… · waiting", "… · waiting for the mount". A pack's grab names its episodes, so the episode window still lists it |
| An adoption's first file (`lib:<n>`, the first action of its History row: one per title, library, release and day) | The group's other files, and the files the adoption replaced (History's own pairing, "History's folded rows"): "Adopted 8 files of Season 1 from Sonarr (sonarr_4k) · replaced 2 files". Files of one season adopted with a release each are one line each, as in History |
| A file's removal (`lib:<n>`, a replace without a download) | The hand unlink's action (`act:<n>`, `unlink_file`) of the same file, title and library within the minute after it: "Removed <file> · by <who> · moved to the trash". The files of one removal (a season taken out with its library) are one line each, beside the copy's own "Removed the copy" line |

An entry is left out only when the line that stands for it matches the same filter (`headParts`' parent column, checked
in the SQL that pages, so a page boundary never splits them). The parent has the entry's title and library, so only
`kind` and `since` can tell them apart: `kind=import` lists the imports, `kind=link` a download's link actions (their
grab is not a link), `kind=action` the unlinks; `since` after a grab lists what came after it on their own lines.
Flags live on History rows: a grab's line shows its own flags and those of its import and failure (the entry page as
well). The flagged view (`suspect`, `severity`) folds nothing: each flagged entry is its own line, so the endpoint's count
is the number of lines.

Every summary names files by their file names only; the full paths are in `paths` (the UI's tooltip, and listed on the
entry). On the list an adoption's line carries its first five files' paths and then "and N more" (read in the statement
that reads its History row, so no query is added per line); the entry lists them all. Stored messages and details
(failure and block reasons, import errors, provider attempts) are cut to 120 characters on the list and the stored
flags, and shown in full on the entry (`GET /journal/{id}`, `theatarr journal -id`), redacted the same way; text that
was stored already cut stays cut. `label` is the kind in History's words: Grabbed, Upgraded, Imported, Import held, Import waiting, Dry run,
Linked, Adopted, Replaced, Removed, Missing, Relinked, Failed, Stalled, Blocked, Search, Search after a failure, and an
action's own (Grabbed by hand, Download removed, Retried, Unblocked, Title removed, Removed; a copy's removal reads
"Removed from the library"). A grab's line reads Upgraded only once its download replaced a file, Failed when the download
failed, else Grabbed. An import refused together with its failed download reads Import failed, not Import held.
What followed lists every link and replace action of the download, dry runs and failed attempts too; only applied ones
count in the line's "imported N files" and "replaced".
A blocklist entry's What followed says "Search again requested" once its row was marked for a re-search and the
search it asked for is recorded (the queued `acquire.research` job of that row, waiting or run, until finished jobs are
pruned); a marked row without one (a library that could not write, a release added by hand, a pruned job) has no such
line. The entry page only: the list does not look it up. On the entry page a provider attempt's
reason is a second line under its outcome on a phone.

A good first pass:

```
theatarr journal -suspect -since 7d -format text
GET /api/theatarr/v1/journal?suspect=1&since=168h
```

## Entries

Each entry is one decision, or one thing that happened after a decision.

| Kind | What |
|---|---|
| `grab` | A release was chosen and sent to a provider or a download client |
| `upgrade` | The same, for a version that already had a file |
| `search` | A theatarr search that routed nothing: nothing was accepted, or the search failed |
| `research` | A theatarr search started by a failure |
| `failure` | A download failed |
| `stall` | A download failed because it stalled |
| `blocklist` | A hash went on a version's blocklist |
| `import` | The library imported a download (linked, rejected or dry run) |
| `link` | A library link action: link, replace, relink, or dead |
| `action` | A person acted by hand in the web UI (grab, remove, retry, unblock, unlink), with who did it |

The **id** names the source row. Ids are stable, so they work as keys in your notes:

| Id | Source row |
|---|---|
| `dl:<n>` | Download n |
| `dl:<n>:fail` | Download n's failure |
| `dl:<n>:import` | Download n's import |
| `lib:<n>` | Library action n |
| `bl:<n>` | Blocklist row n |
| `run:<n>` | Acquire run n |
| `act:<n>` | Web UI action n |

### Fields

| Field | What |
|---|---|
| `at`, `kind`, `source` | When it happened, and who decided or acted: `theatarr`, `router`, `library`, `journal` or `web` |
| `title`, `version` | The title and the library |
| `summary`, `label`, `paths` | One line saying what happened, in plain words with file names only; the kind in History's words; the full paths of the files the summary names (above, "The list") |
| `release` | Title, indexer, info hash, size, quality, score, formats, languages, episodes (`S01E02`), and `how` it was found (`Rss`, `Search`, `InteractiveSearch`; `acquire:<run>`, `rss`). A `link` entry for a link action carries the title and quality of its own download when it has one; a replace or a link without a download has none. Activity › History reads the journal in its own way (below) |
| `reasoning` | Why this release was chosen (next table) |
| `download` | Theatarr's download: state, provider, cached, reused, error |
| `attempts` | Every provider the router considered, with outcome, code and message |
| `cache` | Cache evidence for the hash, per provider: `cached`, `uncached` or `miss`, with the source (`feed:dmm-ad`, `check:torbox`, `add:rd`, `router:add`) |
| `import` | Library state, the files with runtime, resolution and audio languages (from the probe), and the links |
| `after` | What happened next, oldest first. Each event has a `ref` to its own entry when it has one |
| `related` | Ids of related entries |
| `flags` | Suspect checks that fired, as stored (below). Each has a `code`, `severity` and `reason`. Empty for an entry without a History row |
| `suspect`, `severity` | `suspect` is true when a flag is `warn` or `error`. `severity` is the highest one |

The `reasoning` object:

| Field | What |
|---|---|
| `chosen` | The release that was picked, with quality, score, formats, languages, rejections, `cached_on`, `kept` (for a pack, the episodes whose file it would keep), and `existing` (the file it would replace) |
| `runners_up` | The other kept candidates. That is up to 8 accepted ones in rank order, then the 4 rejected ones with the highest scores, each with its rejections. |
| `rejection_counts` | How many results each rejection reason (the arr's `DownloadRejectionReason` names) refused |
| `results`, `accepted` | How many releases the search found, and how many it accepted |
| `missing` | What is not recorded for this entry. Routes from before migration 00024 have no candidate list. A trimmed list is noted. |

Secrets never appear. Every free-text field is masked: URLs, API keys, passkeys, tokens and magnets come out as `[redacted]`.
GUIDs and download links are never read. Don't try to find them.

## Flags

Flags are worked out once and stored (migration 00144, `internal/journal/flag.go`). The events that give a History
row (below) write it with no flags yet; the journal then runs the checks on that row's entry, from what its sources
say at that moment, and stores the result on the row (`history.flags`, `history.severity`). They are not worked out
again while the row stands: a later change to the release, the title or the size medians leaves them as they were.
A row its writer rewrites in place (a partial import that later links, a failure after a retry, an overwritten
blocklist entry) loses its flags and is checked again. The journal does this
for every unchecked row in the background when `theatarr serve` starts (after an upgrade that is every existing row:
the checks are Go, so the migration leaves them unchecked), and for up to 50 new rows before each journal read (list,
entry, count; a read writes nothing when no row waits, and a batch is one transaction). Only the kinds the checks
look at are checked (grab, upgrade, failure, stall, import, blocklist); other rows keep `flags` NULL. The Flagged
filter and the count endpoint are a filter and a `COUNT(*)` on `history`; the Flagged list dates a grab by its entry (when the
download was created), as the full list does.

Only entries with a History row carry flags: grabs that reached a provider or client, failures, imports that linked
or were held for review, and blocklist entries. An import the guard refused together with its download has no row of
its own: its flags (file checks, `library_guard`) are stored on the download's failure row, and its entry shows them.
A grab that never reached a provider, an import that is a dry run or still waits for the mount, link and relink
actions, searches and actions by hand carry none. An entry whose source is gone (a release unblocked since, a library
removed, a failed download the nightly retention deleted) leaves the flagged view: its row's severity is cleared.

| Code | Fires when | Severity | What to do |
|---|---|---|---|
| `title_mismatch` | The release's parsed title is not close to the title's name, original title, aliases or scene names (similarity under 0.6). That is how mis-tagged releases get in. | warn | Compare the release name with the title. If it is another film or show, block it from its download in Activity. |
| `year_mismatch` | The release year is more than a year from the title's year, and isn't its secondary year | warn | Usually another film with the same name. Sometimes a re-release. |
| `episode_mapping` | A season the series doesn't have; an episode past the season's count; a season pack filed under another season's episodes; a release that names SxxEyy but was filed as another episode; an absolute number past the series' total | error or warn. info for anime, for series with scene numbering, and for an episode past the end of the latest season (listings lag) | Check the episode list. |
| `size_outlier` | MB per minute of runtime against the median grab of the same quality (needs 15 grabs of that quality, and the runtime from TMDb or the episodes). An anime pack's bytes are spread over every episode its stored claim names, also when the download was narrowed to some of them; a season pack, a span or a complete-series pack without a claim counts only when its episodes are every episode of the seasons its name states; otherwise it may have been narrowed and is left out of the check and of the median. Under 0.2× the median is an error, under 0.35× or over 4× a warning | error, warn | Very small means a fake, a sample or a re-encode mislabelled as Remux. Very large is usually harmless. |
| `language` | The release's languages hold neither the profile's language nor, without one, English or the original language | warn | Usually a dub. Multi-language releases pass. |
| `file_audio_language` | The imported file's audio tracks (probe) hold none of the wanted languages | warn with a profile language, otherwise info | TMDb's original language is sometimes not the spoken one. Check before you act. |
| `file_runtime` | The file runs under half the expected runtime | error under 0.2×, else warn | A sample or the wrong file |
| `file_resolution` | A 2160p release holds a file under 3000×1900, or a 1080p one a file under 1200×800 (either dimension passing is enough) | warn | Mislabelled quality |
| `sample_or_extras` | The release name says sample, trailer, featurettes, extras, bonus, or behind the scenes | warn | Check that the main feature is in it |
| `upgrade_lower_score` | An upgrade whose format score (scored with today's profile) is below the file it replaced, at the same or lower quality | warn. info for a hand grab | Often a profile change since then. |
| `upgrade_lower_quality` | An upgrade to a lower quality than the file it replaced | warn. info for a hand grab, and for the preferred SeaDex release (its reason ends "(preferred SeaDex release)"; flags stored before keep their warning) | Usually wrong, unless the old file was gone |
| `repeated_failures` | 3 or more failures (5 or more is an error) for the title and library in the 30 days up to this one. Only the latest failure of a run is flagged: every new failure (flagged or not) takes the flag from the earlier ones of the title and library in its window. | warn, error | Look at the failure messages and the cache evidence. Often the only releases are uncached or dead. |
| `library_guard` | The library's sample, runtime or mislabel guard rejected a file, or the mislabel guard noted one it kept | info | The reason says what the import did: blocklisted (a failed import; the re-search runs later and only while grabbing is on), the rest of the release linked (for a series the refused episodes stay wanted), or neither (held for review). A file skipped as not an upgrade is no guard refusal. A note refused nothing |
| `unparseable` | The parser can't read the release name (an anime pack whose stored claim names its seasons is read as the decision read it, "Seasons 1-2" as season 1's pack) | info | — |

Not flagged any more (0.4.5): a download still waiting after a day and an import waiting for the mount
(`download_stuck`: they depend on time passing, not on an event; Activity › Queue shows their age, and the stall check
fails a stalled download, which then has a stall entry), and dead links (`dead_link`: open dead links are on
System › Status under Dangling links, with relink and search; a stored flag would stay after the link is repaired).
Verdicts (Ok and Wrong) are gone; to block a wrong release, use its download's actions in Activity.

What to do:

1. List the flagged entries: `suspect=1`, newest first, and follow the cursor.
2. For each one, read `flags`, `release`, `reasoning` and `after`. Fetch the `related` entries you need.
3. Act on the release from Activity (block it, search again) or on the title page.

## Retention

The journal is mostly a reader, so its entries live as long as their source rows do. Its own tables:

- **`journal_decision`**, the candidates a route was chosen from. Kept in full for 180 days. After that, a decision
  that routed nothing is deleted, and the others keep only the chosen release and one runner-up. This runs nightly in
  `theatarr serve`, in the `db.nightly` job.
  - A full row is about 3–6 KB. At the default limit of 10 automatic searches an hour, the 180-day window is at most
    about 45 MB, and the trimmed history grows by about 9 MB a year after that. At the expected pace of tens of
    searches a day, the whole table stays under 10 MB.
- **`journal_action`**, the actions by hand, is kept for good.
- The flags live on the History rows, which are not pruned.

The source tables keep their own rules. Listing the whole journal takes seconds for thousands of entries; one entry
takes about a millisecond.

## History

Activity › History, the title page's History tab and the episode window's History are what happened to a title's
files (`Journal.History`, `internal/journal/history.go`; the episode window `Journal.EpisodeHistory`, the same rows that hold its episode), read
from the rows stored when each event happened (below), worded like Radarr's: grabbed (a download that reached a
provider or client, dated by `added_at` or its accepted attempt; grab and upgrade alike, classified only when `kinds`
is filtered), imported (one row per import, with the episodes its links held), adopted (adoption link actions grouped
by library, release and day), deleted (an upgrade: an import's replaced files, one row per download and library,
"Upgraded" with the new release and the list of files it replaced), removed (a replace without a download: an unlink by
hand), missing (a dead link), failed (a failed or stalled download, or an import held for review), blocked, the
provider copies' outcomes (`copy:<id>`, stored rows only since their source table went in 00144; not a journal entry) and title_removed. Searches, plans, dry
runs, mount waits and repaired links are journal entries only; the journal's own list still reads its sources.
Each row names its journal entry for the Journal link (`journal.EntryOf`: a held import's is `dl:<id>:import`; a copy
and a release unblocked since have none). A History row carries its entry's stored flags (above). The release facts of library actions
are those recorded with the action (migration 00129); older actions were stored with what a link row still held then.
An adopted row's score of 0 whose link has no scoring evidence (no stored formats and no non-zero score in its file
metadata, as the Libraries table reads it) is read as no score: the file was never scored. An adopted row names its
Sonarr/Radarr instance (`Source`, from "adopted from <instance> <path>").

### Stored History rows (migration 00140)

History is also stored as rows (table `history`, `internal/store/history.go`), written in the transaction that
records each event, and only for the events that give a row: a grab when a provider or client takes the download
(`store.HistoryGrab`, from the router's add, client add and reconciliation transitions; an add that is unmade, a
not-cached trial deleted and requeued, loses its row unless a provider took the download before), a failure
(`store.HistoryFailure`, from every write that fails a download), an import that links or is held for review
(`store.HistoryImport`; a held one `store.HistoryHeld`, its own `dl:<id>:held` row, so a later import that links
keeps it), a library action (`store.HistoryLibraryAction` from `Library.record`, which gives a row only for an
upgrade's replaced file, an adoption, a hand unlink or a missing file), a blocklist entry
(`store.HistoryBlocklist` from `router.Blocklist`; on Unblock its row moves to `bl:<rowid>:<at>`, so a new entry
that takes the rowid does not write over it) and a removal (`store.HistoryRemoval` from `RecordAction`, its outcome
from `SetActionDetail`). History writes are best effort: one that fails is logged and the event goes on.

A row keeps its source's journal id (`ref`: `dl:<id>`, `dl:<id>:fail`, `dl:<id>:import`, `dl:<id>:held`,
`lib:<id>`, `bl:<rowid>`, `act:<id>`, `copy:<id>`) and is rewritten when the same event happens again (a download
failing again, a release blocked again). Rows are a log: a requeued download keeps its failure row, an unblocked
release its blocked row, a held import its held row; they are not pruned. The migration backfilled the rows once from
the same sources (provider copies included; no new copy rows are written since "Add to other providers" was removed in 0.4.5; the action that came back in
0.4.5.6 writes the torrent journal, not History);
`store.BackfillHistory` runs the migration's statements for the demo seed; a test holds the event writers and the
backfill to the same rows (`internal/journal/history_compare_test.go`). Before the readers were cut over, the stored
rows were compared with the old reader (which derived History from the sources at read time) row for row on a copy
of the live database; the one intended difference is that an upgrade's replaced files are one row per download and
library (`Replaced` lists the files: action, time, path, release, episodes), not one row per old release.

### History's folded rows

One change of a file is one History row, folded when it is read (`historyWhere` and `Journal.History`; nothing stored
changes). The episode window reads the same rows (`Journal.EpisodeHistory`): those whose episodes hold its episode,
an upgrade's row also by its import's episodes (an episode the upgrade brought new), and a removal is folded there
only into an adoption that holds the episode too:

- An upgrade: the `deleted` row of a download and library stands for that download's import (`dl:<id>:import`, the
  same download and library). It shows the new release (its quality, score, and the import's hash, indexer, size and
  formats), the import's episodes (what the new release brought; the old files' are in `Replaced`) and state (a
  partial import says so, as on Imported), the replaced files in `Replaced`, and links to the import's journal entry;
  the import's own row is left out. A first import (nothing replaced) is one Imported row (its grab folded, below).
- A grab: a `grabbed` row is left out when a row of the same download with event `imported`, `deleted` (an upgrade)
  or `failed`, dated at or after it, is itself listed: it passes the event and kind filters (title, library and since
  follow from the download and the date), it is not an import folded into its upgrade (so Grabbed with Imported
  lists an upgraded download's grab), and in the episode window it holds the episode (an upgrade's by its import's
  episodes). That row says when it was grabbed (`HistoryRow.Grabbed`, the API's `grabbed_at`, admins only), read in
  the page's query from the grab's row (`dl:<id>`); a manual import's (indexer `import`: a file already on the mount)
  stored grab row is folded the same way but gives no time, since nothing was grabbed. Pairing is by download id
  only, never by release or hash. "At or after" keeps a retried download's new grab its own row while it
  runs; a grab with no outcome yet (still running) stays Grabbed. A Grabbed-only filter lists the grabs (no outcome
  passes it). The cursor bounds the grab, not its outcome, so a page boundary never shows a download twice.
  Household History, the compatible API's history and indexer statistics read the stored rows unchanged.
- An adoption that replaced a file: a `removed` row is left out when an `adopted` row of the same title and library,
  dated within a minute of it, holds one of its episodes (both none for a movie); the adopted row lists it in
  `Replaced` (path from its library action, release, quality, score, episodes). A removal with no such adoption stays
  Removed, and so does the removal of the adopted file itself (its link arrived within that minute; an adoption
  that takes over a link at the same path keeps the link's creation time, so that replacement still folds).

A folded row is left out whatever events are asked for, checked in the SQL that pages, so a row reads the same under
every event filter as under all events and a page boundary never shows half of a pair: an upgrade's import is never
listed as Imported (Activity's "Imported and upgraded" asks for imported and deleted; imported alone lists no upgrade),
and a removal its adoption replaced is never Removed. Only what can leave the partner out lists the folded row on its
own: a `kinds` filter without `link`, and the since bound (the partner shares the row's title and library). Rows at the same time keep their order: newest first by time, then
by journal id, so an upgrade or import shows above an unfolded grab.
