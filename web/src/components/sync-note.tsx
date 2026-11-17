// The one-line notes where an add to a library is configured outside the add sheet (collections, import lists,
// Connected apps): each chosen library kept in sync names the libraries it also adds to that are not chosen too.
import { syncNote } from "@/lib/sync-groups";

export function SyncNotes({ groups, keys, nameOf, className }: { groups?: string[][]; keys: string[]; nameOf: (key: string) => string; className?: string }) {
  const lines = keys.map((k) => syncNote(groups, k, nameOf, keys)).filter(Boolean);
  if (lines.length === 0) return null;
  return (
    <div className={className} data-testid="sync-notes">
      {lines.map((l) => <p key={l} className="text-xs text-muted-foreground">{l}</p>)}
    </div>
  );
}
