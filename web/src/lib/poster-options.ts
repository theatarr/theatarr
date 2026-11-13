// The posters' options (docs/design/ui.md "Library views"): poster size and the details under each poster, with the
// stored choice read back. Moved from lib/copies.ts, which re-exports it.

// ---- Poster options ----

export type PosterSize = "small" | "medium" | "large";
export type PosterDetail = "year" | "next" | "network" | "quality" | "size" | "added" | "rating";

/** The details under each poster, in this order. Quality reads every library's ("4K: Remux-2160p · WEB: WEBDL-2160p");
 *  Size is the title's total; Added is the day the title was added to Theatarr. The old copy_size is dropped on read. */
export const POSTER_DETAILS: { id: PosterDetail; label: string; seeFiles?: boolean }[] = [
  { id: "year", label: "Year" },
  { id: "next", label: "Next episode / release" },
  { id: "network", label: "Network / studio" },
  { id: "quality", label: "Quality", seeFiles: true },
  { id: "size", label: "Size", seeFiles: true },
  { id: "added", label: "Added" },
  { id: "rating", label: "Rating" },
];

export type PosterPrefs = { size: PosterSize; details: PosterDetail[] };

export const DEFAULT_POSTERS: PosterPrefs = { size: "medium", details: ["year"] };

export function normalizePosters(p: unknown): PosterPrefs {
  if (!p || typeof p !== "object") return DEFAULT_POSTERS;
  const o = p as { size?: unknown; details?: unknown };
  const size = o.size === "small" || o.size === "large" || o.size === "medium" ? o.size : DEFAULT_POSTERS.size;
  const details = Array.isArray(o.details)
    ? [...new Set(o.details.filter((d): d is PosterDetail => POSTER_DETAILS.some((x) => x.id === d)))]
    : DEFAULT_POSTERS.details;
  return { size, details };
}
