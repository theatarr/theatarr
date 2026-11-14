/** Matches internal/web/library.go labelFor: a library's default name from its key, profile and kind ("4K WEB",
 * "Anime movies", "Anime series"), or its own name when it has one. The names the server sends (auth/me libraries,
 * lib/library-names) also tell same-named libraries apart; this is the fallback for a key it does not name. */
export function libraryLabel(key: string, profile = "", name?: string, kind?: string): string {
 if (name?.trim()) return name.trim();
 const words: Record<string, string> = { "2160p": "4K", "4k": "4K", uhd: "4K", "1080p": "1080p", "720p": "720p", web: "WEB", remux: "Remux", bluray: "Blu-ray", anime: "Anime", hdr: "HDR" };
 const keyToks = key.toLowerCase().split("-");
 const keyLabels = keyToks.map(t => words[t]).filter(Boolean);
 const labels = [...new Set(keyLabels.includes("Anime") ? keyLabels : [...keyLabels, ...profile.toLowerCase().split("-").map(t => words[t]).filter(Boolean)])];
 if (labels.includes("Anime")) {
  const k = kind || (keyToks.includes("movie") ? "movie" : keyToks.includes("tv") || keyToks.includes("series") ? "series" : "");
  return k === "movie" ? "Anime movies" : k === "series" ? "Anime series" : "Anime";
 }
 const resolution = (label: string) => ["4K", "1080p", "720p"].includes(label);
 return [...labels.filter(resolution), ...labels.filter(l => !resolution(l))].join(" ") || key;
}
