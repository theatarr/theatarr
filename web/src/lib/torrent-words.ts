// The Torrents page's codes in words: a torrent's normalised state (internal/provider State) and the torrents
// journal's actions (internal/torrents: backup, delete, restore) and who ran them. The provider's own status word
// stays in the title.
import { wordsFor } from "./code-words";

const STATES: Record<string, string> = {
  queued: "Queued",
  downloading: "Downloading",
  downloaded: "Complete",
  error: "Error",
  unknown: "Unknown",
};

/** "Complete", "Downloading 40%": the normalised state, with the progress while it is not complete. */
export function torrentStateWords(state: string | null | undefined, progress?: number): string {
  const s = wordsFor(STATES, state);
  return state !== "downloaded" && progress !== undefined && progress < 100 ? `${s} ${Math.round(progress)}%` : s;
}

// The outcomes internal/torrents writes per action: backup ok/failed; delete deleted/failed/skipped; restore
// added/uncached/incomplete/failed/skipped/sending.
const ACTIONS: Record<string, Record<string, string>> = {
  backup: { ok: "Backed up", failed: "Backup failed" },
  delete: { deleted: "Deleted", failed: "Delete failed", skipped: "Not deleted" },
  restore: {
    added: "Restored",
    uncached: "Not cached",
    incomplete: "Restored (incomplete)",
    failed: "Restore failed",
    skipped: "Not restored",
    sending: "Restoring",
  },
};

/** A journal entry's action and outcome: "backup ok" → "Backed up", "delete deleted" → "Deleted", "restore added" →
 *  "Restored". */
export function torrentJournalWords(action: string | null | undefined, outcome: string | null | undefined): string {
  const words = action && outcome ? ACTIONS[action]?.[outcome] : undefined;
  if (words) return words;
  return [wordsFor({}, action), outcome && outcome !== "ok" ? wordsFor({}, outcome).toLowerCase() : ""].filter(Boolean).join(" ");
}

/** Who ran it: "schedule" → "Scheduled", else the account name as stored. */
export const torrentJournalBy = (by: string | null | undefined) => (by === "schedule" ? "Scheduled" : by ?? "");
