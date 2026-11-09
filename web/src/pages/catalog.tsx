// Movies, Series and Anime (docs/design/ui.md): a poster grid (or a table) with the filter panel on the right, as
// Scryer's catalogue has it, and sort in the toolbar. Filters and sort live in the URL, so links such as
// /movies?availability=missing open a filtered grid. Below lg the panel opens as a sheet. Each title shows its
// copies (docs/design/ui.md "Library views"): bars per copy on the posters, a column per copy in the table. The view,
// the table's columns and the poster options are kept per user; Select (managers) feeds the bulk monitor.
import { ArrowDownWideNarrow, ArrowUpNarrowWide, CheckSquare, Columns3, LayoutGrid, Rows3, Settings2, SlidersHorizontal } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router";
import { useAuth } from "@/api/auth";
import { useFacets, usePerms, useTitleGrid } from "@/api/library";
import { BulkBar } from "@/components/library/bulk-monitor";
import { FiltersPanel, SELECT, type FilterValues } from "@/components/library/filters";
import { PosterGrid } from "@/components/library/poster-grid";
import { TitleTable, type Selection } from "@/components/library/title-table";
import { ColumnPicker, PosterOptions } from "@/components/library/view-options";
import { Empty, ErrorState, Loading, PageHeader } from "@/components/page";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Modal } from "@/components/ui/modal";
import { fmtBytes, fmtNumber } from "@/lib/format";
import {
  copyColumns,
  defaultDesc,
  distinctLabels,
  relabel,
  normalizeColumns,
  normalizePosters,
  readPref,
  visibleColumns,
  writePref,
  type ColumnPrefs,
  type PosterPrefs,
} from "@/lib/copies";
import { activeFilters, CATALOGS, FILTER_KEYS, gridQuery, SORTS, type CatalogId } from "@/lib/library";
import { cn } from "@/lib/utils";

const VIEW_KEY = "theatarr.catalog.view";

function readView(): "grid" | "table" {
  try {
    return window.localStorage.getItem(VIEW_KEY) === "table" ? "table" : "grid";
  } catch {
    return "grid";
  }
}

function FilterSheet({ open, onClose, children }: { open: boolean; onClose: () => void; children: React.ReactNode }) {
  if (!open) return null;
  return (
    <Modal
      label="Filters"
      onClose={onClose}
      className="z-50 lg:hidden"
      closeFrom={1024}
      testid="filter-sheet"
      backdropClassName="bg-[var(--scrim)] backdrop-blur-sm animate-in fade-in-0"
      panelClassName="absolute inset-y-0 right-0 flex w-[min(380px,92vw)] flex-col overflow-y-auto border-l border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] animate-in slide-in-from-right"
    >
      {children}
    </Modal>
  );
}

export function Catalog({ id }: { id: CatalogId }) {
  const cat = CATALOGS[id];
  const [params, setParams] = useSearchParams();
  const perms = usePerms();
  const query = useMemo(() => gridQuery(id, params), [id, params]);
  const grid = useTitleGrid(query);
  const facets = useFacets(cat.kind, cat.anime);
  const [sheet, setSheet] = useState(false);
  const [view, setView] = useState(readView);
  const uid = useAuth().data?.id;
  const [colPrefs, setColPrefs] = useState<ColumnPrefs | null>(null);
  const [posterPrefs, setPosterPrefs] = useState<PosterPrefs | null>(null);
  const columnsPref = colPrefs ?? readPref("columns", uid, normalizeColumns);
  const postersPref = posterPrefs ?? readPref("posters", uid, normalizePosters);
  const [dialog, setDialog] = useState<"columns" | "posters" | null>(null);
  const [selecting, setSelecting] = useState(false);
  const [chosen, setChosen] = useState<ReadonlySet<number>>(new Set());
  // A selection belongs to one catalog.
  useEffect(() => {
    setSelecting(false);
    setChosen(new Set());
  }, [id]);

  const set = useCallback(
    (key: string, value: string | undefined) =>
      setParams(
        (p) => {
          const n = new URLSearchParams(p);
          if (value === undefined || value === "") n.delete(key);
          else n.set(key, value);
          return n;
        },
        { replace: true },
      ),
    [setParams],
  );
  const clear = useCallback(
    () =>
      setParams(
        (p) => {
          const n = new URLSearchParams(p);
          FILTER_KEYS.forEach((k) => n.delete(k));
          return n;
        },
        { replace: true },
      ),
    [setParams],
  );
  const values: FilterValues = Object.fromEntries(FILTER_KEYS.map((k) => [k, params.get(k) ?? undefined]));
  const changeView = (v: "grid" | "table") => {
    setView(v);
    try {
      window.localStorage.setItem(VIEW_KEY, v);
    } catch {
      // not kept
    }
  };

  const raw = useMemo(() => grid.data?.pages.flatMap((p) => p.items ?? []) ?? [], [grid.data]);
  // One display label per copy for the whole page (posters, table, filters, bulk): same-named copies told apart.
  const rawColumns = useMemo(() => copyColumns(raw), [raw]);
  const names = useMemo(() => distinctLabels([...rawColumns, ...(facets.data?.versions ?? [])]), [rawColumns, facets.data]);
  const titles = useMemo(() => raw.map((t) => (t.versions ? { ...t, versions: relabel(t.versions, names) } : t)), [raw, names]);
  const columns = useMemo(() => relabel(rawColumns, names), [rawColumns, names]);
  const facetsShown = useMemo(() => (facets.data ? { ...facets.data, versions: relabel(facets.data.versions ?? [], names) } : undefined), [facets.data, names]);
  const total = grid.data?.pages[0]?.total;
  const more = !!grid.hasNextPage && !grid.isFetchingNextPage;
  const onMore = useCallback(() => void grid.fetchNextPage(), [grid]);
  const sort = query.sort ?? "name";
  const desc = query.order ? query.order === "desc" : defaultDesc(sort);
  const extra = visibleColumns(columnsPref, perms.seeFiles);
  const saveColumns = (p: ColumnPrefs) => {
    setColPrefs(p);
    writePref("columns", uid, p);
  };
  const savePosters = (p: PosterPrefs) => {
    setPosterPrefs(p);
    writePref("posters", uid, p);
  };
  // A header asks for a sort: the same one again flips the order, a new one starts in its default order.
  const onSort = (s: { sort: string; sort_version?: string }) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p);
        const same = (n.get("sort") ?? "name") === s.sort && (n.get("sort_version") ?? "") === (s.sort_version ?? "");
        if (same) n.set("order", desc ? "asc" : "desc");
        else n.delete("order");
        if (s.sort === "name") n.delete("sort");
        else n.set("sort", s.sort);
        if (s.sort_version) n.set("sort_version", s.sort_version);
        else n.delete("sort_version");
        return n;
      },
      { replace: true },
    );
  // The dropdown sets the sort and drops the copy and order in one URL update: separate updates in one event each
  // start from the same render's params, so only the last one would stick.
  const onPick = (s: string) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p);
        if (s === "name") n.delete("sort");
        else n.set("sort", s);
        n.delete("sort_version");
        n.delete("order");
        return n;
      },
      { replace: true },
    );
  const select: Selection | undefined = selecting
    ? {
        chosen,
        toggle: (tid, on) =>
          setChosen((c) => {
            const n = new Set(c);
            if (on) n.add(tid);
            else n.delete(tid);
            return n;
          }),
        all: (on) => setChosen(on ? new Set(titles.map((t) => t.id ?? 0)) : new Set()),
      }
    : undefined;
  const chosenTitles = titles.filter((t) => chosen.has(t.id ?? 0));
  const n = activeFilters(params);
  const size = titles.reduce((s, t) => s + (t.size_bytes ?? 0), 0);

  const panel = (onClose?: () => void) => (
    <FiltersPanel values={values} set={set} clear={clear} facets={facetsShown} seeFiles={perms.seeFiles} kind={cat.kind} onClose={onClose} />
  );

  return (
    <div className="lg:grid lg:grid-cols-[minmax(0,1fr)_300px] lg:gap-6 xl:grid-cols-[minmax(0,1fr)_330px]">
      <div className="min-w-0">
        <PageHeader
          title={cat.title}
          description={
            total !== undefined
              ? `${fmtNumber(total)} title${total === 1 ? "" : "s"}${n ? " match the filters" : ""}${perms.seeFiles && size && !grid.hasNextPage ? ` · ${fmtBytes(size)}` : ""}`
              : " "
          }
        />
        <div className="mb-4 flex flex-wrap items-center gap-2">
          <label className="flex min-w-0 items-center gap-2 text-sm text-muted-foreground">
            <span className="hidden sm:inline">Sort</span>
            <select aria-label="Sort by" className={cn(SELECT, "w-auto")} value={sort} onChange={(e) => onPick(e.target.value)}>
              {SORTS.filter((s) => !("seeFiles" in s) || perms.seeFiles).map((s) => (
                <option key={s.id} value={s.id}>{s.label}</option>
              ))}
            </select>
          </label>
          <Button
            variant="outline"
            size="icon-sm"
            aria-label={desc ? "Descending" : "Ascending"}
            title={desc ? "Descending" : "Ascending"}
            onClick={() => set("order", desc ? "asc" : "desc")}
          >
            {desc ? <ArrowDownWideNarrow /> : <ArrowUpNarrowWide />}
          </Button>
          {view === "table" ? (
            <Button variant="outline" size="sm" aria-label="Columns" onClick={() => setDialog("columns")}>
              <Columns3 />
              <span className="hidden sm:inline">Columns</span>
            </Button>
          ) : (
            <Button variant="outline" size="sm" aria-label="Poster options" onClick={() => setDialog("posters")}>
              <Settings2 />
              <span className="hidden sm:inline">Poster options</span>
            </Button>
          )}
          {perms.manage ? (
            <Button
              variant={selecting ? "default" : "outline"}
              size="sm"
              aria-pressed={selecting}
              onClick={() => {
                setSelecting(!selecting);
                setChosen(new Set());
              }}
            >
              <CheckSquare />
              {selecting ? "Done" : "Select"}
            </Button>
          ) : null}
          <div role="radiogroup" aria-label="View" className="ml-auto flex rounded-lg border border-[var(--line)] p-0.5">
            {([["grid", LayoutGrid], ["table", Rows3]] as const).map(([v, Icon]) => (
              <button
                key={v}
                type="button"
                role="radio"
                aria-checked={view === v}
                aria-label={v === "grid" ? "Posters" : "Table"}
                onClick={() => changeView(v)}
                className={cn("grid size-8 place-items-center rounded-md text-[var(--ink-3)] transition", view === v && "bg-[color-mix(in_srgb,var(--accent)_22%,transparent)] text-[var(--accent-ink)]")}
              >
                <Icon className="size-4" />
              </button>
            ))}
          </div>
          <Button variant="outline" size="sm" className="lg:hidden" onClick={() => setSheet(true)} data-testid="filters-button">
            <SlidersHorizontal />
            Filters{n ? <span className="rounded-md bg-[color-mix(in_srgb,var(--accent)_20%,transparent)] px-1.5 text-xs text-[var(--accent-ink)]">{n}</span> : null}
          </Button>
        </div>
        {grid.isPending ? (
          <Loading />
        ) : grid.error ? (
          <ErrorState error={grid.error} />
        ) : titles.length === 0 ? (
          <Card>
            <Empty>{n ? "No titles match these filters." : `No ${cat.title.toLowerCase()} yet.`}</Empty>
          </Card>
        ) : view === "table" ? (
          <TitleTable
            titles={titles}
            more={more}
            onMore={onMore}
            seeFiles={perms.seeFiles}
            manage={perms.manage}
            columns={columns}
            extra={extra}
            sort={{ sort, sortVersion: query.sort_version, desc }}
            onSort={onSort}
            select={select}
          />
        ) : (
          <>
            {select ? (
              <div className="mb-3 flex flex-wrap gap-3 text-sm">
                <button type="button" className="text-[var(--accent-ink)] hover:underline" onClick={() => select.all(true)}>Select all shown</button>
                <button type="button" className="text-[var(--accent-ink)] hover:underline" onClick={() => select.all(false)}>Clear selection</button>
              </div>
            ) : null}
            <PosterGrid titles={titles} more={more} onMore={onMore} library={{ prefs: postersPref, select }} />
          </>
        )}
        {grid.isFetchingNextPage ? <Loading label="Loading more" /> : null}
        {selecting ? <BulkBar titles={chosenTitles} columns={columns} onClear={() => setChosen(new Set())} /> : null}
        {dialog === "columns" ? <ColumnPicker prefs={columnsPref} seeFiles={perms.seeFiles} onChange={saveColumns} onClose={() => setDialog(null)} /> : null}
        {dialog === "posters" ? <PosterOptions prefs={postersPref} seeFiles={perms.seeFiles} onChange={savePosters} onClose={() => setDialog(null)} /> : null}
      </div>
      <div className="hidden lg:block">
        <div className="sticky top-[88px] max-h-[calc(100dvh-104px)] overflow-y-auto rounded-xl border border-[var(--line)] [scrollbar-width:thin]">
          {panel()}
        </div>
      </div>
      <FilterSheet open={sheet} onClose={() => setSheet(false)}>
        {panel(() => setSheet(false))}
      </FilterSheet>
    </div>
  );
}
