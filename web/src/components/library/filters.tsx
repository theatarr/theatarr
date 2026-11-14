// The lists' filter sheet: availability and monitoring as split buttons with counts, quality, genres, years and (for
// callers who see files) version has/lacks and provider. Counts come from GET /titles/facets and are for the whole
// list, before other filters; the values live in the URL. The title search sits in the toolbar (TitleSearch). Adapted from Scryer (github.com/scryer-media/scryer) apps/scryer-web/components/views/
// media-content/catalog-filters-panel.tsx, title-quick-filters.tsx and components/common/underline-filter-button.tsx
// @ 2ef7757c, GPL-3.0: the panel header, the underline filter button, the split filter groups, the label and chip
// styles. Changes: our facets and URL state; selects instead of the range sliders (their inline widths break the
// CSP); token prefix --scry- renamed --th-.
import {
  CalendarDays,
  Eye,
  EyeOff,
  Film,
  HardDrive,
  Layers,
  PackageCheck,
  PackageOpen,
  PackageX,
  Search,
  SlidersHorizontal,
  Sparkle,
  X,
} from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import type { TitleFacets } from "@/api/library";
import { providerLabel } from "@/components/provider-pills";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SELECT_CLASS } from "@/components/ui/select";
import { availabilityFilters, UPGRADE_FILTERS } from "@/lib/library";

/** The status filters in rows of two. */
function availabilityRows(seeFiles: boolean, kind?: string) {
  const f = availabilityFilters(seeFiles, kind);
  const rows = [];
  for (let i = 0; i < f.length; i += 2) rows.push(f.slice(i, i + 2));
  return rows;
}
import { resolutionWords } from "@/lib/upgrade";
import { cn } from "@/lib/utils";
import { fmtNumber } from "@/lib/format";

type Tone = "neutral" | "success" | "danger" | "warning";

function FilterButton({
  selected,
  icon,
  label,
  count,
  tone = "neutral",
  title,
  onClick,
  className,
}: {
  selected: boolean;
  icon?: ReactNode;
  label: string;
  count?: number;
  tone?: Tone;
  title?: string;
  onClick: () => void;
  className?: string;
}) {
  return (
    <button
      type="button"
      aria-pressed={selected}
      title={title}
      onClick={onClick}
      className={cn(
        "relative inline-flex h-10 shrink-0 items-center gap-2 px-3 py-2.5 text-[13px] font-semibold transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--line-focus)]",
        selected ? "text-foreground" : "text-[var(--ink-3)] hover:text-[var(--ink)]",
        className,
      )}
    >
      {icon ? (
        <span
          className={cn(
            "shrink-0 [&>svg]:size-3.5",
            selected
              ? "text-[var(--accent-ink)]"
              : tone === "success"
                ? "text-[var(--ok-ink)]"
                : tone === "danger"
                  ? "text-[var(--bad-ink)]"
                  : tone === "warning"
                    ? "text-[var(--warn)]"
                    : "text-[var(--ink-3)]",
          )}
        >
          {icon}
        </span>
      ) : null}
      <span className="whitespace-nowrap">{label}</span>
      {typeof count === "number" ? (
        <span
          className={cn(
            "inline-flex min-w-[4ch] justify-center rounded-[3px] px-1.5 py-0.5 text-[11px] font-bold leading-none tabular-nums",
            selected ? "bg-[color-mix(in_srgb,var(--accent)_18%,transparent)] text-[var(--accent-ink)]" : "bg-[var(--chip)] text-[var(--ink-3)]",
          )}
        >
          {fmtNumber(count)}
        </span>
      ) : null}
      {selected ? <span className="absolute bottom-[-1px] left-2 right-2 h-[2.5px] rounded-full bg-[var(--line-focus)]" /> : null}
    </button>
  );
}

const SPLIT_GROUP = "flex w-full overflow-hidden rounded-[5px] border border-[color-mix(in_srgb,var(--accent)_55%,transparent)]";

function segment(selected: boolean, divider = false): string {
  return cn(
    "h-10 min-w-0 flex-1 justify-center rounded-none border-0 px-2",
    divider && "border-l border-l-[color-mix(in_srgb,var(--accent)_35%,transparent)]",
    selected ? "bg-[color-mix(in_srgb,var(--accent)_32%,transparent)]" : "bg-[var(--sunken)] hover:bg-[var(--hover)]",
  );
}

function FilterLabel({ children, icon }: { children: ReactNode; icon?: ReactNode }) {
  return (
    <div className="mb-2 flex items-center gap-1.5 text-xs font-bold uppercase tracking-[0.05em] text-[var(--ink-3)] [&>svg]:size-3.5">
      {icon}
      {children}
    </div>
  );
}

/** The library pages' native selects: the Select primitive's shape. */
export const SELECT = SELECT_CLASS;

function Chip({ selected, onClick, children, title }: { selected: boolean; onClick: () => void; children: ReactNode; title?: string }) {
  return (
    <button
      type="button"
      aria-pressed={selected}
      title={title}
      onClick={onClick}
      className={cn(
        "inline-flex max-w-full items-center gap-1.5 rounded-[3px] border px-2.5 py-1 text-xs font-medium transition",
        selected
          ? "border-[color-mix(in_srgb,var(--accent)_50%,transparent)] bg-[color-mix(in_srgb,var(--accent)_20%,transparent)] text-[var(--accent-ink)]"
          : "border-[var(--line)] bg-[var(--sunken)] text-[var(--ink-3)] hover:border-[var(--line-2)] hover:text-foreground",
      )}
    >
      {children}
    </button>
  );
}

export type FilterValues = Record<string, string | undefined>;

/** The toolbar's title search: writes q to the URL a moment (250 ms) after typing stops. */
export function TitleSearch({ value, set, className }: { value: string | undefined; set: (key: string, value: string | undefined) => void; className?: string }) {
  const [q, setQ] = useState(value ?? "");
  useEffect(() => setQ(value ?? ""), [value]);
  useEffect(() => {
    if ((value ?? "") === q) return;
    const t = window.setTimeout(() => set("q", q.trim() || undefined), 250);
    return () => window.clearTimeout(t);
  }, [q, value, set]);
  return (
    <div className={cn("relative", className)}>
      <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-[var(--ink-3)]" />
      <Input aria-label="Filter by title" placeholder="Filter by title" value={q} onChange={(e) => setQ(e.target.value)} className="pl-9" />
    </div>
  );
}

export function FiltersPanel({
  values,
  set,
  clear,
  facets,
  seeFiles,
  kind,
  onClose,
  className,
}: {
  values: FilterValues;
  /** Sets (or with undefined, removes) one URL parameter. */
  set: (key: string, value: string | undefined) => void;
  clear: () => void;
  facets: TitleFacets | undefined;
  seeFiles: boolean;
  /** The catalog's kind (movie or series): movies get no episode-only filter. */
  kind?: string;
  onClose?: () => void;
  className?: string;
}) {
  const any = Object.values(values).some(Boolean);
  const toggle = (key: string, v: string) => set(key, values[key] === v ? undefined : v);
  const genres = (values.genre ?? "").split(",").filter(Boolean);
  const years: number[] = [];
  if (facets?.year_min && facets?.year_max) for (let y = facets.year_max; y >= facets.year_min; y--) years.push(y);
  const av = facets?.availability ?? {};
  const icons = { missing: <PackageX />, partial: <PackageOpen />, available: <PackageCheck />, unmonitored: <EyeOff />, upcoming: <CalendarDays /> } as const;
  const tones = { missing: "danger", partial: "warning", available: "success", unmonitored: "neutral", upcoming: "neutral" } as const;
  const up = facets?.upgrades ?? {};

  return (
    <aside data-testid="filters-panel" className={cn("flex min-h-0 flex-col bg-[var(--surface)] px-[18px] py-4", className)}>
      <div className="mb-1 flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <div className="flex size-9 shrink-0 items-center justify-center rounded-[5px] border border-[var(--th-baccent)] bg-[var(--accent-soft)] text-[var(--accent-ink)]">
            <SlidersHorizontal className="size-[18px]" />
          </div>
          <span className="text-base font-semibold text-[var(--ink)]">Filters</span>
          <button
            type="button"
            disabled={!any}
            onClick={clear}
            className="rounded-[3px] text-xs font-medium text-[var(--accent-ink)] outline-none transition hover:underline focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--line-focus)] disabled:cursor-default disabled:no-underline disabled:opacity-40"
          >
            Clear all
          </button>
        </div>
        {onClose ? (
          <Button variant="ghost" size="icon" onClick={onClose} aria-label="Close filters" className="shrink-0">
            <X className="size-4" />
          </Button>
        ) : null}
      </div>

      <p className="mb-4 text-xs text-[var(--ink-3)]">Counts are for the whole list, before other filters.</p>

      <div className="mb-4 space-y-1.5 border-b border-[var(--line)] pb-4">
        <div role="group" aria-label="Monitoring" className={SPLIT_GROUP}>
          <FilterButton selected={values.monitored === "true"} onClick={() => toggle("monitored", "true")} icon={<Eye />} label="Monitored" count={facets?.monitored} tone="success" className={segment(values.monitored === "true")} />
          <FilterButton selected={values.monitored === "false"} onClick={() => toggle("monitored", "false")} icon={<EyeOff />} label="Not monitored" count={facets?.unmonitored} tone="danger" className={segment(values.monitored === "false", true)} />
        </div>
        {/* Status and upgrade groups are named: "Not monitored" with no file is a status, apart from monitoring above. */}
        {seeFiles ? <div className="pt-2"><FilterLabel>Status</FilterLabel></div> : null}
        {availabilityRows(seeFiles, kind).map((pair, i) => (
          <div key={i} role="group" aria-label={seeFiles ? "Status" : "Availability"} className={SPLIT_GROUP}>
            {pair.map((a, j) => (
              <FilterButton
                key={a.id}
                selected={values.availability === a.id}
                onClick={() => toggle("availability", a.id)}
                icon={icons[a.id as keyof typeof icons]}
                label={a.label}
                title={a.hint}
                count={av[a.id]}
                tone={tones[a.id as keyof typeof tones]}
                className={segment(values.availability === a.id, j > 0)}
              />
            ))}
          </div>
        ))}
        {/* Quality, apart from the status: among titles with a file (file detail). */}
        {seeFiles ? <div className="pt-2"><FilterLabel>Upgrades</FilterLabel></div> : null}
        {seeFiles ? (
          <div role="group" aria-label="Upgrades" className={SPLIT_GROUP}>
            {UPGRADE_FILTERS.map((u, j) => (
              <FilterButton
                key={u.id}
                selected={values.upgrade === u.id}
                onClick={() => toggle("upgrade", u.id)}
                icon={u.id === "found" ? <Sparkle /> : <PackageCheck />}
                label={u.label}
                title={u.hint}
                count={up[u.id]}
                tone={u.id === "found" ? "warning" : "neutral"}
                className={segment(values.upgrade === u.id, j > 0)}
              />
            ))}
          </div>
        ) : null}
      </div>

      {/* Has/lacks per library is copy detail: accounts without "see files" filter by availability only. */}
      {seeFiles && (facets?.versions ?? []).length > 0 ? (
        <div className="mb-4">
          <FilterLabel icon={<Layers />}>Libraries</FilterLabel>
          <div className="space-y-2">
            {(facets?.versions ?? []).map((v) => (
              <div key={v.key} className="flex flex-wrap items-center gap-2">
                <span data-vkey={v.key} className="inline-flex min-w-[6.5rem] items-center gap-1.5 text-[13px] font-medium">
                  <span className="size-2 rounded-[2px] bg-[var(--vc)]" />
                  {v.label}
                </span>
                <Chip selected={values.with === v.key} onClick={() => toggle("with", v.key)} title={`Titles with a ${v.label} file`}>
                  Has <span className="tabular-nums opacity-75">{v.with_file ?? 0}</span>
                </Chip>
                <Chip selected={values.without === v.key} onClick={() => toggle("without", v.key)} title={`Titles without a ${v.label} file`}>
                  Lacks <span className="tabular-nums opacity-75">{(facets?.total ?? 0) - (v.with_file ?? 0)}</span>
                </Chip>
              </div>
            ))}
          </div>
        </div>
      ) : null}

      <div className="mb-4 grid grid-cols-2 gap-x-3 gap-y-4">
        <label className="min-w-0">
          <FilterLabel icon={<Film />}>Quality</FilterLabel>
          <select aria-label="Quality" className={SELECT} value={values.quality ?? ""} onChange={(e) => set("quality", e.target.value || undefined)}>
            <option value="">Any quality</option>
            {(facets?.qualities ?? []).map((q) => (
              <option key={q.value} value={q.value}>
                {seeFiles ? q.value : resolutionWords(q.value) || q.value} ({q.count})
              </option>
            ))}
          </select>
        </label>
        {seeFiles ? (
          <label className="min-w-0">
            <FilterLabel icon={<HardDrive />}>Provider</FilterLabel>
            <select aria-label="Provider" className={SELECT} value={values.provider ?? ""} onChange={(e) => set("provider", e.target.value || undefined)}>
              <option value="">Any provider</option>
              {(facets?.providers ?? []).map((p) => (
                <option key={p.value} value={p.value}>
                  {providerLabel(p.value)} ({p.count})
                </option>
              ))}
            </select>
          </label>
        ) : null}
        <div className="col-span-2 min-w-0">
          <FilterLabel icon={<CalendarDays />}>Year</FilterLabel>
          <div className="flex items-center gap-2">
            <select aria-label="From year" className={SELECT} value={values.year_from ?? ""} onChange={(e) => set("year_from", e.target.value || undefined)}>
              <option value="">From</option>
              {years.map((y) => <option key={y} value={y}>{y}</option>)}
            </select>
            <span className="text-muted-foreground">–</span>
            <select aria-label="To year" className={SELECT} value={values.year_to ?? ""} onChange={(e) => set("year_to", e.target.value || undefined)}>
              <option value="">To</option>
              {years.map((y) => <option key={y} value={y}>{y}</option>)}
            </select>
          </div>
        </div>
      </div>

      {(facets?.genres ?? []).length > 0 ? (
        <div className="mb-2">
          <FilterLabel icon={<Sparkle />}>Genres</FilterLabel>
          <div className="flex flex-wrap gap-1.5">
            {(facets?.genres ?? []).map((g) => {
              const on = genres.includes(g.value);
              return (
                <Chip
                  key={g.value}
                  selected={on}
                  onClick={() => {
                    const next = on ? genres.filter((x) => x !== g.value) : [...genres, g.value];
                    set("genre", next.join(",") || undefined);
                  }}
                >
                  {g.value} <span className="tabular-nums opacity-70">{g.count}</span>
                </Chip>
              );
            })}
          </div>
        </div>
      ) : null}
    </aside>
  );
}
