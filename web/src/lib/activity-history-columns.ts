// Activity › History's table (docs/design/ui.md "Activity"): the optional columns and the default ones, kept under
// theatarr.library.activity-history.<user> (useColumnPrefs), and what each event's words mean (the Event cell's
// tooltip). History is read a page at a time, newest first, so its headers do not sort.
import type { ColumnDef } from "@/lib/prefs";

export type HistoryColumnId = "library" | "quality" | "score" | "date" | "release" | "provider" | "indexer" | "formats" | "size";

export const HISTORY_COLUMNS: ColumnDef<HistoryColumnId>[] = [
  { id: "library", label: "Library", hint: "The library it is about" },
  { id: "quality", label: "Quality", hint: "The release's quality, when it is known" },
  { id: "score", label: "Score", hint: "The release's custom format score, when it was scored" },
  { id: "date", label: "Date", hint: "When it happened" },
  { id: "release", label: "Release", hint: "The release name; for a deleted file, what replaced it" },
  { id: "provider", label: "Provider", hint: "The debrid provider or download client" },
  { id: "indexer", label: "Indexer", hint: "Where the release was found" },
  { id: "formats", label: "Formats", hint: "The custom formats it matched" },
  { id: "size", label: "Size", hint: "The release's size" },
];

export const HISTORY_DEFAULT: HistoryColumnId[] = ["library", "quality", "score", "date", "release"];

/** What each History event means, by its words (the server words them: internal/web/activity.go entryWords). */
export const EVENT_MEANINGS: { words: string; meaning: string }[] = [
  { words: "Grabbed", meaning: "Theatarr picked this release and sent it to a provider or download client." },
  { words: "Imported", meaning: "The release's files were linked into the library." },
  { words: "Adopted", meaning: "A file Sonarr or Radarr had was taken over into the library." },
  { words: "Deleted (upgrade)", meaning: "This file was moved out of the library because a better release replaced it." },
  { words: "Removed", meaning: "This file was taken out of the library by hand (or by Sonarr/Radarr deleting it)." },
  { words: "File missing", meaning: "The file behind a library link is gone and could not be found again." },
  { words: "Failed", meaning: "The download or its import did not complete; the reason says why." },
  { words: "Not started: …", meaning: "It never left the queue because downloads were off." },
  { words: "Blocked", meaning: "This release is on the library's blocklist and won't be tried again." },
  { words: "Added to …", meaning: "The torrent was added to another provider as well (Add to other providers)." },
  { words: "Not cached on …", meaning: "Another provider did not have the torrent cached, so it was not added there." },
];

/** The meaning of an event's words ("Not started: …" for every not-started reason, "Added to …" and "Not cached on …"
 *  for every provider); "" when the list has none. */
export function eventMeaning(words: string): string {
  const prefix = ["Not started: ", "Added to ", "Not cached on "].find((p) => words.startsWith(p));
  const key = prefix ? `${prefix.trimEnd()} …` : words;
  return EVENT_MEANINGS.find((m) => m.words === key)?.meaning ?? "";
}
