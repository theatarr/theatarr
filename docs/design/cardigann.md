# Native indexers: the cardigann kind, the definition catalog and trawl

Theatarr runs Prowlarr's Cardigann definitions itself, so a site does not need Prowlarr in front of it. The engine is a
port of Mediarium's (AGPL-3.0); `internal/indexer/cardigann`'s package doc lists what was ported, what was left out
and every deviation. `NOTICE` lists the sources.

**Nothing is on by default.** Without `cardigann.definitions` nothing is fetched and no definition runs. Torznab
feeds (through Prowlarr or not) and every other indexer are unaffected.

| Piece | Where |
|---|---|
| Engine: login, search, rows, downloads, FlareSolverr | `internal/indexer/cardigann` (`engine.go`, `login.go`, `search.go`, ...) |
| Catalog summaries, setting kinds, status | `internal/indexer/cardigann/catalog.go` |
| Definitions sync source and the custom folder | `internal/indexer/cardigann/store.go`, `archive.go`; fetch in `internal/gateway/cardigann.go` |
| Regular expressions (Go's, regexp2 where RE2 refuses one) | `internal/indexer/cardigann/template.go` (`compileDotNetRegex`) |
| The `cardigann` indexer kind | `internal/indexer/cardigann.go` |
| Saved sessions | `internal/settings/cardigann.go`, table `cardigann_session` (migration 00074) |
| Prowlarr migration reader | `internal/indexer/cardigann/prowlarr.go`, `internal/arr` (`Options.Prowlarr`) |
| Catalog API | `internal/web/cardigann.go` |
| Wiring | `cmd/theatarr/cardigann.go` |

## Configuration

```yaml
cardigann:
  definitions:            # turns the catalog on; {} takes the defaults
    repo: Prowlarr/Indexers   # default
    branch: master            # default
    refresh: 24h              # default; at least 1h
  flaresolverr_url: http://trawl:8191   # optional, see "trawl"
  prowlarr:                             # optional, for the migration reader
    url: http://prowlarr:9696           # LAN only, like import.arr
    api_key_env: THEATARR_PROWLARR_KEY

indexers:
  site-native:
    kind: cardigann
    definition: examplesite        # catalog id (an id it replaces works too)
    url: https://site.example/     # the site, one of the definition's links or a mirror
    privacy: public                # a label; the stricter of this and the definition's type is shown
    budget: site-native            # required, and used by no other indexer
    settings: {sort: seeders}      # the definition's non-secret settings
    hosts: [apibay.org]            # further hosts the definition reaches (the catalog lists them)
```

`cardigann` is a settings section, edited in Admin › Settings › Indexers › Catalog (seeded once from the YAML;
[settings](settings.md#sections-and-views)): a save applies without a restart (the
catalog is made when `definitions` is first set, a changed source is read at once, and indexers already built keep
working), and a change of `definitions` asks for confirmation. Indexers of kind `cardigann` can also be settings
records (the config takes the same fields); the settings form schema offers the kind with `definition`, `settings`
and `hosts`, and the indexers page adds them from the catalog ([settings](settings.md#settings-ux)).

Validation refuses: a cardigann indexer without `definition`, without the catalog (`cardigann.definitions`), without
a budget of its own, with a credential-named setting in `settings` (password, cookie, apikey, passkey, token, ...),
or with a `hosts` entry that is not a host name; `definition`, `settings` and `hosts` on other kinds; a refresh under
1h; a malformed repo or branch; a Prowlarr URL off the LAN or without `api_key_env`.

### Credentials

The indexer's secret (a settings secret, or the variable named by `api_key_env`) is a JSON object of the
definition's secret settings: `{"username": "...", "password": "..."}` or `{"cookie": "..."}`. The definition's
password-type settings and credential-named ones must be there, never in `settings`; a search with one in `settings`
is refused. Policy validation knows the names only; a settings save or import is also checked against the catalog's
entry for the definition, so a password-type setting under any name (`pass`, `uid`) is refused before it reaches
plain settings, exports or the settings history. New or changed plain values are refused with
an error naming the setting unless the loaded definition explicitly declares it non-secret. An unknown definition,
an unavailable catalog, or a setting absent from that definition cannot verify the value; load the definition
before supplying it. This applies to API saves, imports and first bootstrap, including callers bypassing runtime
preparation.

Legacy values saved before this check remain usable without a migration. On load and subsequent saves, exports
mask password-type and unverified settings (all settings while their definition is unavailable); a newly available
catalog is consulted even for an already loaded snapshot. Audit writes and reads mask every Cardigann setting
value, including older audit entries, preserving setting names. Verified non-secret settings remain in exports.
This masks output rather than moving legacy values into secrets: old database rows, generation snapshots and
backups retain their original contents under the database's existing 0600 protection. Re-enter a legacy password
in the indexer's JSON secret and remove it from plain settings to relocate it. Unchanged legacy values do not block
unrelated saves; replacing one requires the same validation as a new value.

The error for a credential that is not a JSON object does not repeat it. Each secret value (not only
the whole JSON) is registered with the gateway's log redactor, and every error a search, test or download returns
has the secret settings masked (as given, and escaped for a query and for a path). Errors never carry the site's reply: a page without the
expected rows says so with its size only, since a body can hold a passkey.

## How a search runs

1. The query becomes keywords: movie `title year`, series `title S01E02` or `title S01`, text as typed, RSS none
   (see [RSS](#rss)).
   Categories: 2000 for a movie, 5000 for a series, unless the query names them. A query by ids alone (no title) is
   not supported.
2. The definition is loaded from the catalog.
3. The engine signs in when the definition has a login and the session has not, then fetches the search paths.
   **Every request is one gateway request** of the indexer's target (`Target.NoFollow`): the engine follows each
   redirect itself, so every hop gets the fresh VPN check (fails closed), the allowlist (the indexer's `url` and
   `hosts` only), the indexer's own budget and its back-off. The engine's browser User-Agent replaces `theatarr/<version>`.
   A 307/308 that would send a posted form (a login) again to another host is refused. Counts (seeders, leechers,
   grabs) read dots between groups of three as thousands, as Prowlarr does: `1.234` is 1234, `5.0` is 5.
4. Releases are stamped as for every source: `Privacy` is the stricter of the policy and the definition (public <
   semi-private < private; a missing or unknown type counts as private), a label only.
   Download links stay in memory (`indexer.Link`).

**Regular expressions** (the `regexp` and `re_replace` filters, the `re_replace` template function) are written
for .NET, which Prowlarr runs them on. Each pattern is compiled with Go's regexp when it accepts it (with `\u` and
`\p{IsBlock}` rewritten), so most run on the linear-time engine with its size limit. A pattern Go refuses
for its syntax alone (look-behind, look-ahead, back-references, atomic groups) runs on regexp2, the .NET engine
already used for the arr parsers (`internal/netregex`): the pattern as written, no options (Prowlarr's `new
Regex(pattern)`), .NET replacements (`$1`, `$0`, `${name}`) and a 250 ms timeout per match. A pattern both engines refuse
is unsupported (in a `regexp` filter it makes the definition unsupported; an `re_replace` leaves the text
unchanged), and one too large for RE2 is refused, never handed to regexp2.

A release with neither an info hash nor a magnet carries only the site's download link. Grabbing goes by hash, so
such a release is listed but not grabbed (see [Limits](#limits)).

### Sessions

Each indexer has one session: cookie jar, login state and, after a Cloudflare check, trawl's User-Agent. It is saved
in `cardigann_session` after a login or a passed check and restored on start for 7 days, if the definition, URL, normalized sorted host list
and settings (credential included) are unchanged; otherwise the site is signed in to afresh. Only a real login is
saved as one: cookies saved after a Cloudflare check alone leave the restored session signed out, so it logs in. The value holds
credentials. It is kept the way the settings secrets are: plain text in the database, which is restricted to 0600.
No API returns it. Theatarr encrypts no secret at rest.

Series searches use the same scene-name and numbering criteria as Torznab: daily episodes search
`Title YYYY MM DD`, anime episodes search each scene title with its absolute number (and standard SxxEyy), and
anime season aliases that already name the season need no additional season suffix. A plain keyword query made
without episode metadata does not establish season search coverage or delay the next acquisition search. Partial answers retain their releases and
report incompleteness; a search still redirected after five hops fails instead of reporting no results.

A session binds its gateway transports after taking its flow lock, so a waiting settings generation cannot change
the transport of a running search, login or download. Host additions and removals start a new session immediately.
Password setting types are recognized without case sensitivity; missing-login-form and invalid-Torrentio-config
errors omit addresses and unknown option text that could contain credentials.

## RSS

The RSS sync (`shadow.rss`, [shadow.md](shadow.md#rss-shadowrss)) reads the catalog sources the shadowed and
active versions list, with the Torznab feeds, under the same rules: the indexer's budget and back-off, the VPN check
on every hop, and picks only planned (shadow) or
routed (active versions, with grabbing on).

- **The feed** is the definition's search without keywords, as in Prowlarr (Cardigann's RSS is an empty search).
  The definitions choose their own "latest" page for an empty `.Keywords` (a recent-items file, the first
  listing pages, or a date window). Categories
  are the versions' (2000, 5000, 5070 for anime), mapped to the site's.
- **How far back:** one search per sync, whatever the definition's paths read. A definition has no offset, so the
  catch-up loop's next page is answered empty without a request (Prowlarr answers an offset on a Cardigann indexer
  with nothing too). When the previous head is not in that search (more new uploads in one interval than the
  definition lists), the run reports the gap; the cursor and seen marks work as for a Torznab feed.
- **Item keys** are the info hash (from the definition's `infohash` field or the magnet), else title and size, so
  they stay the same across syncs on HTML sites too; dates such as "2 hours ago" drift but only bound catch-up. A
  definition sorted by something other than the upload time (a `sort: seeders` setting) still works, since every
  item of the read is decided, but its head no longer marks the newest upload: keep such settings on "created".
- **No RSS:** a definition the engine can't run, one whose site maps no movie or TV category (about a hundred in
  the catalog: music, books, adult), one with no search path for them, or one whose search templates fail without
  keywords (`cardigann.RSSProblem`). The catalog summary says so (`rss`, `rss_problem`), the indexers page shows
  "no RSS: reason" next to such an indexer and in its set-up form, and the RSS run lists it with `skipped`. An RSS
  read of it is refused before any request.

### Replacing Prowlarr feeds (TPB, TheRARBG, Nyaa)

`TestCardigannRSSMatchesTorznab` (`internal/indexer`) is the dry comparison: for the same site pages, the native
read yields the items Prowlarr's Torznab feed listed (titles, hashes, details links, sizes within rounding, dates,
category families, IMDb ids, seeders), with one request for TPB and TheRARBG and two for Nyaa. It runs on
hand-made definitions shaped like the real ones; `THEATARR_CARDIGANN_DEFS=<definitions/v11>` runs it on the real ones
(local files, no traffic), and `THEATARR_CARDIGANN_CUSTOM=<folder>` takes TheRARBG's from a folder of custom definitions.
TheRARBG's case loads it from the catalog's custom folder. All three run natively:

- **Nyaa (`nyaasi`)**: its title fields use look-behind and look-ahead (`(?<![A-Za-z0-9])`), which run on the
  .NET engine (see "Regular expressions" above). The real definition from Prowlarr/Indexers matches.
- **TheRARBG** is not in Prowlarr's catalog (Prowlarr/Indexers blocklists `therarbg.yml`); Prowlarr runs
  it as a custom definition. Copy that file to the custom folder (below).
- A site whose definition calls a separate API host needs that host in the indexer's `hosts`.

## The definition catalog (sync source)

Built like the quality sync's tracked sources. The branch's newest commit is read from GitHub's commits API, and the
archive of that commit from codeload, through the gateway on the `cardigann-definitions` budget (shared by every
settings generation; it does not drain the quality sync's). The archive's newest `definitions/vN` with N ≤ 11 is
taken. An archive with only newer schemas is refused and the catalog stays as it was. Nothing of the repository is
committed or shipped.

The snapshot lives in `<data dir>/cardigann/definitions` (the data dir is the database's folder):
`current/*.yml` is what runs, `held/*.yml` the held updates, `state.json` the commit, schema, times, last error and
holds. A refresh writes a new snapshot beside the old one and swaps it in, so a failed or interrupted refresh keeps
the last good one. An unchanged commit skips the archive. The check runs at start when due, then every `refresh`; a
failure is retried after an hour.

**Updates apply by themselves, except three kinds, held for review** when a configured indexer uses the definition.
The indexer keeps running the definition it had until an admin approves:

| Hold | When |
|---|---|
| `new_required_settings` | it needs a setting it did not need before |
| `removed_in_use` | it is gone from the repository |
| `newly_unsupported` | it now uses something the engine can't run (usenet, CAPTCHA, an unknown filter or login method) |

Approving applies that one update (or the removal); it changes no indexer configuration. A change of the
definition's `type` is not held: privacy is a label.

### Custom definitions

A site the repository does not carry (TheRARBG) runs from the operator's own file:
`<data dir>/cardigann/definitions/custom/*.yml`. Nothing is shipped there (Prowlarr's and Jackett's definitions
are GPL); create the folder and drop the file in yourself, for example the one a Prowlarr instance runs as a custom definition.

- Each file is checked like a synced one: it must parse, and its `id` (not the file name) must be a plain name. One
  that fails is left out with a warning in the log. It then runs, or is unsupported, by the same rules.
- A custom definition with the same id as a synced one wins; the catalog summary marks it `custom`.
- The folder is read at start and at every refresh (`POST /cardigann/refresh`), so a new or changed file needs no
  restart. Nothing about it is held: an edit applies at the next read.
- The sync replaces only `current/` and `held/` (and `state.json`), never their parent, so a sync or an approval
  never touches `custom/` (`TestStoreCustomDefinitions`).

### Catalog statuses

`runs`, `needs_account` (a login or secret settings), `needs_flaresolverr` (the definition says the site uses
Cloudflare), `unsupported` (with the problems). `local` marks a site whose links are all local addresses, `custom`
one from the custom folder.

## API (admin)

Under `/api/theatarr/v1`. With the catalog off, holds returns HTTP 200 and `[]`; the other routes answer 503.
The definitions endpoint still identifies the catalog-off state.

| Route | What |
|---|---|
| `GET /cardigann/definitions?q=&privacy=&status=&language=` | The catalog: summaries (id, name, type, privacy, language, links, settings with secret and required marks but never values, categories, hosts, status, problems) and the sync status |
| `GET /cardigann/definitions/{id}` | One definition |
| `GET /cardigann/holds` | Held updates with reasons and detail; catalog disabled returns HTTP 200 and `[]` |
| `POST /cardigann/holds/{id}/approve` | Apply one held update |
| `POST /cardigann/refresh` | Check the branch now (502 when it fails; the catalog is unchanged) |
| `GET /cardigann/prowlarr` | The migration reader's proposals |

**Test without saving:** the settings API's candidate test, `POST /settings/indexers/test` with `kind: cardigann`
and the fields above (and `secret` for a credential), runs one search through a probe gateway: the VPN check,
budgets and back-offs are the live ones, the candidate's `hosts` are allowed like its URL, and nothing is saved. It
uses a throwaway engine, so a configured indexer's live session is neither used nor changed. The stored credential is
sent only to the saved source's address and hosts: a candidate that changes `url` or `hosts` must send it again.

## Prowlarr migration reader

With `cardigann.prowlarr` set, `GET /cardigann/prowlarr` reads Prowlarr's `GET /api/v1/indexer` (the client can
call nothing else: `arr.Options.Prowlarr`) and proposes one native indexer per Prowlarr indexer. It writes nothing,
to Prowlarr or to the settings.

- `definitionName` is looked up in the catalog (an id a definition replaces counts).
- Privacy is the stricter of Prowlarr's and the definition's.
- The URL is Prowlarr's `baseUrl`, else its first indexer URL, else the definition's first link.
- Non-secret settings that differ from the definition's default come over. A select's numeric value indexes the
  option keys sorted by `Options.OrderBy(x => x.Key)`, as Prowlarr's `CardigannBase.GetBaseTemplateVariables` and
  `IndexerResource.MapCardigannField` do. The default .NET string comparer uses the current culture; Theatarr uses
  English Unicode collation for ordinary option keys (lowercase before uppercase for otherwise equal keys,
  lexical digits and significant punctuation), with stable ties. YAML insertion order only decides equal keys.
  Prowlarr's API does not report its process culture or invariant-globalization override, so an installation using
  a different collation can need a manual setting correction. An option equal to the definition's default is omitted
  (`TestProwlarrSelectOrderMixedCase`, `TestProwlarrSelectCultureOrder`).
  Secret settings are only named: Prowlarr masks them, and they go in the new indexer's credential.
- A suggested key and an own budget (10 per minute) come with it.
- Statuses: `proposed`, `no_definition` (a Prowlarr-native or custom indexer: keep it behind Prowlarr), `unsupported`,
  `usenet`.

The indexers page's add sheet (tab "Migrate from Prowlarr (once)") shows the proposals and writes only the ticked
`proposed` ones, each with its budget and disabled, in no version. Existing Torznab feeds stay as they are; switching versions to native indexers is a separate step
(shadow comparison first).

## trawl (Cloudflare)

Some sites put a Cloudflare browser check in front. trawl (github.com/germondai/trawl, AGPL-3.0) passes it through
a FlareSolverr-compatible `/v1` API; Theatarr uses it when `cardigann.flaresolverr_url` is set. `examples/compose.yaml`
has it as an optional service behind the `trawl` profile (`docker compose --profile trawl up -d`), image
`ghcr.io/germondai/trawl:1.9.3`.

- **trawl's own traffic must leave through the VPN.** It opens the sites itself, so it is indexer traffic: run it in
  the VPN's network namespace (or on a host whose only route out is the VPN), and don't publish its port.
- Theatarr's requests to trawl go through the gateway as the indexer's own: VPN check, allowlist (the
  `flaresolverr_url` host is added) and the indexer's budget. If indexer traffic leaves through a proxy, trawl must
  be reachable through that proxy too. A solve may take up to the solver's own `maxTimeout` (60 s) plus slack, so
  this leg gets its own wait (`Target.Wait`, 90 s) instead of the gateway's 30 s header timeout, and a solve that
  times out does not count as an indexer failure.
- Without trawl, a site showing the check fails with a message saying to set `cardigann.flaresolverr_url`.

## Limits

- `theatarr search` and the other one-shot commands have no catalog: their cardigann indexers answer "the definition
  catalog is not running". Only `theatarr serve` runs it.
- Grabbing a release that has only a site link (resolving it through the engine), and private `.torrent` delivery
  from a cardigann site.
