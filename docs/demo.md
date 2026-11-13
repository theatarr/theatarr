# Demo data

`theatarr demo seed` builds a database full of invented titles, so Theatarr's UI can be tried,
screenshotted, documented and reviewed without a real library. Nothing in it is real: no title, person, network,
studio, release group, path or id comes from a live service or a real install. Code: `internal/demo`,
`cmd/theatarr/demo.go`.

## Run it

```
make ui && make build                                  # the SPA is embedded at build time; Node 20
mkdir -p demo/policy && cp -r examples/policy/. demo/policy/ && cp examples/demo/*.yaml demo/policy/
bin/theatarr demo seed -db demo/theatarr.db -policy demo/policy
bin/theatarr serve -db demo/theatarr.db -policy demo/policy        # with the network off (below)
```

`demo/policy` is the example policy with the demo overlay (`examples/demo`) copied over it, which gives every series
a second copy (below). `examples/policy` alone works too, with one copy per series. `scripts/devserver.sh` mounts the
overlay into the image's example policy the same way.

Then open `http://127.0.0.1:7700` and sign in with any demo account below.

`theatarr demo seed` flags:

| Flag | Default | What |
|---|---|---|
| `-db FILE` | required | The database to create. An existing file is refused unless `-force` |
| `-policy DIR` | `$THEATARR_POLICY`, else `examples/policy` | The policy Theatarr will serve with: its versions, library roots and providers shape the data |
| `-seed N` | 1 | Picks the generated details (dates, sizes, hashes, groups) |
| `-now T` | the current minute | The anchor, `YYYY-MM-DD` (noon UTC) or RFC 3339. Titles are added before it; release and air dates fall around it; downloads are minutes old |
| `-art` | true | Write generated posters and backdrops into the policy's image cache |
| `-force` | false | Replace an existing file |

The same `-seed` and `-now` give the same rows (the command prints the `-now` it used). Password hashes and the web
signing secret differ between runs, by design. Seed with no `-now` just before serving, so the in-progress downloads
look minutes old.

### Network off

The demo needs no network, and it should have none: the example policy names real hosts (indexers, debrid APIs,
TMDb), and `theatarr serve` tries them on its schedules. Offline, each attempt fails
at once and nothing leaves the machine. Two ways:

- **Linux network namespace** (loopback only; the browser runs in the same namespace):

  ```
  sudo ip netns add theatarr-demo && sudo ip netns exec theatarr-demo ip link set lo up
  sudo ip netns exec theatarr-demo bin/theatarr serve -db demo/theatarr.db -policy demo/policy -listen 127.0.0.1:7700
  sudo ip netns exec theatarr-demo chromium --headless ...     # or a Playwright script
  sudo ip netns delete theatarr-demo                            # afterwards
  ```

- **Container** (`make image` builds `theatarr:VERSION`; the `theatarr:local` image from
  [`examples/compose.yaml`](../examples/compose.yaml) works the same. The image ships the example policy at
  `/usr/share/theatarr/examples/policy`, without the overlay: mount its two files into that directory for both
  commands, as `scripts/devserver.sh` does):

  ```
  P=/usr/share/theatarr/examples/policy
  O="-v $PWD/examples/demo/versions-theatarr.yaml:$P/versions-theatarr.yaml:ro -v $PWD/examples/demo/library.yaml:$P/library.yaml:ro"
  docker run --rm --network none $O -v theatarr-demo:/data theatarr:VERSION demo seed -db /data/theatarr.db -policy $P
  docker run -d --name theatarr-demo --network none $O -v theatarr-demo:/data theatarr:VERSION serve -policy $P -listen 127.0.0.1:7700
  docker run --rm --network container:theatarr-demo ...        # a browser image sharing that loopback
  ```

What an offline `theatarr serve` shows, and why: **VPN guard off** in the top bar (the example policy ships the egress check off),
the RSS job (**Check RSS feeds**, `shadow.rss`) failing after a few minutes (the indexers cannot be reached offline), and a few warnings in the log (the mount, the zurg log, TRaSH formats).
Everything else is the seeded data. The seeder itself needs no network, so `demo seed` runs with `--network none` too.

### Artwork

Posters and backdrops are drawn by the seeder (a banded sky and hills; on posters also a disc and the title in a
pixel font, since the title page lays its text over the backdrop) and written as PNG into the image cache in its own layout, `{ui.images.dir}/{size}/{first two chars}/{file}`,
at the sizes the UI asks for (posters w92, w185, w342, w500; backdrops w780, w1280; under 3 MB in all). The `/img`
proxy serves them as cached art, so no image is ever fetched. Logos are left out: the title page falls back to text.

The cache directory is the policy's `ui.images.dir`, `/data/images` in `examples/policy`. That matches the container,
whose database lives in `/data`. Elsewhere, either copy the policy and point `ui.images.dir` at a writable
directory, or seed with `-art=false`. If the directory cannot be written, the seeder says so and leaves the titles
without art paths, and the UI shows the generated SVG (title and year on a gradient) without fetching anything.

## Demo password

All four local accounts share `demo-only-not-a-secret` (`demo.Password`): `admin`, `demo-manager`, `demo-auto` and
`demo-limited`. The command prints the usernames and password after every seed. `demo-manager` has the manager role;
`demo-auto` is a user whose requests auto-approve; `demo-limited` is a user restricted to one movie and one series
version. The password is public and exists only for demo databases. Never use it, or a demo database, for a real
instance. The seeder stores it the way `theatarr admin reset-password` does (argon2id), so changing it works as usual.

## What is in it

| Area | Contents |
|---|---|
| Titles | 59 movies, 5 anime films, 15 series, 14 anime series, 93 in all (`internal/demo/catalog.go`), with overviews, taglines, genres, runtimes, ratings and votes, certifications, networks, status, and TMDb release dates (theatrical, digital, physical) |
| Versions | Every version in the policy that fits each title (`movie-remux` and `movie-web`, `anime-movie`, `tv-2160p` and `tv-2160p-theatarr`, `anime-tv` and `anime-tv-theatarr` with the demo overlay), in every state: wanted, filled (below the cutoff), cutoff met; titles complete, waiting for an upgrade, part-way (a version missing, or seasons part-way), missing, upcoming, and not monitored. Every series has two copies (below) |
| Episodes | 531: weekly air dates, a special for longer shows, absolute numbers for anime; airing shows have the rest of their season in the coming weeks |
| Upcoming | Movie releases in the next weeks and months, and new episodes, so Home's Upcoming row and the calendar fill |
| Files | On the Watch-only libraries, files kept from a former Sonarr/Radarr instance with plausible release names, qualities, sizes, languages, media info and paths in the library's root; season packs; on the Live libraries, Theatarr's own links; the registry behind them (content, files with probe results, provider presence on RD, AD or TB) |
| Downloads | The router in every state: queued (one waiting for a free slot), adding, added, downloading (one stuck for over a day), ready (waiting for the mount, and dry run), linking, imported, failed (no provider took it; stalled three times; removed from the queue; rejected by the library's sample guard). Each with its search (acquire run and journal snapshot: chosen release, runners-up, rejections), provider attempts (accepted, refused, skipped, error, deferred) and cache evidence |
| Library | Library imports in each state, links for Theatarr's own imports, three dead links with the dangling report, a relink, a replaced link in the trash, a Plex scan and a mount alert |
| Blocklist | Stalled, provider, library guard and verdict entries, each followed by a re-search |
| Journal | Suspect flags of several kinds (dead link, download stuck, repeated failures, sample or extras, year mismatch), a `wrong` verdict that blocklisted a release and queued a re-search, and an `ok` verdict on a dead link |
| Jobs | Done (a day of syncs, imports, RSS reads, art runs, searches), queued, failed and needing attention, failed and resolved by a later success, failed and retried with the retry done, and dismissed |
| System | Budget windows (hour and day), back-offs (active and expired), provider pacing, provider sync runs (one failed with `AUTH_BLOCKED`), the TVDB sync, three API keys (hashes of random values nobody holds; one revoked), the views' profiles and tags, view commands and offers |
| Anime | The franchise `aokaze-requiem`, stored through `animestore.Save`: two seasons, two films (one upcoming), an OVA mapped by a heuristic and a short with conflicting mappings (both in the review queue), an arbitrated mapping, release, watch and story orders, and a planned watch-order playlist |
| Users | The admin, a manager and two users with local passwords; role presets, auto-approval and request-version restrictions; requests in every status, including merged requests and TMDb movie and series titles outside the library |
| Settings | The DB settings store bootstrapped from the demo policy with invented `demo-only-*` credentials (never real keys), then two edits (one indexer disabled with its credential cleared, a priority change, a budget change) and two audited test results; generation 3, so Settings › Indexers/Download clients and Advanced › Request limits/Settings history have data. Import preview needs the policy's `*_env` variables set, which the demo leaves unset |

## Two copies

Many installs keep a title in two or more libraries. Movies have two in the example policy (`movie-remux` and
`movie-web`, both Watch only). For series the example has one library per anime flag, so the demo adds a second
through an overlay, `examples/demo`, which is not a policy on its own:

- `versions-theatarr.yaml`: `tv-2160p-theatarr` beside `tv-2160p` and `anime-tv-theatarr` beside `anime-tv`: a
  **Live** library of the same kind (`mode: active`) with the same profile, the built-in naming and roots under
  `/media/library`.
- `library.yaml`: the example's `library.yaml` with those two roots added to `library.versions` (a Live library
  needs roots of its own, and `library` is one section, so the file replaces the example's). A test keeps it equal to
  the example's otherwise.

The example policy itself is left as it is: its version set is pinned by tests across the code base.

The example's libraries are Watch only and hold the files of a former Sonarr/Radarr instance, as an install that
used the removed periodic import keeps them. The seed treats a library `<key>-theatarr` beside such a `<key>` as
Theatarr's own (`internal/demo/copies.go`): the same titles and monitoring, links brought in by the
[one-time import](migration.md#import-from-sonarrradarr) (`adopted from <instance> <path>`, to the same mount
targets), and its own downloads for the rest. `copyPlans` makes the two differ:

| Series | Watch-only library | Live library (`-theatarr`) |
|---|---|---|
| The Harrowgate Files | complete | season 3 not adopted; its pack queued |
| Brindlewood Bakers | complete | season 2 missing; its pack failed (stalled, blocklisted, re-searched) |
| Kindling Hall | season 3 part-way | complete: the missing episodes imported by Theatarr |
| Wickmere | complete, all monitored | seasons 1 and 2 not monitored and not adopted |
| The Pellbrook Inquiries | season 1 below the cutoff | the same files, and an upgrade of S01E02 downloading |
| The Quiet Ledger | missing | missing; a download failed (no provider took it) |
| Lantern Street Station, Ferrous Bay | season part-way | the same; the gap added on AllDebrid, or downloading |
| Saltmarsh Rangers, Orbitfall | airing, aired episodes | the same; the newest episode is Theatarr's own import |
| Kiraboshi Mechanics (anime) | complete | episodes 13 to 24 missing; episode 13 downloading |
| Tsuratsura Detective Office (anime) | complete | season 2 missing |
| Yumegatari Express (anime) | part-way | the same; the next episode waiting for the mount |
| Shirokane Blade Academy (anime) | part-way | the same; the next episode queued |
| Kagerou Postal Service (anime) | airing | the same; the newest episode is Theatarr's own import |
| The others | as their scenario | the same files, adopted |

The series downloads in flight are Theatarr's own, so they belong to the Live library; seeded with `examples/policy` alone
they go to the series' only version, as before. Anime keep their absolute numbers on both copies.

For browser checks (seeded with the overlay and the default `-seed 1`): **Kindling Hall** (title 72) is in both
`tv-2160p` (Watch only, files from the former instance) and `tv-2160p-theatarr` (Live); on the Live library, the
episodes the Watch-only library lacks are Theatarr's own imports (links with no `adopted from`). **Saltmarsh Rangers** (69) and
**Orbitfall** (71) have the same pair, with only the newest episode imported by Theatarr.

## Ids

Every external id sits in a range real ids do not reach (`internal/demo/demo.go`):

| Id | Range |
|---|---|
| TMDb movie | 900000001 to 900000059, anime films 900000501 up |
| TMDb TV | 900100001 to 900100015, anime 900100501 up |
| TVDB series | 900200001 to 900200015, anime 900200501 up |
| IMDb | `tt99000001` up |
| AniList | 900300001 up (franchise entries 900300001 and 900300101 to 900300105) |
| AniDB, Kitsu, MyAnimeList | 900400000, 900500000, 900600000 plus the AniList offset |

Info hashes, provider torrent ids and release groups are generated. Internal row ids start at 1.

## Tests

`internal/demo/demo_test.go`:

- **TestDemoScreens** seeds a database and calls every API the SPA's screens use (titles and facets for each grid,
  discover, a movie, a series and an anime title page with episodes, activity now, history and blocked, Continue,
  calendar, journal and an entry, jobs, registry and one content, anime
  franchises, review and playlists, gateway, imports, sources, policy, keys, versions, search). Each must answer
  200 with its lists filled, the activity must hold every state in hand, and posters and backdrops must come from
  the seeded cache as PNG. The image fetcher and `http.DefaultTransport` are replaced by a recorder that fails every
  call, and the test fails if anything was called.
- **TestDemoCoverage** checks that every state above is in the data, upcoming dates exist, and every title's ids
  are in the demo ranges.
- **TestDemoUsersRequests** checks every request status, signs in as each account, verifies users see only their own
  requests, and checks that a user sees no file details on a title page.
- **TestDemoSettings** checks the settings generation, records, cleared credential and that no audit row holds a credential.
- **TestDemoDeterministic** seeds twice with one seed and once with another, and compares every table.
- **TestDemoTwoCopies** checks that every series has a Watch-only copy with the former instance's files and a Live
  copy with no such records,
  adopted links under the copy's root, links that match the copy's episode states, seasons on one copy only, series
  complete on one copy and part-way on the other (both ways), the copy's downloads in every state (an import waiting
  for the mount among them), an upgrade in flight on an episode the copy has a file for, one series whose copies'
  monitoring differs, and absolute-numbered anime files on both copies. **TestDemoTwoCopiesScreens** reads the
  episode grid of a series and an anime series: two copies, each with files, absolute numbers for the anime.
- **TestDemoOverlayLibrary** keeps `examples/demo/library.yaml` equal to the example's but for the Live
  libraries' roots; **TestDemoExamplePolicy** seeds with `examples/policy` alone (one copy per series).

All tests but the last seed with the overlay, as the dev server does.
