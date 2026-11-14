# The grab journal

For an admin, or an agent working for one, checking what Theatarr grabbed and correcting what looks wrong. The journal
answers that without reading the code or the raw database. Code: `internal/journal`. Migration `00024_journal.sql`.

## Reading it

| How | Use |
|---|---|
| **Activity › Decision journal** (`/activity/journal`, admins) | The list in the UI, newest first. An entry opens from its title link. |
| `GET /api/theatarr/v1/journal` | List entries, newest first. Header `X-Api-Key: <key>`. |
| `GET /api/theatarr/v1/journal/{id}` | Get one entry. |
| `theatarr journal [flags]` | The same list on a database file. Output is JSONL (one entry per line, then `{"next_cursor": ...}` when there are more). `-format text` gives a few lines per entry. `-id ID` prints one entry. `-all` follows the cursor to the end. |

Filters work the same way in the API and the CLI (`-flag` or `?param=`):

| Filter | Value |
|---|---|
| `since` | RFC 3339, `YYYY-MM-DD`, or a duration back from now (`72h`; the CLI also takes `30d`) |
| `title` | A title id, or part of a title's name |
| `version` | A version key (`movie-remux`) |
| `kind` | A comma list of kinds (table below) |
| `suspect` | `true`: only entries with an unacknowledged `warn` or `error` flag |
| `severity` | `info`, `warn` or `error`: only entries with an unacknowledged flag at least that high |
| `cursor` | The `next_cursor` of the previous page |
| `limit` | 1–500, default 50 |
| `scan` | API: how many entries one page may look at while filtering by `suspect` or `severity` (default 2000, at most 20000). The CLI has no limit. |

**With `suspect` or `severity`, a page can come back short, or even empty, and still carry a `next_cursor`.** Keep
following the cursor until it is gone.

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
| `plan` | A router dry run (a Watch-only library, or Pause on) |
| `search` | A theatarr search that routed nothing: nothing was accepted, or the search failed |
| `research` | A theatarr search started by a failure |
| `failure` | A download failed |
| `stall` | A download failed because it stalled |
| `blocklist` | A hash went on a version's blocklist |
| `import` | The library imported a download (linked, rejected or dry run) |
| `link` | A library link action: link, replace, relink, or dead |
| `verdict` | Someone judged an entry |
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
| `plan:<n>` | Router plan n |
| `v:<n>` | Verdict n |
| `act:<n>` | Web UI action n |

### Fields

| Field | What |
|---|---|
| `at`, `kind`, `source` | When it happened, and who decided or acted: `theatarr`, `router`, `library`, `journal` or `web` |
| `title`, `version`, `mode` | `mode` is the version's mode in the running policy: `active` (Live) writes, `shadow` (Watch only) only plans |
| `summary` | One line saying what happened |
| `release` | Title, indexer, info hash, size, quality, score, formats, languages, episodes (`S01E02`), and `how` it was found (`Rss`, `Search`, `InteractiveSearch`; `acquire:<run>`, `rss`). A `link` entry for a link action carries the title and quality of its own download when it has one; a replace or a link without a download has none. Activity › History reads the journal in its own way (below) |
| `reasoning` | Why this release was chosen (next table) |
| `download` | Theatarr's download: state, provider, cached, reused, error |
| `attempts` | Every provider the router considered, with outcome, code and message |
| `cache` | Cache evidence for the hash, per provider: `cached`, `uncached` or `miss`, with the source (`feed:dmm-ad`, `check:torbox`, `add:rd`, `router:add`) |
| `import` | Library state, the files with runtime, resolution and audio languages (from the probe), and the links |
| `after` | What happened next, oldest first. Each event has a `ref` to its own entry when it has one |
| `related` | Ids of related entries |
| `flags` | Suspect checks that fired. Each has a `code`, `severity`, `reason`, and `acked` once a verdict covers it |
| `suspect`, `severity` | `suspect` is true when an unacknowledged flag is `warn` or `error`. `severity` is the highest unacknowledged severity |
| `verdict` | The latest verdict |

The `reasoning` object:

| Field | What |
|---|---|
| `chosen` | The release that was picked, with quality, score, formats, languages, rejections, `cached_on`, and `existing` (the file it would replace) |
| `runners_up` | The other kept candidates. That is up to 8 accepted ones in rank order, then the 4 rejected ones with the highest scores, each with its rejections. |
| `rejection_counts` | How many results each rejection reason (the arr's `DownloadRejectionReason` names) refused |
| `results`, `accepted` | How many releases the search found, and how many it accepted |
| `router_plan` | The router's dry run: summary, and every step with provider, outcome, code and why |
| `missing` | What is not recorded for this entry. Routes from before migration 00024 have no candidate list. A trimmed list is noted. |

Secrets never appear. Every free-text field is masked: URLs, API keys, passkeys, tokens and magnets come out as `[redacted]`.
GUIDs and download links are never read. Don't try to find them.

## Flags

| Code | Fires when | Severity | What to do |
|---|---|---|---|
| `title_mismatch` | The release's parsed title is not close to the title's name, original title, aliases or scene names (similarity under 0.6). That is how mis-tagged releases get in. | warn | Compare the release name with the title. If it is another film or show, give a `wrong` verdict. |
| `year_mismatch` | The release year is more than a year from the title's year, and isn't its secondary year | warn | Usually another film with the same name (`wrong`). Sometimes a re-release (`ok`). |
| `episode_mapping` | A season the series doesn't have; an episode past the season's count; a season pack filed under another season's episodes; a release that names SxxEyy but was filed as another episode; an absolute number past the series' total | error or warn. info for anime, for series with scene numbering, and for an episode past the end of the latest season (listings lag) | Check the episode list. A wrong season or episode gets `wrong`. |
| `size_outlier` | MB per minute of runtime against the median grab of the same quality (needs 15 grabs of that quality, and the runtime from TMDb or the episodes). Under 0.2× the median is an error, under 0.35× or over 4× a warning | error, warn | Very small means a fake, a sample or a re-encode mislabelled as Remux. Very large is usually harmless. |
| `language` | The release's languages hold neither the profile's language nor, without one, English or the original language | warn | Usually a dub (`wrong`). Multi-language releases pass. |
| `file_audio_language` | The imported file's audio tracks (probe) hold none of the wanted languages | warn with a profile language, otherwise info | TMDb's original language is sometimes not the spoken one. Check before you act. |
| `file_runtime` | The file runs under half the expected runtime | error under 0.2×, else warn | A sample or the wrong file (`wrong`) |
| `file_resolution` | A 2160p release holds a file under 3000×1900, or a 1080p one a file under 1200×800 (either dimension passing is enough) | warn | Mislabelled quality (`wrong` if it matters) |
| `sample_or_extras` | The release name says sample, trailer, featurettes, extras, bonus, or behind the scenes | warn | Check that the main feature is in it |
| `upgrade_lower_score` | An upgrade whose format score (scored with today's profile) is below the file it replaced, at the same or lower quality | warn. info for a hand grab | Often a profile change since then. `wrong` if the new file is worse. |
| `upgrade_lower_quality` | An upgrade to a lower quality than the file it replaced | warn. info for a hand grab | Usually wrong, unless the old file was gone |
| `repeated_failures` | 3 or more failures (5 or more is an error) for the title and library in 30 days. Only the latest failure of a run is flagged. | warn, error | Look at the failure messages and the cache evidence. Often the only releases are uncached or dead. |
| `download_stuck` | A grab still queued, added or downloading after 24 h, or an import still waiting for the mount after 6 h | warn | Check the provider and the mount |
| `dead_link` | The library found a link whose target is gone, and no provider folder holds the file. Flagged once per finding, an unbroken run of dead observations of the link: a later scan that finds the same link dead again is a repeat and is not flagged, so a dry-run sweep does not raise a new flag every scan. Only the action log breaks a run: an applied link, relink or replace, or a gated revive, at its path, or a dead action with another target, starts a new finding with its own flag. A recovery that left no library action cannot start a new finding | warn | The relink sweep could not fix it. It needs a new grab. Settled flags can be acknowledged in bulk (below). |
| `library_guard` | The library's sample, runtime or mislabel guard rejected a file, or the mislabel guard noted one it kept | info | The reason says what the import did: blocklisted (a failed import; the re-search runs later and only while grabbing is on), the rest of the release linked (for a series the refused episodes stay wanted), a dry run (nothing happened), or neither (held for review). A file skipped as not an upgrade is no guard refusal. A note refused nothing |
| `unparseable` | The parser can't read the release name | info | — |

## Acting: verdicts

```
POST /api/theatarr/v1/journal/{id}/verdict
X-Api-Key: <key>
Content-Type: application/json

{"verdict": "wrong", "reason": "The Moon (2023), not Killers of the Flower Moon"}
```

- **Both verdicts acknowledge every flag the entry has right now**, so it stops being suspect. A flag that fires later
  is not acknowledged. The verdict is an entry of its own (`v:<n>`), and the judged entry shows it under `verdict` and
  in `after`.
- **`ok`** records that the decision was fine. That is all it does.
- **Settled dead-link flags in bulk.** `GET /journal/resolved` lists the open `dead_link` flags whose finding is
  the action log proves settled: superseded (a newer finding of the same link has its own open flag), or repaired
  (an applied link, relink or replace, or a gated revive, at its path after the finding's latest dead action, while
  the link is not marked dead and not in the latest dangling-link report). Nothing is inferred from a link row alone
  or from a report that does not list the path. `POST /journal/resolved/acknowledge` records an `ok` verdict on each (reason "Acknowledged in bulk:
  …"); the entries stay, and nothing is blocklisted, searched or changed in the library. Reviews › Flagged decisions
  offers it as **Acknowledge N settled flags**.
- **`wrong`** always records. When the entry has a release hash, its library is Live and Pause is
  off, it also does two things in the same transaction:
  - blocklists the hash for that version (`router_blocklist`, source `journal`), so the router never routes it again for
    that version;
  - queues `acquire.research` for the title, and for series the entry's episodes. The item is then due for a search
    at once and starts as `router.wanted.per_hour` allows; one within `again_after` of the last search after a
    failure is skipped.
- **For a Watch-only library, or with Pause on**, `wrong` is only recorded. `action_note` says why.
- The response's `action` is `recorded` or `blocklisted`, and `research_job` is the job id when one was queued.
- **Nothing is ever deleted**, on a debrid provider or anywhere else, and the current link stays. The new grab
  replaces it as an upgrade, or you report it.
- You need an API key (it is recorded as `key:<name>`) or a browser session. A session also needs the CSRF token.
- You can't judge a verdict entry. A reason is required, up to 1000 characters.

What to do:

1. List suspects: `suspect=1`, newest first, and follow the cursor.
2. For each one, read `flags`, `release`, `reasoning` and `after`. Fetch the `related` entries you need.
3. Give a verdict on the decision entry itself: the `grab`, `upgrade` or `plan` entry, not the `failure`
   or `import` that followed. That entry carries the hash and the version.

## Retention

The journal is mostly a reader, so its entries live as long as their source rows do. Its own tables:

- **`journal_decision`**, the candidates a route was chosen from. Kept in full for 180 days. After that, a decision
  that routed nothing is deleted, and the others keep only the chosen release and one runner-up. This runs nightly in
  `theatarr serve`, in the `db.nightly` job.
  - A full row is about 3–6 KB. At the default limit of 10 automatic searches an hour, the 180-day window is at most
    about 45 MB, and the trimmed history grows by about 9 MB a year after that. At the expected pace of tens of
    searches a day, the whole table stays under 10 MB.
- **`journal_verdict`** is kept for good.

The source tables keep their own rules. Listing the whole journal takes seconds for thousands of entries; one entry
takes about a millisecond.

## History

Activity › History and the title page's History tab are what happened to a title's files (`Journal.History`,
`internal/journal/history.go`), worded like Radarr's: grabbed (a download that reached a provider or client, dated by `added_at` or its accepted attempt; grab and
upgrade alike, classified only when `kinds` is filtered), imported (one row per import, with the episodes its links
held, as recorded on its link actions), adopted (adoption link actions grouped by library, release and day), deleted (an import's replace actions,
grouped by old release; "Deleted (upgrade)" with what replaced it), removed (a replace without a download: an unlink
by hand), missing (a dead link), failed, blocked, and the provider copies' outcomes (`copy:<provider_copy_event>`, not
a journal entry). Searches, plans, dry runs, mount waits and repaired links are journal entries only.

History reads the same sources as the journal but not its entries: every filter (title, scope, kind of title, library,
since, events, cursor) is applied inside each source, which gives at most a page of its newest rows, and the page's
facts are read in batch (about a dozen queries a page whatever its size; building journal entries took ten or more a
row: decision, attempts, cache evidence, what followed, failures, checks, verdict). History rows carry no suspect
flags. The release facts of library rows are stored on `library_action` (`release_title`, `quality`, `score`,
`title_id`, `episodes`, migration 00129) when the action happens, since a later file can take over a link row at the same path; older actions
fall back to a link row that still holds the file (an unlinked row, an adopted one, a dead one) and are otherwise
unknown. A copy asked for without a title (a Torrents row) is recorded for the one title whose active link or
download holds its hash. Nothing is backfilled.
