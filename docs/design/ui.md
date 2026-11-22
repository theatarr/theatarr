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
| `GET anime/franchises`, `franchises/{slug}`, `review`, `review/{id}/snippet`, `POST review/{id}/accept|reject`, `GET anime/playlists` | anime metadata | A (review), U (franchise orders, playlists) | A playlist plan's items keep their order for every browsing account; their file `path`, `from` and `rating_key`, and the playlist's own `rating_key` and `detail`, need see_files; a franchise's `job`, `next_refresh`, `pending` and `open_review` are for admins (`TestAnimePlaylistPlanHidesFilesWithoutSeeFiles`) |
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
  tabs, toast, skeleton, empty state) live in `web/src/components/ui`. Toasts stack above every dialog and sheet (z-[95]; dialogs and sheets are z-[70] to z-[85],
  the skip link z-[100]), so a grab's "Grab started" or "Not sent" shows over Interactive search; the stack
  lets clicks through and each toast takes its own. It is a portal on `<body>` that the modal stack never makes inert
  (`data-toaster`), so a toast's Dismiss and link work while a dialog is open and a tap on a toast never reaches the
  backdrop or the sheet under it. While any modal is open (`lib/modal-stack.ts` `topModal`) the stack
  moves to the top of the screen (centred from sm up, clear of a side sheet). On a phone (`toastTop`, measured) it sits
  above the top modal, over the backdrop, when the stack and its margins fit there (a short, bottom-aligned
  confirmation), else just below the modal's header row (the row with its Close), so no toast lies over a dialog's
  button row or its Close; over a full-height sheet it may cover the sheet's first control for the seconds it shows
  (accepted: it is dismissible by a tap). The stack follows which modal is on top (`topModal`), so it is placed
  again when a confirmation over a sheet closes; it goes back to the corner when the last modal closes. A toast's title wraps whole (never cut with an ellipsis; its point is often at the end), Dismiss stays beside it, and a very long toast scrolls inside the screen. A toast stays while it is hovered or focused and gets its full time
  again after; the same toast again (same words, tone and link) while it shows counts on it ("×2") instead of stacking
  a copy.
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
from any row's `grab_state`) and the rows keep only their own verdict; the top-bar pill stays. The library table's
phone cards say it once above the list, not in each card's actions (a row's own reason, such as "Nothing is searched
now", stays in its card). A household account (neither manage nor admin) reads "Downloads are paused" wherever the library
state or the Pause notice shows (`VersionStatus` and `DownloadsOffNotice` read the account themselves), with no
environment name or switch to turn. Controls are named
**Pause**, **Send notifications** and, in the add drawer, **Add to these libraries**. The add drawer shows one row per library: a checkbox, then the name, key and
kind on one wrapping row and its state on the next, with a footer "n libraries · m will download" (ticked
libraries whose `will_grab` is true).

The add drawer's other choices each start at the value that sends the request of a plain add, so leaving them
alone changes nothing. A series not yet in the library gets **Monitor** (a select: the season ticks as picked, or a
preset: all, future or missing episodes, first or latest season, pilot only, none; a preset hides the ticks and
sends `monitor` without `seasons`, and a series without episodes yet takes it at its first refresh; a new series
whose own seasons `GET /add/options` read (seasons without `episodes_known`) also gets **Choose seasons**, which shows a tick per season,
all on but the specials, without episode counts, and sends every shown season's choice and no preset, so its first
refresh monitors exactly those. The seasons are the series' own, TVDB's default order as its episodes will be
numbered, read through the series lookup the Sonarr view's lookup uses, by the TVDB id TMDb names, within about 2
seconds; seasons the lookup could only take from TMDb (TVDB has no such series) do not count, and when none can be read
the select offers the presets only. A request uses the same seasons, read the same way: the request sheet lists them
(without them its button reads **Request all seasons**), and approval stores the seasons not asked for as unmonitored;
when approval cannot read them, the copies it adds get monitor new items none, so every season the request does not
name stays unmonitored when the first refresh brings it) and **Monitor
seasons that appear later** (on; off sends `monitor_new: none`). A new series has no episodes until its first
refresh, so `monitor_new: none` alone would leave the seasons that refresh finds unmonitored too; with no preset
and no season ticks the drawer therefore sends `monitor: all` with it, and only seasons that appear after the first
refresh stay unmonitored. The API itself stores `monitor_new: none` as given. Neither choice is offered for a series
already in the library, because the request names the libraries it already has and would rewrite their monitoring;
the API answers 400 to `monitor` there, naming the library title, and writes nothing. The footer counts the
ticked libraries ("2 libraries"; "Nothing new ticked" before a change) and ends with **Add** (`search: false`) and **Add and search** (primary; `search: true`, so the ticked
libraries that can search are searched at once); for a title already in the library they read **Add libraries** and
**Add libraries and search**. Only **Add** is offered when no ticked library can search (with a line saying adding
searches nothing), or for a new series that monitors no episode (Monitor **None**, or its seasons with every box
cleared; the switch for seasons that appear later does not change that), and only **Save seasons** when only a
series' seasons changed. Nothing is remembered between adds. An add from the Search overlay that succeeds closes
the overlay too, so the title's page it goes to is not covered; Cancel or a failed add returns to the same search,
its text and results kept. The request sheet closes the overlay only when the member follows one of its links.

A new series also gets **Series type** (Standard, Daily, Anime). The select shows the type the series gets without
a choice (Anime when Anime is ticked, else Standard) and the request carries `series_type` only when the pick
differs from it. It is not offered for a series already in the library: an add never changes a series' type, and
the API answers 400 to `series_type` there. Acquisition reads a series as anime by its anime flag or its type, so
the type Anime ticks the libraries of an anime series, as the Anime box does (the box itself stays as it is);
`POST /titles` without `versions` picks the same way, and libraries the request names are left alone. A movie with a library to add ticked gets **Minimum availability**:
**Library default** (the start; sends nothing, so each library's own rule applies) or Announced, In cinemas,
Released, sent as `minimum_availability` and stored as the explicit choice (`availability.Choose`, the library's own
`v/<key>` row) of each library the add adds the movie to, new or restored, whether or not that library has a
default; a library the movie already had keeps its rule. A new movie (not in the library, or every copy removed) also
gets a **Monitored** switch beside it, on by default (sends nothing). Off, it sends `monitored: false`: every library
it adds the movie to holds it unmonitored, only **Add** is offered, and the line under the switch ("Theatarr won't
search for or download this movie automatically until you monitor it.") replaces the no-search line; a search started
by hand still works. A series has the monitoring preset instead, and a movie already in the library its title page's
Monitored switch.

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
| no file, and no library monitors it, or (a series with episode information) no current episode, specials and episodes to come included, is monitored in a monitored library; an episode with no file whose episode or library is not monitored | **Not monitored** | grey `--ink-4` | posters, table Status, filter, title page, library rows, episode cells and window |
| no file, monitored, not out yet (a movie's home release, else its TMDb release date, still to come; a series with nothing aired); an episode still to air | **Upcoming** (with the day), episodes **Not aired** | grey `--ink-3` | posters, table Status, filter, title page, library rows, season header, episode cells |
| in no library | none ("Not in the library yet" on the title page) | — | title page |

A gap in a library that is not monitored is no gap: it reads Not monitored and counts in no missing number (season
headers, library rows, coverage), for a series as for a movie. Wanted and searching are unchanged. A series whose
library is on but that monitors none of its episodes reads Not monitored, as its Libraries row ("none monitored") and
Search series ("Nothing to search") say, and the status filter and its count follow; the Monitoring filter (the
library switch), Home's picks, watchability without See files and the compatible API keep their own rules.

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
cached on RD, AD and TB. Your file is unchanged." (the failure as the history row says it). A pack that keeps some
episodes' files (its reason says "… keep their file") was accepted for the episodes it upgrades, so it reads "Better
pack found for some episodes 4 d ago: …" there; an episode whose file it keeps gets no badge from it (the stored
candidate's `kept`; a pack stored before `kept` was recorded still counts for every episode until a newer search
replaces it), and its badge (the episode cell's word, the Details badge and Why?'s heading)
reads "Better pack found" instead of "Upgrade found". The reason reads in plain words: "1 episode keeps its file", "2 episodes keep their file", and an older
reason's pack-policy suffix ("(packs: any_upgradable)") is left out; the stored reason is unchanged. Without such a row
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
| a failed copy is on the account (rd, ad); … | A failed copy is on RD and AD | An admin can delete it under Torrents. |
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
  `home_release` (the earlier of the digital and disc dates, else 90 days after the first cinema run anywhere; the date Theatarr's
  released minimum waits for, `availability.HomeRelease`) strictly after today, else, for a row without one, its TMDb
  release date strictly after today (the UTC day, as the server's Missing row and fetching count it). A film in cinemas with its digital release to come reads Upcoming until that
  day. It is still wanted; only the word changes, and Wanted's counts do not. `home_release` is per title.
- **Release dates**: one choice of a movie's dates (`activity.FirstReleasesSQL`, `internal/activity/calendar.go`) for
  Home's Upcoming row, the Calendar, the lists' **Next release** column and its sort, the **In
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
  that day. A title's **general release day** (the row's `released`, `releasedSQL` in
  `internal/web/library.go`, formatted by `web/src/lib/released.ts`) is what the lists' **Release date** column, its
  sort, About's **Released** and the posters' Release date line show: a movie's earliest stored cinema date of any
  kind (limited or wide, any source or country) or TMDb's release date, whichever is earlier, so a wide re-release
  never makes an old film read new (Akira 1988, not its 2026 US release); a series' first air date. It takes no part in
  the choice above (**Releases** while still to come). About does not name the cinema milestone, because a stored cinema date can be
  a copy of the general release date the metadata refresh keeps as a fallback. On the Calendar a movie's cinema date alone is never Missing;
  joined with a digital or disc date of the same day it is. Only the primary country's TMDb rows are stored
  (`title_release_date`, source `tmdb`); Details › Release dates lists them (with the arrs' rows for a title not
  refreshed), the country named once under the table; a refreshed title's cinema date taken from the first release
  anywhere is marked "first release, not US".
  Display only: what may be fetched reads `availability.Dates` (the `tmdb-art` schedule, `tmdb.ReleaseSchedule`, plus
  availability's first cinema date anywhere when earlier: router.md "Triggers"). The estimated home release is 90 days
  after the first cinema run anywhere (`availability.HomeRelease`): `home_release`, Wanted's date and its sort, Home's
  Missing row and the compatible `releaseDate` read it, still marked "(estimate)" wherever they were, and the lists'
  Status, About › Status and the compatible status are one calculation (`availability.Status`) from the same run,
  worked out from the dates the page reads (also for a film with no stored status yet, added from a list or
  collection), so they agree before the next metadata refresh. The canonical cinema, digital and physical dates, the
  calendar, the Released column, In cinemas and Details › Release dates stay as they were (the general release day
  leaves availability's first cinema date out).
- **Added and Latest file**: **Added** is the day the title was added to Theatarr (lists' Added column and sort,
  poster detail); **Latest file** is when the newest current file came (lists' Latest file column and sort, Home's
  **Latest files** row with "File added …", About's Latest file). The **Quality** sort orders by the quality each
  library's cell shows (the one most of its files have), the best of the libraries, not the best single file.
- **Family view**: accounts without See files get the table's Status column headed **Availability** (Available,
  Some episodes available, Not available, Upcoming, Getting ready) and are not offered the Libraries, Quality,
  Quality profile, Size, Latest file, Minimum availability, Held on or Folder columns; posters show the badge only,
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
  open into their details in place; household accounts and "View as a user" keep the simple rows. When a background refresh of the Queue or History fails, the rows already shown stay, under "Could not refresh.
  Showing the list from HH:MM." with **Retry**, and their actions stay enabled (the server checks each by id); the full
  error shows only when nothing was loaded yet (`StaleNotice`, `components/activity/row-bits.tsx`). A failed **Load more**
  in History keeps the rows and says "Could not load more." in its place, with **Retry** reading the next page again. **Downloading**
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
  libraries are square toggle chips (a group of libraries kept in sync is one chip, "4K Remux + 4K WEB").
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
- **Now playing** ([now-playing.md](now-playing.md)): below the playbacks, "Files that stopped working" lists the
  files that broke while someone played them in the last 24 hours, newest first, only when there is one. Each row
  wraps (nothing cut, nothing on hover): the title and episodes, linked to the title; what came of it with a coloured
  dot ("Looking for another copy", "Replaced with another release", "Readable again", "No other copy yet", with
  ": the debrid account did not answer" after it when that was why, or "Removed from the library" with a neutral
  dot when the watched copy was removed before its search ended); the
  time and how long ago; who was watching on which player; how many files of the
  release stopped working when more than one; what the player was told ("The player was stopped with the message:
  …"), why Plex refused, or why it was not ("The player was not stopped: the playback had already ended.", the setting off, paused, or the file read again; users see these too for their own rows, but not why Plex refused); for accounts that see files the
  broken copy's release and the one that replaced it. Users see only their own playbacks' rows, without releases.

### Navigation

| Menu path | Route |
|---|---|
| Home | `/` |
| Library › Movies / Series / Anime | `/movies`, `/series`, `/anime` |
| Library › Collections (managers, admins) | `/collections` (one collection: `/collections/{tmdb id}`) |
| Requests | `/requests` |
| Activity › Queue / History / Blocklist | `/activity`, `/activity/history`, `/activity/blocklist` |
| Wanted › Missing / Cutoff unmet (managers and admins) / Manual import (admin) | `/wanted`, `/wanted/cutoff`, `/wanted/manual-import` |
| Now playing / Calendar | `/now-playing`, `/calendar` (Now playing also lists "Files that stopped working": "Other pages") |
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
| Search | overlay, `/search?q=` | two sections: **In your library** (instant, local) and **Add from TMDb** (movies and TV, with "In library" / "Requested" marks); a title whose card is shown in the library group is not shown again under TMDb, a held hit whose card is not shown (a later library page) stays with its "In library" mark, the TMDb count counts what is shown, and "No additional matches." when every hit is shown above; a query ending in four digits after a title that TMDb finds nothing for ("The Thing 1982") is searched once more without the year, keeping that year's hits, while a query TMDb answers is used as typed ("Blade Runner 2049", "1917"). Add opens a sheet: versions to turn on (pre-ticked from the policy defaults), seasons for series, monitor; its footer says "N libraries" and ends with **Add** and **Add and search** (primary), only **Add** when no ticked library can search, and only **Save seasons** when only the seasons of a title in the library changed. For a title already in a library, a line under **Add libraries and search** names what it searches ("Searches: 4K Remux, 4K WEB": every ticked library that downloads, the ones the title is already in too). After the add the toast names the libraries by what was saved: "Turned on: …", or "Added, not monitored: 4K Remux, 4K WEB." for a title added with Monitored off; the hero and toolbar action for a held title no library monitors reads **Monitor** (the toolbar state toggle keeps "Not monitored"). The title page right after an add reads the title again every 3 s while `metadata_pending` (its first metadata refresh still to come: a series until its series refresh tried it, a film until the artwork job did; false when that refresh is not configured), for at most 2 minutes, and reads the episodes and the add options (Search now, the add sheet's seasons) again when it flips, so the poster, overview and episodes appear without a reload; while pending a new series with no episodes says **Loading episodes…** in the hero, its Libraries rows and the Episodes tab instead of Missing and "No episodes are known", which stay for a refresh that found none; once the 2 minutes are up with the refresh still pending, the hero and the Libraries rows read "Episodes not loaded yet" (not "No episode information yet") and the tab "Episodes are not loaded yet. They arrive with the next metadata refresh." (no spinner). Two libraries searching at once read "Searching 2 libraries" in the hero; one is named. An account without `add` sees **Request from TMDb** and Request buttons instead; one with neither `add` nor `request` sees **On TMDb** and no add or request control. A library result shows its library names (tags) only to accounts with See files; others get the title's one availability word (`titleWatchWords`: Available in 4K, Some episodes available, Getting ready, Upcoming, Not available), as the table does. With no TMDb key the TMDb section says so; admins get **Set the TMDb key** (Settings › Metadata & artwork, metadata services), others are told an administrator can add one | new |
| Calendar | `/calendar` | releases and episodes by month or as an agenda; the month's weeks start on the browser language's first day (`Intl.Locale` week info; Sunday when the browser cannot say), no setting (arr views already serve `calendar`). An episode opens its series' Episodes tab with that episode's window open (`/title/{id}?tab=episodes&episode={episode_id}`); a movie opens its title page. An episode with an air instant sits on the day the instant falls on in the browser's zone, and the episode window (`Episode.air_at`) and Home's Upcoming (`DiscoverItem.at`) name the same day; without an instant the stored air date stands. Movie dates are the release-date choice (Library and activity pages, Release dates); one movie's release types of a day are one entry ("Digital + Physical"). Each entry's edge is coloured by status, with a legend under the header: Available (a file is held; a movie with a monitored library still lacking one says **Partly available**, `CalendarEntry.partial`), Getting ready (a download in hand, `CalendarEntry.acquiring`; the entry says its stage, `stage`: Queued, Downloading, Waiting for the mount, …), Missing, Upcoming or in cinemas (a film is missing only from its digital or physical date), Not monitored; accounts without See files get Available in green and the rest in grey. The agenda shows the status once, as its chip. Every account also gets titles no library monitors, greyed (as Home's Upcoming row lists them; household accounts keep their words), and a **Monitored only** toggle. The month (`?month=YYYY-MM`), the Movies/Series/Anime toggles (`?types=`) and Monitored only (`?monitored=1`) live in the URL. This month's agenda starts at today, with **Show earlier**; **+N more** in the month grid scrolls the day's list into view. A cell shows three slots (on the phone, dots); a series' episodes of one season on that day fold into one slot ("S02E01–E03", "S02E01, E03" with a gap, "S02 · 6 episodes" when that would be long; linked to the first, coloured by the episode most in need of a look, a time only when every episode shares it, each episode's time and status in its tooltip), and the day's list keeps every episode with its own status and link; the Agenda folds them into one line the same way | new |
| Collections and Import lists (managers, admins) | `/collections`, `/admin/settings/import-lists` | **Collections** (`/collections`, which links Import lists in one line): TMDb movie collections, monitored first: poster, movies in the library of the total, monitored switch, target libraries (movie libraries; monitoring needs one), search on add, minimum availability; a name filter and **Monitored only**. **Add collection** searches TMDb for a collection and adds it with its libraries (Search on add is ticked by default). Changes save at once; a monitored collection adds its missing movies when it is added, with **Add missing movies now** (**Refresh and add missing movies** once all its movies are in the library: it can still add one to another target library) and at each list refresh; a collection found through a movie with no name or movies yet (and not monitored) waits off the list, counted in one line, until the next list refresh names it; and its searches go through the router. With the collection's Search on add ticked, movies a timed refresh adds are searched on the search schedule, at the hourly pace, and one added or refreshed here is searched at once, except while Pause is on, when it is only added, as a title added by hand (docs/design/router.md, Adds by lists); only monitored movies that have reached their minimum availability are searched, and with Search on add off they are only added. **Import lists** (Settings › Import lists, `/admin/settings/import-lists`; `/collections/lists` redirects; on a phone a list's actions take their own row): **Source** is a TMDb list (by id or address), a TMDb discover rule (year from and to, minimum rating, minimum votes, genres, original language, the first N), a JSON list (its direct https address), or the titles of a Radarr or Sonarr instance (address and API key, with **Check**); **Media** (movies or series) for the first three: a mixed TMDb list is added once per media. **Add list** with libraries, what to monitor, minimum availability (movies), Search on add and Enabled; **Preview**, **Sync now**, Edit and Delete per list. A list's last sync failure is shown under Last sync ("Last sync failed: …") until a sync without failures. Below the lists, **List exclusions**: the movies and series no list adds (name and year where known, else "TMDb *id*" or "TVDB *id*"), each with **Remove**, and one line saying that removing an exclusion lets lists add the title again but does not bring back a library it was removed from (adding it by hand does). Remove title on a title page adds one with **Also keep lists from adding it to any other library** (off by default; the whole title only). With a list's Search on add ticked, its new titles are searched on the search schedule, not at once (monitored and released titles only; with it off they are only added). Sync now is refused while Pause is on. Lists made through the Radarr-compatible API show here with their view (docs/design/arrview.md) | `GET collections`, `POST collections`, `GET collections/search`, `PUT collections/{id}`, `POST collections/{id}/refresh`; `GET`/`POST lists`, `GET lists/lookup`, `PUT`/`DELETE lists/{id}`, `POST lists/check`, `POST lists/{id}/preview`, `POST lists/{id}/sync` (409 while Pause is on), `GET lists/exclusions`, `DELETE lists/exclusions/{kind}/{id}` |
| Activity | `/activity` | **Queue:** for accounts that see files, a table (phone: cards): **Status** and **Title** (episodes under the name) always, then the chosen columns (**Columns**: Library, Quality, Provider (the provider or download client, "cached" or "reused" under it), Size and Progress (the bar, or how long it has waited and the next try) by default; Score, Formats, Indexer, Release, Added and Last update on offer), and the row's actions. Headers sort in the browser (the queue is loaded whole), newest first by default; columns and sort are kept per user in this browser (`theatarr.library.activity-queue.<user>`, `…activity-queue-sort.<user>`). **What do these mean?** lists each status word and its meaning (also the Status cell's tooltip): Queued, Waiting for a provider, Starting, Waiting to start, Downloading, Download complete, Waiting for the mount, Linking, Partly linked, Rejected by the library, Linking failed, Linked, Failed, Not started. Row actions (managers): **Details** (Inspect when it needs attention) opens the steps, release, hash, message (for a failure: **Technical details**, the stored text; once Details has loaded it, for accounts that manage and see files, the whole message up to 500 characters from `GET activity/downloads/{id}/attempts` `message`, also while it waits on the mount, where the row keeps its 120; the short text when the whole one holds a URL or a key), provider attempts and cache evidence under the row; a failed or refused download's panel leads with its reason and detail, then what helps: "Retry sends the same release again; Interactive search lets you choose another.", or, when a retry must end the same way (the library refused its files: a headline "Not imported: …" or "Held for review: …"), "Dismiss clears it from the queue; Interactive search lets you choose another." ("Remove takes it out of the queue" while it is held). Its steps tick Download complete and cross the last step, **Not imported**, when it downloaded and the library refused it (or it was not imported in time); the steps take equal widths with each dot centred and the connectors running from dot to dot; the words under a row's stage wrap rather than end in "…"; on a phone the step it is on or stopped at is named under the dots; **Retry** (or **Import now** for a finished download) when it applies; **Remove** (Dismiss for a failed one), whose confirmation has one checkbox, "Also blocklist this release and search again". Managers can tick rows (or "Select all N shown"): "N selected", **Remove selected…** (the same checkbox; one route call per row; the result says "Removed 3 downloads." or "Removed 2 of 3. Not removed: …") and **Clear**. A failed download (or a link the library refused) shows its reason under the status in plain words (below, "Failure reasons") and its release name under the title, so repeated failures for one item tell apart; Provider says "Not added" when no provider or client accepted it. `?open=<id>` (`downloadHref` in `web/src/lib/activity.ts`; for links such as System › Status's Inspect) opens that download's panel and scrolls to it; closing it drops the parameter; when the filter hides it the page says so with **Show all**, and when it has left the queue it says so with a link to History. Household accounts and "View as a user" keep simple rows: title (quality and episodes under it), stage, progress or waiting time, and Details with the steps. Filters (`?show=`): **Needs attention** (users: Needs a look; a failed step, a refused or partial link, a wait past its limit, or no progress for 6 h), **Downloading**, **Waiting** (queued, starting, not started, finished, linking), **All**. A download the library refused and blocklisted (the media check found a sample or a mislabelled file: failed, its import rejected; a new search is asked for and Retry is refused) leaves the queue by itself, as a miss on every provider does: it is not in the queue, the Activity count or System › Status, and stays in History (Failed, with its reason), the Blocklist and the title page's last-attempt line (`activity.NativeQueueFailureSQL`; rows stored before are covered). A held-for-review import (the download still ready), every other failed download and the arr-compatible queue are unchanged. A finished provider download is **Download complete** until it is linked; "ready to watch" is kept for library readiness. **History:** what happened to the titles' files, worded like Radarr's, one row per event with its release, quality and score where they are known (`internal/journal/history.go`, from the rows stored as each event happens, table `history`: docs/journal.md "Stored History rows"; the server builds every row; the title page's History tab and the episode window's History read the same rows, so they never disagree). Events: **Grabbed** (a download that reached a provider or client, dated when it got there: one stopped before, by Pause, shows only as Not started; an upgrade's grab reads Grabbed too, the journal's grab/upgrade split is unchanged; one row per download: once the download is imported, upgraded or failed, that row stands for its grab and says "Grabbed <time>" (under its words; an upgrade says it at the top of its opened replaced files; for admins, who get Journal links, it links to the grab's journal entry `dl:<download id>`, which has the reasoning and the provider attempts; plain text for others, and for everyone once retention has pruned that entry) except for a manual import, which was never grabbed, and an "Import held for review" row, which keeps its Grabbed row beside it (the grab holds the episodes) until the held import is imported or fails, so a Grabbed row of its own is a download still running, and the Grabbed filter lists every grab; household History has no Grabbed rows and no grab time), **Imported** (one row per import, not per link, with the episodes its links hold: a season pack is one row with its episodes, "6 episodes · S01E01–S01E06", the full list as tooltip; a partial import says so in its reason), **Adopted** (files taken over from Sonarr/Radarr, one row per library, release and day; tooltip "Taken over from Sonarr/Radarr's library; Theatarr did not download it." on the table for accounts that see files and in the episode window, not on household rows; when the adoption replaced the file before, the removal is folded into it: the row's columns are the new file's and "Replaced: <old release or file name> · <quality> · <score>" is under its release; its reason says "From Sonarr" (a series) or "From Radarr" (a movie), the instance as tooltip; a file Theatarr never scored shows "—" in Score, tooltip "Adopted file; not scored", a recorded 0 stays 0; a row with no release says "File name not recorded"), **Upgraded** (one row per download and library for an import that moved files aside, its import folded in: Quality, Score and Release are the new release's, "Replaced: <old release or file name> · <quality> · <score>" or "Replaced N files" under the release, the Event cell lists the episodes the import brought, a partial import says "Partly imported; the rest is tried again" as on Imported, and the journal link opens the import's entry; **Show the replaced file** / **Show the N replaced files** opens the list under the row, across it, in episode order, after "Grabbed <time>", headed "Replaced file" / "Replaced files": each file on its own lines with labelled parts, File (the name; the path as tooltip), Release ("Name not recorded", or said once above the list when no file has one), Quality, Score and Episodes; household History shows the one row; a first import is one Imported row (its grab folded in); when every replaced file has a release name equal to the imported release's and a score no lower than its, a lost file fetched again, the row says **Re-downloaded** instead, its Replaced, Quality and Score lines unchanged, and one differing, unnamed or lower-scored replaced file keeps Upgraded), **Removed** (a link taken out by hand or by Sonarr/Radarr deleting it, with the reason: "Removed by <user>", "Removed by <key> (API key)" for an API key; one an adoption replaced is shown on the Adopted row instead; the adopted file's own removal stays Removed), **Removed from library** (`title_removed`: the title taken out of a library by Remove title or Remove from this library, one row per library, from the `remove_title`/`remove_version` actions; its reason says who (an API key as "<key> (API key)", as on Removed) and whether the files were kept or deleted, and the debrid clean-up's outcome once it ran: `by` and `removal {files, left, reason, cleanup}` for accounts that see files, `journal.Removal`), **File missing** (a link whose file is gone), **Stopped working while playing** (`playing`, accounts that see files only, the filter too: what came of a file that stopped working while someone played it, one row per outcome headed "Stopped working while playing" (the filter's own words), the broken copy's release and quality on the row, and what came of it as its visible detail for accounts that see files ("Replaced: another release was imported in its place.", "Readable again: the same release works again.", or "No other copy yet. A new release can still arrive through RSS.", with ": the debrid account did not answer" after "No other copy yet" when that was why, naming no account); a readable-again file whose link never went dead has this row only), **Failed** (a failed or stalled download with its reason, or an import held for review; Not started stays as below), **Blocked** (with the blocklist's reason in the Blocklist's words: its source's words, then the reason in plain sentences read from the whole stored reason as the Blocklist reads it, none for a release taken over from an arr's blocklist; its tooltip says what happened then, "This release was put on the library's blocklist at the time.", so it stays true after an Unblock), and, for accounts that see files, **Added to <provider>** / **Already on <provider>** / **Not cached on <provider>** for the copies of "Add to other providers" (removed in 0.4.5; its events stay readable, read-only, and copies finished before their events existed are seeded from their stored outcome, without quality or score). Searches with no grab, dry runs, mount waits and repaired links stay in the decision journal. The facts are recorded when the event happens (migration 00129: a link row can be taken over by a later file at the same path, and a copy row is reused); each library row also keeps its title and the episodes its file held, so a later change of episodes or a deleted link row does not change it; rows from before show what the link rows still tell and "unknown" for a file that is not known, and an import whose links are gone lists no episodes rather than the ones it asked for. Episodes and file counts are for accounts that see files. A folded row is never a row of its own while the row that stands for it is there, under every event filter, so a row reads the same under each filter as under All events and paging never shows half of a pair; the one exception is the Grabbed filter, which lists the grabs (docs/journal.md "History's folded rows"). Filtered by title, copy, event type (Grabbed, every grab, also those folded into their outcome's row; Imported and upgraded, upgrades as Upgraded with "Replaced:"; Adopted; Removed and missing, with Removed from library and without upgrades; Failed and blocked; an old `event=deleted` bookmark opens Removed and missing; the copies to other providers have no filter since 0.4.5 and an old `event=copies` bookmark opens All events; bookmarks with the old Grabs, Upgrades, Imports and Failures open the matching new type) and a since date, all in the URL (`?title=&copy=&event=&since=`; `GET activity?state=history&title=&version=&events=&since=`, `web/src/lib/activity-history.ts`), newest first, 50 a page with **Load more**. For accounts that see files it is a table without day headings: **Event** (the words, their meaning as tooltip, the reason under them) and **Title** (episodes under the name) always, then the chosen columns (Library, Quality, Score, Date and Release by default; Provider, Indexer, Formats and Size on offer; kept in `theatarr.library.activity-history.<user>`), then **Retry** on a failed download (none on one a removal ended) and, for admins, the journal link. Headers do not sort. A cell the event has no value for shows "—", never 0. Household accounts and "View as a user" keep the rows grouped by day, with no Grabbed rows (and no Grabbed filter; an old `event=grabbed` link opens All events), and the rows of one title with the same episodes, the same event and the same day (UTC, as adoptions are grouped) are one row with the latest time (`journal.Filter.Household`, chosen before paging), since their rows carry no release or library; nothing is merged across days or events. The journal link on a row is for admins only. Accounts without See files get no library key on a row (the resolution stays), and an opened failed download says in plain words that it did not work and that an admin can look at it. A download that never started because downloads were off when its turn came (paused or locked) and was never sent to a provider or client shows **Not started** with the reason, outside Needs attention; History says Not started, and for admins its line is the plain message. A download a removal ended (Remove title, Remove from this library, or an arr view's delete; `router.EndRemoved`) is in History only, as Failed with who removed the copy, and offers no Retry: the copy is gone. On Wanted, a row's search-blocked reason shows next to its disabled Search now when its waiting reason does not already say it (managers do not read `/status`). **Blocklist** (`/activity/blocklist`, accounts with See files): a table, newest first, 50 a page ("Previous · 1–50 of 312 · Next"), filtered by part of the title's name and **All libraries** / one library (`GET activity?state=blocked&title=&version=&offset=`; the total is the filtered count; "No blocked release matches." when nothing does). Columns, all shown: Title (linked to its page), Library, Release (the blocked download's indexer under it, "from torrentio", when known), Quality, Reason (the plain words; under them the reason in plain sentences ("Failure reasons"), also as tooltip, none for a release taken over from an arr's blocklist; "handled 3 d ago" under it once a re-search, the next release of the same search or a skipped retry took care of it, the time as tooltip) and Blocked, then **Unblock**. Managers can tick rows: "N selected", **Unblock selected…** ("These N releases may be picked again for their libraries"; nothing is searched; one route call per row) and **Clear**. Users see the copies (versions and seasons) of the requests they are on, not another copy of the same title (history by version only; it pages over them, so each page is full until the last); admins see all. Bookmarks `/activity/now`, `/activity/blocked` and `/activity/wanted` redirect to `/activity`, `/activity/blocklist` and `/wanted`, preserving query and hash; Blocklist in the user view goes back to Queue. The **Decision journal** is a page of System (`/admin/system/journal`, below); `/activity/journal[/:id]` redirects there. Journal: every grab decision with its evidence, one line per thing that happened (docs/journal.md "The list": a download's failure, import and file changes are on its grab's line, "Grabbed <release> on RD · imported, replaced <file>"; an adoption's files and the file it replaced are one "Adopted 8 files of Season 1 from Sonarr (…)" line; a hand unlink and its library action one "Removed <file> · by <who> · moved to the trash" line), the same lines in the title page's Journal block (History tab, admins, with **Open in the journal**). Summaries name files only, the full paths as the summary's tooltip (an adoption's first five files, then "and N more") and all listed on the entry. Each line's kind is the entry's label in History's words (Grabbed, Upgraded, Imported, Adopted, Removed, Missing, Failed, Blocked, Search…), the code as tooltip; sources ("Theatarr", "Radarr (radarr_4k)"), flags ("Size outlier") and provider attempts are in words too (`web/src/lib/journal-words.ts`, `router-words.ts`; a pacing message such as "min_gap 20s since the last add" reads "Waiting for the 20 s minimum gap between adds"); the title box and the Older/Newer paging follow the URL's filters, so a link that drops `?title=` empties the box and starts at the newest page; Newer and Older return to the top, and are left out when there is no page either way; **Clear** (shown while Flagged, a kind or a title is chosen) drops those filters. An unblock's line says why the release was blocked in the Blocklist's words ("Unblocked <release> · by <who> · was blocked: Failed the library check: Not imported: …"), read when the line is shown, so lines stored before say it too. An import's summary counts the files a scoped import left out apart ("Imported <release> · 2 linked · 18 for other episodes, left out · 1 for episodes already covered"), "N files matched no episode" only for the others (docs/design/library.md). An entry shows its summary, flags, release, reasoning, provider attempts and what followed first | new (router, library); `waiting_since`, `next_retry` from `internal/web/activity.go` |
| Wanted (managers and admins; Manual import admins only) | `/wanted`, `/wanted/cutoff`, `/wanted/manual-import` | Three tabs; **Manual import** is described under Manual import below (admins only: managers do not get its tab; with `?as=user` its tab is hidden and its address goes back to Missing). Managers get the same lists, Search now and Interactive search, without the admin links (settings, decision journal). **Missing** and **Cutoff unmet** list the search schedule's items, one row per library and film or season (docs/design/router.md, "The search schedule"). Viewing never starts searches or changes version state. A line under the tabs gives the pace: "Up to 10 searches an hour (3 in the last hour). Items are searched again every 24 h. A missing new release is tried again sooner: the wait starts at 1 hour and doubles each time, up to 24 h, until a week after its release." (the last sentence on Missing only, and only while **Search for missing items** is on; both times are the **Search older items again after** setting; "Items are searched again every …" is left out while **Search for missing items** and **Search for upgrades** are both off) When the tab's switch is off a banner says "Theatarr is not searching for missing items by itself. Items here are searched when you ask, or after a failed download. New releases are still picked up from RSS feeds, and one that a search did not find is tried again sooner, until a week after its release." (Cutoff unmet: "for upgrades" and "Better releases are still picked up from RSS feeds."), with an **Automatic search settings** link for admins. Above the table a line names the unit: "48 items (one per film or season in each library, not per episode); Home's Missing counts titles." (the last clause on Missing only, linking to the Missing titles); the empty state says "No items waiting in this selection.". Home's Missing row has an **In Wanted** link beside "See all 25 titles" for managers. Columns (Columns picker; on a phone they show under each title): Library, What ("S20, 139 episodes" for a whole season; a series row opens to its episodes, on Missing each with its air date, on Cutoff unmet with the quality it holds and "Cutoff: WEBDL-2160p", as Sonarr's Wanted lists them; each episode line has **Search** and **Unmonitor**, the existing episode search and monitoring in the row's library only, after which the list is read again so the row's count follows; the answer names the library and counts in words, "Unmonitored 1 episode in 4K", and a library it skips is named too, "not changed: <reason> (4K)"), **Released** (a film's home release, "~Mar 25, 1987 (estimate)" as the title pages mark it when it is the cinema date plus 90 days, `date_estimate`; the newest stored air date of the row's episodes, the day the episode page shows), **Next search**, Last search (for admins the **Decision** link of that search sits in this cell, so the row's actions keep their place; with Last search hidden it stays a row action). **Next search** says **Now** for a due row (" · asked by Search now" when asked; no start time is promised, the hourly limit decides), the time for a scheduled row (the exact time as tooltip) with why it is early ("New release, try 2", "After a failed download", "Added by a list"), **Downloading**, **Not scheduled** (its switch is off and nothing asked for it) or **Paused**; a row never shows a time while paused. Filters (`?waiting=`): All, Due, Scheduled, Downloading, Not scheduled, Paused; they, the library filter and the title filter (`?title=<id>`, a removable chip with the title's name; the title page's **See in Wanted** / **See in Cutoff unmet** links set it, and it carries over between the two tabs) precede 50-row paging, and counts and **Search all** cover the whole selection. Title, Released and Last search headers sort the whole list on the server (`?sort=title|-title|date|-date|last|-last`; first click: titles A–Z, dates and searches newest first; rows without a date or never searched go last); a **Sort** picker offers the same orders on a phone. A row whose last search grabbed downloads says what became of them, from their states (one in flight wins, then a failed one, then an imported one, a candidate every provider missed is never one: never grabbed, so a run that fell back to another release and imported it says "Grabbed" and links to that import, and a run whose every candidate was missed answers `downloaded` false with state `missed` and "No release was cached"): "Grabbed" once imported, "Grabbed · Downloading" (its stage) on the way, "Grabbed, not imported" when it failed; the last two link to the download in Activity (**See in Activity**). **Search now** on a row, **Search selected** and **Search all** make the rows due now under the hourly limit (`router.wanted.per_hour`); Search all, and a selection larger than what the hour has left, confirm first with the count ("They start as the hourly limit allows"). The answer says how many started and how many wait ("Started 6, queued 306"). While asked rows wait, a line counts them, with **Cancel queued searches**, which puts them back on their regular schedule; searches already started stay started. Rows Search now cannot search (a download in flight) cannot be selected, and nothing can be selected while paused (Search selected is hidden with the checkboxes then); the table's cells sit at the top of their row, so an opened season keeps its title, library and actions beside the top of its episode list (on a phone the What line, with the season's episodes, sits under the row's Released, Next search, Last search and actions: the shared table's `card: "foot"` line); a row's search-blocked reason shows next to its disabled Search now when Next search does not already say it. Shared title/season queries display each library separately so its gates remain visible. Routed searches say “Sent to download”; “Grabbed” requires a download from that run. Closed gates link to their settings without toggles. Active title copies link to both queues when they have missing and below-cutoff episodes; shadow copies have no Wanted handoff. Search history shows each search's outcome in one line, links the newest to its results and, for admins, to its **Journal entry**. At most ten newest searches are added after the fifty release-history events are selected. Their related downloads link to the download entry for release actions. | `GET wanted?only=missing\|cutoff&version=&waiting=due\|scheduled\|in_flight\|not_scheduled\|downloads_off&title=&sort=&limit=&offset=` (rows with `waiting`, `next_at`, `why`, `tries`; `search_now_pending`), `POST wanted/search`, `DELETE wanted/search` |
| Requests | `/requests` | users: theirs, by status (an empty list says "No requests match. Show all" when the status filter hides requests, and "You have not requested anything yet. Find a title", linked to Search, on Mine or My requests with none at all; Show all stays inside the same tab); admins: a queue with approve / decline / **Edit request** (versions, seasons; **Approve these** is disabled, with "Pick at least one season", while a series' seasons are unticked down to none, since an empty list means every season; while the editor is open the one-click **Approve** beside it is hidden, so **Approve these** is the approval). Each tab starts its own status filter (To decide: Pending; All and Mine: Open); the list pages 50 at a time. `/requests/<id>` opens a focused request detail; old `?request=<id>` links redirect there (the View request links). | new; `GET upgrades` |
| Profile | `/me` | password (**Change password** stays off until its three fields are filled), sessions, own API keys (if allowed), theme | new |

**Title page** (`web/src/pages/title.tsx`,
`lib/title-layout.ts`):

- **Hero**, compact: poster, title and year, essential facts (certification, runtime, up to three genres, original
  language, rating), readiness for everyone ("Ready to watch in 4K": the best resolution a usable copy has; "5 of 10
  episodes ready"; "Not available yet", or "Getting ready" without See files while a download is in hand; a movie
  missing one of its copies is still ready), audio languages, a
  synopsis cut to three lines with More, and the primary action beside the title: **Add to another library** (add;
  **Monitor** when every library that fits has the title, some not monitored; no action at all
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
  their words and the rest sit under **More**. Under the bar: Pause once for the page, a started search's outcome or the failure with **Retry** ("Search
  series failed: …" on a series), and "No library is monitored." when so. An answer with nothing to search is a toast,
  "Nothing to search" with the reason in its wrapping body, said once, so it cannot stay on the page once monitoring changes; a library row's Search
  now answers the same way. **Add to another library** stays the hero's primary action while a library is left to add.
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
  **Held on** and **File** (the whole file name, wrapping in a column at least 14rem wide; the full path as
  tooltip). On offer: Preferred quality (the profile's
  cutoff, "· upgrades off" when so), Minimum availability, Release, Release group, Languages, Video / audio, Formats,
  Added, Last search, Next search and Path. A series row offers Status, Quality, Size, Held on, Preferred quality,
  Added and Last search. The switch column is headed **Monitored**. Quality is the technical name ("WEBDL-2160p").
  Score shows with the matched formats as its tooltip; a file whose score was never recorded (an adopted file
  Theatarr did not score) shows "—" (tooltip "Not scored"), as History does; a recorded 0 stays "0". **Held on** (until 0.4.5.6 "Cached on"; tooltip "Complete torrents on your accounts, as last checked; not necessarily the one playback uses."; release lists keep **Cached on**, what a provider has in its cache) has a
  pill per debrid provider that holds the file complete now and not failed (RD, AD, TB; TorBox only once downloaded, not at 100% before its files are present), adopted files included (found through the
  hash their link records, else the hash its mount folder points to: torrents.md "Folder hash", display only),
  "—" when none does. For admins a small **Torrent** link beside it (and beside the hash in
  Details) opens Admin › Torrents filtered to the file's torrent (`?q=` the hash's first 12) when the row has one file
  with a known hash; a series row of several files has none (the episode window has it per episode). A fact the file does not have is blank, never 0.
  **Status:** a download in hand and no file reads its stage ("Downloading · 37%", "Waiting for the mount", "Blocked"),
  linked to Activity; a movie without a file "Not monitored", "Upcoming · home release Jan 14" ("home release" only when the
  home release supplied the day, not TMDb's date standing in; not out at home yet, the posters'
  rule) or "Missing"; a held movie "Available" with the quality word beside it (Upgrade found, Preferred quality not
  met or Preferred quality met; Why? unless it is at its cutoff; Status words), with
  "Upgrade downloading · 37%" under it while one is in hand; a series "44 of 46 monitored episodes" with "2 missing ·
  30 not monitored" under it; a library that monitors none of them says it once: "291 of 291 episodes available, not
  monitored" (the library is not monitored) or "37 of 37 episodes · none monitored" (no episode is). A missing
  library's line under the status says why and what next: the last search, or "Not searched yet. Automatic backlog
  searching is off; new releases are still picked up from RSS." (the RSS clause where the library's RSS read covers
  it), with "Use Search now to request a search." for an account that can search an item never searched ("Use Search
  series …" on a series, whose row button below 768 px also reads **Search series**, named "Search series in 4K"). On a
  series the last search names what its run searched, since one Search series writes a run per season and the row
  reports one of them, with that run's own counts (never summed): "Last searched Season 2 today: 474 results, none
  accepted.", "Last search of Season 1 today failed."; after a run that searched and found results, managers get **Show releases** (a run that found nothing stores no list, so it has none),
  which opens that run's stored search (`?run=`) or says it stored none, never an older search; the row's Details
  **Last search results** opens the same run, also after a run that found nothing (it then says the run stored no
  list), so the row gives one answer (a row that shows no such line, such
  as an available or unmonitored series, keeps the library's Last search there). That page heads it as that search,
  not the last one ("the search of Season 2 on Nov 12 that the Libraries row reports", "the search of S02E05 on Nov 8 …" for an episode run, which links without a season; `at=` carries the day and `episode=` the episode, for the heading only).
  The library shown is the address's `version`, so the heading, the panel closed and opened again, and Back always
  show the same one. Picking another library's tab keeps a season and drops the pinned run (its day and episode) and
  a followed release from the address: that tab is headed as its own Last search. Leaving a pinned run or a followed
  release adds a history entry, so Back returns to it; any other tab change replaces the address. An open
  Interactive search closes when the library or season shown changes.
  **Row actions:** **Search now** (icon, managers; disabled with the reason; when other libraries want the
  title, its tooltip says they may use the results too), **Interactive search** (icon, a movie's library) and
  **Details**, which opens a panel under the row: path, edition, a series' episode counts, one **Upgrade** line (the
  quality word with Why? when no Status cell gives it, the episodes under the preferred quality, and the better
  release found), minimum availability when its column is hidden, then the library key (admins) and the hash,
  a movie's **Media** group (see "Media group" below), **Add to other providers** (managers, a movie's file whose torrent is not on every provider with a key; torrents.md
  "Add to other providers"), the library's
  restriction notes, **See in Wanted** / **See in Cutoff unmet** (managers, monitored libraries; Cutoff unmet not once the quality is met), **Last search results**
  (managers), each library link with **Unlink**, and **Remove from this library** (managers). Below 768 px each
  library is a stacked block: switch, name, tag and status; quality · score · size, and the provider pills when any
  provider holds the file; a movie file's media line as the Media column writes it (wrapping, nothing without media);
  the whole file name, wrapping (never cut with an ellipsis); then
  Search now, Interactive search and Details with their words. There is no Columns button there. A library the
  title was removed from has no row; if it still has links, one line under the table says "Still linked from a
  library this title was removed from: <library> (N files)." with **Delete files…** (managers). With no library:
  "This title is in no library yet."
- **Media group** (a movie row's Details at every width, and the episode window): the file's media in plain text
  under "Media", one row per fact it has, nothing on hover: **Video** (resolution · codec and bit depth when stored,
  "x264 8-bit" too · dynamic range; the compact line names a depth above 8 only), **Audio** (every probed track on
  its own line, one track too, "Japanese · FLAC 2.0", the codec alone for a track without a language or with "und";
  a taken-over file: its first track, "TrueHD Atmos 7.1", or "13 tracks; first: TrueHD Atmos 7.1" when its arr
  counted several; channels always with one decimal, as everywhere), **Audio languages** (each once, by name; only
  for a taken-over file, which has no track list), **Subtitles** (every language once, by name, in full, wrapping) and
  **Runtime**. The tracks come from Theatarr's own probe; a Matroska probe read the profile words (Atmos, DTS-HD MA)
  of the first track only, so a later track there reads as its codec family ("TrueHD 7.1"); a taken-over file has
  the arr's first track and its track count only. A file with no media (taken over without the arr's media info) has no group, and neither has a movie
  library with several active links (its Details lists each file). Accounts without "see files" get no media.
- **Latest event** under the status (`latestEvent`, `web/src/lib/title-facts.ts`, from the title response's
  per-library `history`): "4K Remux: Imported yesterday", "Upgraded yesterday" for an import that replaced a file ("Re-downloaded yesterday" when every file it replaced was the same release by name, `redownload`; the
  server's `upgraded`: History has an Upgraded row for that download), or the last failed download with the server's
  reason, the same sentence as History ("Last upgrade attempt Nov 13: not cached on RD and TB").
- **Next search:** a movie's Libraries table can show a **Next search** column, and a series season with
  one library in view adds "Next search: in 3 h (new release)" to its facts. Both read the same time as the Wanted
  page: "now" when due, else the time, with why it is early ("new release", "Search now", "after a file
  stopped working while playing" for the search of a file that broke while someone played it, whichever row of its
  season is shown, "after a failed download"). Nothing is shown when the item has no time there: not monitored, at its cutoff, not
  scheduled, a download in flight, or paused. Schedules are per season, so an episode has no time of its own.
- **Tabs:** Episodes (series and anime, the default) / Watch order (anime with a franchise) / About (overview when
  long, the fact table, outside links; admins also identifiers, release dates, files and registry) / History ("see
  files": the title's rows of Activity › History, the same builder and the same table without filters or a Title
  column, the episodes under each event; admins also the title's journal). There is no Libraries tab: the table is under
  the hero. About's facts say **Released** (a series **First aired**), or **Releases** / **First airs** for a day still
  to come, and **Latest file** ("see files" only: when the newest current file came; when the title was added is under Details, "Added
  to Theatarr"); **On disk** counts a file two libraries share once; **Tags** ("see files" only)
  lists the labels on the title's libraries, each once, each a link to its list filtered by it (`/movies?tag=kids`);
  it is hidden when there are none, except for managers on a title in a library, who see **None** and **Edit**.
  Edit opens **Edit tags**: the labels as chips (× takes one off), a box that adds one (Enter or Add; it suggests
  the labels there are; what is typed is trimmed and lower-cased, and anything but a-z, 0-9 and - is refused as it
  is typed with "Allowed characters a-z, 0-9 and -"), the line "Changes apply to every library that has this title.
  Connected apps see these tags, and a webhook limited to a tag gets this title's future events.", and Save
  (disabled until something changed) and Cancel. Save applies only what changed (docs/design/arrview.md "Tags per
  version"): a typed label, also one already a chip, goes on every library that has the title; one taken off comes
  off every library. Labels are compared ignoring case: a label a connected app stored as "Kids" is one chip with
  "kids", and × on a label added in this edit only cancels it. A good label left typed in the box counts as added on Save (and
  in the bulk dialog); while the box holds a refused one, Save is disabled. × on the chip the box names also clears the box. A chip shows the label's stored spelling
  (the title's, else the label list's), a new one lower-case. About's Files inventory includes old-manager
  files (with their instance) and active native library links (with their size, no instance word), deduplicated by
  path. Release dates list the primary release's stored rows (Library and activity
  pages, Release dates), the country named once under the table and a fallback cinema date marked first release; type and source in words, the raw value as tooltip.
  Old `?tab=seasons|details` links open Episodes and About; `?tab=versions|copies` open the first tab.
- **Minimum availability and series type.** A movie's Libraries table shows **Minimum availability** (a column, or
  the row's Details when the column is hidden), the value in
  force for that library (`versions[].minimum_availability`, from `availability.Rules`: the explicit choice, else
  the imported value, else the library's setting, else released), with no word on where it comes from. A manager
  changes it there: `POST /titles/minimum-availability {title_ids, versions, minimum_availability}` writes an
  explicit choice for that library only (libraries the movie is not in get none). **Library default** (the first
  option; `minimum_availability: "default"`) returns the copy to the library's own setting; the option always reads "Library default (Released)" with the value
  it gives (`versions[].library_minimum_availability`, see files only), also while the copy has a choice of its own;
  `versions[].minimum_availability_default` says the default is in force. An imported `tba` is shown but cannot be picked. A series' About facts show **Series type** (`series_type`, the title's one type for every
  library and arr view), which a manager changes with `POST /titles/series-type {title_ids, series_type}`
  (`catalog.SetSeriesType`; the anime flag stays). A hint under the select (managers) says so: "Saves at once for
  every library. Affects release matching and which libraries search this series." No dialog, no Save button.
  The change is saved at once and can stop searches: acquisition
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
  rejection offers "Add anyway"; a release on the library's blocklist asks "This release is on <library>'s blocklist:
  <reason>. Add it anyway? It is taken off the blocklist." and "Add anyway" sends `unblock`; a failed add starts no
  search (docs/design/router.md).
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
  (`Releases.grabbed`, for an episode its downloads of that episode): "Grabbed #1 via rd → imported" ("Queued #2" for a
  release still waiting behind provider pacing) or "Nothing grabbed: <reason>" ("Nothing grabbed for this episode" when a season search routed only others); it describes
  the downloads that search opened. A search that accepted nothing reads "Nothing grabbed: no accepted release among
  351 results", with "; partial answer: due again sooner" for a partial run, built from the run's count when read (so
  older rows, whose stored message carries the indexers' notes, read the same); the notes stay in the lines below
  (Search incomplete, Technical details). Title History's searched row says the same line. The source badge, the age and "12 results, 2 accepted" sit in one line; under it
  the message says what the stored search came from ("From RSS", "From a search", "From a search for S01"; an
  episode adds ", which covered S01E05"). A search that ran routed with no download says "A release was chosen, but no
  download was started.". The indexers' notes are one line naming what went wrong per indexer ("Search incomplete:
  therarbg was rate-limited, nyaa did not answer in time."; nothing when every indexer answered), with the raw notes
  in a closed **Technical details**. A library that may not search live says why: "The acquirer is not running". The page's header explains the page once: the card
  repeats no hint and the action bar has no Interactive search. The panel offers Interactive search for the shown
  library: a film's, or a series' whole-series window (packs of several seasons; the header says so, and that a season
  or an episode has its own on the title page). The stored grid stays read-only. A season's link (its Why?'s **Show
  releases**; `?season=N`) shows that season's own stored search in each library, the newest decision whose run target
  is exactly `sNN` (an episode search of it, another season or an RSS read is not it), or "No search of season N is
  stored for <library>." with one link, **Show the last search for <library>** (`?version=K`, no season: a season
  upgraded from RSS has no search of its own); the header names the season and Interactive search there searches that season. Without
  `season` the page shows the newest stored decision of any kind, as before; the stored fallback after a failed live
  Interactive search stays the newest of any kind, labelled as such. The grid's filter box reads "Filter releases" (what it
  matches in its tooltip); an indexer is shown without the "prowlarr-" prefix of a linked Prowlarr indexer's key, the
  key as tooltip. The list has one row per torrent (info hash), where Sonarr and Radarr list each indexer's copy: the
  best-ranked copy stands for it, the Indexer cell names every indexer that returned it (the first two, then "+N"; the
  tooltip gives each copy's indexer and name; a narrow card lists them all; with copies the indexers are a button, in
  the table and on a card, that opens a row listing each copy's indexer and name, so a phone reaches them by a tap; on
  a card a failed release's reason wraps in full and its provider answers open by a tap, said once (beside Grab only
  the blocklist note and Try again), and every rejection reason and language is listed, wrapping, where the table keeps
  one line, two reasons or languages and a tooltip), Peers is the copy with the most seeders and Cached on the union; the
  Indexer filter and the text filter match any of its indexers. A live answer counts rows; a stored one keeps its
  search's own count ("N releases returned by the indexers"), and since it keeps only a sample (the best accepted, then
  the best-scored rejected) a list shorter than that count says "Showing N stored results." (the release grid's caption,
  on the Last search page, in the episode window and in Interactive search's fallback to the stored list; "the best N
  of M" is a live search's only); with a filter on it counts what the grid shows ("1 of 6 stored results matches the
  filter."), and says nothing beside "No release matches the filter"; in the episode window a stored season, series or RSS
  search counts the stored indexer entries for the episode, "11 stored indexer entries for this episode, 8 accepted" (each indexer's copy; the grid merges copies of one torrent into one row, so it can show fewer), and its "Nothing
  grabbed" line names the count as the search's: "no accepted release among N results of the season search" (or "of
  the series search"), built from the run's count (`storedGrabbed`'s scope; the search's notes stay in the panel's own
  lines), with "; partial answer: due again sooner" when the run was partial. The badge says what the answer is: "Live search", "Last decided search", "Last
  RSS match" for a stored RSS read (the episode window too), or "Nothing stored" alone, with no counts, no message and no empty-list line (a live-search failure still says why); a stored
  search that returned nothing keeps its "0 releases returned by the indexers, 0 accepted". An episode an RSS match grabbed nothing
  for reads "No new grab for this episode from this RSS match". A live search no indexer answered is a failed search ("Live search failed",
  the stored list shown), and a live search is never retried on its own. `?hash=` picks out the followed release's row, or says it is not in the list.
- **Interactive search** (`components/title/interactive-search.tsx`, the grid `release-grid.tsx`, columns
  `lib/release-columns.ts`): a wide window (a full-height sheet on phones) that searches the indexers the moment it
  opens, as Radarr's does. Managers open it for a movie from the action bar (the whole movie) and each library's
  row (that library). A series (anime too) has it there too, for the whole series (titled with the series
  name and "Whole series"): only the query without a season is asked and only packs of several seasons are kept
  (accepted and rejected with their reasons, one row per torrent, at most 300); under the list "N other results are
  not shown. Search a season for those.", and with no pack "No pack of several seasons found.", followed by "Search a season instead." when no other
  result was left out. Every row there is a pack, so it has no Packs only filter. In every release list a release
  that is a pack of several seasons (the candidate's `multi_season`: its name, for an anime series its claim, also
  an absolute batch that fills whole seasons) shows its seasons ("Seasons 1 to 5") where an episode is shown; any
  other release keeps its episode range ("S01E12–S02E01 (2)"). A grab from the whole-series window sends `whole`:
  the server decides it again with that search's criterion, and keeps its live results apart from a season search's
  of the same hash. Every series has it on each season header (season packs and that season's episodes), each episode
  row (an icon button at the row's end; it does not open the episode overview) and the episode overview (per
  library). On a series the action bar's Search now reads "Search series". Each Wanted row opens it for that library, and the row's season for a series. It is offered only when a
  current library of the title searches live (`control.live_releases`). The title data
  still lists removed copies, so the window takes the current ones from the removal scope (`GET
  titles/{id}/removal`, its `copies`): a removed copy gets no tab, and a search asked for it opens on a current one. While the indexers answer, the window says "Searching the
  indexers for <library>…" with a seconds counter; closing it stops the search. One row per release, with the
  columns Source (indexer kind, private trackers marked), Age, Title, Status (right after Title by default, so the
  verdict stays in view when the table scrolls sideways; it keeps a readable width), Indexer, History (what came of
  the release before), Size, Peers, Cached on (provider pills), Episodes (series), Language, Quality, Score, Formats
  and Flags (a public release shows only the flags its indexer names, no freeleech from the volume factors that
  public feeds send for every torrent), then Grab. Scores carry the thousands separator ("+4,450"; the Current file line "score 4,150"), and no column hint asks to point at a cell. The release name keeps its text and font; the facts in it are coloured, text only
  (`lib/release-highlight.ts`, `components/title/release-name.tsx`, the `--rel-*` tokens): dynamic range (DV, HDR10+,
  HLG …), audio with its channels (Atmos, TrueHD 7.1, DDP5.1 …), resolution (2160p, UHD …) and source (REMUX, BluRay,
  WEB-DL; a bare WEB, and Opus, only after a resolution, so a title's Web or Opus stays plain). A search's one-line summary (Last search, and the title History's search row, whose release and provider follow it) describes the download that counted, by download id: the one supplying the current file, else the newest imported, else one still in flight, else the first; the search's other downloads are counted by state ("Grabbed #2 via AD → imported (the current file); 1 other failed"), never ranked, since in a series search they may be other episodes. History and Grab show what came of each release now, not when the list was read: the three lists (this window, Last search, the episode window's) read `GET titles/{id}/release-outcomes` (`useReleaseOutcomes`, under the title's query key), which download events refresh: at once on a change of state, at most every 5 s per title on progress alone, never polled. A newest download still in flight wins over an older import of the same release. History then shows the download's words as Activity says them ("Downloading (43%) · AD"). Grab shows, instead of the button: "Sent" from the click until a new download answers; then the download's words with a spinner while something works on it (none for an import held for review or a download waiting for its retry time); "✓ Imported" (imported or current); "Failed" with the reason as its title text, under it one line (truncated, the full text with each message as its title) with what each provider answered, the newest download's last attempt per provider in the attempt words Activity uses ("RD: refused by Real-Debrid's name rule · AD: not cached, our add deleted · TB: known not cached"), and the button again ("Try again") at once: a grab by hand asks the providers again (docs/design/router.md "A grab by hand asks the providers again"). The reason names the providers only from explicit uncached answers ("Not cached on AD and TB"); a plain miss may be a 451 or a refused file name. History's Failed badge carries the same answers in its title text. A failed release on the library's blocklist keeps its "Try again" button, with the note "On the blocklist: a grab by hand takes it off" in its title text; its Try again asks first, once ("This release is on <library>'s blocklist: <reason>. Grab it anyway? It is taken off the blocklist.", the reason being the block's own, `blocked_reason` beside `blocklisted` in the outcomes, not the download's failure; then the rejection or the better-scored file it replaces, when either applies) and sends `unblock` (and `override`) with the first request. A grab of a release the list does not know is blocklisted answers 409 with the block, and the confirmation asks "This release is on <library>'s blocklist: <reason>. Grab it anyway? It is taken off the blocklist."; "Grab anyway" sends it again with `unblock`. A grab that would open no download (already downloading, upgrades off) is refused with the reason as the "Not sent" toast. A replaced release gets the button. Phone cards show the same. Last search and the episode window's list
  use the same grid; the journal's stored lists, History and Activity stay plain. Every column but Episodes and Formats sorts (the columns Sonarr's sorts; Status puts the
  accepted first, History the current file first), can be hidden and reordered (Columns); the column choice is kept
  per user in this browser (`localStorage` `theatarr.library.release-columns.<user>`). Filters: text, Accepted only,
  Packs only (a series; not in the whole-series window), and selects for Cached on (anywhere, or one provider), Indexer, Quality and Language, each
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
    sentence). The season's Search is disabled, with "No monitored episodes in this season" on hover, when the
    library in view may search but no library of the title that may search monitors an episode of it (the season
    search takes monitored episodes only, and the server widens it to the title's other monitored libraries), and
    with "<Library> does not monitor this series" when the picked library does not monitor the series;
    Interactive search stays. From All libraries, and when no library in view monitors the season but another
    library that searches does, the button says "Search season N now in the libraries that monitor this season". A season search's toast says
    "Searching season N in the libraries that monitor this season" from All libraries, and for a picked library that
    got no search of its own (`acquire.Selection.RequestedQueued`); a picked library that got one is named. An episode's Search (row, episode window, selection) stays enabled for unmonitored episodes: a
    search that names its episodes takes them unmonitored too, as Sonarr's does.
  - **Jump to** (a series with more than 8 seasons, specials not counted): a select "Jump to season" above the
    seasons, newest first and Specials last; picking one opens that season and moves the focus to its heading, which
    scrolls into view below the sticky header. No route or stored state.
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
    too; the download under it), Quality, Size and **Held on** (with the admins'
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
    (release, quality, score, size, group, **Held on** with the admins' **Torrent** link,
    languages, added, "From" the arr
    instance for an arr's file (none for Theatarr's own link), path), shown openly, and under them the file's
    **Media** group (as a movie row's Details: "Media group" above). **History**:
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
`components/{title/release-grab,title/copy-actions,title/replace-file,title/edit-facts,activity/download-actions}.tsx`, scope words in `lib/ops.ts`).
Managers and admins only (`manage`; 403 otherwise). Every action opens a confirmation that names its scope ("This
will" / "Stays as it is"), except grabbing an accepted release from Interactive search, which is one click, and is recorded in the journal as an `action` entry (`act:<id>`, table `journal_action`,
00039) with who did it (user name, or `key:<name>` for an API key). They reuse the existing paths, never a second one:

| Action | Where | Route | What runs |
|---|---|---|---|
| Grab a release | Interactive search: live answers only (`Releases.grab`); an accepted release is sent at once, a rejected one or one that would replace a better-scored file asks first; the stored "Last search" has no Grab | `POST titles/{id}/releases/grab {version, info_hash, override, unblock}` | The release must come from this server's own live search for that title and copy in the last 30 min (never release data from the browser). It is decided again; a rejected release (then or now) answers 409 with the reasons and needs "Grab anyway" (`override`). A release on the library's blocklist answers 409 with `blocked`; the dialog then asks "This release is on <library>'s blocklist: <reason>. Grab it anyway? It is taken off the blocklist." and "Grab anyway" sends `unblock` (taken off only when nothing else refuses the grab; one unblock in the journal). The action is refused with the Pause (router-disabled) reason before enqueueing when provider writes cannot run; otherwise `router.Enqueue` applies the indexer's provider allowlist, blocklist, pacing and budgets |
| Remove download | Activity › Queue: a row's Remove (Dismiss for a failed one), or **Remove selected…** for ticked rows (one call per row) | `POST activity/downloads/{id}/remove {blocklist, search}` | `router.Dismiss` (as the arr views' `DELETE queue`): Theatarr's record ends as failed; the torrent at the provider stays (shared by every library), linked files stay. The confirmation's checkbox "Also blocklist this release and search again" sends both and blocklists the release for the copy (source `ui`, "Removed in Activity with blocklist") and searches the copy; `search` without `blocklist` is refused. A toast keeps the server's outcome visible when the row disappears |
| Retry | a Queue row's Retry (Import now for a finished download), and History rows about a failed download | `POST activity/downloads/{id}/retry [{unblock}]` | The confirmation says "Try this release again for the <library> library: a new download starts when a provider takes it" (a grab's: "Grab <release> for the <library> library: it is added at the first provider that has it"). failed: the same release, as the download stored it (its release data with cache evidence, as the router rebuilds a download it resumes), goes to the router again as a new download only when `router.enabled` is true (Pause off) and its library is on; a paused or turned-off route is refused with the reason (409). A release on the library's blocklist answers 409 with `blocked`; the confirmation then asks "This release is on <library>'s blocklist: <reason>. Retry it anyway? It is taken off the blocklist." and sends `{unblock: true}` (taken off only when nothing else refuses). Queue and History rows do not carry whether a release is blocklisted, so a blocklisted Retry asks twice (Retry, then Retry anyway). Retry runs the grab's screens: upgrades off, a download in flight and the client-indexer rule refuse it with the reason (409); so does a copy removed since the download failed (the interactive Grab's "the title was removed from library …: add it there again first"). Ready or importing: import now (`library.Import`), answered with its `outcome`: `imported`, `partial`, `rejected` (failed and blocklisted), `held` (left for review), `waiting` (mount) or `dry_run` (gate); anything but `imported` is shown as a warning and journalled as such. Anything else is refused (409) |
| Unblock | Activity › Blocklist: a row's Unblock, or **Unblock selected…** for ticked rows (one call per row, no search) | `POST activity/blocked/unblock {version, info_hash, search}` | deletes the blocklist row (`router.Unblock`) in the same transaction as the journal record; optionally searches the copy (the dialog's **Search for the <Library> library again now**; off, with the reason, while the search would not run: the blocked row's `search_reason`, "The title is not in the <Library> library" for a copy removed from it, "The <Library> library is not monitored" when the title does not monitor that copy, "This release has no title in Theatarr: Unblock only takes it off the blocklist" for a row without a title, read once for the page, else the reason the title page's search buttons give (Pause)). The answer names the library ("Unblocked: the release may be grabbed again for 4K WEB"). A toast keeps the server's search outcome visible when the blocked row disappears |
| Remove from this library / the title | the library row's Details / the action bar's **Remove title** | `GET titles/{id}/removal?versions=` (scope), `POST titles/{id}/remove {versions, unlink}` | the shared removal (`internal/removal`, also the arr views' DELETE): `catalog.Remove` (`arrview_removed_version`, unmonitor; the title leaves when no copy is left), and in the same transaction the downloads in hand dismissed (`router.EndRemoved`; a running import then takes its links back), and the copies' search requests cleared. The dialog's two choices are on by default: **Delete the files** (`unlink`): the copies' symlinks, dead ones too, go to the trash through the gate and the folders they leave empty go, strictly below the library root (a link another current library of the title uses, or the gate refuses, stays and is reported as kept); and **Delete it from the debrid providers** (`debrid`): after the answer, the torrents no other title or library in Theatarr uses are deleted from the providers after the full backup (torrents.md "Removal clean-up"), managers included. The second is off and disabled while the first is off, with one line saying why (kept files would stop playing). Both off: files and torrents stay; files only: the links go and the torrents stay (Torrents › Not used by Theatarr). A removal that takes the title's last copies (**Remove title** on the action bar, or Remove from this library when no other library keeps a copy, the libraries in sync with it included: the scope's `title`) also offers **Also keep lists from adding it to any other library** (`exclude`, off by default): the title goes on the list exclusions (a movie by TMDb id, a series by TVDB id) in the removal's transaction; a removal that leaves a copy does not offer it. Lists never restore a removed library either way (the dialog's "Import lists will not restore the removed libraries" line, always shown); the box extends that to every library. The dialog says the title is hidden from Seerr and the other arr clients only when the last copy goes; removing some copies says "<Library> loses its copy. Apps whose libraries include a remaining copy still show <title>" (an app's view can cover several libraries). The selection bar's Remove from a library says the same when every title keeps another copy; for a mix it gives each group by its count ("1 title keeps a copy in another library and stays visible to apps that include it; 2 are in no other library and are hidden from Seerr and the other arr clients"), and the title-wide sentence when none keeps one. When the removal cancels open requests (approved, processing, failed or available ones left with no library they ask for that stays: the scope's `requests`, counted after the sync expansion with libraries removed before as gone) the dialog and the selection bar's Remove add one line: "1 request for it is canceled" ("2 requests for them are canceled" over a selection); none, no line. The dialog says what goes and what stays (History; "Import lists will not restore the removed libraries; add it yourself to bring it back"; "A file another library of the title uses stays" only while some library of the title stays). After confirming, a removal that leaves the title in no library goes back to the list the title was opened from (Movies, Series or Anime with its filters; opened from Search, Home or elsewhere: the title's own list; `web/src/lib/last-list.ts`), any other closes the dialog; either way a notice states the outcome and opens the title's History, where the Removed event gets the clean-up's outcome. The list's selection bar removes many titles through the same endpoint, one title after another ("Library views"). A move that fails otherwise (a full disk) is reported in `failed` with the rest (200), not a 500 after the hide: the scope then lists the removed copies whose links remain, dead symlinks included (`leftover`) and the dialog offers "Delete files", with only the debrid choice (deleting the files is its point), which deletes just those (and their torrents when asked). The journal's remove entry claims no unlink; each moved link has its own `unlink_file` entry once it moved. A copy an arr still manages is refused (409). Adding the copy again restores it. A title with no copy left is in no library anywhere: not in the lists, their totals and facets, Home's count and Upcoming, the calendar (with unmonitored too), the search's library group or a TMDb hit (which offers Add; the add sheet's `in_library` is false and the add restores it by its title id), a collection's held count (counted as removed); requests whose every library was removed are canceled ("removed from the library", at the removal, by nobody), the others keep only the libraries left (stored at the removal, so adding a library back later does not put it into the old request), and asking for a removed library again is a new request; these moves are not announced (no Now available); offers never go to a removed library. A removed copy of a title that stays in another library is not in the releases picker, and searching, grabbing or adding a release for it is refused (400). Each removed library's History row records what became of its files after unlinking: kept, deleted, or partly deleted with how many were left in place and why (a refusal, a link another library uses, a link that is not Theatarr's symlink), and the debrid clean-up's outcome once it ran. Adding a title every copy of which was removed is a new add: the sheet offers anime, monitoring, series type and new seasons, its options follow the anime choice, and the values it shows are sent and apply. Its page reads "Not in the library" with Add, About and History, and "N files kept from before it was removed: Delete them" (the leftover removal); `titles/{id}/search`, `releases`, `rename` and `episodes` answer 404 as for a title never added, while `titles/{id}`, its removal routes and History keep answering. A title with no version at all yet is not a removed one. With the box cleared, under what stays the dialog says where torrents nothing uses are deleted: Admin › Torrents › Not used by Theatarr (`/admin/torrents?group=unused`; a link for admins, plain words for managers) |
| Change episodes (admins only) | the episode overview's Details tab (the file), for one of Theatarr's links | `POST links/{id}/episodes {episode_ids, replace}` | `library.ReassignEpisodes` ([library](library.md) "Which file is which episode"): the link is renamed for the chosen episodes and the choice is kept for later imports; a chosen episode with another file in that library is refused (409) unless **Replace** is chosen, which moves that file to the trash. A file whose link has no download (`no_download`, taken over from an arr) gets the button disabled with "Taken over from Sonarr or Radarr: use Manual import to change its episodes". The dialog lists the series' episodes as checkboxes under a **Filter episodes** box; a ticked episode the filter hides stays ticked, and **Change episodes** stays off until the set differs from the file's. Recorded in the library's actions, not the journal |
| Add to other providers | a movie's library row Details, the episode overview's Details tab (the file), an Admin › Torrents row's expanded part (admins); shown while `add_to` is not empty | `POST torrents/copy {hash}`, then `GET torrents/copy/{hash}` every 3 s while running | job `torrents.copy`: the torrent is added to the other providers with a key that have it cached; an add that is not cached is deleted again; one line under the button says each provider's outcome ([torrents](torrents.md) "Add to other providers"). The link is not changed |
| Replace file | a movie's library row Details (each link) and the episode overview's Details tab (the file), for managers who see files; the row carries `replace_scope` (whether it may run, why not, the libraries and episodes that lose the file, whether its release is a pack) | `POST links/{id}/replace` | the operation, refusals (among them "<library> does not search this title (it is for anime only / not for anime), so no new file would be searched", and "Not searched now: … (<library>)" by the library's name; and, the same on both pages and without an instruction, "This file is not one of Theatarr's links, so Theatarr cannot replace it" and "Theatarr does not know this file's release, so it cannot keep it from coming back") and confirmation scope of Requests › Problems' **Replace file…** ([users.md](users.md) "Report a problem"; `linkFiles`, `replaceFile` in `internal/web/reports.go`), without a report: the link goes to the trash, its release is blocklisted in each library that loses it (a pack as a whole), and those libraries are searched for the movie or every episode it held; refused (409) before anything changes unless that search can run. The journal records an unlink "replacement search requested". The plain Unlink stays as it is |
| Edit file facts | a movie's library row Details (each link) and the episode overview's Details tab (the file), for managers | `GET links/{id}/facts` (the stored facts and the form's choices), `PATCH links/{id}/facts {quality, revision, languages, release_group}` | `library.EditFileFacts` ([library.md](library.md) "Correcting an installed file"; the revision picker labels 1 "first release", 2 "Proper or Repack", 3 and higher "a later Proper or Repack (vN)"): the file's quality, revision, languages and release group stored as set; formats, score and each library's cutoff state worked out again at once; nothing renamed. Journal action `edit_file` |
| Undo upgrade | History (Activity › History, a title's History tab): a replaced file of an "Upgraded" or "Replaced" row, for managers, when the row's replaced file carries `undo` (the replacement's saved facts kept, still the latest replacement of its slot, the earlier file still on the mount); the button says `undo` before it acts: "Undo upgrade: puts the earlier file back and blocks <release> in this library" | `POST activity/replaced/{id}/undo` (the replaced file's `id`) | `library.UndoUpgrade` ([library](library.md) "Undo an upgrade"): the earlier file back exactly as it was, the replacing release blocked in that library and marked researched (no search); one file at a time, no quality comparison; a refusal is one sentence (409). History then has an "Upgrade undone" row, and the Blocklist row says "Upgrade undone by <who>". A gone file's replacement by a lower quality reads "Replaced", not "Upgraded" |
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
**Request more seasons** when the caller's versions are all there but some series seasons are not monitored: the
hero's (sticky on a phone) is the one button, the card keeps its sentence and requests, and the Episodes tab names the
seasons as information ("Not every season is here. You can ask for seasons 2, 3 with Request more seasons.");
otherwise **Request**. The sheet never ends on a disabled button. The caller's open
requests come from one `GET requests?mine=1&status=open` (the caller's own only), matched by title id or kind and
TMDb id. Global search reads "Search movies, series and anime"; below the `sm` width it reads "Search", so the word stays whole beside the admin's Pause and status pills.

**Library views** (`web/src/pages/catalog.tsx`, `web/src/components/library/`, `web/src/lib/{library,library-status,index-columns,poster-options}.ts`,
`internal/web/copies.go`). A library holds one rendition of a title; its monitoring
can be changed. Its **coverage** is the episodes it holds against those aired (a
movie copy is held or not), and its **acquisition** is one summary of the downloads in hand for it, in stages:
Queued, Starting, Downloading N %, Download complete, Waiting for the mount, Linking, Blocked ("Upgrade …" when the
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
add deleted", "held_error" "a failed copy is on the account; an admin can delete it under Torrents",
"router:miss" "from a search that found it nowhere"), torrent states and the torrents journal
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
  `Up to 2024`), one per genre, `Series status: Ended`, one per tag (`Tag: kids`). Has and Lacks of one library
  exclude each other: picking one drops the other for that library in the same address update (Has Remux with Lacks
  WEB stays possible). Lacks counts and lists only titles of the library's media kind (the facet's `lacks`, sent by
  the server), so on the mixed Anime list "Lacks Anime movies" holds no series; switching Anime to the other kind's
  segment drops such a Lacks in the same address update, as does any switch while its library's kind is unknown (the
  facet's `kind`; facets still loading, or a library with no current copy). Without See files there is no
  library, provider, tag or state chip. With nothing found:
  "No titles match these filters." and **Clear filters**. The sheet heads its first group **Monitoring** (On, Off:
  whether a library monitors the title); the status **Not monitored** (no file and nothing in it monitored) is apart,
  its option named **Not monitored, no file** so its count is not read as Monitoring Off's. On Series (and Anime ›
  Series) the status **Missing** reads **Missing (no episodes available)**: a series with gaps is Partly available.
  A status chip uses its option's words (`availabilityOptionLabel`), never shorter ones. Series and Anime have
  **Series status** (Continuing: TMDb Returning Series or TVDB Continuing; Upcoming: TMDb In Production, Planned or Pilot, as the Series status column calls them; Ended: Ended or Canceled; the title
  metadata's status, else the series metadata's; `series_status=`, refused for movies; on Anime it matches series
  and is dropped when switching to Movies). **Tags** (See files only; `tag=`, refused without it): the labels
  compatible clients set on a title's libraries, with counts; picking several shows titles with any of them on any
  library. **Saved filters** at the top of the sheet: **Save as…** keeps the
  list's filters and sort (on Anime also All / Movies / Series, never the view) under a name, the same name
  replacing that one; picking one applies it, and the menu names it only while the list shows it (after a change it
  shows its placeholder, so picking it again applies it again); **Update “name”** overwrites the last one picked (else
  the one the list showed when the sheet opened) with the current filters and sort; **Delete “name”** forgets it. They are kept per list and per user in this browser (`localStorage`
  `theatarr.library.saved-filters.<list>.<user>`, the address's query string under a name); a field an old saved
  filter has that the list no longer knows is ignored. Filters, sort and a picked
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
  Runtime, Certification, Rating, Genres, Tags (see files), Original language, Minimum availability (movies), Provider and Folder.
  **Status** is the poster badge's word in its tone, with the date for Upcoming, or the stage of a download in hand
  linked to Activity. **Libraries** has one pill per library in the colour rule: "4K · Remux-2160p", "WEB ·
  Missing", "4K · 13/17" (a series has no count before anything aired); a click opens the title's Libraries dialog
  (each library's file, state, stage and monitor switch; search and files stay on the title page). **Added** is the
  day the title was added (sort `created`); **Latest file** is when the newest current file came (sort `added`).
  **Held on** (until 0.4.5.6 "Provider"; its filter too) shows RD, AD, TB pills: the accounts holding the files' torrents complete. Managers get a fixed last column (not in **Columns**) with **Search now** (the
  title page's: every monitored library, missing and upgrades; the answer is a toast) and, for a movie,
  **Interactive search** in its first library, for a series **Choose season or episode** (a link to the Episodes
  tab). Each is disabled from the row's own data with the reason as tooltip: Pause, "No library is monitored.", else each monitored library's verdict; on a phone they end the card with their words. Headers sort where the server can: Title, Studio / Network, Next, Size, Added,
  Latest file, Quality, Year, Release date, Runtime, Certification and Rating. The per-library completion sort
  (`sort=completion&sort_version=<key>`) stays in the API and old links, but no header offers it. The Sort menu and
  the column headers use the list's words (Movies: Next release, Studio, Release date; Series: Next airing, Network,
  First aired; Anime, which mixes both: Next episode / release, Network / studio, Released / first aired). Anime's
  Movies segment offers the Movies columns (no Seasons, Previous airing or Series type; In cinemas, Digital release,
  Physical release and Minimum availability can be picked) under the movie words (Studio, Next release, Release
  date); its Series segment offers the Series columns under the series words. The stored column choice
  stays Anime's, so a column hidden on Movies comes back on All and Series. An undated
  title sorts last by Release date. A sort whose column is not chosen (Next
  release, Latest file, Quality) shows that column first while the sort lasts, so the order follows a visible cell. Below 640 px rows
  are cards: thumbnail, title with Status on its own line under it, then the pills, then each chosen column as a line. Without See files the
  Libraries, Quality, Quality profile, Size, Latest file, Minimum availability, Held on and Folder columns are not
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
  The status bar and the library lines always show."; accounts that do not see files, which have neither, get the
  first sentence only): size (small, medium, large; on a phone small fits three
  across, the others two) and the details under each poster: Year (the default), Next release / Next airing, Studio /
  Network (Anime: "Next episode / release", "Network / studio"), Quality ("4K: Remux-2160p · WEB: WEBDL-2160p"), Size (the title's total), Added (the day the title was
  added) and Rating; Quality and Size need See files. Without See files a poster has the badge only. Sorted by
  Rating, Size, Runtime, Added, Latest file or Release date, each poster first shows that value as one line in the
  table's words ("Rating 7.9", "Size 48.4 GiB", "Runtime 2 h 4 min", "Added Oct 1, 2025", "Latest file 3 d ago",
  "Released Jul 16, 1988" or "First aired Mar 1, 2019"; `lib/sort-line.ts`), so the order follows a visible line;
  none for Title, Year and the other sorts; while it lasts it replaces the chosen detail that says the same (Rating,
  Size, Added, so Added shows the day as the table does); Size and Latest file only with See files.
- **Select** (managers) works on both: tick titles ("Select all 60 shown" selects the loaded rows; with more matches
  than loaded, "Select all 524 matches" loads the remaining pages of the same query with a spinner, the bar waiting
  until they are in, then selects every title; **Clear selection** shows only while something is selected or Select all is loading; changing the selection, Done or another query meanwhile cancels it); the bar counts titles ("12 titles selected") and offers Monitor, Unmonitor, Search now, Add to a library (with the add
  permission), Tags… (with see files), Remove from a library and Remove. Household accounts have no Select.
  - **Monitor / Unmonitor** open a review: the library (**Every library** by default, or one) is picked there, and
    each change asks `POST titles/monitor` with `dry_run`, listing what changes, what is already set and what is
    excluded (titles without the copy); **Apply** sends the preview's values as `expect`, so a copy changed since is
    skipped as stale, and lists each outcome. The review, its button and the result count copies and the libraries
    they are in ("Would unmonitor 4 copies in 2 libraries", **Apply to 4 copies in 2 libraries**, "Unmonitored 1 copy
    in 1 library"). Both go in groups of 500 titles (the route's limit), the apply with
    the preview's groups; an apply group that fails says what the groups before it changed and that the rest was not
    changed. It only sets `monitored` on existing copies: nothing is searched or
    grabbed. The Libraries dialog's switch uses the same route for one copy.
  - **Search now** confirms (**Search N titles in their monitored libraries**: titles, never a count of searches,
    since the server may split a series into several searches), then sends one item per title and library it is monitored in to `POST wanted/search`
    (`only: "missing"`, 200 items a request, one request after another): no library is picked first, and a title
    monitored nowhere is left out and said so. They are made due like the Wanted page's Search now and start as
    `router.wanted.per_hour` allows; nothing goes around it. The bar shows the answers added up: searches queued,
    how many started now and how many wait (the last answer's), and the titles skipped by the server's reason, with
    a link to Wanted. A refused request (409 while new downloads are off) shows the server's message and stops the
    rest.
  - **Remove** is the title page's removal dialog for the selection: `POST titles/removal {title_ids, versions}`
    reads each title's removal scope (the per-title `GET titles/{id}/removal`, in groups of 500 titles in the selection's
    order; an unknown library is refused; a title with nothing to remove is skipped with the reason), and the dialog
    sums the counts ("Remove 12 titles": libraries, downloads, library files), names the first titles ("Alpha, Beta,
    Gamma and 9 more") and has the same two choices, both on. Confirming handles each title in turn: it reads the
    title's scope again for exactly the libraries its preview showed, skips it as "changed since the preview" when
    its libraries, files, downloads or whether it leaves the library differ, and otherwise runs `POST
    titles/{id}/remove` with those libraries (the same removal, debrid clean-up rules included). It shows "Removing
    3 of 12…" and ends in one notice: how many were removed, which were not and why, files left in place. Closing
    the dialog stops after the title in hand and leaves the selection as it is.
  - **Remove from a library** is the same dialog with one library picked in it (the libraries the list shows):
    titles not in it are skipped and counted; a title in no other library leaves the library, as on its page. Its
    notice names the copies removed with it because they are kept in sync ("Removed 5 titles from 4K WEB" / "Also
    removed from 4K Remux (in sync) for 4 titles.").
  - **Add to a library** picks one library of the list's kinds (Anime: movie and series libraries) and turns it on
    for each title through `PUT titles/{id}/versions/{key}` (monitored), as the add sheet does; titles of the other
    kind and titles already monitored there are skipped and counted. The library preselected is the first one that
    fits the list (anime or not) and would add a selected title; when none would, nothing is preselected ("Choose a
    library", confirm disabled) and any library can still be picked. Nothing is searched. There is no bulk profile:
    a library's profile is how a title's quality changes, so moving a title between libraries is the way.
  - **Tags…** opens **Tags for N titles**: **Add to these titles** (the default) or **Remove from these titles**,
    the chosen labels as chips ("No tags chosen" while there are none and the box holds no good label; the title
    page's dialog says "No tags" the same way) and the title page's box to add one, "Tag to remove" in remove mode, its button **Choose** there instead of **Add**
    (same suggestions, trimming, lower-casing and refusal), the line "Changes apply to every library that has these titles. Connected apps see these tags, and a
    webhook limited to a tag gets those titles' future events.", and **Add tags** / **Remove tags** (disabled until
    a label is chosen). It sends one `POST titles/tags` per 500 titles, with only those labels (docs/design/arrview.md
    "Tags per version"); no "replace all". The notice counts the titles that changed: "Added kids to 4 titles",
    "Removed kids from 2 titles", or with several labels "Updated tags on 4 titles". When a later batch fails, the lists are refreshed and the dialog says
    "Stopped after 500 of 700 titles (480 changed): <error>".
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
| System › Status | `/admin` | Needs attention, then Checks, first; the rest is folded under **Details**: Readiness (Downloads, Library links, Notifications ON/OFF, linked to their settings, and the newest complete Theatarr backup), the download and task strips, Libraries and Diagnostics. Needs attention groups failed jobs by kind with newest error, Retry newest and See all; single failures retain Retry/Dismiss. On a phone a failed job's actions (Retry, Dismiss, Open) take a row of their own under its text, in the text's column; from `sm` up they sit beside it. Held indexer definition updates, health failures (the debrid-account check names each provider and what to do: "TorBox premium ends Nov 17 (in 43 h)." with "Renew at TorBox to keep premium access."; a refused key or an account that could not be read keeps "Check the account and its API key under Settings › Download clients."), back-offs and delayed downloads; then download/task summaries, libraries and diagnostics. A download held for review (the library refused it) or whose link failed counts under need attention only, not also in progress; one the library refused and blocklisted is not counted (it left the queue: Activity, above). A delayed download names its title with episodes and library, its stage, its reason when it failed ("Failure reasons") (or whether some files are linked) and its release; Inspect opens that download in Activity (`/activity?open=<id>`). The download and task strips and the readiness switches say Loading (or the error) until their answers arrive, never 0, Unknown or OFF. The dangling-links check counts links under the libraries' roots only (other folders the report walks, `library.dangling.roots`, are not counted); its row has a "Show the N broken links" list, loaded when opened from `GET /health/dangling` (the latest report's links under the libraries' roots; `theatarr library dangling` prints every root; at most the 500 oldest, with a note when there are more): title or release folder, library, since, cause ("target missing" when no other is known), and a hint from the recorded link's state (marked missing, waiting for the repair sweep, or not a link Theatarr made). It only lists: nothing is scanned or repaired from it. |
| System › Tasks | `/admin/system/tasks` | Attention, running/queued, the scheduled tasks (interval, last run and duration, next run, Run now), and folded resolved/finished work; `?kind=` filters on the server before the limit. The "failed, resolved ones included" count is every failed job plus the failed scheduled tasks; "need attention" is the open ones plus the failed scheduled tasks, as listed under Needs attention, each counted once (the server's `attention` count already includes the tasks); with `?kind=` every count in the strip is taken from the rows of that kind. While both backlog switches (`router.wanted.missing`, `cutoff_unmet`) are off, "Search everything wanted" has a muted second line under its name: "Backlog searching is off: only requested searches, new additions, searches after a failed download and new-release retries run." (read from the stored router section). Technical job kinds and details are kept. |
| System › Backup | `/admin/system/backup` | Theatarr backups and confirmed restore; the torrent-list schedule as text (changed in Settings › Download clients › Torrents), backups, import, download and restore preview (a backup no provider could be read reads "Failed: no provider could be read" at every width, left-aligned, and has no Download, DMM or Restore: it holds no torrents; one with some providers unreadable names them, on a phone under the date); review-only settings export link; database counts (title-library entries, torrents in the registry, library links), nightly run (Loading or the error until it loads, never "none recorded") and `#backup-check`. No advanced-settings toggle: the page has no advanced field. |
| System › Logs | `/admin/system/logs` | The log viewer and its filters ("Show lines from", component, and **Filter these lines**: the filter-box word rule over the loaded lines' level, message, component and fields, in the browser, with "N of M lines"; no new request); the service's own log level is a separate card above the lines (`#log-level`) with **Apply**: it changes what the running service writes until a restart. |
| Settings › Anime mappings | `/admin/settings/anime-mappings` | The anime review queue (Waiting, Accepted, Rejected, Replaced, All; one card per item), the franchises with their mapping counts and builds, and anime series without a franchise; a franchise at `/:slug` with alternate cuts and episode numbering (docs/design/anime.md). System › Status shows an **Anime mappings** notice while items wait, with a **Review them** link to this page (it opens on the waiting items). The old Reviews hub went in 0.4.5: `/admin/reviews` and `/admin/reviews/decisions` redirect to the journal's Flagged view, `/admin/reviews/anime[/:slug]` here, `/admin/reviews/indexers` to Settings › Indexers (held definition updates are listed there, above the indexers, while any wait) and `/admin/reviews/quality` to Settings › Quality sync. |
| Torrents | `/admin/torrents` | Provider torrents from the record (torrents.md "The record"): one line per account with counts in their units (account torrents, unique hashes) and "as of" its time ("Not read yet — Refresh reads it" before the first listing; a failed read keeps showing the earlier record and says so), **Refresh** ("Reading the accounts…") says what it read per account and reloads; "not worked out" usage with its reason; usage, filters (`?q=` fills the name or hash filter), sorting, a Provider column with the RD/AD/TB tag under All providers (the strip, tabs, refusals and journal name providers the same way; a refused torrent of Delete selected is named by its selected row's name when the refusal carries none), each hash shown (its first 12 hex), on a failed torrent library links use and another account holds complete one muted line under its status that also starts the usage hover ("This copy failed. Another complete copy is on TB."; only what the record shows), the titles a used torrent belongs to linked under its usage (the first three) and all of them in the expanded row, confirmed deletes (the dialog keeps an overridden row listed with its checkbox, names "Another complete copy stays on RD." for a used duplicate, previews three references and folds the rest, offers "Tick all that have another complete copy on the same account", shows the recent choice only when a chosen torrent needs it, says how many chosen torrents the filter hides, and confirms exactly the last successful preview; torrents.md "UI") and journal (an API key reads "name (API key)", counts "1 torrent", "2,946 torrents"; a finished delete says "Backup taken" with a link to System › Backup instead of the backup's id). The provider tab, filter, text and sort are in the URL (`?provider=&group=&q=&sort=`); a name and its parsed episode list show two lines at most from 640 px, the whole as tooltip; on a phone (no hover) they wrap in full and the hash is shown whole (break anywhere), so two copies of a duplicate can be told apart; the row's second line also carries its age below 640 px and its state and progress below 768 px, where their columns hide. The torrent Journal's time and outcome head the release's cell on a phone, so the release and its detail get the row's width. On a phone the Calendar agenda's title and episode line, a series' episode names and Activity › History's titles and episode ranges (also a title's History tab) wrap in full as well; from 640 px they keep their ellipsis. A **Journal** link beside the filters jumps to the journal below the list (no paging). Each group control shows its count for the chosen account. The hash groups are **Duplicate on account** (an account holds the hash two or more times: the cleanup view, `?group=duplicate_hash`) and **On several accounts** (`?group=several_accounts`); in both a hash's copies sit together, by account and then id, the hashes in the chosen sort. **Not used by Theatarr** (the former Unused) has a line beside the filters saying it checks Theatarr's libraries and the other libraries' folders listed in Settings › Download clients › Torrents, linked; other apps on the same accounts are not seen. An account that cannot be read gets one line: the provider's tag, why in plain words (the provider asked to slow down, paused after failed requests, request limit used up, VPN not confirmed) and the next try in local time, with the raw error under Details. The old Saved inventory addresses open Torrents (an entry filtered to its hash). Backups live in System › Backup ("Back up now" says "Torrent backup queued" with a link to System › Tasks; the restore preview groups the skips of an unavailable account into one line with the count and the reason in plain words). |
| Settings | `/admin/settings` | Search and task pages below. |
| Users & access | `/admin/users` | Users, roles, permissions, sessions and API/view keys. **Give someone access**: the sign-in link to copy, what a first Plex sign-in gets (waits for approval, or is let in at once, as a User) and what each role does; no invite system. Your own role cannot be changed (the select is locked; `PATCH users/{id}` answers 400). |
| Advanced | `/admin/advanced` | Exactly YAML settings, Settings history and Request limits. The old `/admin/advanced/arr` pages redirect to Admin › Import from Sonarr/Radarr (`/admin/arr-import`). |

One admin-only screen lives outside `/admin`, beside the page it belongs with:

| Screen | Path | Holds |
|---|---|---|
| Wanted › Manual import | `/wanted/manual-import` | Existing file linking workflow, as the third tab of Wanted. |
| System › Decision journal | `/admin/system/journal[/:id]` | Decisions and their evidence (admin); **Flagged** (`?suspect=1`) lists the entries with a stored warn or error flag; the "Title or id" box matches part of a title's name, and a typed number also as the id ("2001" finds title 2001 and "2001: A Space Odyssey"; Activity › History's box does the same), while a title's **Open in the journal** link passes `?title_id=` (one title exactly, "Only <title>" with **Show every title**); they are not under Status's Needs attention and have no sidebar count (nothing asks for an action). No verdicts. `/activity/journal[/:id]` redirects here. |

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
| Quality sync | `/admin/settings/quality-sync` | Sources, preview (a dry run), run, link (a source that cannot be read says "Could not preview", "Could not run sync" or "Could not link formats" with "<source> could not be read"), update log and recent runs (a logged run that changed nothing reads "No profile or format changes"). How sync works is folded. |
| Indexers | `/admin/settings/indexers` | Indexers · VPN · Catalog · Search & RSS (Automatic search, stored as `router.wanted.*`, then RSS feed checks; `/rss`). **Search for upgrades** names its stored values `quality`, `score`, `off` as "Files below the preferred quality", "Files below the preferred quality or score", "Off (RSS still picks up better releases)". |
| Download clients | `/admin/settings/downloads` | Debrid providers (with Provider account sync above their status; a status card's Pacing counts adds in the current minute and uncached adds in the current hour, 0 once that minute or hour is over, "1 add"; each provider's editor shows **Also add torrents here** before its advanced options, torrents.md "Add to other providers") · Other clients · Behaviour (what to grab, cache checks, retries) · Torrents (torrent-list backups schedule, other libraries' folders; `/torrents`) |
| Import lists | `/admin/settings/import-lists` | TMDb lists, TMDb discover rules, JSON lists and Radarr or Sonarr instances, and the list exclusions (below, Collections). Managers open it too; it has its own screen, not settings panels. |
| Notifications | `/admin/settings/notifications` | Where and when Theatarr sends messages. |
| Plex & playback | `/admin/settings/plex` | Plex connections, playback and playlists; read-only Playlists Theatarr keeps below the playlists panel, folded with its count ("90 playlists, 8 empty"). |
| Metadata & artwork | `/admin/settings/metadata` | Metadata services, refresh schedules and the artwork cache. |
| Connected apps | `/admin/settings/apps` | Compatible endpoints for Seerr and other apps: each app's libraries, profiles and API key (an app without its own key reads "uses admin API keys" beside its path). The editor offers a Radarr app the movie libraries and a Sonarr app the series ones (a library already ticked stays listed so it can be unticked), and each profile the libraries the app serves; unticking a library drops it from every profile, and a profile left without a library says "Tick a library for this profile, or remove it." and holds Save. Replace a library (an existing app only) puts a new library in an old one's place in the app's libraries and every profile, in the draft; Save sends both. A new app has one key field, always open and labelled "API key (required)" (write-only; no "not set" badge and no Cancel of its own), and Save with it blank is refused; editing an app keeps the stored-key field (Replace, Clear, Keep the stored one). |

Other libraries' folders (`#scan-roots`) and the torrent-list backup schedule (`#torrent-backups`) are on Download
clients › Torrents; Torrents says beside its filters what "Not used by Theatarr" checks and links the folders, and
System › Backup shows the schedule as text with **Change the schedule**. `/admin/settings/general` redirects by anchor.

Libraries includes the existing library editor (`?edit=key`); its table names each library's profile as the
editor's picker does (the key as tooltip) and known debrid providers by their tags (RD › AD › TB); app bindings are on Connected apps. Profiles keeps `?raw=1`, presets,
and the hidden `/admin/settings/profiles/format_sets` editor. Its **Check a release** tab
(`/admin/settings/profiles/check`) judges a pasted release name for one title and library through `POST /decide`,
read-only ([decide.md](decide.md#release-check)). A sole title match is chosen once the search for the text as
typed has answered (once per text; several matches wait for a choice), and the result names the library by its label
(the key as tooltip) and the profile in words, as the library editor's picker shows it (the key as tooltip); clearing
the release name clears the result. Its optional size is in GiB, as sizes are shown, or carries a
unit as in the settings forms (`36 GB` is 36×10⁹ bytes); text that is not a size is refused beside the field and
nothing is checked. Indexers and debrid providers show status beside
their editors, with Edit links. VPN status is above its settings. **Send notifications** is the first field of the
Notifications page's Destinations panel (`notify.enabled`; "Off: events are recorded, nothing is sent.").

**Pause.** Admins see one **Pause** switch in the top bar of every page; it sends `{generation, paused}` in one
`PUT /settings/switches`. Off, its tooltip says "Pause: searches, new grabs and imports stop for every library; Interactive search can still look up releases. List syncs stop too. Downloads already started finish at the provider and are imported when you turn Pause off."; on, "Paused:
searches, new grabs and imports stop for every library; Interactive search can still look up releases. Turn off to resume." Every Pause notice uses that one wording (the pages' notice "Paused: searches, new grabs and imports stop for every library; Interactive search can still look up releases." or "Paused by the
environment (THEATARR_FORCE_OFF): …", the reason on disabled search buttons, a library row's missing line, the unblock
box, `theatarr router status` and preflight; a refused search's toast gives it without library names, as it holds for every library: "Nothing searched: Paused: …"): Interactive search still looks releases up while paused, on purpose; its Grab and
Try again are off then, and one line in the window says why ("Downloads are paused. You can still look up releases.", true
for every account, or "Paused by the environment (THEATARR_FORCE_OFF): no grabs."). Turning it off asks for confirmation; turning it on
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
names are the same as before. Kind reads "Movies" and "Series", Anime "Not anime", "Anime only" and "Any", and the
profile picker names the stored profiles in words as the add sheet does ("Radarr 4K zurg" for `radarr-4k-zurg`); the
stored values are the same. A movie library's "Which releases" also has **Minimum availability** (Not set,
Announced, In cinemas, Released; `minimum_availability`, for movies with neither a choice of their own nor an
imported value) and **Availability delay (days)** (`availability_delay`, whole days, negative for earlier, empty
for none). Like the name, each is left out of the request while unset and sent empty or 0 only to clear a stored
value, so a save that does not touch them stores the library unchanged; a library changed to series drops both.

**Keep in sync.** The library editor's "Keep in sync" ticks the other libraries this one is kept in sync with: a
title that gains a copy in one library of a group gains one in each other library of the group that fits it, unless
the title has a copy there already: an unmonitored one stays unmonitored, and a removed one comes back (restored and
monitored like the named copy, keeping its own saved minimum availability, which the add's choice does not
overwrite: owner, 2026-11-17, "if they are supposed to be in sync then they should stay in
sync"). Known limit: a copy whose removal cleared its per-library minimum availability (removing the whole title, or
removing it through a Radarr client) comes back with its library's default. The one exception is collections and import lists (`catalog.Input.AddMethod` list or collection) and the
intake's missing-library fill and a compatible Refresh whose anime check now answers (it adds missing copies only),
which never restore a removed copy; every add a person makes (the add sheet, the library switch and bulk add, requests
and their approval, the compatible API, Bring in sync) restores it (`restore` on `catalog.SyncPartners` and
`TurnOnSynced`). Monitoring an existing copy
again brings nothing. The new copy takes the add's monitored flag and, for a series, its seasons, preset and monitor-new
choice; when the add names no seasons (the library switch, bulk add, an add or restore without season choices, a compatible profile edit or mass edit without seasons) and
the named library the partner is in sync with had its copy before (never another library the add also names), the
partner takes that copy's season, episode and monitor-new choices, as Bring in sync
does (`catalog.PartnerSeasonIntent`: `CopySeasonIntent` with the episode rows filled in), so it does not monitor every
season at the next refresh; a partner created beside a brand-new copy follows the add, and nothing is copied onto a
partner copy that already exists; quality, profile, naming and providers are each library's own, and monitoring and files stay each
copy's own afterwards (files are not copied: the copy is wanted and searched by its own library's rules). The rule
holds for every route that gives a title a copy (`catalog.TurnOnSynced`, beside `catalog.TurnOn`): the add sheet and
`POST titles`, request approval, the title page's library switch and bulk add (`PUT titles/{id}/versions/{key}`),
collections and import lists (searched on add like the library they name), and the compatible API's add, edits,
mass editor and anime reclassify. The one-time import from an arr adds to its one library only. A group holds two or
more libraries of one kind and one anime rule (unset, anime only and not anime are three rules), a library is in at
most one group; the editor offers only those, lists the same-kind libraries it does not offer with the reason (their
anime setting, or the group they are in), and saves the group with the library (`sync_with`). Each library's row on
Settings › Libraries says "In sync with …". The part's help: "A title added to one of these libraries is added to the others too; a copy removed there comes back when a person adds the title, not from lists or collections. Monitoring, removals and files stay each library's own. Only libraries of the same kind and anime setting can be kept in sync." Stored as `library.sync_groups` (lists of library keys) in the library
settings section, which its form leaves as stored; a library that is deleted, or whose kind or anime setting no
longer matches its group, leaves the group in the same save (also on a YAML import), and a group left with one
library goes. Saving a group adds nothing to titles already in the library. While the draft's kind or anime setting
excludes a saved partner, the part says "Saving this takes 4K Remux out of its sync group with 4K WEB." (not undoable
on its own: undoing the kind or anime change ends it); Keep in sync compares the draft with the saved partners that
still qualify, so Undo never puts an excluded partner back and the save never sends it, and Bring in sync is hidden.
The Kind and Anime fields' Undo restores the rule with what it took: the saved partners come back, partners picked under
the undone rule go, and a movie library's minimum availability and delay are the saved ones again, so an Undo with
nothing else changed leaves no unsaved change. Picking the saved kind or anime setting again from its menu does the
same as the Undo (end check 2026-11-18).
The save's answer names every partner the library was in sync with before the save and is not after it, whatever
the cause (`sync_dropped`), and the result reads "4K Remux saved. It is
no longer in sync with 4K WEB." (UI sweep 5, S3.)

A removal follows the group too (owner, 2026-11-17: "removing the title from one library of a sync group also should
remove it from the other"): for each library a removal names that holds a live copy of the title, the other libraries
of its group that hold a live copy go with it, monitored or not, fit not checked (`catalog.SyncRemovals`, beside
`SyncPartners`); a named library with no live copy (Delete files… on the files a removed library kept) brings none.
Both removal routes use it: the web scope (`GET titles/{id}/removal`, also the selection bar's bulk scope) lists the
partners among `copies`, each with `sync_of` (the named library) and its own file and download counts, before it says
whether the title goes (`title`: then the Exclude box is offered and the action is Remove title); the bad-key check and
the leftover copies stay on the libraries named, so the files a partner kept from an earlier removal are not taken.
`POST titles/{id}/remove` removes the scope's copies in one removal with the same choices (files, debrid torrents,
exclusion), never turning a named removal into one of every copy; the selection bar posts the person's own choice
and the server expands again. A file the pair shares goes to the trash once; one a third current library outside the
group uses stays. Each library gets its Removed action as before; a partner's says "in sync with <library>" in History
(`journal.Removal.Sync`). The dialogs list a partner like a named library and add "<Partner> is kept in sync with
<Library> and is removed too", followed by ", with its files and debrid torrents" (or ", with its files") when the boxes take
them; the bulk dialog says it once per pair "for N titles", and a title the removal leaves with no copy "has no copy
left". No total of deleted files is promised (a file two libraries share is in both): the title dialog lists each
library, named or partner, with its own file count (dead links included, they go too) and downloads in progress
beside its name, and says which libraries' files are deleted or stay; the bulk dialog adds "By library: …" with each
library's files and downloads in progress summed over the selection; Delete files… on kept files names each removed
library with its own count, and the title page's "N files kept" line counts a shared file once. The selection bar posts
the person's own choice (one library, or none for the whole title). A compatible DELETE whose libraries in sync take
the title's last copies drops every app's record of the title (as the web removal does), so a re-add starts fresh. A compatible client's DELETE does the same (arrview.md
"DELETE"). Unmonitoring, unlinking one file and Stop managing a library are not removals and do not follow the group.
The Keep in sync help says: "A title added to one of these libraries is added to the others too, and a title removed
from one is removed from the others, with its files and debrid torrents when the removal deletes them. Adding a
title by hand brings it back to a library it was removed from, while lists and collections leave it removed.
Monitoring stays each library's own. Only libraries of the same kind and anime setting can be kept in sync." Settings ›
Connected apps adds under an app whose libraries have partners outside it: "A delete from this app also removes the
title from <partners> (in sync); a delete with its files takes their files and debrid torrents too.", and beside the
libraries' own addresses (/v/<library>/…) one line per group: "<A> and <B> are kept in sync: a delete through the
address of one also removes the title from the other; a delete with its files takes their files and debrid torrents
too."

Which libraries in sync would gain a copy is one read-only answer, `catalog.SyncPartners` (also used by
`TurnOnSynced`): each add option carries it as `synced` (for that library ticked alone, with the anime reading the
add will use: the stored flag and series type for a title with a live copy, else the sheet's anime choice, which
the add writes first), `PUT
titles/{id}/versions/{key}` returns the keys it added as `synced` (as `POST titles` does), and a request stores it
(users.md). The add sheet and "Add libraries" show a partner of a ticked library ticked and disabled, "in sync with
<library>" (a partner whose copy was removed, `removed` on its option: "in sync with <library>; it was removed and
comes back"), and send only what was ticked (the server adds the partners); a partner that would not gain a copy
(held, monitored or not) stays an ordinary box, and unticking the named library frees it. The add's toast names
the libraries the person chose first, a removed copy brought back included, then the partners: "Turned on: 4K WEB.
4K Remux too (in sync)." The request sheet's
"You are asking for" lists the partners the request stores beside the asked-for libraries, each "in sync with
<library>". Bulk add says once, whenever the chosen library is in a saved group (add options carry `sync_groups`),
"Also added to <partners> where a title has no copy there or was removed from it." and promises only that files stay, and its toast counts the copies added in sync; the title
page's library switch toasts "Also added to <libraries> (in sync)". Collections, import lists and Settings › Connected
apps say under a chosen or shown library that is in a group "Titles added to <library> are also added to <partners>
(in sync)." naming the partners not chosen there too (their pages carry `sync_groups`).

**Bring in sync.** A library's Keep in sync part, for a saved group without unsaved changes (and a draft whose kind
and anime setting still match it), says "N titles would
gain a copy in this group" (hidden at 0) with **Bring in sync…** (`GET` and `POST
settings/versions/{key}/bring-in-sync`, admin). N counts the titles with a copy in a library of the group that would
gain copies in the others by the add rule (`catalog.SyncGains`, as if a person added the title's source copy now: a
removed copy comes back, an existing unmonitored copy is left as it is). The dialog names per library the count and the first five titles,
each with its year ("Scream (1996)"; "and 495 more") and "Each new copy is monitored like the title's first monitored copy in the group, or its first
copy."; a series' new copy takes that copy's season and episode monitoring and monitor-new choice
(`catalog.CopySeasonIntent` with its episode rows filled in; the compatible API's anime reclassify uses the same helper
without filling, so there the episodes follow the intent at the next refresh), and a movie's its explicit minimum
availability unless the library has its own (`catalog.ChoiceKeys`); a restored movie copy keeps its own saved choice.
Each library's line reads "<Library> gains N titles, M of them were removed from it before and come back: …" (`restored`
per library), and the dialog keeps "Existing copies are left as they are". The action runs in one transaction
(`catalog.BringInSync`). "Search for the new copies" is off by default ("No search queued by this action"), and disabled with the
Pause words while Pause is on; ticked,
the new copies of a monitored source go on the paced search schedule (`RequestSearchLater`, the hourly budget,
cancellable on Wanted), never an immediate search. A toast gives the per-library copy counts.

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

`/requests/<id>` shows Requested → Awaiting approval → Getting ready → Available, or the declined/canceled branch, with recorded creation/decision/status times, decision actor and note; self-approval skips the approval wait. A failed request's Getting ready step reads **Did not work** ("This did not work. An admin can look at it."), before any paused-downloads words; each copy's progress stays under it. A user who joined another member's request sees "Someone else asked first; you joined" instead of a name (the server sends no other requester, docs/design/users.md). Getting ready uses Activity’s provider-free stage words for concurrent downloads, requested-season episode availability, or an honest waiting explanation; missing historical transition times and ETAs are left unknown (request events are streamed, not persisted). The phone layout stacks facts and timeline; desktop places them side by side.

Managers keep inline queue Approve/Decline and see what approval does per requested version in the detail and editor: “Approval records this request; automatic downloads are paused” while Pause is on or the library is turned off. Before submission, the sheet applies account copy defaults without version choices and says whether this account needs approval; detail visibility stays own/joined requests for users and all requests for managers, with no file, size, quality-source, provider or path fields in the progress projection.

1. A user finds a title (library or TMDb); Theatarr applies the account’s request versions (by default every version that
   fits the title) without asking them to pick copies. The sheet says whether approval is needed; for a series they
   choose seasons (for a series not in the library, its own seasons, the add sheet's **Choose seasons** source,
   without episode counts; none listed when they cannot be read, and the button then reads **Request all seasons**),
   and may add a note. The sheet's **In library** badge follows the title's current copies (`in_library` of
   `GET /add/options`): a title whose every copy was removed shows none, and counts as not in the library for the
   seasons too; the request still names it by its title id.
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
other application paths retain SPA fallback. The admin screens and other rarely used pages load on first use
(`lazyPage` in `web/src/router.tsx`), so a tab opened before an update asks for hashed files that are gone: the
first failed load reloads the page once at the address being opened, which fetches the current build (a
sessionStorage guard stops a second reload for the same address within 10 seconds). When that did not help, or a
screen throws while rendering, every root route shows Theatarr's own error screen ("This page could not be
loaded.", the reason, and Reload; `web/src/components/route-error.tsx`) instead of the router's default page; it
does not need the signed-in shell.

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
| `status`, `network` | TMDb status (a series without one: its series metadata's, TVDB, as the Series status filter reads it); TV first network |
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
- **Fallback:** no art yet, or TMDb fails: a generated SVG poster (title and year on a gradient from the title id; a
  backdrop is the gradient alone, so no text crosses the hero's),
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
| `GET titles?…&sort=added|created|name|year|release|rating|runtime|certification|network|size|completion|next|quality|activity&order=asc|desc&genre=&quality=&provider=&year_from=&year_to=&anime=&series_status=continuing|ended&tag=` | grids; items gain `poster`, `backdrop` (image URLs), `overview` (short), `rating`, `certification`, `runtime`, `genres`, `added_at` (newest current file), `title_added_at`, `status`, `original_language`, the movie release dates, `series_type`, `season_count`, `previous_airing`, and per version `quality`, `size`, `providers` (cache codes), `profile`, `minimum_availability`, `folder` (see **Library views**) |
| `GET titles/facets?kind=` | counts per version key and state, per quality, per genre, per Series status (not for movies) and (see files) per tag label and per provider cache code, for the filter sheet |
| `GET titles/{id}` | includes `meta` (§5), `art`, and per version `history` (grabs, imports, upgrades, failures with `reason`, searches with `results`, `accepted`, `summary`, `latest`; every event's `trigger`, Theatarr's `provider`), `providers` (keys) and `cached_on` (cache codes) of the providers that hold the file now, `current.formats` (a movie file's matched formats with scores; none for an adopted file) and `current.score` (shown only with scoring evidence: stored formats, an empty list too, since an import keeps `[]` when no format matched (`decide.ExistingFile.Formats` is `omitzero`) or a non-zero score; a real 0 reads "0", while an import stored before the empty list was kept shows the dash), and `removed` for a copy removed from the library (counted in none of the title's rollups); fields for files and hashes only with "see files" |
| `GET titles/{id}/episodes?season=` | seasons with episode lists and a state per version per episode (`episode_version_state`, `link_episode`); a removed copy has no column; cells carry `release_title`, `release_group` and `cached_on` (see files) |
| `GET /img/t/{id}/{kind}/{size}`, `GET /img/tmdb/{size}/{file}` | image proxy (§5) |
| `POST titles/{id}/refresh` | refresh metadata and art now |
| `GET discover` | Home rows in one call: `continue`, `recent`, `missing`, `upcoming`, `requests`, each ≤ 20 items with `more` links |
| `GET calendar?from=&to=` | releases and episodes in a window (shares code with the arr view's `calendar`); each entry says `monitored` (an episode: a library of the title that was not removed monitors the copy and the episode, as Home's Missing row and the episode grid read it; the episode's cached flag only when it has no library row; the monitored-only calendar lists exactly those), `available` (a movie: the title's status word is Available or Partly available, `partial` for the latter), `acquiring` (a download in hand for the movie, or for that very episode; one read for the page) and `stage` (the furthest stage in hand, see files only) |
| `GET activity/continue?limit=` | Home's Getting ready row: titles with a download in hand, each with its library's `version` key and `label` (numbered, never a key, for accounts without See files) |
| `GET search?q=&kind=&offset=` | `{library: [...], library_total, tmdb: [...]}`; the kind applies before the limit, `offset` pages the library part; TMDb results through the gateway and `tmdb` budget, cached 1 day, marked `in_library` and `requested` |
| `POST titles` | add `{kind, tmdb_id, tvdb_id?, versions[], seasons?, monitored, search}`, or `{title_id, versions[], …}` for a title already in the library (also one known only by its TVDB id); runs `catalog.Add` (§3); 201 with the title; `versions` and `new` include `synced`, the libraries added because they are kept in sync with one named |
| `PUT titles/{id}/versions/{key}` | turn a version on/off, monitor; one created or restored brings the libraries kept in sync with it (§2 "Keep in sync"), listed as `synced` |
| `POST titles/{id}/search` | search now for versions (records a command like the arr views' `MoviesSearch`) |
| `GET activity?state=now|history|blocked&title_id=&title=&version=&offset=&user=` | download rows with plain-words state, progress, provider, attempts (admin), library import results and (admin) `formats`; history rows (journal History, `history.go`) with `event` and `words`, and (admin) `episodes`, `files`, the row's `release`, `quality`, `score` (absent when unknown, never 0), `indexer`, `size`, `formats`, `reason` and a deleted file's `replaced_by`; `events=` filters them (the copy events only for callers who see files); blocked rows filtered by part of the title's name (`title`) and library (`version`), `total` the filtered count, each with the blocked download's `quality` and `indexer` when known; from `download`, `download_attempt`, `library_import`, `router_plan`, `acquire_run`, `router_blocklist` (moved out of `arrview/queue.go` into a shared reader) |
| `GET titles/{id}/releases?version=&live=&season=&run=` | Interactive search (`live=1`) and the stored "Last search" (decision engine results with reasons; manager); without `live`, `season` keeps the stored answer to that season's own search (target `sNN`) |
| `GET titles/{id}/release-outcomes?version=&episode=` | What came of each release of a library (or one episode) now, by info hash: the release lists' outcomes read again, with an in-flight download's state and words and whether a failed one can be grabbed again (manager) |
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
languages and release group (the themed selects). A failed listing or import shows its error in a red alert box above the form. A
hash Theatarr has not seen is pointed at the title's Last search, where Add by magnet or hash is. An empty listing
says why, told apart by the native GET (the arr API keeps its empty list): "No download with this hash in this
library" (404, with that pointer), "Its files cannot be listed now: <the planner's wait, or its error without
the library's prefix>" (409: a title hold by its message, "the release folder is not on the mount yet" only when the
plan says so, a mount mismatch or an unreadable file), "Nothing to import: <the hold or rejection reason>" (409, e.g. a
folder with only an .nfo), "The title was removed from this library: add it there again first" (409, checked before
the plan), "The folder cannot be read:
<why>" (422), and for a readable folder without video files "No video files in this folder"; a listing says "N
candidate files". Changing Library or Source clears the result, the error and the candidates, and each source keeps its own typed value;
every new listing clears the old candidates first, so a failed one leaves none to import. Review choices refreshes
the decisions before Import selected files; errors receive
focus and results use a live region. The admin/CSRF-protected native `/manualimport`, `/manualimport/options` and
`/manualimport/execute` endpoints reuse the arr handlers because the SPA has a session rather than an arr API key;
imports retain the library gate and create symlinks while preserving mount files.
