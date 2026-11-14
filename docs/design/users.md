# Users, Plex sign-in and requests

Migration 00026. The design is `docs/design/ui.md` §3, with the decisions in §8.

## Accounts

`web_user` has `role` (`admin`, `manager`, `user`; accounts from before migration 00026 are `admin`), `permissions`
(extra bits on top of the role: only `auto_approve` and `own_keys`; any other bit is refused with 400 and a stored one
grants nothing), `status` (`active`, `pending`, `disabled`), `display_name`, `email`, `avatar_url`, `plex_id`,
`plex_username`, `request_versions` (JSON keys, NULL = the default), `auto_approve`, `last_login_at`. Migration 00107 adds `discord_id` (empty, or a 17 to 20 digit Discord user id). Only an admin sets it
(`PATCH users/{id}` with `discord_id`, Admin › Users); request-ready Discord messages mention it.

- **Local accounts** have a password. The `admin` account stays the break-glass login (`/login`, "Local account").
- **Plex accounts** have no password; `auth.Login` refuses them. Only active accounts sign in; disabling or setting an
  account to pending ends its sessions, and its API keys stop working at once (the key check joins `web_user`).
- The last active admin cannot be demoted or disabled.
- API keys belong to a user and carry that user's permissions. Keys without an owner are refused. A key reaches
  every route its owner's permissions open, the account routes too (`/keys`, `/users`, `/me`, `/me/password`, user
  settings); only `POST /auth/logout` is session only. A key changing its owner's password ends all the owner's
  sessions. A key confirms risky changes like a session, with tokens bound to that key
  ([design/settings.md](settings.md) "Who confirms").
- `theatarr admin create-key [-db path] -user NAME -name KEYNAME` makes a key for an account on the host (for example
  for an agent, [mcp.md](../mcp.md)) and prints it once on stdout.

## Permissions

A role is a preset of bits (`internal/auth/perms.go`); an admin can add `auto_approve` and `own_keys` to a `user`.

| Bit | What it opens | admin | manager | user |
|---|---|---|---|---|
| `browse` | library, title pages, calendar, search, own activity, own requests | yes | yes | yes |
| `request` | `POST /requests`, cancel own | yes | yes | yes |
| `auto_approve` | own requests are approved at once | yes | yes | optional |
| `add` | `POST /titles`, `PUT /titles/{id}/versions/{key}` | yes | yes | no |
| `manage_requests` | every request, approve, decline | yes | yes | no |
| `see_files` | paths, hashes, release names, scores, providers in activity and title pages | yes | yes | no |
| `manage` | search now, show releases, Wanted, refresh art | yes | yes | no |
| `own_keys` | own API keys | yes | yes | optional |
| `admin` | admin panel APIs (status, jobs, gateway, shadow, registry, policy, logs, journal, imports, metadata review, decide, sources) | yes | no | no |
| `users` | users, their settings, everyone's keys | yes | no | no |

Pending and disabled accounts get nothing. Every route declares `Access` and a permission next to each other
(`w.handle(mux, pattern, access, perm, h)`); a non-public route without a permission panics at start-up.
`internal/web/roles_test.go` walks **every** registered route as admin, manager, user, pending and disabled, with a
session and with an API key, and checks 403 exactly where the matrix says so; the admin-only set is pinned so a route
cannot drift out of it. The arr views (`/radarr`, `/sonarr`, `/v/…`) accept an admin's key or a view key only.

## Plex sign-in

The browser flow is Seerr's (`src/utils/plex.ts`): Theatarr creates a PIN, the page opens
`https://app.plex.tv/auth#?clientID=…&code=…&forwardUrl=…` in a popup and polls Theatarr until the PIN carries a token.

| Route | |
|---|---|
| `GET /auth/plex` | is Plex sign-in configured |
| `POST /auth/plex/start` | creates a PIN (`POST /api/v2/pins?strong=true`); returns an opaque handle and the auth URL. 10 starts per 10 minutes per address, 200 open PINs, 15 minutes each |
| `POST /auth/plex/poll` | `waiting`, `signed_in` (session cookie set), `pending` (account waits for an admin), 403 `denied` / `disabled`, 410 `expired`, 502 `error` |

Verification (`internal/plexauth`): `GET /api/v2/user`, then `GET /api/v2/resources`, and the account must list a
resource whose `clientIdentifier` is the configured Plex server's `machineIdentifier` and which provides `server`. The identifier comes
from the local Plex `/identity` with the existing `library.plex` config and token (budget `plex`), cached. All plex.tv
calls go through the gateway as source `plex-tv` (budget `plex-tv`); the token travels only in the `X-Plex-Token`
header, never in a URL.

**Token handling: discard.** No current feature needs a user's Plex token, so it is held in memory for the
verification and then dropped. It is never stored, logged or returned; the PIN is forgotten once it yields a token.
`TestPlexSignIn` checks the database and the logs for it.

The first sign-in creates a `user` account; its status is the admin setting "new Plex users": `active` (let in at
once) or `pending` (default). Later sign-ins refresh display name, email and avatar.

## Requests

| Route | Permission | |
|---|---|---|
| `GET /requests` | browse | own; every request with `manage_requests` (`?mine=1`, `?status=`, `?kind=`) |
| `GET /requests/summary` | browse | counts (the sidebar badge: pending for managers, open for users) |
| `GET /requests/{id}` | browse | own/joined, or any with `manage_requests`; request facts plus safe acquisition progress and approval effects (no file/provider diagnostics) |
| `POST /requests` | request | create; a second request for the same title merges (`request_user`) |
| `POST /requests/{id}/approve` | manage_requests | optional `versions` and `seasons` edits |
| `POST /requests/{id}/decline` | manage_requests | with a reason |
| `DELETE /requests/{id}` | request | cancel own pending request |

Request pages hydrate version states, viewer membership and manager-visible requester lists in batches, independent
of page length. Joined requesters retain their creation-time order; users still receive availability-only states
and no other-requester list or count.

- Versions: a request asks for every version that fits the title (`catalog.DefaultVersions`: movie-web and
  movie-remux for a movie, tv-2160p for a series, the anime versions for anime), cut to the account's
  `request_versions` when an admin set them. Users do not pick versions. The title request card reports readiness
  for those applicable versions; the hero remains title-wide.
- Approval runs `catalog.Add` (the arr views' add path, through `addTitle` in `add.go`) with the request's versions
  and seasons, and asks for a search only for versions `catalog.Searchable` lets through; nothing searches in shadow.
  `auto_approve` approves at creation, and when the ask joins a *pending* request it approves
  that request with the joiner as the decision maker, but only when the request's versions and seasons are all within
  the joiner's own ask (`askCovers`): approving more would let a non-manager approve someone else's request. Otherwise
  the request stays pending and the answer says why (`not_approved: "asks_more"`; the request sheet puts it in
  words, telling an account with `manage_requests` it can approve it in Requests rather than wait for a manager). Joining an approved or processing request approves nothing. The same rule holds for managers' asks. A
  joiner's approval notifies as an approval, not an auto-approval. `TestAutoApproveJoinsPendingRequest` and `TestAutoApproveJoinSeasons` cover it.
- **More seasons**: what a request may ask for is decided per version and season.
  A version the title already has stays requestable for a series season it does not fully monitor (an episode
  unmonitored for it, or without a state row; `seasonGaps`); `GET add/options` lists those as `open_seasons` per
  version, specials aside. A request naming no seasons asks for every season but the specials. Approval monitors just
  the requested seasons in versions the title has (`catalog.SetSeasonMonitored`, never unmonitoring the others) and
  applies the whole-series rule (requested seasons on, the series' other seasons off) only to versions it turns on; a request
  that named no seasons is stored with the seasons it monitored. An open request covers a version only for the
  seasons it holds, so asking for season 3 after an approved request for seasons 1-2 makes a new request.
  The UI: the request card and hero say **Request more seasons**, the Episodes tab shows the same entry above the
  seasons, and the sheet greys out seasons already monitored in every wanted version.
- Status: `pending` → `approved` → `processing` (a download for a requested version is in hand) → `available` (every
  requested version has its file; for a series request naming seasons, every aired episode of those seasons, so a
  version filled before the request does not count) or `failed`; also `declined`, `canceled`. A sync loop and the
  download events move it.
- SSE `request` events go to the requesters and to accounts with `manage_requests`. `Config.OnRequest` is an
  optional hook that hears every request change; request webhooks come from `internal/notify` ([manage.md](manage.md)
  "API").
- Users see availability only: whether something is there, and at what resolution ("Available in 4K", "Available in
  1080p"), nothing about sources, files or acquisition. Every handler that returns title, version, episode, activity,
  facet or search data drops paths, hashes, release names and groups, sizes, scores, providers, indexers and arr
  instances unless the caller has `see_files`. Without it, too:
  - a quality is its resolution only (`web.Resolution` in `perms.go`: Bluray-1080p and WEBDL-1080p are 1080p,
    Remux-2160p is 2160p, a quality with none is left out), in rows, search, the title page, episodes, activity and
    facets (the quality facet counts titles per resolution, and `quality=1080p` filters by it);
  - no file counts, per-version or per-file added dates, cutoff, upgrade explanation or history (title and episode);
    the title's own `added_at` stays (Home's "Recently added"); a library facet (`GET titles/facets` versions) keeps
    its `titles` and drops `with_file`, `wanted`, `filled` and `cutoff_met` (`TestTitleFacets`);
  - `cutoff_met` reads as `filled`, a title below cutoff as `available`, the upgrades row is empty, and
    `upgrade=`, `availability=upcoming|unmonitored`, `state=cutoff_met` and a source quality filter answer 403, like
    sorting by size and the provider filter;
  - requests carry no `others` (how many others asked; for `manage_requests`), and the SSE `request` event never
    carries requester ids: they address it, and a requester's copy says `mine`;
  - a request the user joined carries no `user` (who asked first); `decided_by` is dropped unless it is the caller or
    the decider holds `manage_requests` (`fillRequests`), so a member's auto-approval, the first
    requester's or a joiner's, names nobody, and a manager's decision keeps its name.
    `TestJoinedUserSeesNoOtherRequester` and `TestAutoApproveJoinsPendingRequest` cover the list and the detail.

  Languages and edition stay. Activity (queue and history), the Continue row and `download` events cover only the
  copies the user's live requests ask for (`requestScope`): the request's versions and, for a
  series, its seasons, so another member's movie-web download of a title the user asked for in movie-remux is not
  theirs. These are the request's versions and seasons, not the member's own ask (a pending request grows by what
  a joiner adds, and seasons are kept per request only), and history is scoped by version alone (journal entries
  carry no seasons). History ids are opaque, and a download toast names no library (`downloadToast`).
  Because `acquiring` stays title-wide, a change of state or import of any of a title's downloads also sends its
  requesters a `title` event (reason `acquiring`, id and name only), so posters and the title page refetch it
  (`TestAcquiringTitleEvent`). `TestActivityScopedToRequestedCopies` covers it. Visibility comes from the permissions alone (`?as=user` on activity only
  narrows). `TestUserSeesAvailabilityOnly` calls every route a user may read against a seeded library, download and a
  shared request and checks the JSON for those fields and values (source labels, "cutoff", file counts, history,
  requester ids, the request event too); `TestManagerSeesFileDetail` checks a manager gets them.

## Screens

Login (Sign in with Plex; local login folded below), Requests (user: Mine; manager/admin: To decide, All, Mine),
Request sheet in search and on title pages in place of Add, Admin › Users (roles, status, pending approvals, extra
permissions, request versions, auto-approve, the new-Plex-users setting, each user's keys), Profile (name, theme,
password for local accounts, own keys). API keys are managed in Users and Profile.

Request detail, request lists, add options and title request cards share the server's effective grab decision
(`grab_state.will_grab`, also exposed as add option `searchable`). It requires active mode (Live) and Pause off.
Approval records monitoring intent while these gates are closed; it does not make a library Live or turn Pause off.
