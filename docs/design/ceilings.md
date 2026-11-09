# Source ceilings

Source ceilings explain the best source Theatarr has evidence for, per movie or TVDB season and version. They are
display-only: `version.state`, arr views, search selection, quality/score decisions and grabs retain their
existing meaning. No extra metadata or indexer requests are made. SeaDex lookups, announced discs, TheDiscDB and
active hunting rules are not used; the compute path has a hook point for them.

## Records and rules

Migration 00028 adds shared source sightings and overrides (`source_sighting`, `source_ceiling`), search runs with
per-indexer successful coverage (`ceiling_search`, `ceiling_search_indexer`), and derived version/season explanations
(`version_ceiling`). Sightings aggregate all matched candidates by season, tier and source, including rejected
qualities and candidates outside the journal sample. Only release names and parsed quality are stored, without
links or credentials. Each aggregate carries its source kind, positive-only flag, first/last sightings and count.

Active and interactive searches and the shadow runner record their successful indexers, best tier and timestamp,
including empty answers. RSS decisions supply sightings, not searches. Imported arr grabs/imports are seeded as
positive non-search evidence, skipping synthetic SeaDex names; unresolvable series episodes are not assigned to a
season. Facts are shared across versions, but absence coverage is counted separately for each version and season.

Search history is bounded per title, season and version when a search is recorded: every search of the last week,
then one per day to 90 days and one per week to two years, nothing older. A bucket keeps its earliest successful search
(else its earliest), so the 90-day span `known` needs survives thinning; it can shift by up to a week. The stored
explanation (`version_ceiling.ceiling`) lists the latest 10 searches; the judgement reads the whole kept history
(`TestSearchHistoryBounded`).

The ladder is TV, DVD, 1080p WEB, BD, 2160p WEB, UHD BD. With positive evidence a ceiling is `likely`; with none it is
`unknown`. `known` needs three distinct successful searches spanning at least 90 days, starting no earlier than the
first sighting of the current highest tier. At least one complete torznab answer in each search supplies disc search
coverage. Failed, cached, shared, truncated and episode-only answers cannot establish absence. SeaDex, DMM, streaming
sources, catalogs, TMDb and arr history alone cannot establish it. A newly seen higher tier starts a new absence
window. `versions.<key>.ceiling.disc_only: true` ignores WEB/TV tiers for that version; it defaults to false, preserving
the profile's preference for 2160p WEB over a 1080p disc.

The display cutoff is the lower of the real profile cutoff and its highest allowed item whose entire quality group
fits the ceiling. An owning arr's order/groups are retained during its ordinary profile import; older imports with
no item order keep the old explanation until their next refresh. A manual tier overrides the computed one (status
`override`); sightings above a pinned tier remain in the evidence. Optional expiry restores the computed result.

Evidence writes recompute locally. Imports mark existing ceilings due; the `ceiling.compute` job takes at most 25
new/due titles each minute, with daily recomputation otherwise. Override and TMDb expiry can bring that check forward.
TMDb physical notes are used only when the ordinary enrichment path recorded their fetch time and the physical date
has passed. Explicit 4K/UHD, Blu-ray or DVD notes are positive evidence. Notes without a fetch time are ignored. Within
six calendar months, ceiling evidence and its local release-note text are purged unless refreshed by the existing
metadata path; a recompute never extends the fetch time. Theatarr's search/RSS sightings are retained.

## Screens and API

Upgrade status uses one vocabulary everywhere for managers (grid, title, season, episode; `web/src/lib/upgrade.ts`
`UPGRADE_WORDS`, docs/design/manage.md "Words"):

| API state | Wording |
|---|---|
| `cutoff_met` | Preferred quality met |
| `best_available` (known or manual ceiling reached) | Best available |
| `best_source_seen` (likely, i.e. provisional, ceiling reached) | Best source seen so far |
| `higher_source_exists` | Better source known; no suitable release found |
| `upgrade_available` | Upgrade found (keeps priority over every ceiling state) |
| `below_cutoff` (fallback) | Preferred quality not met; no better copy found |

`higher_source_exists` needs the ceiling's source to be one the version's profile allows above the held quality (a
known disc does not count for a WEB-only profile); otherwise the file reads by its profile alone (`below_cutoff`, or
`cutoff_met` when met).

Every status opens **Why?** in the shared Modal (`components/title/upgrade-why.tsx`): current against preferred
source, what is reachable under the ceiling, evidence kind (known, provisional, set by hand) and check date, search
coverage per indexer, sources seen, and any manual override (who, when, note); the cutoff line and scores sit there
too, with the ceiling control for managers. Users without `see_files` see only availability and resolution. Ceiling
confidence/evidence additionally requires `manage`; without it the ceiling states read `below_cutoff`.

List rows (`GET /titles`, search, Home rows) carry `upgrade_state` when their availability is `upgrade`: the most
pressing state among the title's versions (upgrade found, better source known, preferred not met, best source seen,
best available), with ceilings applied as on the title page, so the poster badge agrees with the title page. It is
redacted like the title page (`redactRowCeiling`).

`GET /titles/{id}` returns `versions[].upgrade.ceiling` and `upgrade.effective_cutoff` alongside the existing
`upgrade.state`. The ceiling states are `best_available`, `best_source_seen` and `higher_source_exists`. The ceiling has
`tier`, `label`, `status`, `checked_at`, `next_check_at`, `evidence`, `searches` (including each successful indexer and
whether its answer was complete), and an optional `override`. `GET /titles/{id}/episodes` puts the same `upgrade`
object in each season/version count and episode cell; `GET /titles/{id}/episodes/{eid}` uses the same explanation.
These ceiling fields and confidence states are stripped for callers without the required permissions.
Ceiling data remains available before a file arrives, so managers can review and clear overrides on missing versions.

`PUT /titles/{id}/ceiling` accepts `{season?, tier, note?, expires_at?}`; `DELETE /titles/{id}/ceiling?season=` clears
it. Both require `auth.PermManage` and normal session CSRF protection. Movies omit season; series require an existing
season, including zero for specials. A tier is `tv`, `dvd`, `web_1080`, `bd`, `web_2160` or `uhd_bd`; expiry is an ISO
timestamp and the optional note is limited to 2,000 bytes. Both operations return `{ok: true}` and recompute locally,
without queuing any search. Managers can set/clear overrides from the title and season views, with a manual link to
blu-ray.com. The library filter `availability=upgrade` ("Preferred quality not met") and Home's row of the same name
keep their profile semantics: a best available title is still in them, and its badge says so.

The demo includes movie and anime-season examples of known, likely, higher-source and unknown ceilings. `schema.ts`
is generated from `internal/web/openapi.json` during the UI build and is not checked in.
