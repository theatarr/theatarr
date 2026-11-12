# Parallel libraries

This guide moved into [Moving from Sonarr and Radarr](migration.md), which is now the one path from an arr to
Theatarr. A parallel library is the follower library of its stage 3.

| Was here | Now |
|---|---|
| The introduction | [migration.md, stage 3: a follower library beside the arr](migration.md#3-a-follower-library-beside-the-arr) |
| What a parallel library gets | [migration.md, What a follower gets](migration.md#what-a-follower-gets) |
| Policy | [migration.md, Policy](migration.md#policy) |
| Adopt (`theatarr library adopt`) | The commands and what they do: [migration.md, Adopt and refresh](migration.md#adopt-and-refresh). The rules in full: [design/library.md (internals)](design/library.md#adopt-theatarr-library-adopt) |
| Refresh (`theatarr library adopt -refresh`, and after every arr.import) | [migration.md, Adopt and refresh](migration.md#adopt-and-refresh), and [design/library.md (internals)](design/library.md#refresh-theatarr-library-adopt--refresh-and-after-every-arrimport) |
| Before grabbing: adopt and refresh each follower | [migration.md, Adopt and refresh](migration.md#adopt-and-refresh) and step 5 of [Before the trial](migration.md#before-the-trial) |
| Plex | Step 3 of [migration.md, Before the trial](migration.md#before-the-trial) |
| Going live check | `theatarr preflight`; `theatarr parallel compare` is in [migration.md, stage 2](migration.md#2-shadow-compare-with-the-arr) |
| Cutover for followed libraries | [migration.md, stage 5: hand over clients and Plex, retire the arr](migration.md#5-hand-over-clients-and-plex-retire-the-arr) |
| How to stop | [migration.md, Stopping or removing a follower](migration.md#stopping-or-removing-a-follower) |
| Changing a root with links | [migration.md, Changing a root with links](migration.md#changing-a-root-with-links) |
| States and the cutoff | [migration.md, States and the cutoff](migration.md#states-and-the-cutoff) |
| Known limits | [migration.md, Known limits](migration.md#known-limits) |

## Changing a root with links

The library editor refuses to change a root with active links. The procedure is in
[migration.md, Changing a root with links](migration.md#changing-a-root-with-links).
