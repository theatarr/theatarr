// Words for the codes the server stores (docs/design/ui.md "Codes in words"): each domain keeps a small map in its own
// file (rejection-words.ts, evidence-words.ts, tmdb-words.ts, anime-words.ts, router-words.ts, torrent-words.ts,
// error-words.ts; job kinds in jobs.ts). A code no map knows is shown as a readable form of itself, never blank; the
// raw code stays in the element's title where an admin may need it.

/** Abbreviations kept in capitals; every other word is lower-cased. */
const ACRONYMS = new Set(["TV", "TVDB", "TMDB", "RSS", "RD", "AD", "TB", "OVA", "ONA", "API", "VPN", "URL", "ID", "HTTP", "DMM", "UHD", "HDR", "BD", "DV", "CF", "SD", "HD", "4K"]);

/** A code as words: "uncached_deleted" → "Uncached deleted", "DiskHigherPreference" → "Disk higher preference",
 *  "inCinemas" → "In cinemas", "TVDB_SPECIAL" → "TVDB special"; known abbreviations (ACRONYMS) stay in capitals. Empty for an empty code. */
export function readableCode(code: string | null | undefined): string {
  if (!code) return "";
  const words = code
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/([A-Z]+)([A-Z][a-z])/g, "$1 $2")
    .split(/[\s_.:-]+/)
    .filter(Boolean)
    .map((w) => (ACRONYMS.has(w.toUpperCase()) ? w.toUpperCase() : w.toLowerCase()));
  if (!words.length) return code;
  const s = words.join(" ");
  return s[0].toUpperCase() + s.slice(1);
}

/** The words a map has for a code, else the code made readable. */
export function wordsFor(map: Readonly<Record<string, string>>, code: string | null | undefined): string {
  if (!code) return "";
  return map[code] ?? readableCode(code);
}
