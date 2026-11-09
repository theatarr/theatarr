TVDB v4 answers recorded from api4.thetvdb.com on 2026-10-26 (GET only) for three public series: Severance (371980),
Slow Horses (372264) and Frieren (424536). Each file is one answer: the extended series record (meta=translations,
short=true), and page 0 of the default, absolute and (Frieren) English episode listings.

Trimmed with jq to the fields the client reads: overviews, images, artworks, characters and companies are dropped,
remote ids are kept for IMDb and TMDb only, name translations for eng and jpn only, and paging links lose their URLs.
No keys, tokens or URLs. Tests edit them in memory to simulate TVDB changes (new seasons, renumbering, removals).
