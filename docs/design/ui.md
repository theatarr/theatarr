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
  phones included) and the live status dot (VPN and failed jobs), whose words always show when it is bad ("VPN down").
- **Look**: tokens in
  `web/src/styles/globals.css` (ink on warm paper, warm charcoal in dark, one Prussian-blue accent, ok/held/broken
  colours, 40/32 px rows), with the shadcn names kept as aliases. Fonts are self-hosted
  @fontsource packages, nothing loads from another origin: IBM Plex Sans for the interface, IBM Plex Mono for keys,
  release names, paths and numbers, Newsreader for page and title names (one token, `--f-display`, so headings can
  switch to Plex Sans). The primitives (button, input, select, switch, checkbox, chip, tag, table, dialog, sheet,
  tabs, toast, skeleton, empty state) live in `web/src/components/ui`.
- **Phone:** a bottom tab bar: Home, Browse (a sheet with Movies, Series, Anime, Collections for managers), Search, Requests (with its badge),
  More (a sheet with Activity, Wanted (managers and admins; Manual import admins only), Now playing, Calendar and the admin pages the account may open).
- **Sidebar** (`web/src/components/shell/nav.ts`): Home, Library (Movies,
  Series, Anime, Collections for managers and admins: `/collections`, docs/design/arrview.md), Requests, Activity, Wanted (managers and admins; Manual import admins only), Now playing, Calendar; then **Admin**: System, Reviews, Torrents,
  Settings, Users & access, Advanced. **Setup** appears first only when `/setup` reports `first_run` or
  nonempty `missing`. Successful section, provider, indexer and library saves refresh this query, including metadata keys and mount changes. A Setup checklist link remains on Status and the Settings home footer.
  System, Settings and Advanced show children while their pages are open; System includes `/admin`
  and `/admin/system/*`. The phone More sheet uses the same groups. System's badge sums new errors excluding source=job
  and attention jobs; Tasks and Errors retain their own badges. Reviews uses an accent badge for all
  waiting decisions. Users and managers never see Admin; users-only accounts see Users & access,
  and `/admin` takes them there. Sign-in still lands on Home.
- Library keys get one fixed colour each (the dots on pills, tags, the title page, filters); bars and pills take the
  status colour.

### Library state

Every page that shows a library (title copies, the add drawer, Setup, Libraries settings and its tables, policy,
Status, Requests, Wanted) shows the same library state (`components/version-status.tsx` over `lib/version-grab.ts`):

- the **mode marker**: **Watch only** (mode shadow: Theatarr searches, downloads and links nothing for this library)
  or **Live** (mode active: searches, downloads and links);
- one **verdict**, decided by the server's `will_grab`: **Downloads on** when it grabs; otherwise **Watch only**,
  **Paused** (Pause on, or forced by `THEATARR_FORCE_OFF`) or **Downloads off** (a server hold, or a library not set
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
**Watch only** / **Live**, **Pause**, **Send notifications** and, in the add drawer, **Add to these libraries**. The add drawer shows one row per library: a checkbox, then the name, key and
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
the API answers 400 to `monitor` there, naming the library title, and writes nothing. **Search now** is off unless
this browser last left it on (`localStorage` `theatarr.add.search`, kept only while on).

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
  day. It is still wanted; only the word changes, and Wanted's counts do not. `home_release` is per title: a
  copy an arr feeds, which reads the arr's own dates, can differ.
- **Family view**: accounts without See files get the table's Status column headed **Availability** (Available,
  Some episodes available, Not available, Upcoming, Getting ready) and are not offered the Libraries, Quality,
  Quality profile, Size, Latest file, Minimum availability, Provider or Folder columns; posters show the badge only,
  without bars or library pills; on a series' Episodes tab each season shows one combined summary. Accounts that
  see files keep the per-library detail. A missing title with a download in hand (the row's `acquiring`: a copy's acquisition that
  is not blocked and not an upgrade; the title page's `acquiring` too) reads **Getting ready** there, on posters and
  Home rows and in the title page's readiness chip, before Upcoming; a failed download is not in
  hand, so it stays Not available;
  accounts that see files keep their stage chips.
- **Title page**: Newsreader title, the readiness chip, small-caps fact rows; the action bar and the **Libraries**
  table sit under the hero. A Watch-only library says **Watch only** once (its tag); the row does not repeat the
  verdict. Pause is said once for the page, under the action bar, and the Episodes tab leaves it to that notice.
- **Activity**: for accounts that see files, Queue, History and Blocklist are tables (shared `DataTable`) whose rows
  open into their details in place; household accounts and "View as a user" keep the simple rows. **Downloading**
  counts only provider transfers in progress; starting (the provider add) and linking count under **Waiting**, the
  filter their library stage links open; failures count under **Needs attention**. On phones the tables become
  cards and the History filters stack one per line with the date input full width.
- **Decision journal**: on phones, where the kind and library columns hide, each entry shows its kind and library
  inline under the title.
- **Colours**: the library key colours (`--v-*`) have light and dark values, each at least 3:1 on the page and the
  surface; Theatarr's own copies (the `-theatarr` keys) have their own rather than the
  accent. Calendar entries are surface chips with a 3 px facet-coloured edge. Labels over artwork (poster chips,
  hover shade, Continue watching) sit on `--art-scrim`, which is dark in both themes. Modal backdrops use `--scrim`.
- Wanted, Collections, Requests and Home use the shared Card, Chip, Tag, Checkbox and Select primitives.

### Other pages

The remaining pages use the same tokens and primitives:

- **Search** (page and overlay): a Design A search field, underline kind tabs, ruled result rows with Button icon
  actions, and the kind as a square chip with its facet mark (`FacetPill`, also in the add and request drawers). On
  TMDb posters only the kind chip sits on the art scrim; In library and Requested sit under the year, since a status
  chip beside the kind does not fit a phone-width poster.
- **Reviews**: the waiting cards' Review is a Button, the empty inbox the shared empty state, and flagged decisions one
  ruled list with each library's key tag.
- **Login**: a ruled card, a Newsreader heading, Sign in with Plex as the primary button (no Plex orange), and the
  error and pending notices as edge-marked notes.
- **Users & access**: the role select, Discord id input and switches are the primitives; the "Requests ask for"
  libraries are square toggle chips.
- **Switches**: the monitor switch (library rows, title page, episode drawer, collections) and the add drawer's
  Search now use the Switch primitive (size `sm` for dense rows); a locked monitor switch stays focusable
  (`aria-disabled`) with its reason as the title. Search now's whole row is its label.
- The confirmation dialog, filter sheet and dropdown menu use the Button, Checkbox, Select and token shadows; the
  components use the short tokens.
- Sheets and dialogs (bulk monitor, copy, view options, episode drawer, upgrade why, add and request drawers) use
  the 8px panel radius and the shadow token; a schema boolean with a default (On / Off / Default) stays a three-state
  outline button rather than a Switch.
- Keyboard focus: `outline-none` sets Tailwind v4's outline style to none, so a primitive that removes the browser
  outline also sets `focus-visible:outline-solid` with its 2px focus outline (Button, Switch, Checkbox, tab triggers and
  the focusable tab panel, search fields).
- At 400 px no page scrolls sideways. Tables with long keys wrap them (`overflow-wrap:anywhere`; `break-words` does
  not shrink a table cell); the indexer rows' phone grid drops the cell height. Request budgets (settings and Request
  limits) and gateway back-offs stack below sm as labelled lines (Rate, Caps, This hour, Today, Used by, Failures,
  Reason), so no value is hidden on a phone; from sm up they are table columns, Used by included. Home's poster rows, the anime YAML
  preview, the franchise episode map still scroll inside their own frame. A journal entry's summary wraps long release names.
- Cancel closes a sheet or dialog; Discard resets a form's draft to saved (both sit in the record forms).

### Navigation

| Menu path | Route |
|---|---|
| Home | `/` |
| Library › Movies / Series / Anime | `/movies`, `/series`, `/anime` |
| Library › Collections / Lists (managers, admins) | `/collections`, `/collections/lists` |
| Requests | `/requests` |
| Activity › Queue / History / Blocklist / Decision journal (admin) | `/activity`, `/activity/history`, `/activity/blocklist`, `/activity/journal` (entry `/:id`) |
| Wanted › Missing / Cutoff unmet (managers and admins) / Manual import (admin) | `/wanted`, `/wanted/cutoff`, `/wanted/manual-import` |
| Now playing / Calendar | `/now-playing`, `/calendar` |
| Admin › Setup (while unfinished) | `/admin/setup` |
| Admin › System › Status / Tasks / Backup / Errors / Logs | `/admin`, `/admin/system/tasks`, `/admin/system/backup`, `/admin/system/errors`, `/admin/system/logs` |
| Admin › Reviews / Torrents | `/admin/reviews`, `/admin/torrents` |
| Admin › Settings | `/admin/settings` (destinations below) |
| Admin › Users & access | `/admin/users` |
| Admin › Advanced | `/admin/advanced` (migration, raw configuration, history and limits) |

Phone tabs are Home, Browse, Search, Requests and More. More shows System, Settings, Users & access and Advanced
as headings with vertical lists, plus Setup (when needed), Reviews and Torrents. Counts retain the desktop badge
sources. Below 640 px route tabs become a **Section** select. Settings authority banners collapse to a single-line
summary with **Details** on phones; source and fallback information remain available.

Setup is a six-step readiness checklist with a read-only activation review. The first-library form also offers the
one-step start (`POST /setup/start`). The shared `useSetupProgress` hook
powers Setup, admin-only Home and Settings reminders and sidebar Setup visibility (`first_run || missing.length > 0`).
See [quickstart](../quickstart.md#readiness-led-setup) for the completion rules; only the one-step start enables a global switch, behind its confirmation.

### Library app (everyone)

| Screen | Path | What | From |
|---|---|---|---|
| Home | `/` | rows: **Getting ready** (in progress: downloads and imports running now; API route `GET activity/continue`), **Recently added** (newest current files), **Missing** (wanted versions, released), **Upcoming** (digital/physical release dates and episode air dates in the next 30 days), **My requests** (users) / **Pending requests** (managers: each poster opens that request in the queue). A family account (no `see_files`, no `manage_requests`) gets Getting ready, My requests, **Recently available** and Upcoming only: Missing and Below cutoff are copy shortfalls, and `GET discover` leaves them empty for it. Each row scrolls sideways and has "See all". For admins the heading line reads "N titles across M libraries" (`GET status`: `counts.title` and the `versions` list; `counts.version` counts each title's place in each library) | `/titles`, `/versions`, `title_release_date`, `download`, `library_import` |
| Movies / Series / Anime | `/movies`, `/series`, `/anime` | poster grid (every poster card, here, on Home and in search, has the title on two lines at most and the year under it, and one restrained availability badge; the overview on hover); filters in a sheet (**Filter**) with the active ones as removable chips: availability, monitored, library (has / lacks each key; accounts with See files only, a family account filters by availability), quality, provider, year, genre, and the title search in the toolbar; sort: title, added, latest file, release date, year, rating, runtime, size, completion, next episode / release, quality, latest activity, network / studio, certification; posters or table (posters by default, the choice kept per list); each library shown as a status bar or pill (see **Library views** below) | `/titles` |
| Title | `/title/{id}` | see **Title page** below | `/titles/{id}` + new |
| Search | overlay, `/search?q=` | two sections: **In your library** (instant, local) and **Add from TMDb** (movies and TV, with "In library" / "Requested" marks). Add opens a sheet: versions to turn on (pre-ticked from the policy defaults), seasons for series, monitor, search now. An account without `add` sees **Request from TMDb** and Request buttons instead; one with neither `add` nor `request` sees **On TMDb** and no add or request control. A library result shows its library keys only to accounts with See files; others get the title's one availability word (`titleWatchWords`: Available in 4K, Some episodes available, Getting ready, Upcoming, Not available), as the table does. With no TMDb key the TMDb section says so; admins get **Set the TMDb key** (Settings › Metadata & artwork, metadata services), others are told an administrator can add one | new |
| Calendar | `/calendar` | upcoming releases and episodes by week (arr views already serve `calendar`) | new |
| Collections (managers, admins) | `/collections`, `/collections/lists` | Two tabs. **Collections:** TMDb movie collections, monitored first: poster, movies in the library of the total, monitored switch, target libraries (only Live movie libraries; monitoring needs one), search on add, minimum availability; a name filter and **Monitored only**. **Add collection** searches TMDb for a collection and adds it with its libraries (Search on add is ticked by default). Changes save at once; a monitored collection adds its missing movies when it is added, with **Add missing movies now** and at each list refresh, and its searches go through the router. The arrs' monitored collections arrive with each Radarr import (docs/design/arrview.md, Collections). **Lists:** TMDb movie lists (no other provider, no series lists): **Add list** by id or address, with libraries, what to monitor, minimum availability, Search on add and Enabled; **Sync now**, Edit and Delete per list. Lists made through the Radarr-compatible API show here with their view (docs/design/arrview.md) | `GET collections`, `POST collections`, `GET collections/search`, `PUT collections/{id}`, `POST collections/{id}/refresh`; `GET`/`POST lists`, `GET lists/lookup`, `PUT`/`DELETE lists/{id}`, `POST lists/{id}/sync` |
| Activity | `/activity` | **Queue:** for accounts that see files, a table (phone: cards): **Status** and **Title** (episodes under the name) always, then the chosen columns (**Columns**: Library, Quality, Provider (the provider or download client, "cached" or "reused" under it), Size and Progress (the bar, or how long it has waited and the next try) by default; Score, Formats, Indexer, Release, Added and Last update on offer), and the row's actions. Headers sort in the browser (the queue is loaded whole), newest first by default; columns and sort are kept per user in this browser (`theatarr.library.activity-queue.<user>`, `…activity-queue-sort.<user>`). **What do these mean?** lists each status word and its meaning (also the Status cell's tooltip): Queued, Waiting for a provider, Starting, Waiting to start, Downloading, Download complete, Waiting for the mount, Linking, Partly linked, Rejected by the library, Linking failed, Linked, Failed, Not started. Row actions (managers): **Details** (Inspect when it needs attention) opens the steps, release, hash, message, provider attempts and cache evidence under the row; **Retry** (or **Import now** for a finished download) when it applies; **Remove** (Dismiss for a failed one), whose confirmation has one checkbox, "Also blocklist this release and search again". Managers can tick rows (or "Select all N shown"): "N selected", **Remove selected…** (the same checkbox; one route call per row; the result says "Removed 3 downloads." or "Removed 2 of 3. Not removed: …") and **Clear**. Household accounts and "View as a user" keep simple rows: title and library, stage, progress or waiting time, and Details with the steps. Filters (`?show=`): **Needs attention** (users: Delayed; a failed step, a refused or partial link, a wait past its limit, or no progress for 6 h), **Downloading**, **Waiting** (queued, starting, not started, finished, linking), **All**. A finished provider download is **Download complete** until it is linked; "ready to watch" is kept for library readiness. **History:** grabs, imports, upgrades, failures, each with a one-line reason, filtered by title, copy, event type (Grabs, Upgrades, Imports and links, Failures and blocks) and a since date, all in the URL (`?title=&copy=&event=&since=`; `GET activity?state=history&title=&version=&kinds=&since=`, `web/src/lib/activity-history.ts`), newest first, 50 a page with **Load more**. For accounts that see files it is a table without day headings: **Event** (the words, with their meaning as tooltip) and **Title** always, then the chosen columns (Library, Quality, Score, Date and Release by default; Provider, Indexer, Formats and Size on offer; kept in `theatarr.library.activity-history.<user>`), then **Retry** on a failed download and, for admins, the journal link. Headers do not sort. A cell the event has no value for shows "—", never 0. Household accounts and "View as a user" keep the rows grouped by day. The journal link on a row is for admins only. Accounts without See files get no library key on a row (the resolution stays), and an opened failed download says in plain words that it did not work and that a manager can look at it. A download that never started because downloads were off when its turn came (paused, locked, or Watch only) and was never sent to a provider or client shows **Not started** with the reason, outside Needs attention; History says Not started, and for admins its line is the plain message. On Wanted, a row's search-blocked reason shows next to its disabled Search now when its waiting reason does not already say it (managers do not read `/status`). **Blocklist** (`/activity/blocklist`, accounts with See files): a table, newest first, 50 a page ("Previous · 1–50 of 312 · Next"), filtered by part of the title's name and **All libraries** / one library (`GET activity?state=blocked&title=&version=&offset=`; the total is the filtered count; "No blocked release matches." when nothing does). Columns, all shown: Title, Library, Release, Quality, Indexer (the blocked download's, when known), Reason (the plain words, the stored detail as tooltip, "searched again" under it) and Blocked, then **Unblock**. Managers can tick rows: "N selected", **Unblock selected…** ("These N releases may be picked again for their libraries"; nothing is searched; one route call per row) and **Clear**. Users see the copies (versions and seasons) of the requests they are on, not another copy of the same title (history by version only; it pages over them, so each page is full until the last); admins see all. Bookmarks `/activity/now`, `/activity/blocked` and `/activity/wanted` redirect to `/activity`, `/activity/blocklist` and `/wanted`, preserving query and hash; Blocklist in the user view goes back to Queue. **Decision journal** (`/activity/journal`, entry `/:id`; accounts with the admin permission): every grab decision with its evidence and Ok/Wrong verdicts; in the user view the tab is hidden and both addresses go back to Queue | new (router, library); `waiting_since`, `next_retry` from `internal/web/activity.go` |
| Wanted (managers and admins; Manual import admins only) | `/wanted`, `/wanted/cutoff`, `/wanted/manual-import` | Three tabs; **Manual import** is described under Manual import below (admins only: managers do not get its tab; with `?as=user` its tab is hidden and its address goes back to Missing). Managers get the same lists, Search now and Interactive search, without the admin links (settings, decision journal). **Missing** and **Cutoff unmet** list the search schedule's items, one row per library and film or season (docs/design/router.md, "The search schedule"). Viewing never starts searches or changes version state. A line under the tabs gives the pace: "Up to 10 searches an hour (3 in the last hour). Items are searched again every 24 h. A missing new release is tried again sooner: the wait starts at 1 hour and doubles each time, up to 24 h, until a week after its release." (both times are the **Search older items again after** setting; "Items are searched again every …" is left out while **Search for missing items** and **Search for upgrades** are both off) When the tab's switch is off a banner says "Theatarr is not searching for missing items by itself. Items here are searched when you ask, or after a failed download." (Cutoff unmet: "for upgrades"), with an **Automatic search settings** link for admins. Columns (Columns picker; on a phone they show under each title): Library, What, **Next search**, Last search. **Next search** says **Now** for a due row (" · asked by Search now" when asked; no start time is promised, the hourly limit decides), the time for a scheduled row (the exact time as tooltip) with why it is early ("New release, try 2", "After a failed download", "Added by a list"), **Downloading**, **Not scheduled** (its switch is off and nothing asked for it) or **Paused**; a row never shows a time while paused. Filters (`?waiting=`): All, Due, Scheduled, Downloading, Not scheduled, Paused; they and the library filter precede 50-row paging, and counts cover the whole selection. **Search now** on a row, **Search selected** and **Search all** make the rows due now under the hourly limit (`router.wanted.per_hour`); Search all, and a selection larger than what the hour has left, confirm first with the count ("They start as the hourly limit allows"). The answer says how many started and how many wait ("Started 6, queued 306"). While asked rows wait, a line counts them, with **Cancel queued searches**, which puts them back on their regular schedule; searches already started stay started. Rows Search now cannot search (a download in flight) cannot be selected, and nothing can be selected while paused; a row's search-blocked reason shows next to its disabled Search now when Next search does not already say it. Shared title/season queries display each library separately so its gates remain visible. Routed searches say “Sent to download”; “Grabbed” requires a download from that run. Closed gates link to their settings without toggles. Active title copies link to both queues when they have missing and below-cutoff episodes; shadow copies have no Wanted handoff. Search history shows each search's outcome in one line, links the newest to its results and, for admins, to its **Journal entry**. At most ten newest searches are added after the fifty release-history events are selected. Search-entry verdicts only record feedback; their related downloads link to the download entry for release actions. | `GET wanted?only=missing\|cutoff&version=&waiting=due\|scheduled\|in_flight\|not_scheduled\|downloads_off&limit=&offset=` (rows with `waiting`, `next_at`, `why`, `tries`; `search_now_pending`), `POST wanted/search`, `DELETE wanted/search` |
| Requests | `/requests` | users: theirs, by status; admins: a queue with approve / decline / **Edit request** (versions, seasons). `/requests/<id>` opens a focused request detail; old `?request=<id>` links redirect there (the View request links). | new; `GET upgrades` |
| Profile | `/me` | password, sessions, own API keys (if allowed), theme | new |

**Title page** (`web/src/pages/title.tsx`,
`lib/title-layout.ts`):

- **Hero**, compact: poster, title and year, essential facts (certification, runtime, up to three genres, original
  language, rating), readiness for everyone ("Ready to watch in 4K": the best resolution a usable copy has; "5 of 10
  episodes ready"; "Not available yet", or "Getting ready" without See files while a download is in hand; a movie
  missing one of its copies is still ready), audio languages, a
  synopsis cut to three lines with More, and the primary action beside the title: **Add to another library** (add),
  else **Request** / **View request** (request). On phones that action also sticks above the bottom tabs.
- **Action bar** (callers with "see files", `components/title/title-toolbar.tsx`), under the hero: **Search now**
  (managers, when a monitored library can search: "Search every monitored Live library now"; a series "Search every
  monitored episode now, in every Live library (missing episodes and upgrades)"), **Interactive search** (managers, a
  movie with a Live library: the whole movie), **Rename files** (admins), **History** (opens the History tab),
  **Refresh metadata** (managers) and **Remove title** (managers). Below 768 px Search now and Interactive search keep
  their words and the rest sit under **More**. Under the bar: Pause once for the page, "Interactive search needs a
  Live library. This film's libraries are Watch only.", the Search now outcome or its failure with **Retry**, and "No
  library is monitored." when so. **Add to another library** stays the hero's primary action.
- **Rename files** shows a preview first (`GET titles/{id}/rename`): one line per file, its library, then old name →
  new name (when only folders change, the path from the first folder that differs); "N files could not be checked."
  for files it could not check; "Every file already has the name the naming settings give it." only when nothing
  would change and nothing was skipped. **Rename N files** renames; a failed preview shows the server's message.
- **Libraries** table (callers with "see files", `components/title/library-rows.tsx`, `lib/library-rows.ts`), for a
  movie and a series alike: one row per library the title is in now, Live or Watch only. Always shown: **Monitored**
  (the switch), **Library** (colour, name with the key as tooltip, the **Live** / **Watch only** tag, and a verdict
  only when it adds something: **Downloads off** with its small **?**, or "New files won't be linked") and the row
  actions. **Columns** picks the rest ("Monitored, Library and the actions always stay. Kept for your account in this
  browser."; `theatarr.library.title-libraries.<user>`). On by default: **Status**, **Quality**, **Score**, **Size**,
  **Cached on** and **File** (the file name, the full path as tooltip). On offer: Preferred quality (the profile's
  cutoff, "· upgrades off" when so), Minimum availability, Release, Release group, Languages, Video / audio, Formats,
  Added, Last search, Next search and Path. A series row offers Status, Quality, Size, Cached on, Preferred quality,
  Added and Last search. Quality is the technical name ("WEBDL-2160p"). Score shows only with the matched formats
  (its tooltip): a file Theatarr adopted stores none, and then Score and Formats are both blank. **Cached on** has a
  pill per debrid provider that holds the file complete now (RD, AD, TB), adopted files included (found through the
  hash their link records), "—" when none does. A fact the file does not have is blank, never 0.
  **Status:** a download in hand and no file reads its stage ("Downloading · 37%", "Waiting for mount", "Blocked"),
  linked to Activity; a movie without a file "Not monitored", "Upcoming · Jan 14" (not out at home yet, the posters'
  rule) or "Missing"; a held movie the upgrade badge with Why? (Available, Upgrade found, Best quality known), with
  "Upgrade downloading · 37%" under it while one is in hand; a series "42 / 48 episodes" (green when complete, red
  when none, amber between; "Nothing aired yet" before any episode), with "6 missing" under it.
  **Row actions:** **Search now** (icon, managers; disabled with the reason; when other Live libraries want the
  title, its tooltip says they may use the results too), **Interactive search** (icon, a movie's Live library) and
  **Details**, which opens a panel under the row: library key, path, hash, edition, a series' episode counts, the
  upgrade explanation when no Status cell gives it, minimum availability when its column is hidden, the library's
  restriction notes, **See in Wanted** / **See in Cutoff unmet** (managers, Live libraries), **Last search results**
  (managers), each library link with **Unlink**, and **Remove from this library** (managers). Below 768 px each
  library is a stacked block: switch, name, tag and status; quality · size · provider pills; the file name; then
  Search now, Interactive search and Details with their words. There is no Columns button there. A library the
  title was removed from has no row; if it still has links, one line under the table says "Still linked from a
  library this title was removed from: <library> (N files)." with **Remove links…** (managers). With no library:
  "This title is in no library yet."
- **Release history** (`History` in `components/title/versions.tsx`, on the History tab and in the episode
  overview): every event says what started it in plain words (`trigger`: automatic search, automatic upgrade search,
  search again after a failure, RSS, manual search, manual grab by a person, manual retry, approved upgrade, Seerr
  request (an arr command whose client names Seerr), arr search, release push, offer, manual import);
  grabs and failures name the provider and the release, a failure its short reason ("RD status error"); a search
  shows one line, "12 results, 3 accepted · Grabbed #1 via rd → imported" or "Nothing grabbed: why", about the
  download that search opened (a later retry of the same release does not change it). For managers the
  release of a grab or failure links to its row in the releases page (`?version=&hash=`), and the newest search
  (`latest`, the one the releases page shows) links to "See this search's results"; older searches have no results
  link, the releases page keeping only the newest. Admins get a quiet "Journal entry" link on a search.
- **Next search:** a movie's Libraries table can show a **Next search** column, and a series season with
  one library in view adds "Next search: in 3 h (new release)" to its facts. Both read the same time as the Wanted
  page: "now" when due, else the time, with why it is early ("new release", "Search now", "after a failed
  download"). Nothing is shown when the item has no time there: not Live, not monitored, at its cutoff, not
  scheduled, a download in flight, or paused. Schedules are per season, so an episode has no time of its own.
- **Tabs:** Episodes (series and anime, the default) / Watch order (anime with a franchise) / About (overview when
  long, the fact table, outside links; admins also identifiers, release dates, files and registry) / History ("see
  files": each library's release history; admins the title's journal). There is no Libraries tab: the table is under
  the hero. About's Files inventory includes old-manager files and active native library links, deduplicated by path.
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
  `Grabbed · rd` (in flight), `Imported`, `Current file`, `Failed · <short reason>` (the newest attempt failed, even
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
  the downloads that search opened. A live search no indexer answered is a failed search ("Live search failed",
  the stored list shown), and a live search is never retried on its own. `?hash=` picks out the followed release's row, or says it is not in the list.
- **Interactive search** (`components/title/interactive-search.tsx`, the grid `release-grid.tsx`, columns
  `lib/release-columns.ts`): a wide window (a full-height sheet on phones) that searches the indexers the moment it
  opens, as Radarr's does. Managers open it for a movie from the action bar (the whole movie) and each Live library's
  row (that library). A series has it per season and per episode only, as Sonarr does (an unscoped series search
  would judge every episode unrequested): each season header (season packs and that season's episodes), each episode
  row (an icon button at the row's end; it does not open the episode overview) and the episode overview (per
  library). Each Wanted row opens it for that library, and the row's season for a series. It is offered only when a
  current library of the title searches live (`control.live_releases`); Watch-only libraries get none. The title data
  still lists removed copies, so the window takes the current ones from the removal scope (`GET
  titles/{id}/removal`, its `copies`): a removed copy gets no tab, and a search asked for it opens on a current one. While the indexers answer, the window says "Searching the
  indexers for <library>…" with a seconds counter; closing it stops the search. One row per release, with the
  columns Source (indexer kind, private trackers marked), Age, Title, Indexer, History (what came of the release
  before), Size, Peers, Cached on (provider pills), Episodes (series), Language, Quality, Score, Formats, Flags and
  Status, then Grab. Columns sort, can be hidden and reordered (Columns); the choice and the sort are kept per user
  in this browser (`localStorage` `theatarr.library.release-columns.<user>`). Filters (text, Accepted only, Cached
  only, Packs only for a series) are not kept. On a phone each release is a card with its indexer, the episodes it covers, languages and flags. A title in several
  libraries that search live gets a tab per library (its colour and name, the key as tooltip):
  each tab searches once per opening, Search again searches the shown library again, and every opening searches
  afresh. Grab sends an accepted release to the router at once (journalled; removable in Activity); a rejected
  release, one that would replace a better-scored file, or one the server rejects at grab time asks first ("Grab
  anyway"). A live answer keeps the best 300 releases ("Showing the best 300 of N" beyond that). The indexers reuse an
  answer for 15 minutes, so opening again within that time re-decides the same results. A search the server refused
  or that failed shows the last stored results with the reason, and those cannot be grabbed.
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
  - **Filters per season:** All / In progress / Missing / Upgrade found (the last for "see files"), with counts; they
    match an episode when any library in view does. The season checkbox selects the episodes shown, so "Missing,
    select all, Search" works. The bulk bar shows only while something is selected: "3 selected · 4K" (the libraries
    in view), **Monitor**, **Unmonitor**, **Search** and clear. Another library view clears the selection.
  - **One library in view, 768 px and up:** Sonarr's table: checkbox, monitor switch, number, title, the chosen
    columns and the actions (Search and Interactive search icons). **Columns** ("The number, title, monitor switch and
    search buttons always show."; `theatarr.library.episode-columns.<user>`): Air date, Status (Available, Upgrade
    found, Missing, Not aired, Not monitored; the download under it), Quality, Size and **Cached on** by default;
    Score, Release group, Release, File and Runtime on offer.
  - **All libraries:** a column per library (the matrix), each cell a word, Available / Missing / Not aired /
    Upgrade found / Not monitored, with the quality beside it and the monitored mark; each row ends in the Search and
    Interactive search icons. No Columns button.
  - **Phones:** one row per episode for the library in view: name, number and air date, and the state in words on
    the right (the quality under it). Never a bare icon.
  - **Numbering:** "E01"; anime with an absolute number "E01 · absolute 13", in the rows, the table and the episode
    overview. Anime with a franchise get a **Watch order** link above the seasons (the tab stays).
  - **Episode overview:** the shared Modal, a centred dialog on desktop and a bottom sheet on phones
    (`?episode=<id>`): number, name, air date, runtime; then three tabs. **Details**: per library its state in the
    upgrade vocabulary, monitor switch and Search now (managers), Interactive search for that episode in that library
    (managers, a library that searches live), the download in hand, the upgrade block with Why?, and the file's facts
    (release, quality, score, size, group, **Cached on**, languages, media, added, path), shown openly. **History**:
    each library's history with the episode's journal entries. **Last search**: the stored decision for the episode,
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
| Grab a release | Interactive search: live answers only (`Releases.grab`); an accepted release is sent at once, a rejected one or one that would replace a better-scored file asks first; the stored "Last search" has no Grab | `POST titles/{id}/releases/grab {version, info_hash, override}` | The release must come from this server's own live search for that title and copy in the last 30 min (never release data from the browser). It is decided again; a rejected release (then or now) answers 409 with the reasons and needs "Grab anyway" (`override`). The action is refused with the router-disabled or shadow-mode reason before enqueueing when provider writes cannot run; otherwise `router.Enqueue` applies the indexer's provider allowlist, blocklist, pacing and budgets |
| Remove download | Activity › Queue: a row's Remove (Dismiss for a failed one), or **Remove selected…** for ticked rows (one call per row) | `POST activity/downloads/{id}/remove {blocklist, search}` | `router.Dismiss` (as the arr views' `DELETE queue`): Theatarr's record ends as failed; the torrent at the provider stays (shared by every library), linked files stay. The confirmation's checkbox "Also blocklist this release and search again" sends both and blocklists the release for the copy (source `ui`, "Removed in Activity with blocklist") and searches the copy; `search` without `blocklist` is refused. A toast keeps the server's outcome visible when the row disappears |
| Retry | a Queue row's Retry (Import now for a finished download), and History rows about a failed download | `POST activity/downloads/{id}/retry` | failed: the same release, as the download stored it (its release data with cache evidence, as the router rebuilds a download it resumes), goes to the router again as a new download only when `router.enabled` is true and its version is active; disabled or shadow routes are refused with the reason (409), as are blocklisted releases. Ready or importing: import now (`library.Import`), answered with its `outcome`: `imported`, `partial`, `rejected` (failed and blocklisted), `held` (left for review), `waiting` (mount) or `dry_run` (gate); anything but `imported` is shown as a warning and journalled as such. Anything else is refused (409) |
| Unblock | Activity › Blocklist: a row's Unblock, or **Unblock selected…** for ticked rows (one call per row, no search) | `POST activity/blocked/unblock {version, info_hash, search}` | deletes the blocklist row (`router.Unblock`) in the same transaction as the journal record; optionally searches the copy. A toast keeps the server's search outcome visible when the blocked row disappears |
| Remove from this library / the title | the library row's Details / the action bar's **Remove title** | `GET titles/{id}/removal?versions=` (scope), `POST titles/{id}/remove {versions, unlink}` | `catalog.Remove` (the arr views' removal: `arrview_removed_version`, unmonitor; the title leaves when no copy is left), downloads in hand dismissed in the same transaction (`router.EndRemoved`, shared with the arr views' DELETE; a running import then takes its links back); with `unlink`, the copies' active symlinks go to the trash through the gate (refused ones are reported as kept). A move that fails otherwise (a full disk) is reported in `failed` with the rest (200), not a 500 after the hide: the scope then lists the removed copies whose links remain (`leftover`) and the dialog offers "Unlink files", a removal with unlink that moves just those. The journal's remove entry claims no unlink; each moved link has its own `unlink_file` entry once it moved. A copy an arr still manages is refused (409). Adding the copy again restores it. Under what stays, the dialog says where torrents nothing uses are deleted: Admin › Torrents, the Unused filter (a link for admins, plain words for managers; the page has no address for the filter) |
| Change episodes (admins only) | the episode overview's Details tab (the file), for one of Theatarr's links | `POST links/{id}/episodes {episode_ids, replace}` | `library.ReassignEpisodes` ([library](library.md) "Which file is which episode"): the link is renamed for the chosen episodes and the choice is kept for later imports; a chosen episode with another file in that library is refused (409) unless **Replace** is chosen, which moves that file to the trash. Recorded in the library's actions, not the journal |
| Unlink a file | the library row's Details, each link's **Unlink** | `POST links/{id}/unlink` | `library.Unlink`: only Theatarr's own active symlink, gate first; the copy is wanted again when it has no other file. Hardlinks, copies and the mount's files are never touched |

There is no arr-shaped `DELETE blocklist/{id}` (the arr views serve no blocklist list to take ids from).

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

**Library labels**: a library uses its generated display label while it is unique among the libraries shown together.
When labels collide, each becomes “Label · key” (for example “4K · tv-2160p”). Setup activation cards are headed by the key, the
display label beside it, so same-labelled libraries can be told apart. There is no editable display-name field.

- **Page:** Movies, Series and Anime use the full width. The toolbar has the title search ("Filter by title", the
  `q` parameter) and **Select** (managers) on the left; **Columns** (table) or **Poster options** (posters), the
  Posters / Table switch, **Sort** (a select and the direction button, in both views since a phone has no headers)
  and **Filter** (with the number of active filters) on the right. Filters open in a sheet at every width ("Counts
  are for the whole list, before other filters."). Active filters show as chips under the toolbar, each a button that
  removes it ("Remove filter: Missing"), then **Clear all**: `Title: “dune”`, Monitored, Not monitored, the
  availability word, `Has 4K`, `Lacks 4K`, `Quality: WEBDL-2160p`, `Provider: RD`, `2019–2024` (or `From 2019`,
  `Up to 2024`), one per genre. Without See files there is no library, provider or state chip. With nothing found:
  "No titles match these filters." and **Clear filters**. Filters and sort stay in the address; a sort the account
  is not offered falls back to Title.
- **Colour rule** (`copyTone`, `lib/library-status.ts`), one for bars, dots and pills: green (a movie library has a
  file; a series library has no monitored, aired episode missing), red (monitored, released or aired, and missing),
  accent and striped (a download in hand), amber (the download in hand is blocked), grey (not monitored, not
  released yet, or nothing aired yet). "Not released yet" is the badge's own rule, so a bar never reads Missing
  while the badge reads Upcoming. Every bar and pill says the same in words in its tooltip and accessible name ("4K:
  13 of 17 episodes linked, 2 in progress", "WEB: missing", "4K: not monitored").
- **Table** (`components/library/title-table.tsx` on the shared `DataTable`): one line per title. **Title** is fixed
  (thumbnail, name, an eye-off icon when not monitored); then the columns chosen for the list (`lib/index-columns.ts`;
  **Columns**: "Title always stays. On a phone the chosen columns show as lines under each title."). Defaults: Movies
  Status, Libraries, Studio, Added, Size; Series and Anime Status, Libraries, Network, Next airing, Seasons, Size. On
  offer as well: Latest file, Previous airing (series), Quality, Quality profile, Year, Release date / First aired, In
  cinemas, Digital release and Physical release (movies), Release status / Series status, Series type (series),
  Runtime, Certification, Rating, Genres, Original language, Minimum availability (movies), Provider and Folder.
  **Status** is the poster badge's word in its tone, with the date for Upcoming, or the stage of a download in hand
  linked to Activity. **Libraries** has one pill per library in the colour rule: "4K · Remux-2160p", "WEB ·
  Missing", "4K · 13/17" (a series has no count before anything aired); a click opens the title's Libraries dialog
  (each library's file, state, stage and monitor switch; search and files stay on the title page). **Added** is the
  day the title was added (sort `created`); **Latest file** is when the newest current file came (sort `added`).
  Provider shows RD, AD, TB pills. Headers sort where the server can: Title, Studio / Network, Next, Size, Added,
  Latest file, Quality, Year, Release date, Runtime, Certification and Rating. The per-library completion sort
  (`sort=completion&sort_version=<key>`) stays in the API and old links, but no header offers it. Below 640 px rows
  are cards: thumbnail, title and Status, then the pills, then each chosen column as a line. Without See files the
  Libraries, Quality, Quality profile, Size, Latest file, Minimum availability, Provider and Folder columns are not
  offered and Status is headed **Availability** with the watchability words.
- **Posters:** one availability badge, top right, in precedence order: Missing (Upcoming for a movie not out at home
  yet or a series with nothing aired yet), Some episodes, Upgrade found, Best quality known or Available
  (docs/design/manage.md, Words). Some episodes means a monitored, aired episode is missing; family accounts get the
  server's watchability as is; a movie playable in one library while another, monitored library still lacks it
  shows Upgrade found when a better release was found, else Best quality known when a held copy is below its
  preferred quality, else Available, as it does once that copy is unmonitored (the server then does not report it
  partial); never Missing. Source-ceiling evidence stays in **Why?**. Top left a small eye-off chip when the title is
  not monitored; the Movie / Series / Anime pill is not shown on these pages. Between the art and the title, for
  accounts that see files: a movie in one library has one thin bar; a movie in several has one bar in the badge's
  tone and a line of pills, one per library (dot and label); a series in one library has one bar with its count
  ("13/17"); a series in several has one line per library (label, bar, count). The in-flight chip over the art shows
  the most advanced stage, linked to Activity. **Poster options** ("Choose the size and what shows under each poster.
  The status bar and the library lines always show."): size (small, medium, large; on a phone small fits three
  across, the others two) and the details under each poster: Year (the default), Next episode / release, Network /
  studio, Quality ("4K: Remux-2160p · WEB: WEBDL-2160p"), Size (the title's total), Added (the day the title was
  added) and Rating; Quality and Size need See files. Without See files a poster has the badge only.
- **Select** (managers) works on both: tick titles ("Select all 60 shown" selects the loaded rows), pick the target
  library (or **Every library**) and Monitor on / off;
  **Review changes** asks `POST titles/monitor` with `dry_run` and lists what changes, what is already set and what
  is excluded (titles without the copy); **Apply** sends the preview's values as `expect`, so a
  copy changed since is skipped as stale, and lists each outcome. It only sets `monitored` on existing copies: no
  copy is added, nothing is searched or grabbed, and `router.enabled` is untouched. The Libraries dialog's switch
  uses the same route for one copy.
- **Search now for a selection** (managers, as `POST wanted/search` needs): with one library picked in the
  select bar, **Search now** confirms and then sends the selected titles to `POST wanted/search`
  (`only: "missing"`, one item per title and that library, 200 a request, one request after another). They are
  made due like the Wanted page's Search now and start as `router.wanted.per_hour` allows; nothing goes around it.
  The bar shows the server's answers added up: searches queued, then how many started now and how many wait (the
  last answer's), and the skipped titles by the server's reason, with a link to Wanted. A refused request (409 while new downloads are off)
  shows the server's message and stops the rest. **Every library** has no Search now: the endpoint takes one
  library per item. There is no bulk remove and no bulk add to a library.
- **Series page:** an episode cell shows its stage (and progress while downloading); an **In progress** filter sits
  beside Missing; the episode overview shows each library's acquisition (words, progress, last update, waits,
  retries) with an Activity link. Anime keep "E13 · absolute 13".
- **Preferences** stay in this browser per user and per list (`<list>` is `movies`, `series` or `anime`):
  `theatarr.library.view.<list>.<user>` (falling back to the old `theatarr.catalog.view`),
  `theatarr.library.columns.<list>.<user>` (the old shared column choice is not read, so each list starts with its
  defaults) and `theatarr.library.posters.<list>.<user>` (falling back to the old shared poster options, so the size
  carries over). Sort and filters are not remembered: they stay in the address.
- **Live:** a download event refreshes its title and the lists at once on a change of state, and at most every
  20 s for progress alone (`web/src/api/live-activity.ts`).

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

Not in the library views: bulk Profile, Upgrades and Minimum availability fields, Add another copy from the
library (the title page's add flow stays the one place), Search / Releases / Grab buttons on a row (only the bulk
bar's Search now; the rest stay on the title page and the episode overview), Original title, Collection, Release
group and Languages columns, sorting by the movie dates, Seasons, Status or Libraries, and an Audio / subtitles
column (no list projection of media facts).

### Admin panel (`/admin/…`)

| Screen | Path | Holds |
|---|---|---|
| System › Status | `/admin` | Readiness: Downloads, Library links, Notifications (ON/OFF, linked to their settings) and newest complete Theatarr backup. Needs attention groups failed jobs by kind with newest error, Retry newest and See all; single failures retain Retry/Dismiss. Waiting Reviews count, health failures, back-offs and delayed downloads; then download/task summaries, libraries and diagnostics. The dangling-links check row has a "Show the N broken links" list, loaded when opened from `GET /health/dangling` (the latest report, as `theatarr library dangling` prints it; at most the 500 oldest, with a note when there are more): title or release folder, library, since, cause, and a hint from the recorded link's state (marked missing, waiting for the repair sweep, or not a link Theatarr made). It only lists: nothing is scanned or repaired from it. |
| System › Tasks | `/admin/system/tasks` | Attention, running/queued, and folded resolved/finished work; `?kind=` filters on the server before the limit. Technical job kinds and details are kept. |
| System › Backup | `/admin/system/backup` | Theatarr backups and confirmed restore; torrent-list schedule, backups, import, download and restore preview; review-only settings export link; database counts, nightly run and `#backup-check`. |
| System › Errors | `/admin/system/errors` | Existing error inbox and its actions. |
| System › Logs | `/admin/system/logs` | Existing log viewer and filters. |
| Reviews | `/admin/reviews` | Waiting cards for Anime mappings (`/anime`, franchise detail `/:slug`, with alternate cuts and episode numbering), Indexer definitions (`/indexers`) and Flagged decisions (`/decisions`). These suffixes are under `/admin/reviews`; the old `/admin/reviews/quality` opens the Waiting tab (TRaSH and Recyclarr updates apply by themselves, Settings › Quality sync logs them). Counts poll every 60 seconds for admins; catalog off returns an empty holds list (HTTP 200). Pending counts show Loading and failed counts show unavailable. Journal has no total: a full page or next_cursor marks a lower bound (`50+`, `1+`, or `?` when no matches were found in the scan). Waiting and Status never give an all-clear for unknown counts; Status names failed review sources. An empty scanned window says “None in the newest entries.” and links to all flagged decisions. Definition reviews show loading, errors, or “No definition updates waiting.”. Flagged rows link to the journal verdict controls. Wrong blocks and searches again only with new downloads on, an active library and a known release; otherwise it records the verdict. |
| Torrents | `/admin/torrents` | Provider torrents: usage, filters, sorting, confirmed deletes and journal. An account that cannot be read gets one line: the provider's name, why in plain words (the provider asked to slow down, paused after failed requests, request limit used up, VPN not confirmed) and the next try in local time, with the raw error under Details. Saved inventory (`/inventory`, detail `/:hash`) shows known hashes and provider holdings. Backups live in System › Backup. |
| Settings | `/admin/settings` | Search and task pages below. |
| Users & access | `/admin/users` | Users, roles, permissions, sessions and API/view keys. |
| Advanced | `/admin/advanced` | Exactly YAML settings, Settings history and Request limits. The old `/admin/advanced/arr` pages redirect to Admin › Import from Sonarr/Radarr (`/admin/arr-import`). |

Two admin-only screens live outside `/admin`, beside the pages they belong with:

| Screen | Path | Holds |
|---|---|---|
| Wanted › Manual import | `/wanted/manual-import` | Existing file linking workflow, as the third tab of Wanted. |
| Activity › Decision journal | `/activity/journal[/:id]` | Decisions, evidence and Ok/Wrong verdicts. The list is a tab of Activity shown to accounts with the admin permission (not in "View as a user"); other accounts get the unknown-tab notice. |

Older Jobs, Errors, Logs, Database, Metadata review, inventory, migration, journal (`/admin/journal[/:id]`,
`/admin/advanced/journal[/:id]`) and manual import (`/admin/advanced/manual-import`) URLs redirect in one hop to the current
pages, preserving query and hash, including detail parameters. The General #torrent-backups bookmark goes to System Backup with its query; server registry and anime detail redirects go directly to the current inventory and Reviews routes; the old Sonarr/Radarr migration, imports and comparison addresses go to **Import from Sonarr/Radarr** (`/admin/arr-import`). `/admin/system` and `/admin/system/health`
redirect only on exact matches; they do not shadow the System pages. `/classic/` routes return 404. Inventory and journal detail links
remain admin-only; managers keep copyable hashes and unlinked summaries.

### Settings

`/admin/settings` is the Settings home, with **Find a setting** and a card for each page below in sidebar order.
Search understands familiar arr terms, schema field labels and technical paths; Enter opens the first result.
Panel anchors scroll into view and briefly highlight. **Show advanced settings** is shared across all forms and
remembered in this browser. Normal database mode has a compact saved-settings notice with a Details disclosure;
Startup fallback retains the full notice and reload/confirmation behavior.

| Page | Route | Tabs / contents |
|---|---|---|
| Libraries | `/admin/settings/libraries` | Libraries · Naming · File management |
| Profiles | `/admin/settings/profiles` | Profiles · Check a release |
| Custom formats | `/admin/settings/formats` | Rules that match releases and influence quality scores. |
| Quality sync | `/admin/settings/quality-sync` | Sources, preview (a dry run), run, link, update log and recent runs. How sync works is folded. |
| Indexers | `/admin/settings/indexers` | Indexers · VPN · Catalog · RSS |
| Download clients | `/admin/settings/downloads` | Debrid providers (with Provider account sync above their status) · Other clients · Behaviour |
| Notifications | `/admin/settings/notifications` | Where and when Theatarr sends messages. |
| Plex & playback | `/admin/settings/plex` | Plex connections, playback and playlists; read-only Playlists Theatarr keeps below the playlists panel. |
| Metadata & artwork | `/admin/settings/metadata` | Metadata services, refresh schedules and the artwork cache. |
| Connected apps | `/admin/settings/apps` | Compatible endpoints for Seerr and other apps: each app's libraries, profiles and API key. Replace a library puts a new library in an old one's place in the app's libraries and every profile, in the draft; Save sends both. |

Other libraries' folders (`#scan-roots`) are on Torrents, above the torrent list; `/admin/settings/general` redirects
by anchor.

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

**Library control.** Each row on Settings › Libraries has a **Watch only** / **Live** select beside the library's
state in words, from the running `grab_state` ("Watch only", "Live", "Live, paused", "Not set up"). The select shows
the stored mode; changing it saves the stored library with only `mode` changed through the library save (going Live
asks for confirmation first; Watch only saves at once).

**Library editor.** "Where" starts with the optional **Name** (shown in place of the guessed label; the request
leaves it out while it is empty, and sends it empty only to clear a stored one) and the **Key** (fixed once the
library exists), then **State** (the same Watch only / Live select as the rows). Prefer SeaDex is a switch; Upgrades
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
On phones (below `md`) the side list is hidden, so the top of each Settings page has a labelled **Settings page**
select that moves between the pages.

**Settings forms.** Panels (`section-panel.tsx`, through `field-input.tsx`) and record sheets
(`record-form.tsx`) share one pattern: an edited field is marked **Changed** with **Undo** back to the saved value;
**Reset** sets a leaf back to its default (the key is removed); help text and the server's validation message sit
beside the field and are tied to its control with `aria-describedby` (`aria-invalid` on an error). A panel with
changes shows a sticky bar (above the phone tab bar) with the change count, **Discard** and **Save**; a record sheet
shows the count and Discard in its footer, and Discard keeps the sheet open and clears the discarded draft's
validation. A new provider or indexer is tracked against its first draft, its name included. Fields that generate other
values have a Changed mark but no Undo (a record's Service, which fills a new record's name and URL; a library's
Root and Write roots); Discard resets the whole draft. Undo under an object the saved settings
did not have leaves it absent, so the draft equals the saved value again. The Libraries editor has the same marks (a new
library's key included), count and Discard (it has no schema defaults, so no Reset); list rows tie their indexed error and the help to their
control. Most saves apply at once; the few changes the server confirms (going Live, Pause off, removing a library, or an address change that keeps a stored
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
  clients sideways; Errors rows give the message the full width with Seen/Resolve/Ignore below.
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
| Admin panel (system, registry, policy, logs) | yes | no | no |
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

Managers keep inline queue Approve/Decline and see what approval does per requested version in the detail and editor: “Approval records this request; automatic downloads are paused” for shadow versions or a disabled router. Before submission, the sheet applies account copy defaults without version choices and says whether this account needs approval; detail visibility stays own/joined requests for users and all requests for managers, with no file, size, quality-source, provider or path fields in the progress projection.

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

Seerr stays supported as a client of the arr views. Built-in requests are for people who use Theatarr directly.

Approved and processing request text and timelines use the effective grab gates of their applicable versions.
When all those versions are Watch only, or Pause is on, they say the request
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
| `GET titles/{id}/episodes?season=` | seasons with episode lists and a state per version per episode (`episode_version_state`, `version_file_episode`); a removed copy has no column; cells carry `release_title`, `release_group` and `cached_on` (see files) |
| `GET /img/t/{id}/{kind}/{size}`, `GET /img/tmdb/{size}/{file}` | image proxy (§5) |
| `POST titles/{id}/refresh` | refresh metadata and art now |
| `GET discover` | Home rows in one call: `continue`, `recent`, `missing`, `upcoming`, `requests`, each ≤ 20 items with `more` links |
| `GET calendar?from=&to=` | releases and episodes in a window (shares code with the arr view's `calendar`) |
| `GET search?q=&kind=&offset=` | `{library: [...], library_total, tmdb: [...]}`; the kind applies before the limit, `offset` pages the library part; TMDb results through the gateway and `tmdb` budget, cached 1 day, marked `in_library` and `requested` |
| `POST titles` | add `{kind, tmdb_id, tvdb_id?, versions[], seasons?, monitored, search}`, or `{title_id, versions[], …}` for a title already in the library (also one known only by its TVDB id); runs `catalog.Add` (§3); 201 with the title |
| `PUT titles/{id}/versions/{key}` | turn a version on/off, monitor |
| `POST titles/{id}/search` | search now for versions (records a command like the arr views' `MoviesSearch`) |
| `GET activity?state=now|history|blocked&title_id=&title=&version=&offset=&user=` | download rows with plain-words state, progress, provider, attempts (admin), library import results and (admin) `formats`; history rows (admin) with `quality`, `score` (absent when unknown, never 0), `indexer`, `size`, `formats` and `episodes` from the journal entry's release; blocked rows filtered by part of the title's name (`title`) and library (`version`), `total` the filtered count, each with the blocked download's `quality` and `indexer` when known; from `download`, `download_attempt`, `library_import`, `router_plan`, `acquire_run`, `router_blocklist` (moved out of `arrview/queue.go` into a shared reader) |
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

## 9. Visual sources

The visual language follows Scryer and Seerr: Seerr's poster cards with a status badge and hover overlay, sideways
sliders, a backdrop hero, and Scryer's sidebar, top bar and settings layout, in light and dark. Code copied or adapted
from them carries a header comment naming the source repo, path, commit and licence, and is listed in
[`web/THIRD_PARTY.md`](../../web/THIRD_PARTY.md) (Scryer GPL-3.0, Seerr MIT, shadcn/ui MIT).

## 10. Admin workflows

### Manual import

Wanted › Manual import (`/wanted/manual-import`, admins) lists files for a download hash or mount folder
in a chosen version, shows decisions and rejections, and edits each file's title, episodes, quality/revision,
languages and release group. Review choices refreshes the decisions before Import selected files; errors receive
focus and results use a live region. The admin/CSRF-protected native `/manualimport`, `/manualimport/options` and
`/manualimport/execute` endpoints reuse the arr handlers because the SPA has a session rather than an arr API key;
imports retain the library gate and create symlinks while preserving mount files.
