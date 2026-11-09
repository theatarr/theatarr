// A short list of grab journal entries (see files only), for a version card or an episode: when, kind, summary. For
// admins each links to the entry in Admin › Advanced › Decision journal; managers see the summary as plain text,
// since the journal is an admin page they cannot open.
import { Link } from "react-router";
import { usePerms, type JournalPage } from "@/api/library";
import { Badge } from "@/components/ui/badge";
import { adminLink, journalLink } from "@/lib/admin-links";
import { fmtAgo, fmtTime } from "@/lib/format";

type Entry = NonNullable<JournalPage["items"]>[number];

export function JournalList({ items, empty = "No journal entries yet." }: { items: Entry[]; empty?: string }) {
  const admin = usePerms().admin;
  if (items.length === 0) return <p className="text-sm text-muted-foreground">{empty}</p>;
  return (
    <ul className="divide-y divide-[var(--line)] text-sm" data-testid="journal-list">
      {items.map((e) => {
        const to = adminLink(admin, journalLink(e.id ?? ""));
        return (
        <li key={e.id} className="flex items-start gap-2 py-1.5">
          <Badge tone={e.suspect ? "warning" : "neutral"} className="mt-0.5">{e.kind}</Badge>
          {to ? (
            <Link to={to} className="min-w-0 flex-1 [overflow-wrap:anywhere] hover:text-primary">
              {e.summary || e.id}
            </Link>
          ) : (
            <span className="min-w-0 flex-1 [overflow-wrap:anywhere]">{e.summary || e.id}</span>
          )}
          <span className="shrink-0 whitespace-nowrap text-xs text-muted-foreground" title={fmtTime(e.at)}>{fmtAgo(e.at)}</span>
        </li>
        );
      })}
    </ul>
  );
}
