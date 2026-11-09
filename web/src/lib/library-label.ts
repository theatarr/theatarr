/** Matches internal/web/library.go labelFor: the library name used on title pages. */
export function libraryLabel(key: string, profile: string): string {
 const words: Record<string, string> = { "2160p": "4K", "4k": "4K", uhd: "4K", "1080p": "1080p", "720p": "720p", web: "WEB", remux: "Remux", bluray: "Blu-ray", anime: "Anime", hdr: "HDR" };
 const labels = [...new Set(`${key}-${profile}`.toLowerCase().split("-").map(t => words[t]).filter(Boolean))];
 if (labels.includes("Anime")) return "Anime";
 const resolution = (label: string) => ["4K", "1080p", "720p"].includes(label);
 return [...labels.filter(resolution), ...labels.filter(l => !resolution(l))].join(" ") || key;
}
