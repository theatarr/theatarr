// Where the "Why?" window's quality evidence was seen (ceiling_sighting.source, internal/ceiling): "<origin>:<name>",
// e.g. "rss:dmm", "search:prowlarr-therarbg", "arr_history:sonarr_4k_zurg", "tmdb:US". The origin is said in words
// and the indexer, instance or country follows in brackets.
import { readableCode } from "./code-words";

const ORIGINS: Record<string, string> = {
  search: "Search",
  shadow_search: "Watch-only search",
  live: "Search",
  rss: "RSS",
  arr_history: "Sonarr/Radarr history",
  tmdb: "TMDb release note",
  manual_catalog: "Disc catalog",
  seadex: "SeaDex",
  ceiling: "Set by hand",
  manual: "Set by hand",
};

/** "RSS (dmm)", "Sonarr/Radarr history (sonarr_4k_zurg)"; an origin no map knows is made readable. */
export function evidenceSourceWords(source: string | null | undefined): string {
  if (!source) return "";
  const i = source.indexOf(":");
  const origin = i < 0 ? source : source.slice(0, i);
  const rest = i < 0 ? "" : source.slice(i + 1);
  const words = ORIGINS[origin] ?? readableCode(origin);
  return rest ? `${words} (${rest})` : words;
}

/** The note for evidence that only shows a release exists (a feed, a catalog), not that it is good to grab. */
export const POSITIVE_ONLY_WORDS = "seen, not confirmed";
