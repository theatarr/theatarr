// The decision journal's codes in words (internal/journal: entry kinds in entry.go, sources in build.go, check codes in
// checks.go). The raw code stays in the element's title.
import { wordsFor } from "./code-words";

const KINDS: Record<string, string> = {
  grab: "Grab",
  upgrade: "Upgrade grab",
  plan: "Dry run",
  search: "Search",
  research: "Search after a failure",
  failure: "Failure",
  stall: "Stalled",
  blocklist: "Blocked",
  import: "Import",
  link: "Link",
  verdict: "Verdict",
  action: "By hand",
};

/** An entry's kind: "research" → "Search after a failure". */
export const journalKindWords = (k: string | null | undefined) => wordsFor(KINDS, k);

/** The kinds the list's filter offers, in its order. */
export const JOURNAL_KINDS = ["grab", "upgrade", "plan", "search", "research", "failure", "stall", "blocklist", "import", "link", "verdict"] as const;

const SOURCES: Record<string, string> = {
  theatarr: "Theatarr",
  router: "Download router",
  library: "Library",
  web: "Web UI",
  journal: "Journal verdict",
  queue: "Queue",
  ui: "Activity",
  provider: "Provider",
  ready_timeout: "Ready timeout",
};

/** Who wrote an entry: "theatarr" → "Theatarr"; "arr:radarr_4k" → "Radarr (radarr_4k)". */
export function journalSourceWords(s: string | null | undefined): string {
  const inst = s?.startsWith("arr:") ? s.slice(4) : "";
  if (inst) return `${inst.includes("sonarr") ? "Sonarr" : inst.includes("radarr") ? "Radarr" : "Sonarr/Radarr"} (${inst})`;
  return wordsFor(SOURCES, s);
}

const FLAGS: Record<string, string> = {
  size_outlier: "Unusual size",
  language: "Language",
  file_audio_language: "Audio language",
  episode_mapping: "Episode mapping",
  title_mismatch: "Title does not match",
  year_mismatch: "Year does not match",
  sample_or_extras: "Sample or extras",
  file_runtime: "Runtime",
  file_resolution: "Resolution",
  upgrade_lower_score: "Upgrade scored lower",
  upgrade_lower_quality: "Upgrade of lower quality",
  repeated_failures: "Repeated failures",
  download_stuck: "Download stuck",
  dead_link: "Dead link",
  library_guard: "Library guard note",
  unparseable: "Name not understood",
};

/** A check's flag: "dead_link" → "Dead link". */
export const journalFlagWords = (f: string | null | undefined) => wordsFor(FLAGS, f);
