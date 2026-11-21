# Users, Plex sign-in and requests

Migration 00026. The design is `docs/design/ui.md` §3, with the decisions in §8.

## Accounts

`web_user` has `role` (`admin`, `manager`, `user`; accounts from before migration 00026 are `admin`), `permissions`
(extra bits on top of the role: only `auto_approve` and `own_keys`; any other bit is refused with 400 and a stored one
grants nothing), `status` (`active`, `pending`, `disabled`; `invited` for an invite, see Invites), `display_name`, `email`, `avatar_url`, `plex_id`,
`plex_username`, `request_versions` (JSON keys, NULL = the default), `auto_approve`, `last_login_at`. Migration 00107 adds `discord_id` (empty, or a 17 to 20 digit Discord user id). Only an admin sets it
(`PATCH users/{id}` with `discord_id`, Admin › Users); request-ready Discord messages mention it.

- **Local accounts** have a password. The `admin` account stays the break-glass login (`/login`, "Local account").
  The user name is matched exactly first, else ignoring case when exactly one account matches that way (Unicode
  folding); a name two accounts share that way signs in neither (`TestLoginAndSessionExpiry`).
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
| `request` | `POST /requests`, cancel own, `POST /reports` (report a problem) | yes | yes | yes |
| `auto_approve` | own requests are approved at once | yes | yes | optional |
| `add` | `POST /titles`, `PUT /titles/{id}/versions/{key}` | yes | yes | no |
| `manage_requests` | every request, approve, decline; every open problem report, mark fixed | yes | yes | no |
| `see_files` | paths, hashes, release names, scores, providers in activity and title pages | yes | yes | no |
| `manage` | search now, show releases, Wanted, refresh art, replace a reported file | yes | yes | no |
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

## Invites

Migration 00136 (`internal/auth/invite.go`, `internal/web/invite.go`). An admin (the `users` permission) invites a
person from Admin › Users & access: role, auto-approve, an optional name the account starts with, and an expiry (1, 7
or 30 days; 7 by default). There is no invite table: the invite is a `web_user` row with status `invited`, no
password and no Plex id, carrying the role, `auto_approve`, `invite_token_hash` (SHA-256 of a 256-bit random token)
and `invite_expires_at`. The token is in the create answer only; the link is `/invite#<token>`, so the token sits in
the fragment, which the browser never sends, and travels in JSON bodies only. It is not stored, listed or logged.

| Route | |
|---|---|
| `POST /users/invites` | users: `{role, auto_approve, name, days}` (days 0 is 7, at most 30); 201 `{token, user}` |
| `DELETE /users/{id}/invite` | users: revoke an invite not redeemed yet (deletes the row); 404 for anything else |
| `POST /auth/invite` | public: `{token}` → the name, expiry and whether Plex sign-in is offered; 404 not valid or used, 410 expired |
| `POST /auth/invite/accept` | public: `{token, username, password}` → an active local account, signed in (cookie plus `csrf_token`) |
| `POST /auth/plex/poll` with `invite` | a confirmed Plex account with access to our server redeems the invite and is signed in; 409 `taken` when that Plex account already has an account here; 404/410 `invite_gone` when the invite is used, revoked or expired |

Redeeming sets the credentials, status `active` and clears both invite columns in one `UPDATE … WHERE status =
'invited' AND invite_token_hash = ? AND invite_expires_at > now`, so a token works once. A username already in use
(any case) or a Plex account that already has an account is refused and the invite stays unused: an invite never
takes over an account. The public routes are same-origin JSON, and each attempt counts against the client address in
the sign-in throttle; a valid, unexpired token clears the count. An invite cannot be changed with `PATCH users/{id}`
(404), gets no session and no API key (`POST /keys`, `theatarr admin create-key`), and `theatarr admin
reset-password` refuses it. Any role may be invited: whoever holds `users` can already set any role on another
account. An expired invite stays listed with its date until revoked; to send a link again, make a new invite. The
old way still works: someone signs in with Plex and waits as pending until an admin lets them in. `TestInvites`,
`TestInviteNotLogged` and `TestMigrationUserInvite` cover it.

## Requests

| Route | Permission | |
|---|---|---|
| `GET /requests` | browse | own; every request with `manage_requests` (`?mine=1`, `?status=`, `?kind=`) |
| `GET /requests/summary` | browse | counts (the sidebar badge: pending for managers, open for users) |
| `GET /requests/{id}` | browse | own/joined, or any with `manage_requests`; request facts plus safe acquisition progress and approval effects (no file/provider diagnostics) |
| `POST /requests` | request | create; a second ask for the same title joins an open request that holds all of it (`request_user`), grows a pending one it holds all of, or is a request of its own |
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
- Libraries kept in sync (`library.sync_groups`, ui.md "Keep in sync"): at creation, before the joining rule runs,
  the request's libraries are widened by the libraries in sync with them that would gain a copy
  (`catalog.SyncPartners`: not one the title holds, monitored or not; a removed copy is a partner, and the approval
  restores it), and the result is
  stored in `request.versions`. The request sheet's "You are asking for" shows them beside the asked-for libraries,
  each with "in sync with <library>" (the add options' `synced`). The title is looked up as the approval will find it (`catalog.Lookup`: a series by
  TVDB id, then TMDb id), so the partners stored are the ones the approval adds or restores; a title without a live copy is read with
  the request's anime choice, which the approval writes. Known limit: when every copy of a group was removed, each with
  different season choices, and a request without seasons is approved, each restored copy keeps its own old choices
  (the request names every library of the group, so none is a partner that copies another's). Rare, and live has no
  native requests. An approval stores the libraries it was
  given plus those its add created in sync (`synced`), so an edited approval keeps following a partner it adds.
  Readiness, status and the library badges use that stored scope; nothing widens it when it is read. The approval editor shows the stored libraries as ordinary ticked boxes, and a library in sync with
  a ticked one that is not stored ticked and disabled, "in sync with …" (approval adds it). "Requests ask for" offers
  a group as one choice ("4K Remux + 4K WEB") whose tick stores every member's key; a stored set holding only part of
  a group (saved before the group existed) shows that group as its members. A stored member key is never read as
  its group. `TestSyncGroupsAnswers` covers the stored scope. A removal from one library of a group removes the
  title from the group's other libraries too (ui.md "Keep in sync"); requests follow it as they follow any removal
  (a request whose every library was removed is canceled), and household accounts see nothing new about files.
- Approval runs `catalog.Add` (the arr views' add path, through `addTitle` in `add.go`) with the request's versions
  and seasons, and asks for a search only for versions `catalog.Searchable` lets through; nothing searches in shadow.
  `auto_approve` approves at creation, and when the ask joins a *pending* request it approves
  that request with the joiner as the decision maker, but only when the request's versions and seasons are all within
  the joiner's own ask (`askCovers`): approving more would let a non-manager approve someone else's request. Otherwise
  the request stays pending and the answer says why (`not_approved: "asks_more"`; the request sheet puts it in
  words, telling an account with `manage_requests` it can approve it in Requests; any other account reads that it
  needs approval, with no role named). Before sending, the sheet says "Your request needs approval." to an account
  without auto-approve, a season already in the library reads "Already added", and while Pause is on (ordinary or
  locked, from the add options' `grab_state`) "Downloads are paused right now. Your request is saved and starts when
  they are turned back on."; once sent it says "Sent. It is waiting for approval. …". Cancelling your own request asks
  with **Keep request** / **Cancel request**. Joining an approved or processing request approves nothing. The same rule holds for managers' asks. A
  joiner's approval notifies as an approval, not an auto-approval. `TestAutoApproveJoinsPendingRequest` and `TestAutoApproveJoinSeasons` cover it.
- **Joining**: containment only (`askCovers`, `createRequest`). A new ask joins an open request (pending, approved or
  processing) only when that one request holds all of it: every library asked, for every season asked (a request
  naming no seasons holds every season). The requester is added to it (`request_user`); the request does not change.
  A new ask that holds all of a *pending* request grows the first such request to the ask (its libraries and seasons
  become the ask's), so a smaller request never waits for approval of work a larger one covers. Anything else is a
  new request for exactly what was asked, even where an open request overlaps part of it: season 1 in library A and
  season 2 in library B stay two requests. Approving overlapping requests leaves what is already there as it is (each
  approval only adds monitoring and starts its own search). `TestRequestJoinIsContainment` and
  `TestOverlappingRequestsApproveApart` cover it.
- **More seasons**: what a request may ask for is decided per version and season.
  A version the title already has stays requestable for a series season it does not fully monitor (an episode
  unmonitored for it, or without a state row; `seasonGaps`); `GET add/options` lists those as `open_seasons` per
  version, specials aside. A request naming no seasons asks for every season but the specials. Approval monitors just
  the requested seasons in versions the title has (`catalog.SetSeasonMonitored`, never unmonitoring the others) and
  applies the whole-series rule (requested seasons on, the series' other seasons off) only to versions it turns on; a request
  that named no seasons is stored with the seasons it monitored. An open request covers a version only for the
  seasons it holds, so asking for season 3 after an approved request for seasons 1-2 makes a new request.
  The UI: the hero (on a phone its sticky copy) says **Request more seasons**, the page's one button for it; the
  request card keeps its status and history, and the Episodes tab names the seasons that can be asked for above the
  seasons, as information; and the sheet greys out seasons already monitored in every wanted version.
- Status: `pending` → `approved` → `processing` (a download for a requested version is in hand, an imported one not
  included, as Activity counts them; for a series request
  naming seasons, one for those seasons or the whole series, the season match the request detail uses) → `available` (every
  requested version has its file; for a series request naming seasons, every aired episode (the pages' air clock: the
  instant, else the day) of those seasons, so a
  version filled before the request does not count and a gap in another season does not hold it back; the library's
  available badge on such a request follows the same seasons) or `failed` (a download counted as above failed since the
  approval; for a series, only one for the whole series or naming a current, aired episode of the request still
  missing in that library, so an old failure of an episode filled or removed since does not count; the request
  detail and the request-failed notification use the same selection, `requestFailures`;
  `TestSeasonRequestImportedMissingFailed`);
  also `declined`, `canceled`. A sync loop and the
  download events move it. The request detail keeps the list's badge and counts the aired or held episodes only
  (`TestRequestDetailOngoingSeries`).
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
  - the episode grid's season counts stay (have, total, missing, monitored: the season header says "2 of 3";
    `TestHouseholdSeasonHeaderKeepsTotal`);
    the title's own `added_at` stays (Home's "Recently added"); a library facet (`GET titles/facets` versions) keeps
    its `titles` and drops `with_file`, `wanted`, `filled` and `cutoff_met` (`TestTitleFacets`);
  - `cutoff_met` reads as `filled`, a title below cutoff as `available`, the upgrades row is empty, and
    `upgrade=`, `availability=upcoming|unmonitored`, `state=cutoff_met`, `tag=` and a source quality filter answer
    403, like sorting by size and the provider filter;
  - no tags: rows and the title page carry no `tags`, and the facets no `tags` (`TestTitleTags`);
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

## Report a problem

A household member says a film or an episode is broken without leaving the app; a manager fixes it in one step
(`internal/web/reports.go`, table `problem_report`, migration 00147).

| Route | Permission | |
|---|---|---|
| `GET /reports` | browse | own reports, open and fixed; every open report with `manage_requests` (`?mine=1` for your own) |
| `POST /reports` | request | report a movie or an episode that has a file: `kind` (picture, sound, subtitles, wont_play, other) and an optional note |
| `POST /reports/{id}/fix` | manage_requests | mark fixed, with an optional reply the reporter sees |
| `POST /reports/{id}/replace` | manage | replace one of the reported files: unlink, blocklist, search again |

- **Reporting**: "Report a problem" beside a movie's status on its title page when it has a file, and under the
  synopsis in the episode window for an episode with a file, for every account that may request. Without a file there
  is nothing to report (409: that is a request). One open report per account, title and episode (`episode_id` 0 for a
  movie, one unique index); a second report updates it. Note and reply are at most 500 characters. No quotas, no
  threads, no automatic closing.
- **Requests › Problems** (`/requests/problems`): the reporter's own reports with the status and the reply; for
  managers every open report, oldest first: title and episode, what is wrong, the note, who and when, and for accounts
  that see files the files the title's libraries hold for it now. For managers the tab shows the open count and To decide
  the pending count: the two parts of the Requests badge (no number at 0).
- **Replace file…** per file. Unlinking a file takes it out of every library and every episode it serves
  (`Ops.UnlinkLink`), so the row and the confirmation name exactly those: "The file is removed now and a new one is
  searched. Until one is found it is missing in <libraries>.", the episodes that lose it, and for a file from a pack
  (its release holds more episodes, or other files of the same content) that the whole pack is excluded there. On
  confirm the link goes to the trash (the debrid torrent and every other file are untouched), the release is
  blocklisted in each of those libraries (`router.Blocklist`, source `ui`) and a search is asked for the movie or
  every episode the file held in those libraries. It is offered only when that search can run: Theatarr's own
  symlink, its release known (a hash or a release name, so the blocklist keeps it out), searching running, Pause off,
  each of those libraries searching this title (a library for anime only skips a title that is not anime, and the
  other way round: "4K WEB does not search this title (it is not for anime), so no new file would be searched"),
  each of those libraries monitored for the movie or each of those episodes, and no episode the file holds removed
  from the series' episode list (the search leaves a removed episode out: "This episode is no longer in the series'
  episode list, so nothing can replace its file"). Otherwise the row says why and links
  to the title page; the server refuses the same cases (409) before anything changes. The report stays open. The
  journal records it as an unlink with "replaced for problem report N". The title page offers the same **Replace file…** without a report (a movie's library row and the episode overview, `POST links/{id}/replace`; [ui.md](ui.md) "Recovery actions").
- **Mark fixed** with an optional reply: the reporter's list shows it, and the reporter gets an in-app toast (SSE
  `problem` event, to managers and the reporter, whose copy says `mine`).
- **Notice**: the Requests sidebar count includes the open reports for managers (`GET /requests/summary` `problems`,
  added to `badge`); System › Status "Needs attention" shows "N problem reports" for admins, linking to the tab. A new
  report sends one message (`notify.ProblemReported`) to the targets that get request messages: Discord and ntfy
  ("Problem reported: <Title> S01E02", what is wrong, who, the note) and native targets (`theatarr.problem_reported`
  with `problem`); the policy event name is `request_ready`, so a target filtered to requests gets it too. Arr
  webhooks and installed arr-view notifications get nothing.
- Household accounts see no file, release, provider, indexer or library detail here: their list carries no `files`,
  `user` or `resolved_by`. Tests: `web.TestProblemReports`, `web.TestReplaceReportedFile`,
  `notify.TestProblemReportedMessage`.

## Screens

Login (Sign in with Plex; local login folded below), Requests (user: My requests, Problems; manager/admin: To decide,
All, Mine, Problems),
Request sheet in search and on title pages in place of Add, Admin › Users (roles, status, pending approvals, extra
permissions, request versions, auto-approve, the new-Plex-users setting, each user's keys), Profile (name, theme,
password for local accounts, own keys). API keys are managed in Users and Profile.

Request detail, request lists, add options and title request cards share the server's effective grab decision
(`grab_state.will_grab`, also exposed as add option `searchable`). It requires active mode (Live) and Pause off.
Approval records monitoring intent while these gates are closed; it does not make a library Live or turn Pause off.
