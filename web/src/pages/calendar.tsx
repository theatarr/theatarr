// Calendar (docs/design/ui.md): movie release dates (in cinemas, digital, physical) and episode air dates of the
// monitored titles, as a month grid or an agenda. Layout after Scryer's calendar view (github.com/scryer-media/scryer
// apps/scryer-web/components/views/calendar-view.tsx @ 2ef7757c, GPL-3.0): the toolbar with facet toggles, title and
// Today / Month-Agenda / prev-next buttons, day pills, event chips with a status edge (lib/calendar.ts calendarTone),
// "+N more". Scryer renders with FullCalendar; this grid is our own (see styles/calendar.css). At phone width the grid
// shows dots and the chosen day's list below it; the agenda is the default there.
import { CalendarDays, Check, ChevronLeft, ChevronRight, List } from "lucide-react";
import { useMemo, useState } from "react";
import { Link } from "react-router";
import { useCalendar, type CalendarEntry } from "@/api/activity";
import { usePerms } from "@/api/library";
import { Empty, ErrorState, Loading, PageHeader } from "@/components/page";
import { Chip } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { calendarHref, calendarLegend, calendarTone, episodeCode, joinReleases, monthDays, parseYmd, placeEntries, releaseLabel, shiftDay, ymd } from "@/lib/calendar";
import { cn } from "@/lib/utils";
import "@/styles/calendar.css";
import { plural, titleWithYear } from "@/lib/format";

type Facet = "movie" | "series" | "anime";
const FACETS: { id: Facet; label: string }[] = [
  { id: "movie", label: "Movies" },
  { id: "series", label: "Series" },
  { id: "anime", label: "Anime" },
];
const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const VIEW_KEY = "theatarr.calendar.view";

function readView(): "month" | "agenda" {
  try {
    const v = window.localStorage.getItem(VIEW_KEY);
    if (v === "month" || v === "agenda") return v;
  } catch {
    // not kept
  }
  return window.matchMedia?.("(max-width: 639px)").matches ? "agenda" : "month";
}

/** An entry with the release types of its day joined (joinReleases). */
type Entry = CalendarEntry & { types: string[] };

/** A movie's release types of the day: "Digital + Physical". */
const releasesOf = (e: Entry) => e.types.map(releaseLabel).join(" + ");

function badgeOf(e: Entry): string {
  return e.kind === "episode" ? episodeCode(e.season, e.number) : releasesOf(e);
}

function airTime(e: CalendarEntry): string | null {
  if (!e.at) return null;
  const d = new Date(e.at);
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

/** A status as one of the theme's chip tones (the agenda's word). */
const CHIP_TONE = { ok: "ok", progress: "info", bad: "bad", neutral: "neutral", idle: "ghost" } as const;

function tooltip(e: Entry, status: string): string {
  const lines = [titleWithYear(e.title.name, e.title.year)];
  if (e.kind === "episode") lines.push(`${episodeCode(e.season, e.number)}${e.episode ? ` · ${e.episode}` : ""}`);
  else lines.push(releasesOf(e));
  lines.push(status);
  return lines.join("\n");
}

function EventChip({ e, plain, now }: { e: Entry; plain: boolean; now: Date }) {
  const time = airTime(e);
  const s = calendarTone(e, now, plain);
  return (
    <Link
      to={calendarHref(e)}
      title={tooltip(e, s.label)}
      data-testid="cal-event"
      data-tone={s.tone}
      className={cn("theatarr-cal-event", `theatarr-cal-tone-${s.tone}`, !plain && !e.monitored && "is-unmonitored")}
    >
      <div className="theatarr-cal-event-title">{e.title.name}</div>
      <div className="theatarr-cal-event-meta">
        <span className="theatarr-cal-event-badge">{badgeOf(e)}</span>
        {e.available ? <Check className="size-3 shrink-0 text-[var(--ok)]" aria-label={s.label} /> : null}
        {time ? <span className="theatarr-cal-event-time">{time}</span> : null}
      </div>
    </Link>
  );
}

function AgendaItem({ e, plain, now }: { e: Entry; plain: boolean; now: Date }) {
  const s = calendarTone(e, now, plain);
  return (
    <li>
      <Link to={calendarHref(e)} data-tone={s.tone} className="flex items-center gap-3 px-3 py-2.5 hover:bg-[var(--hover)] sm:px-4" data-testid="agenda-item">
        <img src={e.title.poster} alt="" loading="lazy" decoding="async" className="aspect-[2/3] w-9 shrink-0 rounded-[4px] object-cover ring-1 ring-[var(--line)]" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium text-[var(--ink)]">{e.title.name}</div>
          <div className="truncate text-xs text-muted-foreground">
            {e.kind === "episode" ? `${episodeCode(e.season, e.number)}${e.episode ? ` · ${e.episode}` : ""}` : releasesOf(e)}
            {airTime(e) ? ` · ${airTime(e)}` : ""}
          </div>
        </div>
        <Chip tone={CHIP_TONE[s.tone]}>{s.label}</Chip>
      </Link>
    </li>
  );
}

/** One line under the header: what the chip colours mean for this account. */
function Legend({ plain }: { plain: boolean }) {
  return (
    <ul className="-mt-2 mb-4 flex flex-wrap items-center gap-x-4 gap-y-1 text-[12.5px] text-muted-foreground" aria-label="Colours" data-testid="cal-legend">
      {calendarLegend(plain).map((l) => (
        <li key={l.tone} className="inline-flex items-center gap-1.5">
          <span className={cn("theatarr-cal-dot", `theatarr-cal-tone-${l.tone}`)} aria-hidden />
          {l.label}
        </li>
      ))}
    </ul>
  );
}

function dayHeading(s: string, today: string): string {
  const d = parseYmd(s);
  const label = d.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric" });
  return s === today ? `Today · ${label}` : label;
}

function Agenda({ days, today, plain, now }: { days: [string, Entry[]][]; today: string; plain: boolean; now: Date }) {
  if (days.length === 0) {
    return (
      <Card>
        <Empty icon={CalendarDays}>Nothing is released or airs in this month.</Empty>
      </Card>
    );
  }
  return (
    <div className="space-y-4">
      {days.map(([day, items]) => (
        <section key={day}>
          <h3 className={cn("mb-2 text-[12px] font-semibold uppercase tracking-[0.14em]", day === today ? "text-[var(--accent-ink)]" : "text-[var(--ink-3)]", day < today && "opacity-70")}>
            {dayHeading(day, today)}
          </h3>
          <Card className="p-0">
            <ul className="divide-y divide-border">
              {items.map((e, i) => (
                <AgendaItem key={i} e={e} plain={plain} now={now} />
              ))}
            </ul>
          </Card>
        </section>
      ))}
    </div>
  );
}

function MonthGrid({
  days,
  byDay,
  month,
  today,
  selected,
  onSelect,
  plain,
  now,
}: {
  days: Date[];
  byDay: Map<string, Entry[]>;
  month: number;
  today: string;
  selected: string | null;
  onSelect: (d: string) => void;
  plain: boolean;
  now: Date;
}) {
  return (
    <div className="theatarr-cal-grid">
      <div className="grid grid-cols-7">
        {WEEKDAYS.map((w) => (
          <div key={w} className="theatarr-cal-head">
            <span className="sm:hidden">{w[0]}</span>
            <span className="hidden sm:inline">{w}</span>
          </div>
        ))}
      </div>
      <div className="grid grid-cols-7">
        {days.map((d) => {
          const key = ymd(d);
          const items = byDay.get(key) ?? [];
          const shown = items.slice(0, 3);
          return (
            <div
              key={key}
              data-day={key}
              className={cn(
                "theatarr-cal-cell",
                items.length > 0 && "has-events",
                key === today && "is-today",
                d.getMonth() !== month && "is-other",
                selected === key && "is-selected",
              )}
            >
              <button type="button" onClick={() => onSelect(key)} className="flex w-full justify-end" aria-label={`${dayHeading(key, today)}: ${plural(items.length, "item")}`}>
                <span className="theatarr-cal-day-pill">{d.getDate()}</span>
              </button>
              <div className="hidden sm:block">
                {shown.map((e, i) => (
                  <EventChip key={i} e={e} plain={plain} now={now} />
                ))}
                {items.length > shown.length ? (
                  <button type="button" className="theatarr-cal-more" onClick={() => onSelect(key)}>
                    +{items.length - shown.length} more
                  </button>
                ) : null}
              </div>
              {items.length > 0 ? (
                <button type="button" onClick={() => onSelect(key)} className="mt-1 flex flex-wrap justify-center gap-1 sm:hidden" aria-hidden tabIndex={-1}>
                  {items.slice(0, 4).map((e, i) => (
                    <span key={i} className={cn("theatarr-cal-dot", `theatarr-cal-tone-${calendarTone(e, now, plain).tone}`)} />
                  ))}
                </button>
              ) : null}
            </div>
          );
        })}
      </div>
    </div>
  );
}

export function CalendarPageView() {
  const now = new Date();
  const today = ymd(now);
  const plain = !usePerms().seeFiles;
  const [anchor, setAnchor] = useState({ y: now.getFullYear(), m: now.getMonth() });
  const [view, setView] = useState<"month" | "agenda">(readView);
  const [facets, setFacets] = useState<Facet[]>(["movie", "series", "anime"]);
  const [selected, setSelected] = useState<string | null>(null);
  const days = useMemo(() => monthDays(anchor.y, anchor.m), [anchor]);
  const from = view === "month" ? ymd(days[0]) : ymd(new Date(anchor.y, anchor.m, 1));
  const to = view === "month" ? ymd(days[days.length - 1]) : ymd(new Date(anchor.y, anchor.m + 1, 0));
  // One day wider on each side: an episode's air moment can fall on the day before or after the network's here.
  const q = useCalendar(shiftDay(from, -1), shiftDay(to, 1));
  const placed = useMemo(() => joinReleases(placeEntries(q.data?.items ?? [], from, to)), [q.data, from, to]);

  const byDay = useMemo(() => {
    const m = new Map<string, Entry[]>();
    for (const { day, e } of placed) {
      if (!facets.includes(e.title.kind as Facet)) continue;
      const list = m.get(day) ?? [];
      list.push(e);
      m.set(day, list);
    }
    return m;
  }, [placed, facets]);
  // The toggles count the month shown, in both views: the grid's days of the neighbouring months are not counted.
  const counts = useMemo(() => {
    const first = ymd(new Date(anchor.y, anchor.m, 1));
    const last = ymd(new Date(anchor.y, anchor.m + 1, 0));
    const c: Record<Facet, number> = { movie: 0, series: 0, anime: 0 };
    for (const { day, e } of placed) if (day >= first && day <= last) c[e.title.kind as Facet] = (c[e.title.kind as Facet] ?? 0) + 1;
    return c;
  }, [placed, anchor]);

  const move = (n: number) => {
    setSelected(null);
    setAnchor((a) => {
      const d = new Date(a.y, a.m + n, 1);
      return { y: d.getFullYear(), m: d.getMonth() };
    });
  };
  const pickView = (v: "month" | "agenda") => {
    setView(v);
    try {
      window.localStorage.setItem(VIEW_KEY, v);
    } catch {
      // not kept
    }
  };
  const title = new Date(anchor.y, anchor.m, 1).toLocaleDateString(undefined, { month: "long", year: "numeric" });
  const agendaDays = [...byDay.entries()].filter(([d]) => d >= from && d <= to).sort(([a], [b]) => a.localeCompare(b));
  const selectedItems = selected ? byDay.get(selected) ?? [] : [];

  return (
    <div className="theatarr-cal">
      <PageHeader title="Calendar" description="Release dates and episode air dates of the titles you follow." />
      <Legend plain={plain} />
      <div className="mb-4 grid grid-cols-1 items-center gap-3 lg:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)]">
        <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Media types">
          {FACETS.map((f) => {
            const on = facets.includes(f.id);
            return (
              <button
                key={f.id}
                type="button"
                aria-pressed={on}
                onClick={() => setFacets((cur) => (on ? (cur.length > 1 ? cur.filter((x) => x !== f.id) : cur) : [...cur, f.id]))}
                className={cn(
                  "inline-flex h-8 items-center gap-1.5 rounded-[5px] border border-border px-2.5 text-[12.5px] font-medium transition",
                  on ? "bg-card text-foreground" : "text-muted-foreground opacity-70",
                )}
              >
                {f.label}
                <span className="rounded-full bg-muted px-1.5 text-[10px] tabular-nums text-muted-foreground">{counts[f.id]}</span>
              </button>
            );
          })}
        </div>
        <h2 className="order-first text-center text-2xl font-bold tracking-tight text-[var(--ink)] lg:order-none" data-testid="cal-title">
          {title}
        </h2>
        <div className="flex flex-wrap items-center gap-2 lg:justify-end">
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setAnchor({ y: now.getFullYear(), m: now.getMonth() });
              setSelected(today);
            }}
          >
            Today
          </Button>
          <div className="inline-flex" role="group" aria-label="View">
            <Button variant={view === "month" ? "default" : "outline"} size="sm" className="rounded-r-none" onClick={() => pickView("month")} aria-pressed={view === "month"}>
              <CalendarDays /> Month
            </Button>
            <Button variant={view === "agenda" ? "default" : "outline"} size="sm" className="-ml-px rounded-l-none" onClick={() => pickView("agenda")} aria-pressed={view === "agenda"}>
              <List /> Agenda
            </Button>
          </div>
          <div className="ml-auto inline-flex lg:ml-0">
            <Button variant="outline" size="sm" className="rounded-r-none" onClick={() => move(-1)} aria-label="Previous month">
              <ChevronLeft />
            </Button>
            <Button variant="outline" size="sm" className="-ml-px rounded-l-none" onClick={() => move(1)} aria-label="Next month">
              <ChevronRight />
            </Button>
          </div>
        </div>
      </div>
      {q.isPending ? (
        <Loading />
      ) : q.error ? (
        <ErrorState error={q.error} />
      ) : view === "agenda" ? (
        <Agenda days={agendaDays} today={today} plain={plain} now={now} />
      ) : (
        <>
          <MonthGrid days={days} byDay={byDay} month={anchor.m} today={today} selected={selected} onSelect={setSelected} plain={plain} now={now} />
          {selected ? (
            <section className="mt-5">
              <h3 className="mb-2 text-[12px] font-semibold uppercase tracking-[0.14em] text-[var(--ink-3)]">{dayHeading(selected, today)}</h3>
              {selectedItems.length === 0 ? (
                <Card>
                  <Empty icon={CalendarDays}>Nothing on this day.</Empty>
                </Card>
              ) : (
                <Card className="p-0">
                  <ul className="divide-y divide-border">
                    {selectedItems.map((e, i) => (
                      <AgendaItem key={i} e={e} plain={plain} now={now} />
                    ))}
                  </ul>
                </Card>
              )}
            </section>
          ) : null}
        </>
      )}
      {q.isFetching && !q.isPending ? <p className="mt-2 text-[12.5px] text-muted-foreground" aria-live="polite">Loading…</p> : null}
    </div>
  );
}
