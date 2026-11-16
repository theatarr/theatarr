# The web UI

The web UI is a React single-page app embedded in the binary (`web/`, served by `internal/webui`). It talks only to
the native API (`/api/theatarr/v1`). Where it sits: [overview.md](overview.md).

Two apps share one shell. The **library app** is for everyone: posters, discover rows, a title page, search and add,
activity in plain words, and requests. The **admin panel** is for the operator: system status, jobs, budgets,
indexers, providers, users and keys.

**References.** The genre, the information architecture and the visual language follow Scryer and Seerr, and code
from both is adapted under attribution (§9). Their branding is not used.
- **Seerr** (MIT): discover rows, poster cards with a status badge, a title page with backdrop,
  poster, overview and a request button, requests with approve/decline, permissions per user, settings kept apart.
- **Scryer** (GPL-3.0; its theme and components are adapted under attribution, see §9): a
  sidebar grouped into Overview / Catalogs (Movies, Series, Anime as separate facets) / Discover / Automation (Wanted,
  Calendar, Activity) / System (Users, Jobs, Logs); a command palette for global search; title art kept in its own
  store and served from its own route (`/images/titles/{id}/{kind}/{variant}?v=<hash>`, long cache) plus a token route
  for art of titles not in the library; requests with pending/approved/rejected/canceled and the art captured at
  request time; live updates over a subscription that the UI turns into toasts. Where Theatarr follows one of these, the
  text says "Scryer does X".

## 1. Surfaces

Marks: **U** user-facing (the library app), **A** admin panel.

### Native API (`/api/theatarr/v1`, `internal/web`)

| Route | Data | Mark | Note |
|---|---|---|---|
| `GET openapi.json` | the hand-written spec | A | stays; TypeScript types are generated from it |
| `GET status` | version, schema, row counts | A | System › Status / Backup |
| `GET titles`, `titles/{id}` | titles with version coverage, art fields, sort and facets (§6); one title in full | U | |
| `GET versions`, `versions/{id}` | versions by key, state, has_file | U | wanted lists; Home "Missing" row |
| `GET registry`, `registry/{hash}` | contents and everything that points at them | A | |
| `GET jobs` | counts and jobs | A | System › Tasks |
| `GET tasks`, `POST tasks/{key}/run` | the scheduled tasks; run one now | A | System › Tasks "Scheduled" |
| `GET gateway` | egress, budgets, back-offs | A | Settings › Indexers › VPN / Advanced › Request limits |
| `GET policy`, `policy/validate` | policy summary, validation | A | |
| `GET events` (SSE) | `jobs`, `gateway` and the user events (§6) | A | |
| `POST auth/login`, `auth/logout`, `GET auth/me` | session login, who am I (with role and permissions) + CSRF | U | |
| `GET/POST keys`, `DELETE keys/{id}` | API keys | A | per user (§3) |
| `GET anime/franchises`, `franchises/{slug}`, `review`, `review/{id}/snippet`, `POST review/{id}/accept|reject`, `GET anime/playlists` | anime metadata | A (review), U (franchise orders, playlists) | A playlist plan's items keep their order for every browsing account; their file `path`, `from` and `rating_key` need see_files (`TestAnimePlaylistPlanHidesFilesWithoutSeeFiles`) |
| `POST decide`, `POST import` | native decision and import for devices (`internal/arrview/nativeapi`) | A | API only; a "Test a release" box in Admin uses `decide` |

### arr views (`internal/arrview`, mounted at `/radarr`, `/sonarr`, `/v/<version>/{radarr|sonarr}`)

All `…/api/v3/…`: `system/status`, `qualityprofile`, `rootfolder`, `tag`, `movie`/`series` (+ lookup, POST/PUT/DELETE),
`episode`, `episodefile`, `moviefile`, `queue` (+ status, details, DELETE), `wanted/missing`, `wanted/cutoff`,
`history` (+ per title, failed), `release` (GET, POST, push), `parse`, `manualimport`, `calendar`, `indexer`,
`notification` (+ schema, test), `command`, `diskspace` and the rest in [`docs/design/arrview.md`](arrview.md).
**Not UI.** They serve Seerr, Homepage, decypharr, autobrr and the phone apps and keep their shapes. The UI never
calls them. The UI shares two things with them (§3, §6): the add path (`catalog.Add`) and the queue rows.

### Server (`internal/server`)

`/healthz`, `/readyz`, `/metrics`: A (machine routes; System › Status reads the same checks).

## 2. Information architecture

### Shell

- **Desktop:** a left sidebar, a top bar with global search (also `/` or Ctrl-K, as Scryer's command palette does),
  the system signals, and the user menu. The signals are for admins and come from endpoints the shell already reads:
  the **Pause** pill (below; "Paused (locked)" when `THEATARR_FORCE_OFF` forces it, the same words at every width,
  phones included) and the live status dot (VPN and what Tasks lists under Needs attention, "1 failed task": queue jobs and scheduled
  tasks alike; Status's "Failed jobs" check counts queue jobs only), whose words always show when it is bad ("VPN down").
- **Look**: tokens in
  `web/src/styles/globals.css` (ink on warm paper, warm charcoal in dark, one Prussian-blue accent, ok/held/broken
  colours, 40/32 px rows), with the shadcn names kept as aliases. Fonts are self-hosted
  @fontsource packages, nothing loads from another origin: IBM Plex Sans for the interface, IBM Plex Mono for keys,
  release names, paths and numbers, Newsreader for page and title names (one token, `--f-display`, so headings can
  switch to Plex Sans). The primitives (button, input, select, switch, checkbox, chip, tag, table, dialog, sheet,
  tabs, toast, skeleton, empty state) live in `web/src/components/ui`.
- **Phone:** a bottom tab bar: Home, Browse (a sheet with Movies, Series, Anime, Collections for managers), Activity,
  Requests (with its badge), More (a sheet with Wanted (managers and admins; Manual import admins only), Now playing,
  Calendar and the admin pages the account may open, top level only). Search stays in the header.
- **Sidebar** (`web/src/components/shell/nav.ts`): Home, Library (Movies,
  Series, Anime, Collections for managers and admins: `/collections`, docs/design/arrview.md), Requests, Activity, Wanted (managers and admins; Manual import admins only), Now playing, Calendar; then **Admin**: System, Torrents,
  Settings, Users & access, Advanced. A manager, who has no Settings, gets **Import lists** under Admin. **Setup** appears first only when `/setup` reports `first_run` or
  nonempty `missing`. Successful section, provider, indexer and library saves refresh this query, including metadata keys and mount changes. A Setup checklist link remains on Status and the Settings home footer.
  System, Settings and Advanced show children while their pages are open; System includes `/admin`
  and `/admin/system/*` (Status, Tasks, Backup, Logs, Decision journal). The phone More sheet lists the same
  pages top level only: System, Settings and Advanced are closed groups that open on a tap, their own page first. System's badge counts the attention jobs, the
  same on every page; Tasks keeps its own badge. The Decision journal has none: its flags are after-the-fact checks
  that ask for no action (its Flagged filter lists them). Users and managers never see Admin; users-only accounts see Users & access,
  and `/admin` takes them there. Sign-in still lands on Home.
- Each library gets its own colour mark (the dots on pills, tags, the title page, filters): the configured libraries
  in key order take the palette `--v-0` … `--v-9` in turn (`web/src/lib/library-names.tsx`), so five libraries get
  five marks and a reload gives the same ones; a key no longer configured keeps the accent. Bars and pills take the
  status colour.

### Library state

Every page that shows a library (title copies, the add drawer, Setup, Libraries settings and its tables, policy,
Status, Requests, Wanted) shows the same library state (`components/version-status.tsx` over `lib/version-grab.ts`).
Setup and Settings › Libraries always show it; rows and windows elsewhere (the episode window, the add drawer, the
libraries window, Requests, Status, Advanced › Policy, Connected apps) show it only when it says something (`quiet`):
Paused, Downloads off or "New files won't be linked", never a "Downloads on" on every row:

- one **verdict**, decided by the server's `will_grab`: **Downloads on** when it grabs; otherwise **Paused** (Pause on, or forced by `THEATARR_FORCE_OFF`) or **Downloads off** (a server hold, or a library not set
  up);
- **Why?**, a real button (keyboard reachable, `aria-expanded`), listing every closed gate, each with a link to where
  it is changed for admins (Settings › Libraries `?edit=key`, the Pause switch at the top of the page). Dense rows (library
  tables, request chips) show a small **?** button ("Why Downloads off?") instead; it opens the same list, with the
  admin links, in a portalled popover (Radix) that stays on screen outside clipping rows, by keyboard or touch;
  Escape closes it and returns focus to the button.

`can_search` and `search_reason` are shown too; their notes use the verdict words, and a server hold with
every displayed gate open keeps the server's reason beside **Downloads off**. Wanted takes each row's state from
`GET /status` (`versions[].grab_state`): rows read **Paused** while Pause is on, and a library's closed
download gate stays visible while the search loop is off. Until `/status` answers (or if it fails) Wanted says only
**Paused**, without naming a cause. The releases header names each monitored library's
verdict when nothing would be searched. While Pause is on, a page says so once at the top (`DownloadsOffNotice`,
from any row's `grab_state`) and the rows keep only their own verdict; the top-bar pill stays. Controls are named
**Pause**, **Send notifications** and, in the add drawer, **Add to these libraries**. The add drawer shows one row per library: a checkbox, then the name, key and
kind on one wrapping row and its state on the next, with a footer "n libraries · m will download" (ticked
libraries whose `will_grab` is true).

The add drawer's other choices each start at the value that sends the request of a plain add, so leaving them
alone changes nothing. A series not yet in the library gets **Monitor** (a select: the season ticks as picked, or a
preset: all, future or missing episodes, first or latest season, pilot only, none; a preset hides the ticks and
sends `monitor` without `seasons`, and a series without episodes yet takes it at its first refresh) and **Monitor
seasons that appear later** (on; off sends `monitor_new: none`). A new series has no episodes until its first
refresh, so `monitor_new: none` alone would leave the seasons that refresh finds unmonitored too; with no preset
and no season ticks the drawer therefore sends `monitor: all` with it, and only seasons that appear after the first
refresh stay unmonitored. The API itself stores `monitor_new: none` as given. Neither choice is offered for a series
already in the library, because the request names the libraries it already has and would rewrite their monitoring;
the API answers 400 to `monitor` there, naming the library title, and writes nothing. The footer counts the
ticked libraries ("2 libraries"; "Nothing new ticked" before a change) and ends with **Add** (`search: false`) and **Add and search** (primary; `search: true`, so the ticked
libraries that can search are searched at once); for a title already in the library they read **Add libraries** and
**Add libraries and search**. Only **Add** is offered when no ticked library can search, with a line saying adding
searches nothing, and only **Save seasons** when only a series' seasons changed. Nothing is remembered between adds.

A new series also gets **Series type** (Standard, Daily, Anime). The select shows the type the series gets without
a choice (Anime when Anime is ticked, else Standard) and the request carries `series_type` only when the pick
differs from it. It is not offered for a series already in the library: an add never changes a series' type, and
the API answers 400 to `series_type` there. Acquisition reads a series as anime by its anime flag or its type, so
the type Anime ticks the libraries of an anime series, as the Anime box does (the box itself stays as it is);
`POST /titles` without `versions` picks the same way, and libraries the request names are left alone. A movie with a library to add ticked gets **Minimum availability**:
**Library default** (the start; sends nothing, so each library's own rule applies) or Announced, In cinemas,
Released, sent as `minimum_availability` and stored as the explicit choice (`availability.Choose`, the library's own
`v/<key>` row) of each library the add adds the movie to, new or restored, whether or not that library has a
default; a library the movie already had keeps its rule.

### Status words

One vocabulary for what a title, a library copy or an episode holds, worked out in one place on each side: the
server's `titleAvailabilitySQL` (`internal/web/library.go`: list rows, `availability=` filters, facet counts, the title
page) and the SPA's `posterStatus` / `qualityWord` (`web/src/lib/library.ts`), `upgradeBadge` (`lib/upgrade.ts`) and
`cellStatus` (`lib/seasons.ts`). The same state says the same word on posters, the table's Status, the filters and
their counts, Home, the title page's status chip and Libraries table, season headers, episode cells and the episode
window. **In the library** means the title is in a library; **Available** means a file is held.

The status word (one per title, copy or episode):

| State | Word | Colour | Where |
|---|---|---|---|
| a file is held, and no monitored library has a gap (a series: every aired, monitored episode of every monitored library has a file), whatever its quality against the cutoff | **Available** (title page: "Available in 4K") | green `--ok` | posters, table Status, filter, title page, library rows, episode cells and window |
| a file is held, and a monitored library has a gap (a movie library with no file; a series library missing an aired, monitored episode) | **Partly available** | amber `--warn` | posters, table Status, filter, title page |
| no file, monitored, out (a movie past its home release; a series with an aired episode); an episode aired, monitored, no file | **Missing** | red `--bad` | posters, table Status, filter, title page, library rows, episode cells and window |
| no file, and no library monitors it; an episode with no file whose episode or library is not monitored | **Not monitored** | grey `--ink-4` | posters, table Status, filter, title page, library rows, episode cells and window |
| no file, monitored, not out yet (a movie's home release, else its TMDb release date, still to come; a series with nothing aired); an episode still to air | **Upcoming** (with the day), episodes **Not aired** | grey `--ink-3` | posters, table Status, filter, title page, library rows, season header, episode cells |
| in no library | none ("Not in the library yet" on the title page) | — | title page |

A gap in a library that is not monitored is no gap: it reads Not monitored and counts in no missing number (season
headers, library rows, coverage), for a series as for a movie. Wanted and searching are unchanged.

The quality word, beside the status word of something with a file and never in its place (those who see files):

| State (`UpgradeInfo.state`) | Word | Colour | Where |
|---|---|---|---|
| a better release was found (`upgrade_available`) | **Upgrade found** | accent | posters' year line, table Status (second line), library rows, season headers, episode cells, episode window, quality filter, Home's row |
| a file below its preferred quality, nothing better found (`below_cutoff`) | **Preferred quality not met** | neutral | title page: library rows, season headers, episode window |
| every file at its preferred quality (`cutoff_met`), or at the wanted quality with only the score under the target (`below_cutoff` with `quality_met`), nothing better found | **Preferred quality met** | green | title page: library rows, season headers, episode window |

A file at the wanted quality whose format score is under the profile's score target stays `below_cutoff` on the
server (searches, Wanted › Cutoff unmet and the lists' filters are unchanged); only the word changes. It keeps Why?
("The quality is reached, the score is not."), its episode tooltip has no quality word, and a series library row
leaves out "N episodes under preferred quality" and the found line beside it.

A better release that was tried after it was seen and failed (on every provider, or refused) is no longer
**Upgrade found** anywhere (title page, Home, the lists' Upgrade found filter); another untried better release still
is. Why? keeps "Better release found …" with "Tried …" for it (docs/design/manage.md, the upgrade-evidence rule).

List rows and posters say only **Upgrade found** (lib/library.ts qualityWord); the other two words are the title page's.
A library that does not monitor the title shows no quality word on its seasons and episodes (nothing is searched
there); a season with nothing held that nothing monitors reads **Not monitored**, not "0 / 10"; and the episode window
says "<Library> is not monitored for this title: nothing is searched by itself. The switch keeps your choice." in
place of the library's download state. In the series table, a series **Available** that holds every monitored episode
but not every episode says what Available counts under the word ("6 of 6 monitored · 22 episodes"; "6 of 22 episodes
· 16 not monitored" while monitored episodes are still to air), in the word's tooltip when it has several libraries.

**Why?** is headed by the quality word that opened it, then three plain lines (`whyLines` in `lib/upgrade.ts`), the
same layout everywhere: "You have: Remux-2160p, score 8,575." (the file's quality name; no score when never
recorded); "Upgrades stop at: Remux-2160p and a score of 10,000." (the profile's own cutoff name, a group's name as
it is; "Upgrades are off in this library's profile." when it allows none); then the verdict and what was found. The
verdict comes from the server's `quality_met` (the stored quality-cutoff judging, so quality names are never compared
in the page): "Below the wanted quality." or, when the score falls short, "The quality is reached, the score is
not."; nothing for a prefer-SeaDex library, which the server does not judge that way. What was found: "No better
release seen yet (last search 2 d ago)."; "Better release found 4 d ago: <release> (WEBDL-2160p, score 6,200): better
format score, 6,200 over 4,500." (the reason stored with the search that saw it, about the file it was judged
against); or, when the library's history on the page has a failed download of that release, "… Tried Nov 12: not
cached on RD, AD and TB. Your file is unchanged." (the failure as the history row says it). Without such a row
nothing is said about downloading: the history on the page is capped. **Show releases** follows. The Libraries table's Details and the episode window say the same "Better
release found …" sentence.

Filters on Movies, Series and Anime (those who see files): **Available**, **Partly available**, **Missing**, **Not
monitored**, **Upcoming** (`availability=`), and apart from them **Upgrade found**
(`upgrade=found`), each with the facet count of exactly the titles it lists; the five status counts add up
to the list. An old saved `availability=upgrade` ("Preferred quality not met") opens as Available. Accounts without
see files keep watchability: Not available, Some episodes, Available (Getting ready and Upcoming on posters).

A score shows only with scoring evidence: a link's score is shown when its import decision or adoption stored a
non-zero calculated score or matched formats (an empty list too, so a calculated 0 shows 0), and is blank otherwise,
a stored 0 without formats included (`linkScoreSQL`), in the Episodes table, the
episode window, cell tooltips, the Libraries table and Why?.

### Failure reasons

A failed or refused download says why in one wording everywhere: the queue row and Inspect, the title page's
"Last upgrade attempt <date>: <headline in lower case>" line and its attempt **Details**, System › Status, the
Blocklist's second line and History's Failed rows. One server function (`failureWords`, `internal/web/grabbed.go`)
turns what is stored (the download's error, the library import's state and reason, the blocklist row's reason, the
file refusals the import's plan saved, the providers the download's own attempts found it not cached on) into a headline and, when it is stored, a detail:

| Stored | Headline | Detail |
|---|---|---|
| not an upgrade (`not an upgrade for the existing files`, or an upgrade-check rejection inside the old `no file passed the sample guard (…)`) | Not imported: these files do not improve on the files you have | "This release scores 1,202, yours 6,200. Nothing was replaced." (scores or qualities when stored) |
| a coverage refusal ("Episode file on disk contains more episodes …": replacing would lose episodes) | Not imported: the files you have cover more episodes | Nothing was replaced. |
| several files refused for different reasons (every refusal the import's plan saved is read; a pack's extras refused as samples are left out when other files were refused for another reason; the scores are shown when every file compared the same) | Not imported: the library refused its files | Different files were refused for different reasons. |
| a sample or short runtime | Not imported: the file is a sample or too short | the measure: "The file runs 4 min, under the 10 min minimum." |
| `mislabelled …`, a resolution that contradicts the label | Not imported: the video does not match its quality label | "Labelled 2160p, but the video is 1920x1080." |
| runtime of 0 | Not imported: not a valid video file | |
| no file matched | Not imported: none of its files matched the wanted episodes | |
| the old wrapper with no rejection recorded | Not imported: no file passed the library's checks | |
| miss on every provider | Not cached on RD, AD and TB (only the providers the attempts establish), else No provider accepted this release | |
| stalled | Download stalled | Deleted from RD. / Kept on RD. |
| ready timeout | Finished at the provider, but its files could not be imported within 6 hours | |
| provider status | RD reported an error | Its status: magnet_error. |
| anything else | the stored text without its "library: " / "provider " prefix | |

An import held for review (the download is ready and waits) says the same sentence with "Held for review:" in place
of "Not imported:", everywhere it is shown. The stored text stays as **Technical details** in Inspect and the attempt Details (accounts that see files; the
existing redaction applies). Household accounts get none of it.

### Library and activity pages

- **Library lists** use the full width, with the poster and table views (see **Library views** below). The table has
  one line per title and a **Columns** chooser (one checkbox per column, saved per list, browser and user in
  `theatarr.library.columns.<list>.<user>`). While the table fits its frame it clips so its header sticks; when the
  chosen columns make it wider (measured, not guessed from the viewport) it scrolls sideways inside its frame, so
  every column stays reachable, and the page itself never scrolls sideways.
- **Upcoming**: a missing movie that is not out at home yet reads **Upcoming · <date>** instead of Missing, on
  library lists, posters, Home and Discover › Upcoming, all through the one rule (`upcomingRelease`): the row's
  `home_release` (the earlier of the digital and disc dates, else the cinema date plus 90 days; the date Theatarr's
  released minimum waits for, `availability.HomeRelease`) strictly after today, else, for a row without one, its TMDb
  release date strictly after today (the UTC day, as the server's Missing row and fetching count it). A film in cinemas with its digital release to come reads Upcoming until that
  day. It is still wanted; only the word changes, and Wanted's counts do not. `home_release` is per title.
- **Release dates**: one choice of a movie's dates (`activity.FirstReleasesSQL`, `internal/activity/calendar.go`) for
  Home's Upcoming row, the Calendar, the lists' **Next release** and **Release date** columns and their sorts, the **In
  cinemas**, **Digital release** and **Physical release** columns and the title page's About facts and Details ›
  Release dates. It is the **primary release**, as Radarr has it: a movie's primary country is the US when TMDb lists
  a US cinema, digital or physical date, otherwise the country with the earliest of those dates (the original
  release). In cinemas is that country's first wide cinema date (else its first limited one, else the first cinema
  date anywhere, which a re-release never is); digital and physical are that country's first date of each type and
  never another country's, so a country re-release does not put an old film on the Calendar or give it a next
  release. A title without a TMDb refresh shows the arrs' dates (country empty), else TMDb's stored rows. A later date
  of a type (a re-release, a streaming service picking an old film up) and premiere or TV releases are never a type's
  date. A movie's next release is the earliest of its three dates today or later (in cinemas first on a shared day);
  TMDb's release date stands in only for a movie with no chosen date at all; the **Next** sort orders by exactly
  that day. A movie's **Release date** (lists) and About's **Released** are its first cinema date, else TMDb's
  release date (**Releases** while still to come). On the Calendar a movie's cinema date alone is never Missing;
  joined with a digital or disc date of the same day it is. Only the primary country's TMDb rows are stored
  (`title_release_date`, source `tmdb`); Details › Release dates lists them (with the arrs' rows for a title not
  refreshed), the country named once under the table; a refreshed title's cinema date taken from the first release
  anywhere is marked "first release, not US".
  Display only: what may be fetched reads `availability.Dates` (the `tmdb-art` schedule, `tmdb.ReleaseSchedule`) and
  `home_release` (above), unchanged; the arr-compatible API's dates are those too.
- **Added and Latest file**: **Added** is the day the title was added to Theatarr (lists' Added column and sort,
  poster detail); **Latest file** is when the newest current file came (lists' Latest file column and sort, Home's
  **Latest files** row with "File added …", About's Latest file). The **Quality** sort orders by the quality each
  library's cell shows (the one most of its files have), the best of the libraries, not the best single file.
- **Family view**: accounts without See files get the table's Status column headed **Availability** (Available,
  Some episodes available, Not available, Upcoming, Getting ready) and are not offered the Libraries, Quality,
  Quality profile, Size, Latest file, Minimum availability, Provider or Folder columns; posters show the badge only,
  without bars or library pills; on a series' Episodes tab each season shows one combined summary. Accounts that
  see files keep the per-library detail. A missing title with a download in hand (the row's `acquiring`: a copy's acquisition that
  is not blocked and not an upgrade; the title page's `acquiring` too) reads **Getting ready** there, on posters and
  Home rows and in the title page's readiness chip, before Upcoming; a failed download is not in
  hand, so it stays Not available;
  accounts that see files keep their stage chips.
- **Title page**: Newsreader title, the status chip (for those who see files the title's status word, "Available in
  4K"; for everyone else its watchability: "Ready to watch in 4K", "Some episodes ready", "Not available yet" in a
  library without a file, "Not in the library yet" in none), small-caps fact rows; the action bar and the **Libraries**
  table sit under the hero. Pause is said once for the page, under the action bar, and the Episodes tab leaves it to that notice.
- **Activity**: for accounts that see files, Queue, History and Blocklist are tables (shared `DataTable`) whose rows
  open into their details in place; household accounts and "View as a user" keep the simple rows. **Downloading**
  counts only provider transfers in progress; starting (the provider add) and linking count under **Waiting**, the
  filter their library stage links open; failures count under **Needs attention**. On phones the tables become
  cards and the History filters stack one per line with the date input full width.
- **Decision journal**: on phones, where the kind and library columns hide, each entry shows its kind (label) and
  library inline under the title.
- **Colours**: the library marks (`--v-0` … `--v-9`) have light and dark values, each at least 3:1 on the page and
  the surface. Calendar entries are surface chips with a 3 px edge coloured by status (`--ok` available, the accent
  getting ready, `--bad` missing, grey upcoming and not monitored; accounts without see files get only green or grey),
  with a legend under the header; the media kind is the badge text. Labels over artwork (poster chips, hover shade,
  Continue watching) sit on `--art-scrim`, which is dark in both themes. Modal backdrops use `--scrim`.
- Wanted, Collections, Requests and Home use the shared Card, Chip, Tag, Checkbox and Select primitives.

### Other pages

The remaining pages use the same tokens and primitives:

- **Search** (page and overlay): a Design A search field, underline kind tabs, ruled result rows with Button icon
  actions, and the kind as a square chip with its facet mark (`FacetPill`, also in the add and request drawers). On
  TMDb posters only the kind chip sits on the art scrim; In library and Requested sit under the year, since a status
  chip beside the kind does not fit a phone-width poster.
- **Login**: a ruled card, a Newsreader heading, Sign in with Plex as the primary button (no Plex orange), and the
  error and pending notices as edge-marked notes.
- **Users & access**: the role select, Discord id input and switches are the primitives; the "Requests ask for"
  libraries are square toggle chips.
- **Switches**: the monitor switch (library rows, title page, episode drawer, collections) and the add drawer's
  Monitor seasons that appear later use the Switch primitive (size `sm` for dense rows); a locked monitor switch stays
  focusable (`aria-disabled`) with its reason as the title. The add drawer's switch row is its whole label.
- The confirmation dialog, filter sheet and dropdown menu use the Button, Checkbox, Select and token shadows; the
  components use the short tokens.
- Sheets and dialogs (bulk monitor, copy, view options, episode drawer, upgrade why, add and request drawers) use
  the 8px panel radius and the shadow token. A schema boolean in a settings form is a Switch with its state beside it
  ("On", "Off", "On (default)" while nothing is stored).
- Keyboard focus: `outline-none` sets Tailwind v4's outline style to none, so a primitive that removes the browser
  outline also sets `focus-visible:outline-solid` with its 2px focus outline (Button, Switch, Checkbox, tab triggers and
  the focusable tab panel, search fields).
- At 400 px no page scrolls sideways. Tables with long keys wrap them (`overflow-wrap:anywhere`; `break-words` does
  not shrink a table cell); the indexer rows' phone grid drops the cell height. Request budgets (settings and Request
  limits) and gateway back-offs stack below sm as labelled lines (Rate, Caps, This hour, Today, Used by, Failures,
  Reason), so no value is hidden on a phone; from sm up they are table columns, Used by included. Home's poster rows, the anime YAML
  preview, the franchise episode map still scroll inside their own frame. A journal entry's summary and What followed wrap long release names and messages; the entry shows stored messages in full (the list keeps them short).
- Cancel closes a sheet or dialog; Discard resets a form's draft to saved (both sit in the record forms).

### Navigation

| Menu path | Route |
|---|---|
| Home | `/` |
| Library › Movies / Series / Anime | `/movies`, `/series`, `/anime` |
| Library › Collections (managers, admins) | `/collections` (one collection: `/collections/{tmdb id}`) |
| Requests | `/requests` |
| Activity › Queue / History / Blocklist | `/activity`, `/activity/history`, `/activity/blocklist` |
| Wanted › Missing / Cutoff unmet (managers and admins) / Manual import (admin) | `/wanted`, `/wanted/cutoff`, `/wanted/manual-import` |
| Now playing / Calendar | `/now-playing`, `/calendar` |
| Admin › Setup (while unfinished) | `/admin/setup` |
| Admin › System › Status / Tasks / Backup / Logs / Decision journal | `/admin`, `/admin/system/tasks`, `/admin/system/backup`, `/admin/system/logs`, `/admin/system/journal` (entry `/:id`) |
| Admin › Settings › Import lists (managers and admins) | `/admin/settings/import-lists` (old `/collections/lists` redirects) |
| Admin › Settings › Anime mappings | `/admin/settings/anime-mappings` (franchise `/:slug`) |
| Admin › Torrents | `/admin/torrents` |
| Admin › Settings | `/admin/settings` (destinations below) |
| Admin › Users & access | `/admin/users` |
| Admin › Advanced | `/admin/advanced` (migration, raw configuration, history and limits) |

Phone tabs are Home, Browse, Activity, Requests and More (the search page belongs to Browse). More lists Wanted, Now
playing and Calendar, then under Admin: Setup (when needed), System, Torrents, Settings, Users & access and
Advanced; System, Settings and Advanced open their pages on a tap. Counts retain the desktop badge
sources. Below 640 px route tabs become a **Section** select. Settings authority banners collapse to a single-line
summary with **Details** on phones; source and fallback information remain available.

Setup is a six-step readiness checklist with a read-only activation review. The first-library form also offers the
one-step start (`POST /setup/start`). The shared `useSetupProgress` hook
powers Setup, admin-only Home and Settings reminders and sidebar Setup visibility (`first_run || missing.length > 0`).
See [quickstart](../quickstart.md#readiness-led-setup) for the completion rules; only the one-step start enables a global switch, behind its confirmation.

### Library app (everyone)

| Screen | Path | What | From |
|---|---|---|---|
| Home | `/` | rows: **Getting ready** (in progress: downloads and imports running now; API route `GET activity/continue`; each card opens the title's page, and accounts with See files read its library's label under the year, the `label` the route sends), **Latest files** (titles by their newest current file, captioned "File added …"; Added elsewhere is the day the title was added), **Missing** (wanted versions, released), **Upcoming** (digital/physical release dates and episode air dates in the next 30 days), **My requests** (users) / **Pending requests** (managers: each poster opens that request in the queue). A family account (no `see_files`, no `manage_requests`) gets Getting ready, My requests, **Recently available** and Upcoming only: Missing and Below cutoff are copy shortfalls, and `GET discover` leaves them empty for it. Each row scrolls sideways and has "See all N titles". Missing captions a movie with the libraries that lack it ("4K Remux and 4K WEB"; the status word already says Missing); Upgrade found with the libraries that have one. For admins the heading line reads "N titles across M libraries" (`GET status`: `counts.title` and the `versions` list; `counts.version` counts each title's place in each library) | `/titles`, `/versions`, `title_release_date`, `download`, `library_import` |
| Movies / Series / Anime | `/movies`, `/series`, `/anime` | **Add movie** / **Add series** / **Add anime** in the header and under the empty list's text (accounts with `add`), opening the top search (it finds every kind); poster grid (every poster card, here, on Home and in search, has the title on two lines at most and the year under it, and one restrained availability badge; the overview on hover); filters in a sheet (**Filter**) with the active ones as removable chips: availability, monitored, library (has / lacks each key; accounts with See files only, a family account filters by availability), quality, provider, year, genre, and the title search in the toolbar; sort: title, added, latest file, release date, year, rating, runtime, size, completion (not Movies), next episode / release, quality, latest activity, network / studio, certification; posters or table (posters by default, the choice kept per list and in the address, `?view=`); each library shown as a status bar or pill (see **Library views** below) | `/titles` |
| Title | `/title/{id}` | see **Title page** below | `/titles/{id}` + new |
| Search | overlay, `/search?q=` | two sections: **In your library** (instant, local) and **Add from TMDb** (movies and TV, with "In library" / "Requested" marks). Add opens a sheet: versions to turn on (pre-ticked from the policy defaults), seasons for series, monitor; its footer says "N libraries" and ends with **Add** and **Add and search** (primary), only **Add** when no ticked library can search, and only **Save seasons** when only the seasons of a title in the library changed. An account without `add` sees **Request from TMDb** and Request buttons instead; one with neither `add` nor `request` sees **On TMDb** and no add or request control. A library result shows its library names (tags) only to accounts with See files; others get the title's one availability word (`titleWatchWords`: Available in 4K, Some episodes available, Getting ready, Upcoming, Not available), as the table does. With no TMDb key the TMDb section says so; admins get **Set the TMDb key** (Settings › Metadata & artwork, metadata services), others are told an administrator can add one | new |
| Calendar | `/calendar` | releases and episodes by month or as an agenda (arr views already serve `calendar`). An episode opens its series' Episodes tab with that episode's window open (`/title/{id}?tab=episodes&episode={episode_id}`); a movie opens its title page. Movie dates are the release-date choice (Library and activity pages, Release dates); one movie's release types of a day are one entry ("Digital + Physical"). Each entry's edge is coloured by status, with a legend under the header: Available (a file is held; a movie with a monitored library still lacking one says **Partly available**, `CalendarEntry.partial`), Getting ready (a download in hand, `CalendarEntry.acquiring`; the entry says its stage, `stage`: Queued, Downloading, Waiting for mount, …), Missing, Upcoming or in cinemas (a film is missing only from its digital or physical date), Not monitored; accounts without See files get Available in green and the rest in grey. The agenda shows the status once, as its chip. Every account also gets titles no library monitors, greyed (as Home's Upcoming row lists them; household accounts keep their words), and a **Monitored only** toggle. The month (`?month=YYYY-MM`), the Movies/Series/Anime toggles (`?types=`) and Monitored only (`?monitored=1`) live in the URL. This month's agenda starts at today, with **Show earlier**; **+N more** in the month grid scrolls the day's list into view | new |
| Collections and Import lists (managers, admins) | `/collections`, `/admin/settings/import-lists` | **Collections** (`/collections`, which links Import lists in one line): TMDb movie collections, monitored first: poster, movies in the library of the total, monitored switch, target libraries (movie libraries; monitoring needs one), search on add, minimum availability; a name filter and **Monitored only**. **Add collection** searches TMDb for a collection and adds it with its libraries (Search on add is ticked by default). Changes save at once; a monitored collection adds its missing movies when it is added, with **Add missing movies now** (**Refresh and add missing movies** once all its movies are in the library: it can still add one to another target library) and at each list refresh; a collection found through a movie with no name or movies yet (and not monitored) waits off the list, counted in one line, until the next list refresh names it; and its searches go through the router. With the collection's Search on add ticked, movies a timed refresh adds are searched on the search schedule, at the hourly pace, and one added or refreshed here is searched at once (docs/design/router.md, Adds by lists); only monitored movies that have reached their minimum availability are searched, and with Search on add off they are only added. **Import lists** (Settings › Import lists, `/admin/settings/import-lists`; `/collections/lists` redirects; on a phone a list's actions take their own row): **Source** is a TMDb list (by id or address), a TMDb discover rule (year from and to, minimum rating, minimum votes, genres, original language, the first N), a JSON list (its direct https address), or the titles of a Radarr or Sonarr instance (address and API key, with **Check**); **Media** (movies or series) for the first three: a mixed TMDb list is added once per media. **Add list** with libraries, what to monitor, minimum availability (movies), Search on add and Enabled; **Preview**, **Sync now**, Edit and Delete per list. A list's last sync failure is shown under Last sync ("Last sync failed: …") until a sync without failures. Below the lists, **List exclusions**: the movies and series no list adds (name and year where known, else "TMDb *id*" or "TVDB *id*"), each with **Remove**, and one line saying that removing an exclusion lets lists add the title again but does not bring back a library it was removed from (adding it by hand does). Remove title on a title page adds one with **Don't add it again from lists** (off by default; the whole title only). With a list's Search on add ticked, its new titles are searched on the search schedule, not at once (monitored and released titles only; with it off they are only added). Sync now is refused while Pause is on. Lists made through the Radarr-compatible API show here with their view (docs/design/arrview.md) | `GET collections`, `POST collections`, `GET collections/search`, `PUT collections/{id}`, `POST collections/{id}/refresh`; `GET`/`POST lists`, `GET lists/lookup`, `PUT`/`DELETE lists/{id}`, `POST lists/check`, `POST lists/{id}/preview`, `POST lists/{id}/sync` (409 while Pause is on), `GET lists/exclusions`, `DELETE lists/exclusions/{kind}/{id}` |
| Activity | `/activity` | **Queue:** for accounts that see files, a table (phone: cards): **Status** and **Title** (episodes under the name) always, then the chosen columns (**Columns**: Library, Quality, Provider (the provider or download client, "cached" or "reused" under it), Size and Progress (the bar, or how long it has waited and the next try) by default; Score, Formats, Indexer, Release, Added and Last update on offer), and the row's actions. Headers sort in the browser (the queue is loaded whole), newest first by default; columns and sort are kept per user in this browser (`theatarr.library.activity-queue.<user>`, `…activity-queue-sort.<user>`). **What do these mean?** lists each status word and its meaning (also the Status cell's tooltip): Queued, Waiting for a provider, Starting, Waiting to start, Downloading, Download complete, Waiting for the mount, Linking, Partly linked, Rejected by the library, Linking failed, Linked, Failed, Not started. Row actions (managers): **Details** (Inspect when it needs attention) opens the steps, release, hash, message (for a failure: **Technical details**, the stored text), provider attempts and cache evidence under the row; a failed or refused download's panel leads with its reason and detail, then what helps: "Retry sends the same release again; Interactive search lets you choose another.", or, when a retry must end the same way (the library refused its files: a headline "Not imported: …" or "Held for review: …"), "Dismiss clears it from the queue; Interactive search lets you choose another." ("Remove takes it out of the queue" while it is held). Its steps tick Download complete and cross the last step, **Not imported**, when it downloaded and the library refused it (or it was not imported in time); on a phone the step it is on or stopped at is named under the dots; **Retry** (or **Import now** for a finished download) when it applies; **Remove** (Dismiss for a failed one), whose confirmation has one checkbox, "Also blocklist this release and search again". Managers can tick rows (or "Select all N shown"): "N selected", **Remove selected…** (the same checkbox; one route call per row; the result says "Removed 3 downloads." or "Removed 2 of 3. Not removed: …") and **Clear**. A failed download (or a link the library refused) shows its reason under the status in plain words (below, "Failure reasons") and its release name under the title, so repeated failures for one item tell apart; Provider says "Not added" when no provider or client accepted it. `?open=<id>` (`downloadHref` in `web/src/lib/activity.ts`; for links such as System › Status's Inspect) opens that download's panel and scrolls to it; closing it drops the parameter; when the filter hides it the page says so with **Show all**, and when it has left the queue it says so with a link to History. Household accounts and "View as a user" keep simple rows: title (quality and episodes under it), stage, progress or waiting time, and Details with the steps. Filters (`?show=`): **Needs attention** (users: Needs a look; a failed step, a refused or partial link, a wait past its limit, or no progress for 6 h), **Downloading**, **Waiting** (queued, starting, not started, finished, linking), **All**. A download the library refused and blocklisted (the media check found a sample or a mislabelled file: failed, its import rejected; a new search is asked for and Retry is refused) leaves the queue by itself, as a miss on every provider does: it is not in the queue, the Activity count or System › Status, and stays in History (Failed, with its reason), the Blocklist and the title page's last-attempt line (`activity.NativeQueueFailureSQL`; rows stored before are covered). A held-for-review import (the download still ready), every other failed download and the arr-compatible queue are unchanged. A finished provider download is **Download complete** until it is linked; "ready to watch" is kept for library readiness. **History:** what happened to the titles' files, worded like Radarr's, one row per event with its release, quality and score where they are known (`internal/journal/history.go`, from the rows stored as each event happens, table `history`: docs/journal.md "Stored History rows"; the server builds every row; the title page's History tab and the episode window's History read the same rows, so they never disagree). Events: **Grabbed** (a download that reached a provider or client, dated when it got there: one stopped before, by Pause, shows only as Not started; an upgrade's grab reads Grabbed too, the journal's grab/upgrade split is unchanged), **Imported** (one row per import, not per link, with the episodes its links hold: a season pack is one row with its episodes, "6 episodes · S01E01–S01E06", the full list as tooltip; a partial import says so in its reason), **Adopted** (files taken over from Sonarr/Radarr, one row per library, release and day; tooltip "Taken over from Sonarr/Radarr's library; Theatarr did not download it." on the table for accounts that see files and in the episode window, not on household rows; when the adoption replaced the file before, the removal is folded into it: the row's columns are the new file's and "Replaced: <old release or file name> · <quality> · <score>" is under its release; its reason says "From Sonarr" (a series) or "From Radarr" (a movie), the instance as tooltip; a file Theatarr never scored shows "—" in Score, tooltip "Adopted file; not scored", a recorded 0 stays 0; a row with no release says "File name not recorded"), **Upgraded** (one row per download and library for an import that moved files aside, its import folded in: Quality, Score and Release are the new release's, "Replaced: <old release or file name> · <quality> · <score>" or "Replaced N files" under the release, the Event cell lists the episodes the import brought, a partial import says "Partly imported; the rest is tried again" as on Imported, and the journal link opens the import's entry; **Show the replaced file** / **Show the N replaced files** opens the list under the row, across it, in episode order, headed "Replaced file" / "Replaced files": each file on its own lines with labelled parts, File (the name; the path as tooltip), Release ("Name not recorded", or said once above the list when no file has one), Quality, Score and Episodes; household History shows the one row; a first import stays Grabbed then Imported; when every replaced file has a release name equal to the imported release's, a lost file fetched again, the row says **Re-downloaded** instead, its Replaced, Quality and Score lines unchanged, and one differing or unnamed replaced file keeps Upgraded), **Removed** (a link taken out by hand or by Sonarr/Radarr deleting it, with the reason: "Removed by <user>", "Removed by <key> (API key)" for an API key; one an adoption replaced is shown on the Adopted row instead; the adopted file's own removal stays Removed), **Removed from library** (`title_removed`: the title taken out of a library by Remove title or Remove from this library, one row per library, from the `remove_title`/`remove_version` actions; its reason says who and whether the files were kept or deleted, and the debrid clean-up's outcome once it ran: `by` and `removal {files, left, reason, cleanup}` for accounts that see files, `journal.Removal`), **File missing** (a link whose file is gone), **Failed** (a failed or stalled download with its reason, or an import held for review; Not started stays as below), **Blocked** (with the blocklist's reason), and, for accounts that see files, **Added to <provider>** / **Already on <provider>** / **Not cached on <provider>** for the copies of "Add to other providers" (removed in 0.4.5; its events stay readable, read-only, and copies finished before their events existed are seeded from their stored outcome, without quality or score). Searches with no grab, dry runs, mount waits and repaired links stay in the decision journal. The facts are recorded when the event happens (migration 00129: a link row can be taken over by a later file at the same path, and a copy row is reused); each library row also keeps its title and the episodes its file held, so a later change of episodes or a deleted link row does not change it; rows from before show what the link rows still tell and "unknown" for a file that is not known, and an import whose links are gone lists no episodes rather than the ones it asked for. Episodes and file counts are for accounts that see files. A folded row is never a row of its own while the row that stands for it is there, under every event filter, so a row reads the same under each filter as under All events and paging never shows half of a pair (docs/journal.md "History's folded rows"). Filtered by title, copy, event type (Grabbed; Imported and upgraded, upgrades as Upgraded with "Replaced:"; Adopted; Removed and missing, with Removed from library and without upgrades; Failed and blocked; an old `event=deleted` bookmark opens Removed and missing; the copies to other providers have no filter since 0.4.5 and an old `event=copies` bookmark opens All events; bookmarks with the old Grabs, Upgrades, Imports and Failures open the matching new type) and a since date, all in the URL (`?title=&copy=&event=&since=`; `GET activity?state=history&title=&version=&events=&since=`, `web/src/lib/activity-history.ts`), newest first, 50 a page with **Load more**. For accounts that see files it is a table without day headings: **Event** (the words, their meaning as tooltip, the reason under them) and **Title** (episodes under the name) always, then the chosen columns (Library, Quality, Score, Date and Release by default; Provider, Indexer, Formats and Size on offer; kept in `theatarr.library.activity-history.<user>`), then **Retry** on a failed download (none on one a removal ended) and, for admins, the journal link. Headers do not sort. A cell the event has no value for shows "—", never 0. Household accounts and "View as a user" keep the rows grouped by day, with no Grabbed rows (and no Grabbed filter; an old `event=grabbed` link opens All events), and the rows of one title with the same episodes, the same event and the same day (UTC, as adoptions are grouped) are one row with the latest time (`journal.Filter.Household`, chosen before paging), since their rows carry no release or library; nothing is merged across days or events. The journal link on a row is for admins only. Accounts without See files get no library key on a row (the resolution stays), and an opened failed download says in plain words that it did not work and that a manager can look at it. A download that never started because downloads were off when its turn came (paused or locked) and was never sent to a provider or client shows **Not started** with the reason, outside Needs attention; History says Not started, and for admins its line is the plain message. A download a removal ended (Remove title, Remove from this library, or an arr view's delete; `router.EndRemoved`) is in History only, as Failed with who removed the copy, and offers no Retry: the copy is gone. On Wanted, a row's search-blocked reason shows next to its disabled Search now when its waiting reason does not already say it (managers do not read `/status`). **Blocklist** (`/activity/blocklist`, accounts with See files): a table, newest first, 50 a page ("Previous · 1–50 of 312 · Next"), filtered by part of the title's name and **All libraries** / one library (`GET activity?state=blocked&title=&version=&offset=`; the total is the filtered count; "No blocked release matches." when nothing does). Columns, all shown: Title (linked to its page), Library, Release (the blocked download's indexer under it, "from torrentio", when known), Quality, Reason (the plain words; under them the reason in plain sentences ("Failure reasons"), also as tooltip, none for a release taken over from an arr's blocklist; "searched again 3 d ago" under it, the time as tooltip) and Blocked, then **Unblock**. Managers can tick rows: "N selected", **Unblock selected…** ("These N releases may be picked again for their libraries"; nothing is searched; one route call per row) and **Clear**. Users see the copies (versions and seasons) of the requests they are on, not another copy of the same title (history by version only; it pages over them, so each page is full until the last); admins see all. Bookmarks `/activity/now`, `/activity/blocked` and `/activity/wanted` redirect to `/activity`, `/activity/blocklist` and `/wanted`, preserving query and hash; Blocklist in the user view goes back to Queue. The **Decision journal** is a page of System (`/admin/system/journal`, below); `/activity/journal[/:id]` redirects there. Journal: every grab decision with its evidence, one line per thing that happened (docs/journal.md "The list": a download's failure, import and file changes are on its grab's line, "Grabbed <release> on RD · imported, replaced <file>"; an adoption's files and the file it replaced are one "Adopted 8 files of Season 1 from Sonarr (…)" line; a hand unlink and its library action one "Removed <file> · by <who> · moved to the trash" line), the same lines in the title page's Journal block (History tab, admins, with **Open in the journal**). Summaries name files only, the full paths as the summary's tooltip (an adoption's first five files, then "and N more") and all listed on the entry. Each line's kind is the entry's label in History's words (Grabbed, Upgraded, Imported, Adopted, Removed, Missing, Failed, Blocked, Search…), the code as tooltip; sources ("Theatarr", "Radarr (radarr_4k)"), flags ("Size outlier") and provider attempts are in words too (`web/src/lib/journal-words.ts`, `router-words.ts`); the title box and the Older/Newer paging follow the URL's filters, so a link that drops `?title=` empties the box and starts at the newest page; Newer and Older return to the top. An entry shows its summary, flags, release, reasoning, provider attempts and what followed first | new (router, library); `waiting_since`, `next_retry` from `internal/web/activity.go` |
| Wanted (managers and admins; Manual import admins only) | `/wanted`, `/wanted/cutoff`, `/wanted/manual-import` | Three tabs; **Manual import** is described under Manual import below (admins only: managers do not get its tab; with `?as=user` its tab is hidden and its address goes back to Missing). Managers get the same lists, Search now and Interactive search, without the admin links (settings, decision journal). **Missing** and **Cutoff unmet** list the search schedule's items, one row per library and film or season (docs/design/router.md, "The search schedule"). Viewing never starts searches or changes version state. A line under the tabs gives the pace: "Up to 10 searches an hour (3 in the last hour). Items are searched again every 24 h. A missing new release is tried again sooner: the wait starts at 1 hour and doubles each time, up to 24 h, until a week after its release." (the last sentence on Missing only, and only while **Search for missing items** is on; both times are the **Search older items again after** setting; "Items are searched again every …" is left out while **Search for missing items** and **Search for upgrades** are both off) When the tab's switch is off a banner says "Theatarr is not searching for missing items by itself. Items here are searched when you ask, or after a failed download. New releases are still picked up from RSS feeds, and one that a search did not find is tried again sooner, until a week after its release." (Cutoff unmet: "for upgrades" and "Better releases are still picked up from RSS feeds."), with an **Automatic search settings** link for admins. Above the table a line names the unit: "48 items (one per film or season in each library, not per episode); Home's Missing counts titles." (the last clause on Missing only, linking to the Missing titles); the empty state says "No items waiting in this selection.". Home's Missing row has an **In Wanted** link beside "See all 25 titles" for managers. Columns (Columns picker; on a phone they show under each title): Library, What ("S20, 139 episodes" for a whole season; on Cutoff unmet a whole-season row opens to its episodes, each with the quality it holds, and "Cutoff: WEBDL-2160p", as Sonarr's Cutoff Unmet lists them), **Released** (a film's home release; the newest stored air date of the row's episodes, the day the episode page shows), **Next search**, Last search (for admins the **Decision** link of that search sits in this cell, so the row's actions keep their place; with Last search hidden it stays a row action). **Next search** says **Now** for a due row (" · asked by Search now" when asked; no start time is promised, the hourly limit decides), the time for a scheduled row (the exact time as tooltip) with why it is early ("New release, try 2", "After a failed download", "Added by a list"), **Downloading**, **Not scheduled** (its switch is off and nothing asked for it) or **Paused**; a row never shows a time while paused. Filters (`?waiting=`): All, Due, Scheduled, Downloading, Not scheduled, Paused; they, the library filter and the title filter (`?title=<id>`, a removable chip with the title's name; the title page's **See in Wanted** / **See in Cutoff unmet** links set it, and it carries over between the two tabs) precede 50-row paging, and counts and **Search all** cover the whole selection. Title, Released and Last search headers sort the whole list on the server (`?sort=title|-title|date|-date|last|-last`; first click: titles A–Z, dates and searches newest first; rows without a date or never searched go last); a **Sort** picker offers the same orders on a phone. A row whose last search grabbed downloads says what became of them, from their states (one in flight wins, then a failed one, then an imported one): "Grabbed" once imported, "Grabbed · Downloading" (its stage) on the way, "Grabbed, not imported" when it failed; the last two link to the download in Activity (**See in Activity**). **Search now** on a row, **Search selected** and **Search all** make the rows due now under the hourly limit (`router.wanted.per_hour`); Search all, and a selection larger than what the hour has left, confirm first with the count ("They start as the hourly limit allows"). The answer says how many started and how many wait ("Started 6, queued 306"). While asked rows wait, a line counts them, with **Cancel queued searches**, which puts them back on their regular schedule; searches already started stay started. Rows Search now cannot search (a download in flight) cannot be selected, and nothing can be selected while paused; a row's search-blocked reason shows next to its disabled Search now when Next search does not already say it. Shared title/season queries display each library separately so its gates remain visible. Routed searches say “Sent to download”; “Grabbed” requires a download from that run. Closed gates link to their settings without toggles. Active title copies link to both queues when they have missing and below-cutoff episodes; shadow copies have no Wanted handoff. Search history shows each search's outcome in one line, links the newest to its results and, for admins, to its **Journal entry**. At most ten newest searches are added after the fifty release-history events are selected. Their related downloads link to the download entry for release actions. | `GET wanted?only=missing\|cutoff&version=&waiting=due\|scheduled\|in_flight\|not_scheduled\|downloads_off&title=&sort=&limit=&offset=` (rows with `waiting`, `next_at`, `why`, `tries`; `search_now_pending`), `POST wanted/search`, `DELETE wanted/search` |
| Requests | `/requests` | users: theirs, by status; admins: a queue with approve / decline / **Edit request** (versions, seasons). Each tab starts its own status filter (To decide: Pending; All and Mine: Open); the list pages 50 at a time. `/requests/<id>` opens a focused request detail; old `?request=<id>` links redirect there (the View request links). | new; `GET upgrades` |
| Profile | `/me` | password, sessions, own API keys (if allowed), theme | new |

**Title page** (`web/src/pages/title.tsx`,
`lib/title-layout.ts`):

- **Hero**, compact: poster, title and year, essential facts (certification, runtime, up to three genres, original
  language, rating), readiness for everyone ("Ready to watch in 4K": the best resolution a usable copy has; "5 of 10
  episodes ready"; "Not available yet", or "Getting ready" without See files while a download is in hand; a movie
  missing one of its copies is still ready), audio languages, a
  synopsis cut to three lines with More, and the primary action beside the title: **Add to another library** (add;
  **Monitor again** when every library that fits has the title, some not monitored; no action at all
  when every library that fits has it monitored, nor while the add options load), else **Request** / **View
  request** (request). On phones that action also sticks above the bottom tabs; with no action nothing is pinned
  there.
- **Action bar** (callers with "see files", `components/title/title-toolbar.tsx`), under the hero: **Search now**
  (managers: "Search every monitored library now"; a series "Search every monitored episode now, in every
  library (missing episodes and upgrades)"; when nothing can be searched it stays, disabled, with the reason as its
  tooltip, shared with the list rows: Pause, "No library is monitored.", else
  each monitored library's verdict ("Nothing is searched now: WEB: Downloads off"); disabled, without a tooltip, while the add options load, so the bar does not
  shift), **Interactive search** (managers, a
  movie with a library: the whole movie; not on the Last search page, whose panel has it per library),
  **Rename files** (admins, only when a library holds a file or a removed library still has links),
  **History** (opens the History tab),
  **Refresh metadata** (managers) and **Remove title** (managers). Below 768 px Search now and Interactive search keep
  their words and the rest sit under **More**. Under the bar: Pause once for the page, the Search now outcome or its failure with **Retry**, and "No
  library is monitored." when so. **Add to another library** stays the hero's primary action while a library is left to add.
- **Rename files** shows a preview first (`GET titles/{id}/rename`): one line per file, its library, then old name →
  new name (when only folders change, the path from the first folder that differs); "N files could not be checked."
  for files it could not check; "Every file already has the name the naming settings give it." only when nothing
  would change and nothing was skipped. **Rename N files** renames; a failed preview shows the server's message.
- **Libraries** table (callers with "see files", `components/title/library-rows.tsx`, `lib/library-rows.ts`), for a
  movie and a series alike: one row per library the title is in now. Always shown: **Monitored**
  (the switch), **Library** (colour, name with the key as tooltip, and a verdict
  only when it adds something: **Downloads off** with its small **?**, or "New files won't be linked") and the row
  actions. **Columns** picks the rest ("Monitored, Library and the actions always stay. Kept for your account in this
  browser."; `theatarr.library.title-libraries.<user>`). On by default: **Status**, **Quality**, **Score**, **Size**,
  **Cached on** and **File** (the file name, the full path as tooltip). On offer: Preferred quality (the profile's
  cutoff, "· upgrades off" when so), Minimum availability, Release, Release group, Languages, Video / audio, Formats,
  Added, Last search, Next search and Path. A series row offers Status, Quality, Size, Cached on, Preferred quality,
  Added and Last search. The switch column is headed **Monitored**. Quality is the technical name ("WEBDL-2160p").
  Score shows with the matched formats as its tooltip; a file whose score was never recorded (an adopted file
  Theatarr did not score) shows "—" (tooltip "Not scored"), as History does; a recorded 0 stays "0". **Cached on** has a
  pill per debrid provider that holds the file complete now and not failed (RD, AD, TB), adopted files included (found through the
  hash their link records, else the hash its mount folder points to: torrents.md "Folder hash", display only),
  "—" when none does. For admins a small **Torrent** link beside it (and beside the hash in
  Details) opens Admin › Torrents filtered to the file's torrent (`?q=` the hash's first 12) when the row has one file
  with a known hash; a series row of several files has none (the episode window has it per episode). A fact the file does not have is blank, never 0.
  **Status:** a download in hand and no file reads its stage ("Downloading · 37%", "Waiting for mount", "Blocked"),
  linked to Activity; a movie without a file "Not monitored", "Upcoming · Jan 14" (not out at home yet, the posters'
  rule) or "Missing"; a held movie "Available" with the quality word beside it (Upgrade found, Preferred quality not
  met or Preferred quality met; Why? unless it is at its cutoff; Status words), with
  "Upgrade downloading · 37%" under it while one is in hand; a series "44 of 46 monitored episodes" with "2 missing ·
  30 not monitored" under it; a library that monitors none of them says it once: "291 of 291 episodes available, not
  monitored" (the library is not monitored) or "37 of 37 episodes · none monitored" (no episode is). A missing
  library's line under the status says why and what next: the last search, or "Not searched yet. Automatic backlog
  searching is off; new releases are still picked up from RSS." (the RSS clause where the library's RSS read covers
  it), with "Use Search now to request a search." for an account that can search an item never searched.
  **Row actions:** **Search now** (icon, managers; disabled with the reason; when other libraries want the
  title, its tooltip says they may use the results too), **Interactive search** (icon, a movie's library) and
  **Details**, which opens a panel under the row: path, edition, a series' episode counts, one **Upgrade** line (the
  quality word with Why? when no Status cell gives it, the episodes under the preferred quality, and the better
  release found), minimum availability when its column is hidden, then the library key (admins) and the hash,
  **Add to other providers** (managers, a movie's file whose torrent is not on every provider with a key; torrents.md
  "Add to other providers"), the library's
  restriction notes, **See in Wanted** / **See in Cutoff unmet** (managers, monitored libraries; Cutoff unmet not once the quality is met), **Last search results**
  (managers), each library link with **Unlink**, and **Remove from this library** (managers). Below 768 px each
  library is a stacked block: switch, name, tag and status; quality · score · size, and the provider pills when any
  provider holds the file; the file name; then
  Search now, Interactive search and Details with their words. There is no Columns button there. A library the
  title was removed from has no row; if it still has links, one line under the table says "Still linked from a
  library this title was removed from: <library> (N files)." with **Delete files…** (managers). With no library:
  "This title is in no library yet."
- **Latest event** under the status (`latestEvent`, `web/src/lib/title-facts.ts`, from the title response's
  per-library `history`): "4K Remux: Imported yesterday", "Upgraded yesterday" for an import that replaced a file ("Re-downloaded yesterday" when every file it replaced was the same release by name, `redownload`; the
  server's `upgraded`: History has an Upgraded row for that download), or the last failed download with the server's
  reason, the same sentence as History ("Last upgrade attempt Nov 13: not cached on RD and TB").
- **Next search:** a movie's Libraries table can show a **Next search** column, and a series season with
  one library in view adds "Next search: in 3 h (new release)" to its facts. Both read the same time as the Wanted
  page: "now" when due, else the time, with why it is early ("new release", "Search now", "after a failed
  download"). Nothing is shown when the item has no time there: not monitored, at its cutoff, not
  scheduled, a download in flight, or paused. Schedules are per season, so an episode has no time of its own.
- **Tabs:** Episodes (series and anime, the default) / Watch order (anime with a franchise) / About (overview when
  long, the fact table, outside links; admins also identifiers, release dates, files and registry) / History ("see
  files": the title's rows of Activity › History, the same builder and the same table without filters or a Title
  column, the episodes under each event; admins also the title's journal). There is no Libraries tab: the table is under
  the hero. About's facts say **Released** (a series **First aired**), or **Releases** / **First airs** for a day still
  to come, and **Latest file** ("see files" only: when the newest current file came; when the title was added is under Details, "Added
  to Theatarr"); **On disk** counts a file two libraries share once. About's Files inventory includes old-manager
  files (with their instance) and active native library links (with their size, no instance word), deduplicated by
  path. Release dates list the primary release's stored rows (Library and activity
  pages, Release dates), the country named once under the table and a fallback cinema date marked first release; type and source in words, the raw value as tooltip.
  Old `?tab=seasons|details` links open Episodes and About; `?tab=versions|copies` open the first tab.
- **Minimum availability and series type.** A movie's Libraries table shows **Minimum availability** (a column, or
  the row's Details when the column is hidden), the value in
  force for that library (`versions[].minimum_availability`, from `availability.Rules`: the explicit choice, else
  the imported value, else the library's setting, else released), with no word on where it comes from. A manager
  changes it there: `POST /titles/minimum-availability {title_ids, versions, minimum_availability}` writes an
  explicit choice for that library only (libraries the movie is not in get none). An imported `tba` is shown but
  cannot be picked. A series' About facts show **Series type** (`series_type`, the title's one type for every
  library and arr view), which a manager changes with `POST /titles/series-type {title_ids, series_type}`
  (`catalog.SetSeriesType`; the anime flag stays). The change is saved at once and can stop searches: acquisition
  searches a series only in libraries whose anime rule fits it (anime by its flag or its type; a library set to
  no anime skips an anime series, one set to anime only skips the rest), and neither Wanted nor search now says
  why a copy is left out. So the monitored copies left out in libraries searches run for
  (`versions[].anime_skip`) are named under the control, "Not searched in ...", with the reason, also after a
  change. Both endpoints take 1 to 500 titles, the page sends one, and a title of the other kind answers 400 with
  nothing written. Without manage both show as text.
- **Family accounts** (no "see files"): hero with readiness and languages, their request card, Episodes for a
  series, About; no action bar, Libraries table, states or history (the API sends none).
- **Add by magnet or hash** (admins, under the releases list, for the shown library): a magnet link or info hash
  with a release name goes through the same decision as a found release (`POST /titles/{id}/releases/add`); a
  rejection offers "Add anyway"; a failed add starts no search (docs/design/router.md).
- **Releases page, "Last search"** (`/title/{id}/releases`, reached from a library row's Details, **Last search
  results**, and from history links; read-only, it has no Grab: Interactive search grabs): managers and admins only, matching the release API; other accounts see
  the standard permission screen with a link back to title availability. Malformed or non-positive title ids show
  the not-found state and a Library link. Each row carries, next to accepted or its rejections, what came of that
  release for the library, matched by info hash against all the library's downloads (`Releases.outcomes`):
  `Grabbed · rd` (in flight), `Imported`, `Current file`, `Failed · <reason's headline>` (the newest attempt failed, even
  after an older import) or `Replaced` (by the current release), for the stored list and a live search alike. The
  episode overview's Releases badge the same way from that episode's downloads and files only, each release by its
  newest download's own evidence: a link of that download holding the episode, or, once it is imported, its
  recorded files (`download_file`) mapping one to it. So a season pack whose episode was upgraded reads `Replaced`
  there, a manual choice not yet imported stays `Grabbed`, and an imported pack with no file known for the episode
  (none recorded for it, or none recorded at all) has no badge; its search line then says only "→ imported", or
  "→ imported, without a file for this episode" when its records hold none for it. Files are recorded per import
  since `download_file` existed; a pack imported before that, with later partial corrections, may read as lacking
  an episode it held. Above the table one line says what the shown stored search did
  (`Releases.grabbed`, for an episode its downloads of that episode): "Grabbed #1 via rd → imported" or "Nothing
  grabbed: <reason>" ("Nothing grabbed for this episode" when a season search routed only others); it describes
  the downloads that search opened. The source badge, the age and "12 results, 2 accepted" sit in one line; under it
  the message says what the stored search came from ("From RSS", "From a search", "From a search for S01"; an
  episode adds ", which covered S01E05"). A search that ran routed with no download says "A release was chosen, but no
  download was started.". The indexers' notes are one line naming what went wrong per indexer ("Search incomplete:
  therarbg was rate-limited, nyaa did not answer in time."; nothing when every indexer answered), with the raw notes
  in a closed **Technical details**. A library that may not search live says why: "The acquirer is not running". The page's header explains the page once: the card
  repeats no hint and the action bar has no Interactive search. The grid's filter box reads "Filter releases" (what it
  matches in its tooltip); an indexer is shown without the "prowlarr-" prefix of a linked Prowlarr indexer's key, the
  key as tooltip. The list has one row per torrent (info hash), where Sonarr and Radarr list each indexer's copy: the
  best-ranked copy stands for it, the Indexer cell names every indexer that returned it (the first two, then "+N"; the
  tooltip gives each copy's indexer and name; a narrow card lists them all), Peers is the copy with the most seeders and Cached on the union; the
  Indexer filter and the text filter match any of its indexers. A live answer counts rows; a stored one keeps its
  search's own count ("N releases returned by the indexers"). A live search no indexer answered is a failed search ("Live search failed",
  the stored list shown), and a live search is never retried on its own. `?hash=` picks out the followed release's row, or says it is not in the list.
- **Interactive search** (`components/title/interactive-search.tsx`, the grid `release-grid.tsx`, columns
  `lib/release-columns.ts`): a wide window (a full-height sheet on phones) that searches the indexers the moment it
  opens, as Radarr's does. Managers open it for a movie from the action bar (the whole movie) and each library's
  row (that library). A series has it per season and per episode only, as Sonarr does (an unscoped series search
  would judge every episode unrequested): each season header (season packs and that season's episodes), each episode
  row (an icon button at the row's end; it does not open the episode overview) and the episode overview (per
  library). Each Wanted row opens it for that library, and the row's season for a series. It is offered only when a
  current library of the title searches live (`control.live_releases`). The title data
  still lists removed copies, so the window takes the current ones from the removal scope (`GET
  titles/{id}/removal`, its `copies`): a removed copy gets no tab, and a search asked for it opens on a current one. While the indexers answer, the window says "Searching the
  indexers for <library>…" with a seconds counter; closing it stops the search. One row per release, with the
  columns Source (indexer kind, private trackers marked), Age, Title, Status (right after Title by default, so the
  verdict stays in view when the table scrolls sideways; it keeps a readable width), Indexer, History (what came of
  the release before), Size, Peers, Cached on (provider pills), Episodes (series), Language, Quality, Score, Formats
  and Flags (a public release shows only the flags its indexer names, no freeleech from the volume factors that
  public feeds send for every torrent), then Grab. The release name keeps its text and font; the facts in it are coloured, text only
  (`lib/release-highlight.ts`, `components/title/release-name.tsx`, the `--rel-*` tokens): dynamic range (DV, HDR10+,
  HLG …), audio with its channels (Atmos, TrueHD 7.1, DDP5.1 …), resolution (2160p, UHD …) and source (REMUX, BluRay,
  WEB-DL; a bare WEB, and Opus, only after a resolution, so a title's Web or Opus stays plain). Last search and the episode window's list
  use the same grid; the journal's stored lists, History and Activity stay plain. Every column but Episodes and Formats sorts (the columns Sonarr's sorts; Status puts the
  accepted first, History the current file first), can be hidden and reordered (Columns); the column choice is kept
  per user in this browser (`localStorage` `theatarr.library.release-columns.<user>`). Filters: text, Accepted only,
  Packs only (a series), and selects for Cached on (anywhere, or one provider), Indexer, Quality and Language, each
  offering the values in the answer's releases and shown only when it can narrow the list. Sorting and filtering take
  every release of the answer before the 50-row paging. The sort and the filters (and a rejection chip) last while
  the window shows that answer; a new search (Search again, another library, another opening) starts unfiltered in
  the best-match order, as does another library's tab on Last search. A score with matched
  formats is a button: it opens a line under the row with each matched format and its score. On a phone each release
  is a card with its indexer, the episodes it covers, its formats with their scores, languages and flags. A title in several
  libraries that search live gets a tab per library (its colour and name, the key as tooltip):
  each tab searches once per opening, Search again searches the shown library again, and every opening searches
  afresh. Grab sends an accepted release to the router at once (journalled; removable in Activity); a rejected
  release, one that would replace a better-scored file, or one the server rejects at grab time asks first ("Grab
  anyway"). A live answer keeps the best 300 releases ("Showing the best 300 of N" beyond that). The indexers reuse an
  answer for 15 minutes, so opening again within that time re-decides the same results. A search the server refused
  or that failed shows the last stored results with the reason, and those cannot be grabbed. When the stored
  decision came from another search than the one asked, it says so ("The stored results are from a search for S01,
  not Season 2.").
- **Episodes** (`components/title/seasons.tsx`, `episode-drawer.tsx`, logic in
  `lib/seasons.ts`, columns `lib/episode-columns.ts`), phone first:
  - **Library in view:** with two or more libraries a selector above the seasons: **All libraries** (the default from
    768 px) or one library (phones have no All and show the first). Not stored. Every action on the tab acts on the
    libraries in view, and in All on every library; tooltips say which ("Monitor season 2 in 4K", "Search season 2
    now in every library").
  - **Season header**, one line from 768 px: chevron, the season **monitor switch** (managers), "Season 2" /
    "Specials"; with one library in view a badge "8 / 10" (red when something is missing, green when nothing is),
    then "2 missing · 1 upgrade found · 1 downloading · 14.2 GB", the upgrade status with Why? and, when the server
    sends it, "Next search: in 3 h (new release)"; in All a badge per library (colour, name, "8 / 10") and the summary
    sentence, "8 of 10 available · 2 missing · 1 upgrade found · 1 not aired". "Of N" counts the episodes aired or
    already held. At the right, for managers: **Search** (icon) and **Interactive search** (icon, when a library in
    view searches live). Outcomes come back as toasts ("Search started" / "Not searched" with the server's
    sentence).
  - **Restrictions once:** read-only monitoring and disabled search are one note above the seasons, a line per
    distinct reason, naming the libraries only when not all share it. The controls stay disabled with the reason as
    their title.
  - **Filters per season:** All / In progress / Missing / Upgrade found (the last for "see files"), with counts, a
    chip only when it would show something (All and the chosen one always); they
    match an episode when any library in view does. The season checkbox selects the episodes shown, so "Missing,
    select all, Search" works. The bulk bar shows only while something is selected: "3 selected · 4K" (the libraries
    in view), **Monitor**, **Unmonitor**, **Search** and clear. Another library view clears the selection.
  - **One library in view, 768 px and up:** Sonarr's table: checkbox, monitor switch, number, title, the chosen
    columns and the actions (Search and Interactive search icons). **Columns** ("The number, title, monitor switch and
    search buttons always show."; `theatarr.library.episode-columns.<user>`): Air date, Status (Available, with
    Upgrade found beside it; Missing, Not aired, Not monitored, which a gap in a library that is not monitored reads
    too; the download under it), Quality, Size and **Cached on** (with the admins'
    **Torrent** link, as the episode window, on one line) by default;
    Score, Release group, Release, File and Runtime on offer, and from the file (blank without one or without the
    fact; see files only): Video codec, Dynamic range (blank for SDR), Audio (codec and channels), Audio languages,
    Subtitles (the first four, all in the tooltip), Languages (as decided at import) and Formats (the matched custom
    formats with their scores, as in the release list). The media is Theatarr's own probe, else the arr's media info
    of an adopted file; with more than one file for the episode, every fact is the newest link's. Phone rows show none
    of the chosen columns.
  - **All libraries:** a column per library (the matrix), each cell a word, Available (Upgrade found beside it) /
    Missing / Not aired / Not monitored, with the quality beside it and the monitored mark; each row ends in the Search and
    Interactive search icons. No Columns button.
  - **Phones:** one row per episode for the library in view: name, number and air date, and the state in words on
    the right (the quality under it). Never a bare icon.
  - **Numbering:** "E01"; anime with an absolute number "E01 · absolute 13", in the rows, the table and the episode
    overview. Anime with a franchise get a **Watch order** link above the seasons (the tab stays).
  - **Episode overview:** the shared Modal, a centred dialog on desktop and a bottom sheet on phones
    (`?episode=<id>`): number, name, air date, runtime, and the synopsis under them (`EpisodeDetail.overview`, every
    account: it is public metadata, as Sonarr's episode dialog shows it); then three tabs. **Details**: per library its state in the
    upgrade vocabulary, monitor switch and Search now (managers), Interactive search for that episode in that library
    (managers, a library that searches live), the download in hand, the upgrade block with Why?, and the file's facts
    (release, quality, score, size, group, **Cached on** with the admins' **Torrent** link,
    languages, media with at most four subtitle languages and "+N" (all of them as tooltip), added, "From" the arr
    instance for an arr's file (none for Theatarr's own link), path), shown openly. **History**:
    per library the title History's rows that hold the episode (`EpisodeVersionDetail.history`, read by
    `Journal.EpisodeHistory`: the same folds, words and table as the title's History tab, without the Library column
    and the column choice; an upgrade is one Upgraded row with "Replaced:", an adoption that replaced a file one
    Adopted row; the row's episodes are shown only when they are more than this one); no journal lines, and the tab's
    count is the rows shown. **Last search**: the stored decision for the episode,
    read-only.
  - **Family accounts** (no "see files"): one availability column across libraries at every width (an episode is
    Available in its best resolution when any library holds it, Missing only when none does, Not aired, Not
    available when nobody wants it), no library names, selector, upgrade filter or checkboxes; the overview is one
    availability line. The API already sends them nothing more (`redactEpisodes`).

**Title actions** (by permission): Search now (per version), Turn a version on or off, Monitor, Request a version (a
user without add rights), Interactive search (managers), Last search (the stored search, read-only; managers), Grab a
release from Interactive search, Rename files (admins, with a preview), Remove from this library, Unlink a library file, Remove the title (managers; see **Recovery
actions**; never deletes provider torrents or files on the mount).

**Recovery actions** (`internal/web/ops.go`, `internal/arrview/ops.go`, SPA
`components/{title/release-grab,title/copy-actions,activity/download-actions}.tsx`, scope words in `lib/ops.ts`).
Managers and admins only (`manage`; 403 otherwise). Every action opens a confirmation that names its scope ("This
will" / "Stays as it is"), except grabbing an accepted release from Interactive search, which is one click, and is recorded in the journal as an `action` entry (`act:<id>`, table `journal_action`,
00039) with who did it (user name, or `key:<name>` for an API key). They reuse the existing paths, never a second one:

| Action | Where | Route | What runs |
|---|---|---|---|
| Grab a release | Interactive search: live answers only (`Releases.grab`); an accepted release is sent at once, a rejected one or one that would replace a better-scored file asks first; the stored "Last search" has no Grab | `POST titles/{id}/releases/grab {version, info_hash, override}` | The release must come from this server's own live search for that title and copy in the last 30 min (never release data from the browser). It is decided again; a rejected release (then or now) answers 409 with the reasons and needs "Grab anyway" (`override`). The action is refused with the Pause (router-disabled) reason before enqueueing when provider writes cannot run; otherwise `router.Enqueue` applies the indexer's provider allowlist, blocklist, pacing and budgets |
| Remove download | Activity › Queue: a row's Remove (Dismiss for a failed one), or **Remove selected…** for ticked rows (one call per row) | `POST activity/downloads/{id}/remove {blocklist, search}` | `router.Dismiss` (as the arr views' `DELETE queue`): Theatarr's record ends as failed; the torrent at the provider stays (shared by every library), linked files stay. The confirmation's checkbox "Also blocklist this release and search again" sends both and blocklists the release for the copy (source `ui`, "Removed in Activity with blocklist") and searches the copy; `search` without `blocklist` is refused. A toast keeps the server's outcome visible when the row disappears |
| Retry | a Queue row's Retry (Import now for a finished download), and History rows about a failed download | `POST activity/downloads/{id}/retry` | failed: the same release, as the download stored it (its release data with cache evidence, as the router rebuilds a download it resumes), goes to the router again as a new download only when `router.enabled` is true (Pause off) and its library is on; a paused or turned-off route is refused with the reason (409), as are blocklisted releases. Ready or importing: import now (`library.Import`), answered with its `outcome`: `imported`, `partial`, `rejected` (failed and blocklisted), `held` (left for review), `waiting` (mount) or `dry_run` (gate); anything but `imported` is shown as a warning and journalled as such. Anything else is refused (409) |
| Unblock | Activity › Blocklist: a row's Unblock, or **Unblock selected…** for ticked rows (one call per row, no search) | `POST activity/blocked/unblock {version, info_hash, search}` | deletes the blocklist row (`router.Unblock`) in the same transaction as the journal record; optionally searches the copy. A toast keeps the server's search outcome visible when the blocked row disappears |
| Remove from this library / the title | the library row's Details / the action bar's **Remove title** | `GET titles/{id}/removal?versions=` (scope), `POST titles/{id}/remove {versions, unlink}` | the shared removal (`internal/removal`, also the arr views' DELETE): `catalog.Remove` (`arrview_removed_version`, unmonitor; the title leaves when no copy is left), and in the same transaction the downloads in hand dismissed (`router.EndRemoved`; a running import then takes its links back), and the copies' search requests cleared. The dialog's two choices are on by default: **Delete the files** (`unlink`): the copies' symlinks, dead ones too, go to the trash through the gate and the folders they leave empty go, strictly below the library root (a link another current library of the title uses, or the gate refuses, stays and is reported as kept); and **Delete it from the debrid providers** (`debrid`): after the answer, the torrents no other title or library in Theatarr uses are deleted from the providers after the full backup (torrents.md "Removal clean-up"), managers included. The second is off and disabled while the first is off, with one line saying why (kept files would stop playing). Both off: files and torrents stay; files only: the links go and the torrents stay (Torrents › Not used by Theatarr). **Remove title** (the action bar, the whole title) also offers **Don't add it again from lists** (`exclude`, off by default): the title goes on the list exclusions (a movie by TMDb id, a series by TVDB id) in the removal's transaction; Remove from this library does not offer it. The dialog says what goes and what stays (History; "Import lists will not restore the removed libraries; add it yourself to bring it back"; "A file another library of the title uses stays" only while some library of the title stays). After confirming, a removal that leaves the title in no library goes back to the list the title was opened from (Movies, Series or Anime with its filters; opened from Search, Home or elsewhere: the title's own list; `web/src/lib/last-list.ts`), any other closes the dialog; either way a notice states the outcome and opens the title's History, where the Removed event gets the clean-up's outcome. The list's selection bar removes many titles through the same endpoint, one title after another ("Library views"). A move that fails otherwise (a full disk) is reported in `failed` with the rest (200), not a 500 after the hide: the scope then lists the removed copies whose links remain, dead symlinks included (`leftover`) and the dialog offers "Delete files", with only the debrid choice (deleting the files is its point), which deletes just those (and their torrents when asked). The journal's remove entry claims no unlink; each moved link has its own `unlink_file` entry once it moved. A copy an arr still manages is refused (409). Adding the copy again restores it. A title with no copy left is in no library anywhere: not in the lists, their totals and facets, Home's count and Upcoming, the calendar (with unmonitored too), the search's library group or a TMDb hit (which offers Add; the add sheet's `in_library` is false and the add restores it by its title id), a collection's held count (counted as removed); requests whose every library was removed are canceled ("removed from the library", at the removal, by nobody), the others keep only the libraries left (stored at the removal, so adding a library back later does not put it into the old request), and asking for a removed library again is a new request; these moves are not announced (no Now available); offers never go to a removed library. A removed copy of a title that stays in another library is not in the releases picker, and searching, grabbing or adding a release for it is refused (400). Each removed library's History row records what became of its files after unlinking: kept, deleted, or partly deleted with how many were left in place and why (a refusal, a link another library uses, a link that is not Theatarr's symlink), and the debrid clean-up's outcome once it ran. Adding a title every copy of which was removed is a new add: the sheet offers anime, monitoring, series type and new seasons, its options follow the anime choice, and the values it shows are sent and apply. Its page reads "Not in the library" with Add, About and History, and "N files kept from before it was removed: Delete them" (the leftover removal); `titles/{id}/search`, `releases`, `rename` and `episodes` answer 404 as for a title never added, while `titles/{id}`, its removal routes and History keep answering. A title with no version at all yet is not a removed one. With the box cleared, under what stays the dialog says where torrents nothing uses are deleted: Admin › Torrents › Not used by Theatarr (`/admin/torrents?group=unused`; a link for admins, plain words for managers) |
| Change episodes (admins only) | the episode overview's Details tab (the file), for one of Theatarr's links | `POST links/{id}/episodes {episode_ids, replace}` | `library.ReassignEpisodes` ([library](library.md) "Which file is which episode"): the link is renamed for the chosen episodes and the choice is kept for later imports; a chosen episode with another file in that library is refused (409) unless **Replace** is chosen, which moves that file to the trash. Recorded in the library's actions, not the journal |
| Add to other providers | a movie's library row Details, the episode overview's Details tab (the file), an Admin › Torrents row's expanded part (admins); shown while `add_to` is not empty | `POST torrents/copy {hash}`, then `GET torrents/copy/{hash}` every 3 s while running | job `torrents.copy`: the torrent is added to the other providers with a key that have it cached; an add that is not cached is deleted again; one line under the button says each provider's outcome ([torrents](torrents.md) "Add to other providers"). The link is not changed |
| Unlink a file | the library row's Details, each link's **Unlink** | `POST links/{id}/unlink` | `library.Unlink`: only Theatarr's own active symlink, gate first; the copy is wanted again when it has no other file. Hardlinks, copies and the mount's files are never touched |

There is no arr-shaped `DELETE blocklist/{id}` (the arr views serve no blocklist list to take ids from).

Revoking an invite or an API key, signing an account out everywhere, disabling it and cancelling your own request ask
in the same dialog (`ConfirmDialog`), never the browser's confirm box: the question as its heading, its consequence
under "This will"; a heading with nothing under it is left out.

**Request state everywhere** (`web/src/lib/request-state.ts`). For an account that requests,
search cards, the title page (hero button and request card) and the request sheet show one action from the same
rule: **View request** when the caller has an open request (pending, approved, processing) for the title, linked to
`/requests?request=<id>`; **View availability** when every version the caller could ask for is in the library or
monitored. The title hero keeps title-wide readiness; the request card uses only the versions applicable to this
account's request set: **Available** only when those copies can be watched, else **Getting ready** (part of it) or
**Waiting for a copy** (monitored, nothing yet). "On its way" and "being looked for" need a
copy that is acquiring (`control.grab_state`, kept for every account: mode active and Pause off); with
none, the card says **Partly available** or **Not available yet** and that the rest is not being fetched yet. The card's sentence names those copies ("What you can ask for (4K Remux) is already monitored but not ready to watch yet…"), so beside a hero that says another copy is ready to watch it reads as about this account's copies, not the title;
**Request more seasons** when the caller's versions are all there but some series seasons are not monitored (also on
the Episodes tab); otherwise **Request**. The sheet never ends on a disabled button. The caller's open
requests come from one `GET requests?mine=1&status=open` (the caller's own only), matched by title id or kind and
TMDb id. Global search reads "Search movies, series and anime".

**Library views** (`web/src/pages/catalog.tsx`, `web/src/components/library/`, `web/src/lib/{library,library-status,index-columns,poster-options}.ts`,
`internal/web/copies.go`). A library holds one rendition of a title; its monitoring
can be changed. Its **coverage** is the episodes it holds against those aired (a
movie copy is held or not), and its **acquisition** is one summary of the downloads in hand for it, in stages:
Queued, Starting, Downloading N %, Download complete, Waiting for mount, Linking, Blocked ("Upgrade …" when the
copy keeps a file meanwhile). Every stage links to Activity (`?show=downloading`, `waiting`, `attention` for Blocked).

**Library names**: one rule, on the server (`internal/policy/names.go` `LibraryNames`), for every account and screen. A
library's name is its **Name** when an admin set one (Settings › Libraries, the editor's optional Name, at most 40
characters; connected apps never see it), else a default from its key, profile and kind ("4K WEB", "4K Remux",
"Anime movies", "Anime series"). Libraries that would share a name are told apart by words, never by key: by kind
when their kinds differ ("4K movies", "4K series"), then by number in key order ("4K", "4K (2)"). `GET auth/me`
sends every library's name (`libraries`), and the shared library tag (`VersionTag`) and other library words in the
SPA take it from there (`useLibraryName`), so a screen that only holds a key still shows the name. The key is the
tag's tooltip (accounts with See files only) and its `data-vkey`; it is printed only where it is the thing being
edited or identified: Settings › Libraries (muted, under the name), the Naming tab and Connected apps (beside the
name), and Advanced › Policy files.

**Providers and torrents**: a provider is printed through the provider tag (`ProviderPills` / `providerLabel`: RD, AD,
TB). Where only an account key is at hand, `ProviderTag` resolves it to its kind's code first (`GET auth/me`
`providers`, accounts with See files; a custom key such as `realdebrid` reads RD) with the key as tooltip; server text
does the same ("Grabbed #1 via RD"). A torrent hash with a destination is a link to Admin › Torrents filtered to it
(`InfoHash`, `TorrentLink`). A title named next to its id
links to the title page.

**Formatting** (`web/src/lib/format.ts`): one helper per kind of value, used by every screen. Numbers, counts and
scores go through `fmtNumber` ("1,128", "4,200"); sizes through `fmtBytes` (binary units, "9.6 MiB", never "KB");
moments through `fmtTime` ("Nov 13 14:15", the year only when it is not this year) and `fmtAgo` ("3 h ago"); calendar
days through `fmtDay` ("Oct 27", "Oct 27, 2024"), and a list whose dates span more than this year (an episode table,
a watch order) through `dayFormatter`, which then puts the year on every date so "Jan 1" never follows "Dec 25,
2025"; Go durations through `fmtGoDuration` ("2 h", not "2h0m0s"). No screen prints an ISO date, a browser-locale
`toLocaleString` or a raw byte count. A count and its noun use `plural` ("1 title", "2 titles", "1 frozen copy";
`pluralWord` for a verb: "1 title matches", "2 titles match"). A title's name with its year uses `titleWithYear`,
which does not add a year the name already ends with ("Foundation (2021)", never "Foundation (2021) (2021)");
where the year is printed beside or under the name, the name goes through `nameWithoutYear`. Equal sort names order
by the full name, then the year ("Alien" before "Alien³").

**Shared controls**: a sortable header (`SortHead`) looks like every other header (small capitals). A list read a page
at a time shows the pager only when there is more than one page; a list that fits one page says "All 48 shown". A
table wider than its frame scrolls inside the frame, and while columns are hidden on the right the frame's right edge
fades. On a phone, a title page and Home's "See all" lists highlight **Browse**.

**Codes in words** (`web/src/lib/code-words.ts` and one file per domain): a stored code is never printed as it is.
Rejection reasons (`rejection-words.ts`: "DiskHigherPreference" reads "The file held is preferred"), TMDb statuses and release types
(`tmdb-words.ts`: "inCinemas" reads "In cinemas", "theatrical_limited" "Limited cinema release" or, in the Movies
"Next" line, "Limited release"), anime formats, mapping statuses, roles, sources and order kinds (`anime-words.ts`:
"TVDB_SPECIAL" reads "TVDB special", "heuristic" "Matched by date and title"), the router's attempt outcomes, step
codes, cache states and sources and back-off subjects (`router-words.ts`: "uncached_deleted" reads "not cached, our
add deleted", "router:miss" "from a search that found it nowhere"), torrent states and the torrents journal
(`torrent-words.ts`: "downloaded" reads "Complete", "backup ok" "Backed up", "schedule" "Scheduled"), and job kinds
(`jobs.ts`). A code no map knows reads as itself made readable (underscores and camel case to words, sentence case:
"some_new_code" reads "Some new code"), never blank. Where an admin may need the code, it stays as the element's
tooltip. Sentences the server builds keep their words in one Go function: the blocklist's reason (`blockWords`,
including "Imported from Radarr's blocklist" for `arr:<instance>`) and the health checks' messages (dates as "Nov 17").

- **Page:** Movies, Series and Anime use the full width. The toolbar has the title search ("Filter by title", the
  `q` parameter) and **Select** (managers) on the left; **Columns** (table) or **Poster options** (posters), the
  Posters / Table switch, **Sort** (a select and the direction button, in both views since a phone has no headers; a
  row of its own on a phone) and **Filter** (with the number of active filters) on the right. Anime has **All /
  Movies / Series** (`?kind=movie|series`: the list, its counts and filters follow it); Series and Anime have a
  **Monitored** shortcut (`?monitored=true`); nothing is filtered by default. The count line keeps its height while
  it loads, so the toolbar does not move. Filters open in a sheet at every width ("Counts
  are for the whole list, before other filters."). Active filters show as chips under the toolbar, each a button that
  removes it ("Remove filter: Missing"), then **Clear all**: `Title: “dune”`, `Monitoring: on`, `Monitoring: off`, the
  availability word, `Has 4K`, `Lacks 4K`, `Quality: WEBDL-2160p`, `Provider: RD`, `2019–2024` (or `From 2019`,
  `Up to 2024`), one per genre. Without See files there is no library, provider or state chip. With nothing found:
  "No titles match these filters." and **Clear filters**. The sheet heads its first group **Monitoring** (On, Off:
  whether a library monitors the title); the status **Not monitored** (no file and nothing monitors it) is apart,
  its option named **Not monitored, no file** so its count is not read as Monitoring Off's. Filters, sort and a picked
  view (`?view=grid|table`) stay in the address, so a link opens the list as it was; a sort the account
  is not offered falls back to Title (Movies offer no Completion, and their column hints speak of movies only).
- **Colour rule** (`copyTone`, `lib/library-status.ts`), one for bars, dots and pills: green (a movie library has a
  file; a series library has no monitored, aired episode missing), red (monitored, released or aired, and missing),
  accent and striped (a download in hand), amber (the download in hand is blocked), grey (not monitored, not
  released yet, or nothing aired yet). "Not released yet" is the badge's own rule, so a bar never reads Missing
  while the badge reads Upcoming. Every bar and pill says the same in words in its tooltip and accessible name ("4K:
  13 of 17 episodes linked, 2 in progress", "WEB: missing", "4K: not monitored").
- **Table** (`components/library/title-table.tsx` on the shared `DataTable`): one line per title. **Title** is fixed
  (thumbnail, name with the year beside it, an eye-off icon when not monitored; it keeps its width, library pills wrap first); then the columns chosen for the list (`lib/index-columns.ts`;
  **Columns**: "Title always stays. On a phone the chosen columns show as lines under each title."). Defaults: Movies
  Status, Libraries, Size, Added (Studio on offer); Series and Anime Status, Libraries, Network, Next airing, Seasons, Size. On
  offer as well: Latest file, Previous airing (series), Quality, Quality profile, Year, Release date / First aired, In
  cinemas, Digital release and Physical release (movies), Release status / Series status, Series type (series),
  Runtime, Certification, Rating, Genres, Original language, Minimum availability (movies), Provider and Folder.
  **Status** is the poster badge's word in its tone, with the date for Upcoming, or the stage of a download in hand
  linked to Activity. **Libraries** has one pill per library in the colour rule: "4K · Remux-2160p", "WEB ·
  Missing", "4K · 13/17" (a series has no count before anything aired); a click opens the title's Libraries dialog
  (each library's file, state, stage and monitor switch; search and files stay on the title page). **Added** is the
  day the title was added (sort `created`); **Latest file** is when the newest current file came (sort `added`).
  Provider shows RD, AD, TB pills. Managers get a fixed last column (not in **Columns**) with **Search now** (the
  title page's: every monitored library, missing and upgrades; the answer is a toast) and, for a movie,
  **Interactive search** in its first library, for a series **Choose season or episode** (a link to the Episodes
  tab). Each is disabled from the row's own data with the reason as tooltip: Pause, "No library is monitored.", else each monitored library's verdict; on a phone they end the card with their words. Headers sort where the server can: Title, Studio / Network, Next, Size, Added,
  Latest file, Quality, Year, Release date, Runtime, Certification and Rating. The per-library completion sort
  (`sort=completion&sort_version=<key>`) stays in the API and old links, but no header offers it. The Sort menu and
  the column headers use the list's words (Movies: Next release, Studio, Release date; Series: Next airing, Network,
  First aired; Anime, which mixes both: Next episode / release, Network / studio, Released / first aired). An undated
  title sorts last by Release date. A sort whose column is not chosen (Next
  release, Latest file, Quality) shows that column first while the sort lasts, so the order follows a visible cell. Below 640 px rows
  are cards: thumbnail, title with Status on its own line under it, then the pills, then each chosen column as a line. Without See files the
  Libraries, Quality, Quality profile, Size, Latest file, Minimum availability, Provider and Folder columns are not
  offered and Status is headed **Availability** with the watchability words.
- **Posters:** one status badge, top right: the server's status word (Status words: Available, Partly available,
  Missing, Not monitored, Upcoming), and "Upgrade found" on the year line when a better release was found. **Partly
  available** (amber) is a title that one library holds while another, monitored library lacks a file or an aired
  episode (the server's `partial`, the Partly available filter); a library that is not monitored does not count, so
  such a title reads Available. Family accounts get the server's watchability as is, so that movie reads Available
  for them; never Missing. Top left a small eye-off chip when the title is
  not monitored; the Movie / Series / Anime pill is not shown on these pages. Between the art and the title, for
  accounts that see files: a movie in one library has one thin bar; a movie in several has one bar in the badge's
  tone and a line of pills, one per library (dot and label); a series in one library has one bar with its count
  ("13/17"); a series in several has one line per library (label, bar, count). The in-flight chip over the art shows
  the most advanced stage, linked to Activity. **Poster options** ("Choose the size and what shows under each poster.
  The status bar and the library lines always show."): size (small, medium, large; on a phone small fits three
  across, the others two) and the details under each poster: Year (the default), Next release / Next airing, Studio /
  Network (Anime: "Next episode / release", "Network / studio"), Quality ("4K: Remux-2160p · WEB: WEBDL-2160p"), Size (the title's total), Added (the day the title was
  added) and Rating; Quality and Size need See files. Without See files a poster has the badge only.
- **Select** (managers) works on both: tick titles ("Select all 60 shown" selects the loaded rows); the bar counts
  titles ("12 titles selected") and offers Monitor, Unmonitor, Search now, Add to a library (with the add
  permission), Remove from a library and Remove. Household accounts have no Select.
  - **Monitor / Unmonitor** open a review: the library (**Every library** by default, or one) is picked there, and
    each change asks `POST titles/monitor` with `dry_run`, listing what changes, what is already set and what is
    excluded (titles without the copy); **Apply** sends the preview's values as `expect`, so a copy changed since is
    skipped as stale, and lists each outcome. It only sets `monitored` on existing copies: nothing is searched or
    grabbed. The Libraries dialog's switch uses the same route for one copy.
  - **Search now** confirms, then sends one item per title and library it is monitored in to `POST wanted/search`
    (`only: "missing"`, 200 items a request, one request after another): no library is picked first, and a title
    monitored nowhere is left out and said so. They are made due like the Wanted page's Search now and start as
    `router.wanted.per_hour` allows; nothing goes around it. The bar shows the answers added up: searches queued,
    how many started now and how many wait (the last answer's), and the titles skipped by the server's reason, with
    a link to Wanted. A refused request (409 while new downloads are off) shows the server's message and stops the
    rest.
  - **Remove** is the title page's removal dialog for the selection: `POST titles/removal {title_ids, versions}`
    reads each title's removal scope (the per-title `GET titles/{id}/removal`, for up to 500 titles in the selection's
    order; an unknown library is refused; a title with nothing to remove is skipped with the reason), and the dialog
    sums the counts ("Remove 12 titles": libraries, downloads, library files), names the first titles ("Alpha, Beta,
    Gamma and 9 more") and has the same two choices, both on. Confirming handles each title in turn: it reads the
    title's scope again for exactly the libraries its preview showed, skips it as "changed since the preview" when
    its libraries, files, downloads or whether it leaves the library differ, and otherwise runs `POST
    titles/{id}/remove` with those libraries (the same removal, debrid clean-up rules included). It shows "Removing
    3 of 12…" and ends in one notice: how many were removed, which were not and why, files left in place. Closing
    the dialog stops after the title in hand and leaves the selection as it is.
  - **Remove from a library** is the same dialog with one library picked in it (the libraries the list shows):
    titles not in it are skipped and counted; a title in no other library leaves the library, as on its page.
  - **Add to a library** picks one library of the list's kinds (Anime: movie and series libraries) and turns it on
    for each title through `PUT titles/{id}/versions/{key}` (monitored), as the add sheet does; titles of the other
    kind and titles already monitored there are skipped and counted. Nothing is searched. There is no bulk profile:
    a library's profile is how a title's quality changes, so moving a title between libraries is the way.
- **Series page:** an episode cell shows its stage (and progress while downloading); an **In progress** filter sits
  beside Missing; the episode overview shows each library's acquisition (words, progress, last update, waits,
  retries) with an Activity link. Anime keep "E13 · absolute 13".
- **Preferences** stay in this browser per user and per list (`<list>` is `movies`, `series` or `anime`):
  `theatarr.library.view.<list>.<user>` (falling back to the old `theatarr.catalog.view`),
  `theatarr.library.columns.<list>.<user>` (the old shared column choice is not read, so each list starts with its
  defaults) and `theatarr.library.posters.<list>.<user>` (falling back to the old shared poster options, so the size
  carries over). The view last picked is the default when the address has no `?view=`. Sort and filters are not
  remembered: they stay in the address.
- **Live:** a download's change of state refreshes the Activity lists (Home's Continue row among them), the
  Calendar, its title and the lists at once. Progress alone refreshes only what shows progress: the queue and the
  Continue row at most every 5 s per download, its title and the lists at most every 20 s
  (`web/src/api/live-activity.ts`). A request event refreshes the one title it names. When the event stream
  reconnects after a drop (a settings save closes the streams), the page reads what it shows again, once.

API fields (all additive): `TitleRow.versions[]` (VersionBrief) gains `control`, `coverage` {have, total, aired,
upcoming, missing, monitored, in_flight} and `acquisition` {stage, words, progress, download_id, downloads,
episodes, stages, upgrade, quality, updated_at, waiting_since, next_retry}; `TitleRow` gains `next_up` {date,
season, number, name, type}, `network`, `home_release` (movies, YYYY-MM-DD) and `acquiring` (every caller); the episode list's season counts gain `in_flight` and `stages`, its
cells `acquisition`; `Version` (title page) gains `acquisition`. A movie copy's `control` (`versions[].control`) and
each season's counts on the episodes route gain `next_search_at` (from when the next automatic search may start; now
or earlier: due) and `next_search_why` (`asked`, `new`, or the failure reason), both left out when the Wanted page
gives the item no time. `GET titles` sorts gain `completion`, `next`,
`quality` and `activity` (the last two need "see files") with `sort_version`. Accounts without "see files" get
coverage counts only (no control, no acquisition, no missing/monitored/in_flight). New route: `POST titles/monitor`
{title_ids (1–500), versions (absent: every copy), monitored, dry_run, expect} → {dry_run, items [{title_id, title,
version, label, outcome: changed | unchanged | read_only | missing | stale, from, to, reason}], counts, message}
(manage permission).

List rows also carry `title_added_at` (when the title was added), `status` (a movie's announced, inCinemas or
released; a series' TMDb status), `original_language`, for movies `in_cinemas`, `digital_release` and
`physical_release`, for series `series_type`, `season_count` (specials left out) and `previous_airing`; each version,
for "see files" only, `profile`, `minimum_availability` (movies) and `folder` (the title's folder in that library).
`versions[].providers` are cache codes (`rd`, `ad`, `tb`; a provider of another kind by its key) of the providers that
hold a current file complete now, adopted files included. `GET titles` sorts gain `created` (`title_added_at`),
`runtime`, `certification` and `network` (the last two A to Z by default); `added` stays the newest current file.
The `provider` filter takes a cache code or a provider key, and the provider facet counts cache codes. A copy removed
from the library is left out of the rows, availability, sorts, filters and facets.

Not in the library views: bulk Profile, Upgrades and Minimum availability fields, Search / Releases / Grab buttons on a row (only the bulk
bar's Search now; the rest stay on the title page and the episode overview), Original title, Collection, Release
group and Languages columns, sorting by the movie dates, Seasons, Status or Libraries, and an Audio / subtitles
column (no list projection of media facts).

### Admin panel (`/admin/…`)

| Screen | Path | Holds |
|---|---|---|
| System › Status | `/admin` | Needs attention, then Checks, first; the rest is folded under **Details**: Readiness (Downloads, Library links, Notifications ON/OFF, linked to their settings, and the newest complete Theatarr backup), the download and task strips, Libraries and Diagnostics. Needs attention groups failed jobs by kind with newest error, Retry newest and See all; single failures retain Retry/Dismiss. Held indexer definition updates, health failures (the debrid-account check names each provider and what to do: "TorBox premium ends Nov 17 (in 43 h)." with "Renew at TorBox to keep premium access."; a refused key or an account that could not be read keeps "Check the account and its API key under Settings › Download clients."), back-offs and delayed downloads; then download/task summaries, libraries and diagnostics. A download held for review (the library refused it) or whose link failed counts under need attention only, not also in progress; one the library refused and blocklisted is not counted (it left the queue: Activity, above). A delayed download names its title with episodes and library, its stage, its reason when it failed ("Failure reasons") (or whether some files are linked) and its release; Inspect opens that download in Activity (`/activity?open=<id>`). The download and task strips and the readiness switches say Loading (or the error) until their answers arrive, never 0, Unknown or OFF. The dangling-links check counts links under the libraries' roots only (other folders the report walks, `library.dangling.roots`, are not counted); its row has a "Show the N broken links" list, loaded when opened from `GET /health/dangling` (the latest report's links under the libraries' roots; `theatarr library dangling` prints every root; at most the 500 oldest, with a note when there are more): title or release folder, library, since, cause ("target missing" when no other is known), and a hint from the recorded link's state (marked missing, waiting for the repair sweep, or not a link Theatarr made). It only lists: nothing is scanned or repaired from it. |
| System › Tasks | `/admin/system/tasks` | Attention, running/queued, the scheduled tasks (interval, last run and duration, next run, Run now), and folded resolved/finished work; `?kind=` filters on the server before the limit. The "failed, resolved ones included" count is every failed job plus the failed scheduled tasks; "need attention" is the open ones plus the failed scheduled tasks, as listed under Needs attention, each counted once (the server's `attention` count already includes the tasks); with `?kind=` every count in the strip is taken from the rows of that kind. While both backlog switches (`router.wanted.missing`, `cutoff_unmet`) are off, "Search everything wanted" has a muted second line under its name: "Backlog searching is off: only requested searches, new additions, searches after a failed download and new-release retries run." (read from the stored router section). Technical job kinds and details are kept. |
| System › Backup | `/admin/system/backup` | Theatarr backups and confirmed restore; the torrent-list schedule as text (changed in Settings › Download clients › Torrents), backups, import, download and restore preview; review-only settings export link; database counts (title-library entries, torrents in the registry, library links), nightly run (Loading or the error until it loads, never "none recorded") and `#backup-check`. No advanced-settings toggle: the page has no advanced field. |
| System › Logs | `/admin/system/logs` | The log viewer and its filters ("Show lines from", component); the service's own log level is a separate card above the lines (`#log-level`) with **Apply**: it changes what the running service writes until a restart. |
| Settings › Anime mappings | `/admin/settings/anime-mappings` | The anime review queue (Waiting, Accepted, Rejected, Replaced, All; one card per item), the franchises with their mapping counts and builds, and anime series without a franchise; a franchise at `/:slug` with alternate cuts and episode numbering (docs/design/anime.md). System › Status shows an **Anime mappings** notice while items wait. The old Reviews hub went in 0.4.5: `/admin/reviews` and `/admin/reviews/decisions` redirect to the journal's Flagged view, `/admin/reviews/anime[/:slug]` here, `/admin/reviews/indexers` to Settings › Indexers (held definition updates are listed there, above the indexers, while any wait) and `/admin/reviews/quality` to Settings › Quality sync. |
| Torrents | `/admin/torrents` | Provider torrents from the record (torrents.md "The record"): one line per account with counts in their units (account torrents, unique hashes) and "as of" its time ("Not read yet — Refresh reads it" before the first listing; a failed read keeps showing the earlier record and says so), **Refresh** ("Reading the accounts…") says what it read per account and reloads; "not worked out" usage with its reason; usage, filters (`?q=` fills the name or hash filter), sorting, a Provider column with the RD/AD/TB tag under All providers (the strip, tabs, refusals and journal name providers the same way), each hash shown (its first 12 hex), on a failed torrent library links use and another account holds complete one muted line under its status that also starts the usage hover ("This copy failed. Another complete copy is on TB."; only what the record shows), the titles a used torrent belongs to linked under its usage (the first three) and all of them in the expanded row, confirmed deletes and journal. The provider tab, filter, text and sort are in the URL (`?provider=&group=&q=&sort=`); a name and its parsed episode list show two lines at most, the whole as tooltip. **Not used by Theatarr** (the former Unused) has a line beside the filters saying it checks Theatarr's libraries and the other libraries' folders listed in Settings › Download clients › Torrents, linked; other apps on the same accounts are not seen. An account that cannot be read gets one line: the provider's tag, why in plain words (the provider asked to slow down, paused after failed requests, request limit used up, VPN not confirmed) and the next try in local time, with the raw error under Details. The old Saved inventory addresses open Torrents (an entry filtered to its hash). Backups live in System › Backup. |
| Settings | `/admin/settings` | Search and task pages below. |
| Users & access | `/admin/users` | Users, roles, permissions, sessions and API/view keys. **Give someone access**: the sign-in link to copy, what a first Plex sign-in gets (waits for approval, or is let in at once, as a User) and what each role does; no invite system. Your own role cannot be changed (the select is locked; `PATCH users/{id}` answers 400). |
| Advanced | `/admin/advanced` | Exactly YAML settings, Settings history and Request limits. The old `/admin/advanced/arr` pages redirect to Admin › Import from Sonarr/Radarr (`/admin/arr-import`). |

One admin-only screen lives outside `/admin`, beside the page it belongs with:

| Screen | Path | Holds |
|---|---|---|
| Wanted › Manual import | `/wanted/manual-import` | Existing file linking workflow, as the third tab of Wanted. |
| System › Decision journal | `/admin/system/journal[/:id]` | Decisions and their evidence (admin); **Flagged** (`?suspect=1`) lists the entries with a stored warn or error flag; they are not under Status's Needs attention and have no sidebar count (nothing asks for an action). No verdicts. `/activity/journal[/:id]` redirects here. |

Older Jobs, Errors, Logs, Database, Metadata review, inventory, migration, journal (`/admin/journal[/:id]`,
`/admin/advanced/journal[/:id]`, `/activity/journal[/:id]`) and manual import (`/admin/advanced/manual-import`) URLs redirect in one hop to the current
pages, preserving query and hash, including detail parameters. The General #torrent-backups and #scan-roots bookmarks go to Settings › Download clients › Torrents with their query; an old anchor of a panel that moved off a page that still exists (`/admin/settings/downloads/behaviour#searches`, `/admin/torrents#scan-roots`, `/admin/system/backup#torrent-backups`) goes on to the panel's new page (`movedAnchor`, web/src/lib/legacy-routes.ts); server registry and anime detail redirects go directly to the current inventory and Reviews routes; the old Sonarr/Radarr migration, imports and comparison addresses go to **Import from Sonarr/Radarr** (`/admin/arr-import`). `/admin/system` and `/admin/system/health`
redirect only on exact matches; they do not shadow the System pages. `/classic/` routes return 404. Inventory and journal detail links
remain admin-only; managers keep copyable hashes and unlinked summaries.

### Settings

`/admin/settings` is the Settings home, with **Find a setting** and a card for each page below in sidebar order.
Search understands familiar arr terms, schema field labels and technical paths; Enter opens the first result.
Panel anchors scroll into view and briefly highlight. **Show advanced settings** is shared across all forms and
remembered in this browser; the button shows only where it shows something (Settings home, and a page tab with an
advanced panel or field). Settings home has a compact saved-settings notice with a Details disclosure; the task pages
show the notice only when something is off (startup fallback, the settings service unavailable), with the full notice
and reload/confirmation behavior. Each page says its intro once, as its description.

| Page | Route | Tabs / contents |
|---|---|---|
| Libraries | `/admin/settings/libraries` | Libraries · Naming · File management |
| Profiles | `/admin/settings/profiles` | Profiles · Check a release |
| Custom formats | `/admin/settings/formats` | Rules that match releases and influence quality scores. |
| Quality sync | `/admin/settings/quality-sync` | Sources, preview (a dry run), run, link, update log and recent runs (a logged run that changed nothing reads "No profile or format changes"). How sync works is folded. |
| Indexers | `/admin/settings/indexers` | Indexers · VPN · Catalog · Search & RSS (Automatic search, stored as `router.wanted.*`, then RSS feed checks; `/rss`). **Search for upgrades** names its stored values `quality`, `score`, `off` as "Files below the preferred quality", "Files below the preferred quality or score", "Off (RSS still picks up better releases)". |
| Download clients | `/admin/settings/downloads` | Debrid providers (with Provider account sync above their status; a status card's Pacing counts adds in the current minute and uncached adds in the current hour, 0 once that minute or hour is over, "1 add") · Other clients · Behaviour (what to grab, cache checks, retries) · Torrents (torrent-list backups schedule, other libraries' folders; `/torrents`) |
| Import lists | `/admin/settings/import-lists` | TMDb lists, TMDb discover rules, JSON lists and Radarr or Sonarr instances, and the list exclusions (below, Collections). Managers open it too; it has its own screen, not settings panels. |
| Notifications | `/admin/settings/notifications` | Where and when Theatarr sends messages. |
| Plex & playback | `/admin/settings/plex` | Plex connections, playback and playlists; read-only Playlists Theatarr keeps below the playlists panel, folded with its count ("90 playlists, 8 empty"). |
| Metadata & artwork | `/admin/settings/metadata` | Metadata services, refresh schedules and the artwork cache. |
| Connected apps | `/admin/settings/apps` | Compatible endpoints for Seerr and other apps: each app's libraries, profiles and API key (an app without its own key reads "uses admin API keys" beside its path). Replace a library puts a new library in an old one's place in the app's libraries and every profile, in the draft; Save sends both. |

Other libraries' folders (`#scan-roots`) and the torrent-list backup schedule (`#torrent-backups`) are on Download
clients › Torrents; Torrents says beside its filters what "Not used by Theatarr" checks and links the folders, and
System › Backup shows the schedule as text with **Change the schedule**. `/admin/settings/general` redirects by anchor.

Libraries includes the existing library editor (`?edit=key`); app bindings are on Connected apps. Profiles keeps `?raw=1`, presets,
and the hidden `/admin/settings/profiles/format_sets` editor. Its **Check a release** tab
(`/admin/settings/profiles/check`) judges a pasted release name for one title and library through `POST /decide`,
read-only ([decide.md](decide.md#release-check)). Its optional size is in GiB, as sizes are shown, or carries a
unit as in the settings forms (`36 GB` is 36×10⁹ bytes); text that is not a size is refused beside the field and
nothing is checked. Indexers and debrid providers show status beside
their editors, with Edit links. VPN status is above its settings. **Send notifications** is the first field of the
Notifications page's Destinations panel (`notify.enabled`; "Off: events are recorded, nothing is sent.").

**Pause.** Admins see one **Pause** switch in the top bar of every page; it sends `{generation, paused}` in one
`PUT /settings/switches`. Off, its tooltip says "Pause stops searches, new downloads, list syncs and changes in your
library folders, for every library. Downloads already started wait and are imported when you turn Pause off."; on, "Paused:
nothing is searched, downloaded or linked. Turn off to resume." Turning it off asks for confirmation; turning it on
does not. When `THEATARR_FORCE_OFF` forces it, the switch is a disabled pill **Paused (locked)** titled "Paused by
THEATARR_FORCE_OFF. Remove it and restart to resume." Searches that are due (Search now, the schedule) wait while paused and start when Pause is turned off; downloads
already sent keep going. The Wanted page shows those rows as Paused, never with a time, and its Search now line says
the searches wait until Pause is off.

**Library state.** Each row on Settings › Libraries says the library's state in words, from the running
`grab_state` ("Downloads on", "Paused", "Downloads held by the server", "New files won't be linked", "Not set up").
There is no per-library on/off: Pause holds every library, and a title's Monitored switch holds it in one library.

**Library editor.** "Where" starts with the optional **Name** (shown in place of the guessed label; the request
leaves it out while it is empty, and sends it empty only to clear a stored one) and the **Key** (fixed once the
library exists). Prefer SeaDex is a switch; Upgrades
reads "Automatic" and "Never". Write roots, Send grabs to and Client sit in an **Advanced** disclosure at the end of "Where", with Storage and Import mode, which are shown only
when the saved or the draft value is not the one supported value (debrid, symlink). Advanced starts open when the
library has write roots other than its root, sends its grabs to a download client, or shows Storage or Import mode, and a field inside that the server refuses keeps it open. The request body and its field
names are the same as before. A movie library's "Which releases" also has **Minimum availability** (Not set,
Announced, In cinemas, Released; `minimum_availability`, for movies with neither a choice of their own nor an
imported value) and **Availability delay (days)** (`availability_delay`, whole days, negative for earlier, empty
for none). Like the name, each is left out of the request while unset and sent empty or 0 only to clear a stored
value, so a save that does not touch them stores the library unchanged; a library changed to series drops both.

Advanced includes **YAML settings**
(`/admin/advanced/config`, with Policy files, Sections and Import & export tabs), **Settings history**
(`/admin/advanced/history`) and **Request limits** (`/admin/advanced/limits`, including back-offs and budgets).
Previous admin URLs redirect with their query string and hash intact.
On phones (below `md`) the side list is hidden, so the top of each Settings page has one labelled **Settings page**
select that moves between the pages and their tabs (grouped by page, in place of the route tabs' Section select), with
Show advanced settings under it. Request limits lists each budget once, in the editable table, with its cap bars and
who uses it by name ("Provider AD", "Indexer prowlarr-tpb"), its use read from the live gateway state ("no hourly limit"
/ "no daily limit" for a period the budget does not cap, which the gateway does not count); the status part below keeps
the back-offs.

**Settings forms.** Panels (`section-panel.tsx`, through `field-input.tsx`) and record sheets
(`record-form.tsx`) share one pattern: an edited field is marked **Changed** with **Undo** back to the saved value;
**Reset** sets a leaf back to its default (the key is removed); help text and the server's validation message sit
beside the field and are tied to its control with `aria-describedby` (`aria-invalid` on an error). A panel with
changes shows a sticky bar (above the phone tab bar) with the change count, **Discard** and **Save**; a record sheet
shows the count and Discard in its footer, and Discard keeps the sheet open and clears the discarded draft's
validation. Leaving a page with unsaved changes (another page, Back) asks first ("Leave without saving?"), and so
does closing a sheet with them (Escape, the backdrop, Close, Cancel: "Close without saving?"); a reload or closed tab
gets the browser's own question; tabs, anchors and `?edit=` on the same page are not leaving it (`lib/unsaved.ts`,
`LeaveGuard` in `unsaved-bar.tsx`, the sheet's guard in `ui/sheet.tsx`). A few values the server would refuse are
checked as typed (`lib/field-checks.ts`: a daily time as HH:MM, the http(s) addresses, a new record's stable name):
the reason sits under the field and Save waits, with "Fix the marked field to save." beside it; the server stays the
authority. Indexer and provider order arrows save at once and say so ("Saved: dmm moved up."). A new provider or
indexer is tracked against its first draft, its name included. Fields that generate other
values have a Changed mark but no Undo (a record's Service, which fills a new record's name and URL; a library's
Root and Write roots); Discard resets the whole draft. Undo under an object the saved settings
did not have leaves it absent, so the draft equals the saved value again. The Libraries editor has the same marks (a new
library's key included), count and Discard (it has no schema defaults, so no Reset); list rows tie their indexed error and the help to their
control. Durations and byte sizes are shown and entered in plain units, booleans are switches, and Add buttons name
one item; stored values are never rewritten on load ([Settings](settings.md), Section editing). Most saves apply at once; the few changes the server confirms (Pause off, removing a library, or an address change that keeps a stored
key; [Settings](settings.md#sections-and-views)) keep its 422 confirmation: the dialog (`EffectsConfirmDialog`) lists the effects word for word as the server returns them,
grouped by library (effects that name none first, under "This change"; nothing is added to what the server sends), says which libraries are affected and, when the change is about one
library, names the other libraries it also affects; Cancel has the focus.
The torrent-backups panel is owned by the System Backup page and is searchable without appearing in Settings navigation. The field placement table is in [Settings](settings.md).

### Settings and system pages

- There is no Settings › General: Provider account sync is on Download clients › Debrid providers, Artwork cache on
  Metadata (renamed **Metadata & artwork**), other libraries' usage folders on Admin › Torrents. Old links, search
  entries and `/admin/settings/general#…` bookmarks go to the new places (`lib/legacy-routes.ts`).
- **Connected apps** (was Apps) is the one place for arr app bindings and keys; libraries and profiles are named
  checkboxes and the key help links to Users & access. Libraries links there.
- New indexers show name and URL up front; the indexer provider allowlist is Any or named checkboxes.
- Profiles have structured **Size limits**; Custom formats is one definition list with Edit definition, linked both
  ways with profile scores.
- Settings history is newest first with Older/Newer.
- Phones (400 px): Cache checks labels wrap with a one-word Reset; provider status cards do not push Download
  clients sideways.
- Details: [settings.md](settings.md#form-details).

## 3. Multi-user

Details: [users.md](users.md) (permission matrix, Plex sign-in, requests API). Accounts, sessions, CSRF and API keys
live in `internal/auth`.

### Roles and permissions

A role is a preset of permission bits; an admin can tick extra bits per user (Seerr does the same with a bitmask).

| Permission | admin | manager | user |
|---|---|---|---|
| Browse library, title pages, calendar, own activity | yes | yes | yes |
| Request a title or a version | yes | yes | yes |
| `auto_approve` (requests apply at once) | yes | yes | optional |
| `request_versions` (which version keys they may ask for; e.g. users may ask for `movie-web` but not `movie-remux`) | all | all | policy default list |
| `add` (add titles and turn versions on directly, no request) | yes | yes | no |
| Approve / decline others' requests | yes | yes | no |
| See files, paths, hashes, providers, scores | yes | yes | no (quality and size only) |
| Search now, show releases, relink, remove a version | yes | yes | no |
| Admin panel (system, torrents, policy, logs) | yes | no | no |
| Users and API keys of others | yes | no | no |
| Own API key | yes | yes | optional |

`manager` is the operator role: it runs the library without seeing the plumbing. There are no request quotas.

### What each sees

- **user:** Home without the "Getting ready" row's provider detail and without the copy-shortfall rows (Missing,
  Below cutoff); availability as watchability: a movie with any copy is Available, a series with some episodes and
  no complete version "Some episodes available", nothing "Not available" (`docs/design/library-ui.md`); grids and title pages without the Details tab;
  Search adds become requests; Activity only for the copies they requested; Requests: their own.
- **manager:** everything in the library app; the Requests queue; no Admin group in the sidebar.
- **admin:** everything.

The API enforces this, not the UI: each handler declares the permission it needs next to its `Access`
(`SignedIn`, `SessionOnly`, `Public`), and the field filter for "see files" runs in the handler. API keys belong to a
user and carry that user's permissions; the arr views keep accepting admin keys and view keys only.

### Requests

`/requests/<id>` shows Requested → Awaiting approval → Getting ready → Available, or the declined/canceled branch, with recorded creation/decision/status times, decision actor and note; self-approval skips the approval wait. A failed request's Getting ready step reads **Did not work** ("This did not work. A manager can look at it."), before any paused-downloads words; each copy's progress stays under it. A user who joined another member's request sees "Someone else asked first; you joined" instead of a name (the server sends no other requester, docs/design/users.md). Getting ready uses Activity’s provider-free stage words for concurrent downloads, requested-season episode availability, or an honest waiting explanation; missing historical transition times and ETAs are left unknown (request events are streamed, not persisted). The phone layout stacks facts and timeline; desktop places them side by side.

Managers keep inline queue Approve/Decline and see what approval does per requested version in the detail and editor: “Approval records this request; automatic downloads are paused” while Pause is on or the library is turned off. Before submission, the sheet applies account copy defaults without version choices and says whether this account needs approval; detail visibility stays own/joined requests for users and all requests for managers, with no file, size, quality-source, provider or path fields in the progress projection.

1. A user finds a title (library or TMDb); Theatarr applies the account’s request versions (by default every version that
   fits the title) without asking them to pick copies. The sheet says whether approval is needed; for a series they
   choose seasons, and may add a note.
2. Theatarr stores a `request` row, `pending` (or `approved` at once with `auto_approve`). Duplicates merge: a second request
   for the same title and version adds the user to the existing one; an `auto_approve` ask approves a pending one it
   joins only when the request asks for nothing beyond that ask (docs/design/users.md), and the sheet says so.
3. A manager or admin approves (optionally changing versions or seasons), or declines with a reason.
4. **Approval runs the same add path as the arr write API**, `catalog.Add` (`internal/catalog`): find or create the
   `title` row by TMDb/TVDB id, turn the versions on (new versions start `wanted`), apply seasons and monitoring, and
   optionally record a search command. The arr views call it with the versions chosen from the view's profile;
   request approval calls it with the request's versions. One code path, one set of tests; the arrview contract
   tests keep Seerr's view unchanged.
5. The request then follows the versions: `approved` → `processing` (a download exists) → `available` (every
   requested version is filled) or `failed`. Users get an SSE event and, later, a notification (`internal/notify`).

**Problems** (`/requests/problems`, a tab beside the request tabs for every account; docs/design/users.md "Report a problem"): a household member's own problem reports with the status and the manager's reply; for managers every open report with what is wrong, the note, who and when, the files the title's libraries hold for it now with **Replace file…** (or why it is not offered, with a link to the title page), and **Mark fixed** with an optional reply. "Report a problem" sits beside a movie's status on its title page, and under the synopsis in the episode window, when there is a file. The Requests count includes open reports for managers; System › Status "Needs attention" shows "N problem reports" for admins.

Seerr stays supported as a client of the arr views. Built-in requests are for people who use Theatarr directly.

Approved and processing request text and timelines use the effective grab gates of their applicable versions.
When all those versions are held (Pause on, or a server hold), they say the request
is not being fetched yet. Available copies remain available. Library rows and request progress show closed grab
gates, with links to the library and Pause for admins who can change them.


### Schema (migration `00026_users.sql`)

- `web_user`: `role TEXT NOT NULL DEFAULT 'admin'`, `permissions INTEGER`,
  `display_name`, `disabled_at`.
- `api_key`: `user_id` is the owner.
- `request`: `id`, `user_id`, `kind`, `tmdb_id`, `tvdb_id`, `title_id` (NULL until approved), `name`, `year`,
  `poster_path` (art captured at request time, as Scryer does, so a pending request shows a poster before the title
  exists), `versions` (JSON keys), `seasons` (JSON), `note`, `status` (`pending`, `approved`, `declined`, `canceled`,
  `processing`, `available`, `failed`), `decided_by`, `decided_at`, `reason`, `created_at`, `updated_at`.
- `request_user`: extra requesters of a merged request.

## 4. Frontend stack

**A single-page app in React + TypeScript + Vite + Tailwind CSS, built to `internal/webui/dist` and embedded
in the binary with `go:embed`, talking to `/api/theatarr/v1` only.** Small dependency set: React Router, TanStack Query,
openapi-typescript for types, shadcn-style components on Radix primitives with lucide icons (as Scryer and Seerr use),
most of them copied from Scryer's `apps/scryer-web` under attribution.

Versions (`web/package.json`, exact versions, lockfile committed): React 19.3, React Router 7 (library
mode), TanStack Query 5, Tailwind CSS 4 through `@tailwindcss/vite`, `radix-ui`, `lucide-react`, Vite 7, TypeScript
5.9, ESLint 9, Vitest 4. React 19 and Tailwind 4 are what Scryer's components are written for, so they copy without porting. Two CSP rules shape the code: no inline
`style=""` (bar widths and generated poster art are class steps), and no Radix modal dialog or menu (react-remove-scroll
injects a `<style>`), so menus run with `modal={false}` and every overlay and sheet (search, add, filters, phone
navigation) is Theatarr's own `Modal` (`web/src/components/ui/modal.tsx`): focus moved in, kept in and given back, the rest of
the page `inert`, and a reference-counted scroll lock class (`lib/modal-stack.ts`), so a sheet over the search overlay
closes without unlocking the page. Query keys come from one module (`web/src/api/query-keys.ts`; title ids always
strings), used by reads, mutations and the live event handlers alike.

Why not server-rendered templates plus htmx partials: the screens are interactive (instant search
across library and TMDb, filter chips with live counts, add sheets, sideways rows, a season grid, live activity).
Built with templates, each of those is a hand-made partial endpoint plus JavaScript glue, and the result is two UIs
(HTML partials and the JSON API) to keep in sync. With an SPA the UI is the first client of the same API the phone
apps and scripts use, so every screen also proves an API. It is also the stack Seerr and Scryer both chose.

| Concern | How the SPA handles it |
|---|---|
| CSP `default-src 'self'` (`img-src 'self' data:`, `script-src 'self'`, `style-src 'self'`) | Vite emits hashed `.js` and `.css` files: no inline script, no `eval` (React and Vite's production build need none). Tailwind is a compiled CSS file. Inline `style=""` attributes are blocked by `style-src 'self'`, but React sets styles through the CSSOM, which CSP allows; a lint rule bans raw HTML strings. Images come from `/img/…` (same origin) |
| Session, cookie, CSRF | Login is `POST /api/theatarr/v1/auth/login` (JSON, returns the CSRF token); `GET auth/me` gives the token after a reload. The API client sends `X-CSRF-Token` on every non-GET. Cookie stays `HttpOnly`, `SameSite=Lax`; the SPA never sees it. A 401 sends the user to `/login?next=…` |
| SSE (`events.go`) | `EventSource('/api/theatarr/v1/events')` is same-origin and sends the cookie. TanStack Query caches are updated from events (`jobs`, `gateway`, `download`, `request`, `title`). One connection per tab; it reconnects with the browser's built-in retry |
| Docker multi-stage (`Dockerfile`, `make image`) | A first stage `node:20-alpine` (pinned) runs `npm ci && npm run build` in `web/`; the Go stage copies `dist/` into `internal/webui/dist` before `go build` |
| `go test` / `make check` without node | `internal/webui` embeds `all:dist`; the repo commits only `dist/.keep`. When `index.html` is missing the handler serves a small built-in page ("UI not built: run `make ui`") with build instructions and the API link. Go tests use an `fstest.MapFS`. So `make check` needs no node. `make ui` builds it; `make ui-check` runs typecheck, lint and unit tests; CI gets a `ui` job with Node 20 |
| Upkeep | React + TS + Tailwind is the most common web stack; the types are generated from `openapi.json` (which a Go test already keeps in step with the router), so a backend change that breaks the UI fails `tsc`. Each screen is one route file with its query hooks; the layout and components live in one place. Vitest for logic |
| Phone width | Tailwind breakpoints, a bottom tab bar under 768 px, grids that go 2 / 3 / 4–8 columns, tables that become cards, 16 px gutters, no horizontal page scroll (rows scroll inside themselves) |

Svelte would give smaller bundles, but React is what most contributors and
tools know, and size is not a concern for a LAN service (expected ~150 KB gzipped).

Routing: the SPA catch-all serves `index.html` for application GET and HEAD requests, including `/`, `/movies`
and `/title/{id}`. API, image, asset, arr-view and service routes keep their own handlers; `/classic/`
and `/static/` paths return 404. `index.html` gets `Cache-Control: no-cache`;
hashed assets get a year, immutable. Unknown API paths return JSON 404;
other application paths retain SPA fallback.

## 5. Artwork and metadata

### Metadata

- `internal/metadata/tmdb` fetches movie and TV details: title, original title and language, release dates with
  certifications, runtime, status, popularity, genres, collection, external ids, alternative titles, translations,
  overview, tagline, art paths and rating, plus TV content ratings and networks. Responses are cached in SQL
  (`metadata_cache`) and go through the gateway on the `tmdb` budget.
- Titles carry `tmdb_id` (series also `tvdb_id`), `title_release_date` with `certification`, and `episode` rows for
  series.

### Title metadata (migration `00023_title_meta.sql`)

`title_meta`, one row per title:

| Column | From |
|---|---|
| `title_id` (PK) | |
| `overview`, `tagline` | TMDb `overview`, `tagline` |
| `poster_path`, `backdrop_path`, `logo_path` | TMDb paths (`/abc.jpg`); a new path means new art |
| `genres` (JSON names) | `genres` |
| `runtime_minutes` | movie `runtime`, TV `episode_run_time[0]` |
| `rating`, `rating_votes` | `vote_average`, `vote_count` |
| `certification` | US theatrical (movies, from `release_dates`), US `content_ratings` (TV) |
| `status`, `network` | TMDb status; TV first network |
| `original_language` | |
| `art_hash` | short hash of the three paths, used as `?v=` on image URLs |
| `fetched_at`, `refresh_after`, `error` | refresh bookkeeping |

Movie details use `append_to_response`; `TV(id)` reads external ids, content ratings and images in one request;
`Images` gives a logo in the title's language.

### Image proxy

- **Route:** `GET /img/t/{title_id}/{kind}/{size}?v={art_hash}` with `kind` ∈ `poster`, `backdrop`, `logo` and
  `size` from a fixed list (`w92`, `w185`, `w342`, `w500`, `w780`, `w1280`; `original` only with `ui.images.original`). Scryer does the same with
  `/images/titles/{id}/{kind}/{variant}`. Search results that are not in the library use
  `GET /img/tmdb/{size}/{file}` where `file` must match `^[A-Za-z0-9]{10,40}\.(jpg|png)$` (a TMDb path; nothing else
  is fetchable, so it is not an open proxy).
- **Fetch:** through the gateway only, new source `tmdb-images` (`https://image.tmdb.org/t/p`) on the allowlist with
  its own budget. TMDb images need no key, so no secret is involved.
- **Cache:** `/data/images/{size}/{first two chars}/{file}`. TMDb paths never change content, so a cached file is
  served forever; eviction is least-recently-used when the directory passes `max_bytes` (default 2 GiB). Single-flight
  per file. Responses: `Cache-Control: private, max-age=31536000, immutable` (the `?v=` changes when the art does),
  `Content-Type` from the file, `nosniff`. Access: signed in (cookie or key); the page's `<img>` sends the cookie.
- **Fallback:** no art yet, or TMDb fails: a generated SVG poster (title and year on a gradient from the title id),
  served from the same route, so the grid never shows a broken image.
- The CSP stays `img-src 'self' data:`; browsers never talk to TMDb.

### Refresh and budgets

- **Job `meta.art`:** every 6 h. Order: titles with no `title_meta` first, then due ones (`refresh_after` passed).
  Up to `per_run` titles (default 200) per run; at 1 request per title (movies need one with `append_to_response`;
  TV one more for content ratings).
- **Refresh age:** released in the last 90 days or unreleased: 3 days; released in the last year: 14 days; older:
  90 days. A 404 from TMDb marks `error` and retries after 30 days.
- **On add:** a new title (arr view add, UI add, approved request) gets a `meta.art` job for that title at once.
- **Images:** fetched lazily on first view; the job pre-warms `w342` posters for library titles so grids load at once.
- **Budgets** (policy, `budgets.yaml`): the details calls use `tmdb` (the example: 30/s, burst 40), which the job
  paces well below; images use `tmdb-images` (the example: `{requests: 20, per: 1s, burst: 40, daily: 20000}`). The `meta.art` job also stops
  for the run when the gateway reports `tmdb` back-off. Both budgets show in Admin › Advanced › Request limits.
- **Policy:** `metadata.sources.tmdb-images: {url: https://image.tmdb.org/t/p, budget: tmdb-images}` and
  `ui.images: {dir: /data/images, max_bytes: 2GiB}`, `metadata.art: {per_run: 200, interval: 6h}`.

## 6. Native API for the UI

All under `/api/theatarr/v1` unless noted. Every list pages with `limit` (≤ 200) and `cursor` or `offset`, and returns
`total`.

| Route | Purpose |
|---|---|
| `GET titles?…&sort=added|created|name|year|release|rating|runtime|certification|network|size|completion|next|quality|activity&order=asc|desc&genre=&quality=&provider=&year_from=&year_to=&anime=` | grids; items gain `poster`, `backdrop` (image URLs), `overview` (short), `rating`, `certification`, `runtime`, `genres`, `added_at` (newest current file), `title_added_at`, `status`, `original_language`, the movie release dates, `series_type`, `season_count`, `previous_airing`, and per version `quality`, `size`, `providers` (cache codes), `profile`, `minimum_availability`, `folder` (see **Library views**) |
| `GET titles/facets?kind=` | counts per version key and state, per quality, per genre and (see files) per provider cache code, for the filter sheet |
| `GET titles/{id}` | includes `meta` (§5), `art`, and per version `history` (grabs, imports, upgrades, failures with `reason`, searches with `results`, `accepted`, `summary`, `latest`; every event's `trigger`, Theatarr's `provider`), `providers` (keys) and `cached_on` (cache codes) of the providers that hold the file now, `current.formats` (a movie file's matched formats with scores; none for an adopted file), and `removed` for a copy removed from the library (counted in none of the title's rollups); fields for files and hashes only with "see files" |
| `GET titles/{id}/episodes?season=` | seasons with episode lists and a state per version per episode (`episode_version_state`, `link_episode`); a removed copy has no column; cells carry `release_title`, `release_group` and `cached_on` (see files) |
| `GET /img/t/{id}/{kind}/{size}`, `GET /img/tmdb/{size}/{file}` | image proxy (§5) |
| `POST titles/{id}/refresh` | refresh metadata and art now |
| `GET discover` | Home rows in one call: `continue`, `recent`, `missing`, `upcoming`, `requests`, each ≤ 20 items with `more` links |
| `GET calendar?from=&to=` | releases and episodes in a window (shares code with the arr view's `calendar`); each entry says `monitored`, `available` (a movie: the title's status word is Available or Partly available, `partial` for the latter), `acquiring` (a download in hand for the movie, or for that very episode; one read for the page) and `stage` (the furthest stage in hand, see files only) |
| `GET activity/continue?limit=` | Home's Getting ready row: titles with a download in hand, each with its library's `version` key and `label` (numbered, never a key, for accounts without See files) |
| `GET search?q=&kind=&offset=` | `{library: [...], library_total, tmdb: [...]}`; the kind applies before the limit, `offset` pages the library part; TMDb results through the gateway and `tmdb` budget, cached 1 day, marked `in_library` and `requested` |
| `POST titles` | add `{kind, tmdb_id, tvdb_id?, versions[], seasons?, monitored, search}`, or `{title_id, versions[], …}` for a title already in the library (also one known only by its TVDB id); runs `catalog.Add` (§3); 201 with the title |
| `PUT titles/{id}/versions/{key}` | turn a version on/off, monitor |
| `POST titles/{id}/search` | search now for versions (records a command like the arr views' `MoviesSearch`) |
| `GET activity?state=now|history|blocked&title_id=&title=&version=&offset=&user=` | download rows with plain-words state, progress, provider, attempts (admin), library import results and (admin) `formats`; history rows (journal History, `history.go`) with `event` and `words`, and (admin) `episodes`, `files`, the row's `release`, `quality`, `score` (absent when unknown, never 0), `indexer`, `size`, `formats`, `reason` and a deleted file's `replaced_by`; `events=` filters them (the copy events only for callers who see files); blocked rows filtered by part of the title's name (`title`) and library (`version`), `total` the filtered count, each with the blocked download's `quality` and `indexer` when known; from `download`, `download_attempt`, `library_import`, `router_plan`, `acquire_run`, `router_blocklist` (moved out of `arrview/queue.go` into a shared reader) |
| `GET titles/{id}/releases?version=&live=&season=` | Interactive search (`live=1`) and the stored "Last search" (decision engine results with reasons; manager) |
| `GET requests?status=&mine=1`, `POST requests`, `POST requests/{id}/approve` (body may change versions/seasons), `POST requests/{id}/decline`, `DELETE requests/{id}` (cancel own) | requests (§3) |
| `GET users`, `POST users`, `PATCH users/{id}` (role, permissions, disable), `POST users/{id}/reset-password`, `DELETE users/{id}/sessions` | users |
| `GET auth/me` | includes `role`, `permissions`, `display_name` |
| `PATCH me`, `POST me/password` | own profile |
| `GET keys?user=` | keys for all users (admin) |
| `GET logs?level=&component=&since=` | ring buffer of recent log lines (admin) |
| `GET logs/files`, `GET logs/files/{name}` | list/download persistent JSON logs, current file and numbered rotations (admin) |
| `GET logs/level`, `PUT logs/level` | read/change the service threshold; body `{"level":"debug"}` (debug/info/warn/error), audited, admin and session CSRF on PUT |
| `GET sources` | indexers and providers with state: last result, back-off, last sync, counts |
| `GET indexers/stats?days=7\|30\|90` | per indexer: requests (settings tests left out), failed and average response over the measured requests, grabbed, imported and failed downloads from History; Settings › Indexers shows it as the Statistics table under Status (admin; [indexers.md](indexers.md) "Statistics") |
| `GET events` | event types beside `jobs` and `gateway`: `download`, `request`, `title` (art or state changed), `toast` |

## 7. Logs

Admin › System › Logs keeps its ring level/component filters and adds persistent log downloads plus a separate
service log-level selector, applied immediately to stderr, files and the ring. Rotated files survive restart; the ring
empties and the runtime level resets to the configured default.

Recent lines poll every five seconds using the existing `since` filter with a one-millisecond overlap and sequence/
timestamp deduplication, retaining the latest 500 lines for each filter. A new service sequence clears the old
process's lines. Log files poll every 30 seconds; the service level refreshes after its mutation and on query focus,
without an interval. React Query's foreground-only interval default pauses log, file and request polling while the
document is hidden, and resumes on visibility changes.

## 8. Product rules

1. **Sign-in: Plex.** Members sign in with their Plex account (Seerr's way: plex.tv PIN/OAuth, then check
   that the account has access to the configured Plex server). The local `admin` login stays as the break-glass account.
2. **Seerr stays supported** as a request front end through the arr views, beside Theatarr's own requests.
3. **Requested versions:** a request asks for **every version that fits the title by default** (for example movie:
   `movie-web` and `movie-remux`; series: `tv-2160p`; anime: the anime versions). An admin can set, per user, which
   versions their requests ask for (e.g. only `movie-web`), so `request_versions` is a per-user default set chosen by
   the admin, not a user-facing picker of allowed keys. Auto-approve is a per-user flag.
4. **Users see availability only** ("Available in 4K WEB", "Available in 1080p"): no release names, sizes, paths, hashes or providers, no quality source, cutoff, file counts or history (`docs/design/users.md`).
   Managers and admins see the files.
5. **Name:** the product is Theatarr. The name lives in one place in
   the SPA (`web/src/brand.ts`), so a rename is one change.
6. **Admins land on Home**, with the status dot in the top bar linking to System › Status.
7. **Filter boxes:** every free-text filter matches every word typed, in any order: the text is split on whitespace
   and each word must be found, case-insensitively, as a substring (`knight frame` finds
   "The.Dark.Knight…-FraMeSToR"). Empty text matches everything; at most 10 words count. No fuzzy matching, ranking
   or operators. The client uses `matchesWords` (`web/src/lib/words.ts`), the server's LIKE filters
   `internal/textsearch` (title lists and the top search's library section, the inventory, the blocklist, the
   journal and history title filter); `%` and `_` typed are literal, and SQLite folds case for ASCII only. Kept
   beside it: a whole number in the journal's title filter is a title id, and the whole text as a hash prefix still
   finds a torrent (Torrents, inventory). TMDb/TVDB lookups, indexer searches and the Sonarr/Radarr compatible views
   are not filters and keep their own matching.

## 9. Visual sources

The visual language follows Scryer and Seerr: Seerr's poster cards with a status badge and hover overlay, sideways
sliders, a backdrop hero, and Scryer's sidebar, top bar and settings layout, in light and dark. Code copied or adapted
from them carries a header comment naming the source repo, path, commit and licence, and is listed in
[`web/THIRD_PARTY.md`](../../web/THIRD_PARTY.md) (Scryer GPL-3.0, Seerr MIT, shadcn/ui MIT).

## 10. Admin workflows

### Manual import

Wanted › Manual import (`/wanted/manual-import`, admins) lists files for a download hash or mount folder
in a chosen version, shows decisions and rejections, and edits each file's title, episodes, quality/revision,
languages and release group (the themed selects). A
hash Theatarr has not seen is pointed at the title's Last search, where Add by magnet or hash is. Review choices refreshes the decisions before Import selected files; errors receive
focus and results use a live region. The admin/CSRF-protected native `/manualimport`, `/manualimport/options` and
`/manualimport/execute` endpoints reuse the arr handlers because the SPA has a session rather than an arr API key;
imports retain the library gate and create symlinks while preserving mount files.
