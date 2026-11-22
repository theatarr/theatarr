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
  { id: "release", label: "Release", hint: "The release name; for an upgrade or an adoption, what it replaced" },
  { id: "provider", label: "Provider", hint: "The debrid provider or download client" },
  { id: "indexer", label: "Indexer", hint: "Where the release was found" },
  { id: "formats", label: "Formats", hint: "The custom formats it matched" },
  { id: "size", label: "Size", hint: "The release's size" },
];

export const HISTORY_DEFAULT: HistoryColumnId[] = ["library", "quality", "score", "date", "release"];

/** What each History event means, by its words (the server words them: internal/web/activity.go entryWords). */
export const EVENT_MEANINGS: { words: string; meaning: string }[] = [
  { words: "Grabbed", meaning: "Theatarr picked this release and sent it to a provider or download client. Once it is imported or fails, that row stands for it and says when it was grabbed." },
  { words: "Imported", meaning: "The release's files were linked into the library." },
  { words: "Adopted", meaning: "Taken over from Sonarr/Radarr's library; Theatarr did not download it." },
  { words: "Upgraded", meaning: "A better release was imported; \"Replaced\" says which files it took the place of." },
  { words: "Re-downloaded", meaning: "The same release was fetched again for a file that went missing." },
  { words: "Removed", meaning: "This file was taken out of the library by hand (or by Sonarr/Radarr deleting it)." },
  { words: "Removed from library", meaning: "The title was removed from this library by hand; the reason says who removed it and whether its files were kept." },
  { words: "File missing", meaning: "The file behind a library link is gone and could not be found again." },
  { words: "Failed", meaning: "The download or its import did not complete; the reason says why." },
  { words: "Not started: …", meaning: "It never left the queue because downloads were off." },
  { words: "Blocked", meaning: "This release was put on the library's blocklist at the time." },
  { words: "Added to …", meaning: "The torrent was added to another provider as well (Add to other providers, removed in 0.4.5)." },
  { words: "Already on …", meaning: "The other provider held the torrent already, so nothing was added there." },
  { words: "Not cached on …", meaning: "Another provider did not have the torrent cached, so it was not added there." },
  { words: "Replaced after it stopped working", meaning: "The file stopped working while someone played it, and another release was imported in its place." },
  { words: "Readable again after it stopped working", meaning: "The file stopped working while someone played it, and the same release works again (for example from another provider)." },
  { words: "Stopped working, no other copy yet", meaning: "The file stopped working while someone played it, and no other copy was found; the normal searches go on." },
  { words: "Earlier copy put back", meaning: "The copy that stopped working works again and is better than the one that replaced it, so it was put back." },
];

/** The meaning of an event's words ("Not started: …" for every not-started reason, "Added to …" and "Not cached on …"
 *  for every provider); "" when the list has none. */
export function eventMeaning(words: string): string {
  const prefix = ["Not started: ", "Added to ", "Already on ", "Not cached on "].find((p) => words.startsWith(p));
  const key = prefix ? `${prefix.trimEnd()} …` : words;
  return EVENT_MEANINGS.find((m) => m.words === key)?.meaning ?? "";
}
