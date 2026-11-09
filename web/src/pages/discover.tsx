// /discover/:row: a Home row in full ("See all"). The rows mix movies and series, so this page lists every kind,
// with the row's own predicate and order (GET /discover/{row}), not a Movies grid with a filter that means
// something else. Each card keeps the row's note.
import { CalendarClock } from "lucide-react";
import { useCallback, useMemo } from "react";
import { Link, useParams } from "react-router";
import { useDiscoverRow, usePerms, type DiscoverItem } from "@/api/library";
import { PosterGrid } from "@/components/library/poster-grid";
import { Empty, ErrorState, Loading, PageHeader } from "@/components/page";
import { Card } from "@/components/ui/card";
import { fmtNumber } from "@/lib/format";
import { fmtDay, isFamily, relDay } from "@/lib/library";

const DISCOVER_ROWS: Record<string, { title: string; description: string; empty: string; caption: (t: DiscoverItem) => string | undefined }> = {
  recent: {
    title: "Recently added",
    description: "Movies and series by their newest file.",
    empty: "Nothing has been added yet.",
    caption: (t) => (t.date ? `Added ${relDay(t.date)}` : undefined),
  },
  upcoming: {
    title: "Upcoming",
    description: "Release and air dates in the next 30 days, movies and series.",
    empty: "Nothing comes out in the next 30 days.",
    caption: (t) => [fmtDay(t.date), t.note].filter(Boolean).join(" · ") || undefined,
  },
  missing: {
    title: "Missing",
    description: "Released movies and aired episodes that are monitored and still wanted, in any library.",
    empty: "Nothing released is missing.",
    caption: (t) => t.note,
  },
  upgrades: {
    title: "Upgrade waiting",
    description: "Monitored titles with a library whose file is below its preferred quality; the badge says whether an upgrade was found or the file is the best available.",
    empty: "Every file is at its preferred quality.",
    caption: () => undefined,
  },
};

export function DiscoverPage() {
  const row = useParams().row ?? "";
  const perms = usePerms();
  // Missing and below cutoff are copy shortfalls: accounts without "see files" have neither row (discover.go).
  const shortfall = row === "upgrades" || row === "missing";
  const base = shortfall && !perms.seeFiles ? undefined : DISCOVER_ROWS[row];
  const def =
    base && row === "recent" && isFamily(perms)
      ? { ...base, title: "Recently available", description: "Movies and series by the day they became available.", empty: "Nothing is available yet.", caption: (t: DiscoverItem) => (t.date ? `Available ${relDay(t.date)}` : undefined) }
      : base;
  const q = useDiscoverRow(row);
  const items = useMemo(() => q.data?.pages.flatMap((p) => p.items) ?? [], [q.data]);
  const total = q.data?.pages[0]?.total;
  const onMore = useCallback(() => void q.fetchNextPage(), [q]);
  if (!def) {
    return (
      <div>
        <PageHeader title="Not found" description="There is no such row." />
        <Link to="/" className="text-sm text-[var(--accent-ink)] hover:underline">Home</Link>
      </div>
    );
  }
  return (
    <div data-testid={`discover-${row}`}>
      <PageHeader
        crumb={[{ label: "Home", to: "/" }]}
        title={def.title}
        description={
          <span>
            {def.description}
            {total !== undefined ? ` ${fmtNumber(total)} title${total === 1 ? "" : "s"}.` : ""}
            {row === "upcoming" ? (
              <>
                {" "}
                <Link to="/calendar" className="inline-flex items-center gap-1 text-[var(--accent-ink)] hover:underline">
                  <CalendarClock className="size-3.5" /> Calendar
                </Link>
              </>
            ) : null}
          </span>
        }
      />
      {q.isPending ? (
        <Loading />
      ) : q.error ? (
        <ErrorState error={q.error} />
      ) : items.length === 0 ? (
        <Card>
          <Empty>{def.empty}</Empty>
        </Card>
      ) : (
        <PosterGrid titles={items} more={!!q.hasNextPage && !q.isFetchingNextPage} onMore={onMore} caption={def.caption} />
      )}
      {q.isFetchingNextPage ? <Loading label="Loading more" /> : null}
    </div>
  );
}
