// Activity › History's table (docs/design/ui.md "Activity"): the optional columns and the default ones, kept under
// theatarr.library.activity-history.<user> (useColumnPrefs), and what each event's words mean (the Event cell's
// tooltip). History is read a page at a time, newest first, so its headers do not sort.
import type { ColumnDef } from "@/lib/prefs";

export type HistoryColumnId = "library" | "quality" | "score" | "date" | "release" | "provider" | "indexer" | "formats" | "size";

export const HISTORY_COLUMNS: ColumnDef<HistoryColumnId>[] = [
  { id: "library", label: "Library", hint: "The library it is about" },
  { id: "quality", label: "Quality", hint: "The release's quality, when the event has a download" },
  { id: "score", label: "Score", hint: "The release's custom format score, when the event has one" },
  { id: "date", label: "Date", hint: "When it happened" },
  { id: "release", label: "Release", hint: "The release name" },
  { id: "provider", label: "Provider", hint: "The debrid provider or download client" },
  { id: "indexer", label: "Indexer", hint: "Where the release was found" },
  { id: "formats", label: "Formats", hint: "The custom formats it matched" },
  { id: "size", label: "Size", hint: "The release's size" },
];

export const HISTORY_DEFAULT: HistoryColumnId[] = ["library", "quality", "score", "date", "release"];

/** What each History event means, by its words. */
export const EVENT_MEANINGS: { words: string; meaning: string }[] = [
  { words: "Found a release", meaning: "Theatarr picked a release and sent it to a provider or client." },
  { words: "Found a better release", meaning: "The same, to replace a file the library already has." },
  { words: "Linked into the library", meaning: "The file is now in the library folder." },
  { words: "Replaced by an upgrade", meaning: "An older file was swapped for a better one." },
  { words: "Link repaired", meaning: "A broken library link was pointed at its file again." },
  { words: "A link broke and could not be repaired", meaning: "The file behind a library link is gone." },
  { words: "Partly imported (retrying)", meaning: "Some files are linked; the rest are tried again." },
  { words: "Rejected by the library check", meaning: "The file failed the library's check and was not linked." },
  { words: "Waiting for the mount", meaning: "Finished, files not visible on the mount yet." },
  { words: "Linking failed", meaning: "Linking hit an error." },
  { words: "Download failed", meaning: "The provider or client could not complete it." },
  { words: "Download stalled and was dropped", meaning: "No progress for too long." },
  { words: "Not started: …", meaning: "It never left the queue because downloads were off." },
  { words: "Release blocked; it won't be tried again", meaning: "This release is on the library's blocklist." },
  { words: "Searching again after a failure", meaning: "A new search was started to replace a failed download." },
];

/** The meaning of an event's words ("Not started: …" for every not-started reason); "" when the list has none. */
export function eventMeaning(words: string): string {
  const key = words.startsWith("Not started: ") ? "Not started: …" : words;
  return EVENT_MEANINGS.find((m) => m.words === key)?.meaning ?? "";
}
