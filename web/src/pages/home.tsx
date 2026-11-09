import { SetupReminder } from "@/components/setup-reminder";
// Home (docs/design/ui.md): for admins first a Now playing card (who watches what and how it plays; nothing while
// nothing plays or the feature is off). Then Seerr-style sideways rows. "Getting ready" (downloads in progress; GET /activity/continue)
// is ContinueRow; it renders nothing while nothing is in progress. Then the requests row (pending ones
// for those who decide them, linked to the request in the queue; your own for everyone else), and the GET /discover
// rows: recently added, upcoming, missing and below cutoff. A family account (no file detail, no queue) gets My
// requests, Recently available and the browsing rows only: missing and below cutoff are the household's copy
// shortfalls, which the server leaves empty for it. The row header follows Seerr's slider-header
// (src/components/MediaSlider, MIT).
import { ArrowRight, CalendarClock, ChevronRight, Film, Sparkles, Tv } from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { useDiscover, type DiscoverItem, type DiscoverRow } from "@/api/library";
import { useStatus } from "@/api/hooks";
import { usePerms } from "@/api/library";
import { REQUEST_STATUS, useRequests } from "@/api/requests";
import { Empty, ErrorState, Loading, PageHeader } from "@/components/page";
import { PosterCard, Slider } from "@/components/poster-card";
import { Card } from "@/components/ui/card";
import { ContinueRow } from "@/components/continue-row";
import { NowPlayingCard } from "@/components/now-playing-card";
import { fmtNumber } from "@/lib/format";
import { fmtDay, isFamily, kindOf, posterStatus, relDay } from "@/lib/library";
import { requestLink } from "@/lib/request-state";

function Row({
  id,
  title,
  row,
  empty,
  caption,
}: {
  /** The row's key (data-row). */
  id: string;
  title: string;
  row: DiscoverRow | undefined;
  empty: string;
  caption: (it: DiscoverItem) => ReactNode;
}) {
  const perms = usePerms();
  const items = row?.items ?? [];
  return (
    <section className="mt-6 first:mt-0" data-row={id}>
      <div className="mb-1 flex items-center justify-between gap-3">
        <Link to={row?.more ?? "#"} className="group inline-flex min-w-0 items-center font-display text-lg font-semibold text-[var(--ink)] transition hover:text-[var(--accent-ink)] sm:text-xl">
          <span className="truncate">{title}</span>
          <ChevronRight className="ml-1 size-5 shrink-0 text-muted-foreground transition group-hover:translate-x-0.5 group-hover:text-foreground" />
        </Link>
        {row && row.total > items.length ? (
          <Link to={row.more} className="shrink-0 text-sm text-muted-foreground hover:text-foreground">
            See all {fmtNumber(row.total)}
          </Link>
        ) : null}
      </div>
      {items.length === 0 ? (
        <Card className="mt-2"><Empty>{empty}</Empty></Card>
      ) : (
        <Slider label={title}>
          {items.map((t) => (
            <div role="listitem" key={t.id}>
              <PosterCard
                to={`/title/${t.id}`}
                title={t.name ?? "?"}
                year={t.year}
                kind={kindOf(t)}
                status={posterStatus(t.availability, t.upgrade_seen, !perms.seeFiles, t.upgrade_state, t)}
                image={t.poster}
                overview={t.overview}
                caption={caption(t)}
              />
            </div>
          ))}
        </Slider>
      )}
    </section>
  );
}

/** Home's requests row: pending requests for those who decide them, your open requests for everyone else. */
function RequestsRow() {
  const perms = usePerms();
  const manager = perms.manageRequests;
  const q = useRequests(manager ? { status: "pending", limit: 20 } : { status: "open", mine: true, limit: 20 });
  const items = q.data?.items ?? [];
  if (items.length === 0) return null;
  const title = manager ? "Pending requests" : "My requests";
  return (
    <section className="mb-6" data-row="requests">
      <div className="mb-1 flex items-center justify-between gap-3">
        <Link to="/requests" className="group inline-flex min-w-0 items-center font-display text-lg font-semibold text-[var(--ink)] transition hover:text-[var(--accent-ink)] sm:text-xl">
          <span className="truncate">{title}</span>
          <ChevronRight className="ml-1 size-5 shrink-0 text-muted-foreground transition group-hover:translate-x-0.5 group-hover:text-foreground" />
        </Link>
        {(q.data?.total ?? 0) > items.length ? (
          <Link to="/requests" className="shrink-0 text-sm text-muted-foreground hover:text-foreground">See all {fmtNumber(q.data?.total)}</Link>
        ) : null}
      </div>
      <Slider label={title}>
        {items.map((r) => (
          <div role="listitem" key={r.id}>
            <PosterCard
              to={manager ? requestLink(r.id) : r.title_id ? `/title/${r.title_id}` : requestLink(r.id)}
              title={r.name}
              year={r.year}
              kind={r.anime ? "anime" : r.kind === "series" ? "series" : "movie"}
              image={r.poster}
              caption={
                <span>
                  <span className="font-medium text-foreground">{REQUEST_STATUS[r.status]?.label ?? r.status}</span>
                  {manager && r.user ? ` · ${r.user.display_name || r.user.username}` : ""}
                </span>
              }
            />
          </div>
        ))}
      </Slider>
    </section>
  );
}

/** Home's line under the heading: the titles and the libraries they sit in. counts.version counts each title's
 * place in each library (one title in three libraries is three), so the libraries are the status's list of them. */
export function librarySummary(titles = 0, libraries = 0) {
  const n = (v: number, one: string, many: string) => `${fmtNumber(v)} ${v === 1 ? one : many}`;
  return `${n(titles, "title", "titles")} across ${n(libraries, "library", "libraries")}.`;
}

export function Home() {
  const d = useDiscover();
  const perms = usePerms();
  const st = useStatus(perms.admin);
  const family = isFamily(perms);
  return (
    <div>
      <PageHeader
        title="Home"
        description={
          st.data?.counts ? librarySummary(st.data.counts.title, st.data.versions?.length) : undefined
        }
      />
      <SetupReminder />
      <div className="mb-7 grid grid-cols-3 gap-2 sm:gap-3">
        {[
          { to: "/movies", icon: Film, label: "Movies" },
          { to: "/series", icon: Tv, label: "Series" },
          { to: "/anime", icon: Sparkles, label: "Anime" },
        ].map((x) => (
          <Link key={x.to} to={x.to} className="group">
            <Card className="flex items-center gap-2.5 px-3 py-2.5 transition group-hover:border-[var(--line-2)] sm:gap-3 sm:p-4">
              <span className="grid size-8 shrink-0 place-items-center rounded-[5px] bg-[var(--accent-soft)] text-[var(--accent-ink)] sm:size-9">
                <x.icon className="size-4 sm:size-5" />
              </span>
              <span className="min-w-0 truncate text-sm font-medium sm:text-base">{x.label}</span>
              <ArrowRight className="ml-auto hidden size-4 text-muted-foreground transition group-hover:translate-x-0.5 sm:block" />
            </Card>
          </Link>
        ))}
      </div>
      {perms.admin ? <NowPlayingCard /> : null}
      <ContinueRow />
      <RequestsRow />
      {d.isPending ? (
        <Loading />
      ) : d.error ? (
        <ErrorState error={d.error} />
      ) : (
        <>
          <Row
            id="recent"
            title={family ? "Recently available" : "Recently added"}
            row={d.data.recent}
            empty={family ? "Nothing is available yet." : "Nothing has been added yet."}
            caption={(t) => (t.date ? `${family ? "Available" : "Added"} ${relDay(t.date)}` : null)}
          />
          <Row
            id="upcoming"
            title="Upcoming"
            row={d.data.upcoming}
            empty="Nothing comes out in the next 30 days."
            caption={(t) => (
              <span className="flex items-start gap-1">
                <CalendarClock className="mt-px size-3.5 shrink-0" />
                <span>
                  <span className="font-medium text-foreground">{fmtDay(t.date)}</span> · {t.note}
                </span>
              </span>
            )}
          />
          {perms.seeFiles ? (
            <>
              <Row id="missing" title="Missing" row={d.data.missing} empty="Nothing released is missing." caption={(t) => t.note} />
              <Row id="upgrades" title="Upgrade waiting" row={d.data.upgrades} empty="No upgrades waiting." caption={() => null} />
            </>
          ) : null}
        </>
      )}
    </div>
  );
}
