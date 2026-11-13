// The Episodes tab's chosen columns for one library in view (docs/design/ui.md, Title page / Episodes): which columns
// there are, what they mean and which show by default. Kept per user in this browser (lib/prefs.ts,
// theatarr.library.episode-columns.<user>). The checkbox, monitor switch, number, title and the search icons are fixed
// columns, not in the list; the all-libraries matrix has no chosen columns.
import type { ColumnDef } from "@/lib/prefs";

export type EpisodeColumnId = "air" | "status" | "quality" | "size" | "cached" | "score" | "group" | "release" | "file" | "runtime";

export const EPISODE_COLUMNS: ColumnDef<EpisodeColumnId>[] = [
  { id: "air", label: "Air date", hint: "When the episode aired or airs" },
  { id: "status", label: "Status", hint: "Available, Upgrade found, Missing, Not aired or Not monitored; the download under it" },
  { id: "quality", label: "Quality", hint: "The file's quality" },
  { id: "size", label: "Size", hint: "The file's size" },
  { id: "cached", label: "Cached on", hint: "Debrid providers that hold the file's content now" },
  { id: "score", label: "Score", hint: "The file's custom format score" },
  { id: "group", label: "Release group", hint: "The group that made the release" },
  { id: "release", label: "Release", hint: "The release name of the file" },
  { id: "file", label: "File", hint: "The file's path in the library" },
  { id: "runtime", label: "Runtime", hint: "The episode's running time" },
];

export const DEFAULT_EPISODE_COLUMNS: EpisodeColumnId[] = ["air", "status", "quality", "size", "cached"];

/** The prefs key under theatarr.library.<what>.<user>. */
export const EPISODE_COLUMNS_PREF = "episode-columns";
