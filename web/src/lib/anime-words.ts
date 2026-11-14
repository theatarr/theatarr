// The anime mapping's codes in words (internal/metadata/anime): entry formats (AniList's, plus TVDB_SPECIAL),
// mapping statuses, an entry's role in a watch order and the order kinds. Unknown values are made readable.
import { readableCode, wordsFor } from "./code-words";

const FORMATS: Record<string, string> = {
  TV: "TV series",
  TV_SHORT: "TV short",
  MOVIE: "Movie",
  SPECIAL: "Special",
  OVA: "OVA",
  ONA: "ONA",
  MUSIC: "Music video",
  TVDB_SPECIAL: "TVDB special",
};

/** An entry's format: "TVDB_SPECIAL" → "TVDB special". */
export const animeFormatWords = (f: string | null | undefined) => wordsFor(FORMATS, f);

const MAPPING: Record<string, string> = {
  auto: "Matched",
  arbitrated: "Matched (lists disagreed)",
  conflict: "Conflict",
  heuristic: "Matched by date and title",
  override: "Set by override",
  rejected: "Not matched",
};

/** A mapping status: "auto" → "Matched", "heuristic" → "Matched by date and title". */
export const mappingWords = (s: string | null | undefined) => wordsFor(MAPPING, s);

/** The statuses of a franchise's entries, "Matched 5 · Matched by date and title 1". */
export const mappingCounts = (statuses: Readonly<Record<string, number>> | null | undefined) =>
  Object.entries(statuses ?? {}).map(([k, v]) => `${mappingWords(k)} ${v}`).join(" · ");

const ROLES: Record<string, string> = {
  main: "Main",
  recap: "Recap",
  side: "Side story",
  sequel: "Sequel",
  prequel: "Prequel",
  "alternate-in-story": "Alternate version",
};

/** An entry's role in an order: "side" → "Side story". */
export const roleWords = (r: string | null | undefined) => wordsFor(ROLES, r);

const SOURCES: Record<string, string> = {
  "tvdb-only": "TVDB only",
  anidb: "AniDB",
  anilist: "AniList",
  anibridge: "AniBridge",
  fribb: "Fribb's lists",
  kitsu: "Kitsu",
  manami: "Manami",
  "anime-lists": "Anime-Lists (ScudLee)",
  seadex: "SeaDex",
  thexem: "TheXEM",
};

/** Where a mapping came from: "tvdb-only" → "TVDB only". */
export const animeSourceWords = (s: string | null | undefined) => wordsFor(SOURCES, s);

/** An order's tab label: "release" → "Release order". */
export const orderWords = (k: string | null | undefined) => (k ? `${readableCode(k)} order` : "Order");
