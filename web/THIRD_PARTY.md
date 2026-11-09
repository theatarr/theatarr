# Third-party code in web/

The UI's look follows Scryer and Seerr. Code adapted from them is listed here; each file also says so in its header.
npm dependencies are not listed: their licences ship in `node_modules` and are pinned in `package-lock.json`.

## Scryer

- Source: github.com/scryer-media/scryer, `apps/scryer-web`, commit `2ef7757c774672a50c1499e03ed3436af7359a42`
  (2026-10-21).
- Licence: GPL-3.0. Theatarr is AGPL-3.0 (LICENSE), which GPL-3.0 code may be combined with, so no new obligation;
  the notice below and the file headers keep the attribution.

| Our file | From | How |
|---|---|---|
| `src/styles/globals.css` | `app/globals.css` | Adapted: theme tokens (light and `.dark`), shell, sidebar, card, table and scrollbar rules; `--scry-` renamed `--th-`; Inter only; Scryer-only sections left out; Theatarr additions at the end |
| `src/styles/component-utilities.css` | `app/component-utilities.css` | Copied unchanged (itself shadcn, MIT, notice inside) |
| `src/lib/utils.ts` | `lib/utils.ts` | Copied (`cn`, the shadcn helper) |
| `src/components/ui/button.tsx`, `badge.tsx`, `card.tsx`, `table.tsx`, `tabs.tsx`, `label.tsx`, `separator.tsx`, `skeleton.tsx` | `components/ui/*.tsx` | Copied; token prefix renamed |
| `src/components/ui/input.tsx` | `components/ui/input.tsx` | Adapted: numeric helpers left out |
| `src/components/shell/app-shell.tsx` | `components/root/root-sidebar.tsx`, `root-header.tsx` | Adapted: nav button and group label classes, header grid, gradient-ringed search trigger |
| `src/pages/login.tsx` | `src/pages/login.tsx` | Layout only (centred card) |
| `src/components/search/search-overlay.tsx` | `components/root/root-header.tsx` | Adapted: the search overlay's backdrop, panel, input row (ESC, close) and sections; a portal without Radix Dialog |
| `src/components/search/search-results.tsx` | `components/root/global-search-parts.tsx` | Adapted: the "In library" row card, section loading, empty state |
| `src/components/add/add-sheet.tsx` | `components/root/add-to-catalog-dialog.tsx` | Layout only: poster header, option rows, footer actions |
| `src/components/title/show-releases.tsx` | interactive search results | Layout only: ranked rows, score column, rejection chips |
| `src/components/library/filters.tsx` | `components/views/media-content/catalog-filters-panel.tsx`, `title-quick-filters.tsx`, `components/common/underline-filter-button.tsx` | Adapted: panel header, underline filter button, split filter groups, label and chip styles; selects instead of range sliders (inline widths break the CSP) |
| `src/components/library/poster-grid.tsx` | `components/views/media-content/poster-grid.tsx` | Adapted: responsive grid, off-screen skipping, end-of-list sentinel |
| `src/components/title/seasons.tsx` | `components/views/series-overview/season-section.tsx` | Adapted: bordered season block, clickable header row, episode table columns, row and season checkboxes, the season's monitored state counted from its episodes |
| `src/components/title/episode-drawer.tsx` | `components/views/series-overview/episode-details-panel.tsx`, `episode-panel-content.tsx` | Contents only: episode header, files on disk with their media facts, history list |
| `src/styles/calendar.css` | `app/globals.css` (`.fc-scryer` rules) | Adapted: day grid, day cells and pills, gradient event chips with an accent edge, badges, "+N more"; selectors renamed `.theatarr-cal-*` for our own grid (FullCalendar injects `<style>`, which the CSP refuses) |
| `src/pages/calendar.tsx` | `components/views/calendar-view.tsx` | Layout: toolbar with facet toggles, title, Today / view / prev-next; event chip content (title, episode or release badge, time) |
| `src/pages/activity.tsx` | `components/views/activity/queue-row-presentation.tsx` | Layout: title and release lines, details panel with small uppercase labels |
| `src/components/activity-bits.tsx` | `components/views/activity-progress-bar.tsx` | Adapted: the progress bar (fill widths as 5 % class steps for the CSP) |

## Seerr

- Source: github.com/seerr-team/seerr, commit `c167958ad6f11b0f984dbc7b5938660c017d4c79`.
- Licence: MIT, below.

| Our file | From | How |
|---|---|---|
| `src/components/poster-card.tsx` | `src/components/TitleCard/index.tsx`, `src/components/Slider/index.tsx` | Adapted: the card's shape, type pill, status badge and hover overlay with overview; the sideways row |
| `src/components/shell/app-shell.tsx` (phone tab bar) | `src/components/Layout/MobileMenu` | Idea and layout |
| `src/components/search/search-results.tsx` (TMDb card) | `src/components/TitleCard/index.tsx` | Adapted: poster card with type pill, status badge, hover overlay with the add button |
| `src/components/add/add-sheet.tsx` (seasons) | `src/components/RequestModal` | Idea: a toggle per season |
| `src/pages/title.tsx` | `src/components/MovieDetails/index.tsx`, `src/styles/globals.css` (`.media-page`, `.media-header`, `.media-poster`, `.media-title`, `.media-attributes`, `.media-overview`, `.media-facts`) | Adapted: backdrop hero, poster, title block, overview and facts box as Tailwind classes; one status badge per version |
| `src/pages/home.tsx` | `src/components/MediaSlider` | Row header (title with chevron, "See all") |
| `src/components/continue-row.tsx` | `src/components/TitleCard/index.tsx` | Adapted: the poster card shape and type pill, with the download state and a progress bar over the poster |
| `src/components/toaster.tsx` | — | Idea only: bottom-right toasts for live events |
| `src/api/auth.tsx` (Plex sign-in), `internal/plexauth/plexauth.go` | `src/utils/plex.ts` | Adapted: PIN, popup to `app.plex.tv/auth` with the client and product parameters, polling; the PIN is created and read on the server here |
| `src/pages/login.tsx` (Plex button) | `src/components/Login`, `src/components/PlexLoginButton` | Layout: the Plex button first, local login below |
| `src/components/request/request-sheet.tsx` | `src/components/RequestModal` | Adapted: wording, the season list, the done state |
| `src/pages/requests.tsx` | `src/components/RequestList`, `src/components/RequestCard` | Adapted: poster row with status badge, requester, dates, approve and decline actions, filter bar |
| `src/pages/admin/users.tsx` | `src/components/UserList`, `src/components/UserProfile/ProfileHeader` | Layout: user rows with avatar, role and status, a settings panel per user |

```
MIT License

Copyright (c) 2020 sct

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## shadcn/ui

`src/components/ui/dropdown-menu.tsx` is shadcn/ui's dropdown menu (MIT, Copyright (c) 2023 shadcn), trimmed; the
MIT text is in `src/styles/component-utilities.css`.
