// Movies, Series and Anime (docs/design/ui.md "Library views"): posters or a table across the full width. The toolbar
// has the title search and Select (managers) on the left; Columns or Poster options, the view switch, Sort and Filter on
// the right. Filters open in a sheet at every width and show as removable chips under the toolbar. Filters and sort live
// in the URL, so links such as /movies?availability=missing open a filtered list; the view, the table's columns and the
// poster options are kept per list and per user in this browser (lib/prefs.ts). Select (managers) feeds the bulk bar.
import { ArrowDownWideNarrow, ArrowUpNarrowWide, CheckSquare, Columns3, LayoutGrid, Plus, Rows3, Settings2, SlidersHorizontal, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router";
import { useAuth } from "@/api/auth";
import { useFacets, usePerms, useTitleGrid } from "@/api/library";
import { BulkBar } from "@/components/library/bulk-monitor";
import { FiltersPanel, SELECT, TitleSearch, type FilterValues } from "@/components/library/filters";
import { PosterGrid } from "@/components/library/poster-grid";
import { TitleTable, type Selection } from "@/components/library/title-table";
import { ColumnPicker, PosterOptions } from "@/components/library/view-options";
import { Empty, ErrorState, Loading, PageHeader } from "@/components/page";
import { providerLabel } from "@/components/provider-pills";
import { useSearchUI } from "@/components/search/search-context";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Modal } from "@/components/ui/modal";
import { copyColumns, distinctLabels, relabel } from "@/lib/copies";
import { fmtBytes, plural, pluralWord } from "@/lib/format";
import { defaultColumns, defaultDesc, indexColumns, listColumns, sortColumn, visibleIndexColumns } from "@/lib/index-columns";
import { activeFilters, CATALOGS, FILTER_KEYS, filterChips, gridQuery, offeredSorts, withoutChip, type CatalogId } from "@/lib/library";
import { normalizePosters, type PosterPrefs } from "@/lib/poster-options";
import { prefKey, readPref, useColumnPrefs, writePref } from "@/lib/prefs";
import { resolutionWords } from "@/lib/upgrade";
import { cn } from "@/lib/utils";

/** The view kept before views were per list; read when a list has none of its own. */
const OLD_VIEW_KEY = "theatarr.catalog.view";

type View = "grid" | "table";

/** The list's Add button opens the top search (it finds every kind), where a result's Add opens the add sheet. */
const ADD_LABEL: Record<CatalogId, string> = { movies: "Add movie", series: "Add series", anime: "Add anime" };

function readView(list: CatalogId, uid: number | undefined): View {
  const norm = (v: unknown): View | undefined => (v === "table" || v === "grid" ? v : undefined);
  const own = readPref<View | undefined>(`view.${list}`, uid, norm);
  if (own) return own;
  try {
    return window.localStorage.getItem(OLD_VIEW_KEY) === "table" ? "table" : "grid";
  } catch {
    return "grid";
  }
}

/** Poster options per list, falling back to the shared ones kept before (so the size carries over). */
function readPosters(list: CatalogId, uid: number | undefined): PosterPrefs {
  try {
    if (window.localStorage.getItem(prefKey(`posters.${list}`, uid)) === null) return readPref("posters", uid, normalizePosters);
  } catch {
    // fall through to the default
  }
  return readPref(`posters.${list}`, uid, normalizePosters);
}

/** A choice kept per user under theatarr.library.<what>.<user>: what was saved in this session, else what the browser
 *  holds (read). */
function useListPref<T>(what: string, uid: number | undefined, read: () => T): [T, (v: T) => void] {
  const key = prefKey(what, uid);
  const [saved, setSaved] = useState<{ key: string; value: T } | null>(null);
  const value = saved?.key === key ? saved.value : read();
  return [
    value,
    (v: T) => {
      setSaved({ key, value: v });
      writePref(what, uid, v);
    },
  ];
}

function FilterSheet({ open, onClose, children }: { open: boolean; onClose: () => void; children: React.ReactNode }) {
  if (!open) return null;
  return (
    <Modal
      label="Filters"
      onClose={onClose}
      className="z-50"
      testid="filter-sheet"
      backdropClassName="bg-[var(--scrim)] backdrop-blur-sm animate-in fade-in-0"
      panelClassName="absolute inset-y-0 right-0 flex w-[min(380px,100vw)] flex-col overflow-y-auto border-l border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] animate-in slide-in-from-right"
    >
      {children}
    </Modal>
  );
}

export function Catalog({ id }: { id: CatalogId }) {
  const cat = CATALOGS[id];
  const [params, setParams] = useSearchParams();
  const perms = usePerms();
  const { openOverlay } = useSearchUI();
  const query = useMemo(() => gridQuery(id, params, perms.seeFiles), [id, params, perms.seeFiles]);
  const grid = useTitleGrid(query);
  const facets = useFacets(cat.kind, cat.anime);
  const [sheet, setSheet] = useState(false);
  const uid = useAuth().data?.id;
  const [view, setView] = useListPref<View>(`view.${id}`, uid, () => readView(id, uid));
  const [postersPref, savePosters] = useListPref<PosterPrefs>(`posters.${id}`, uid, () => readPosters(id, uid));
  const allColumns = useMemo(() => listColumns(id), [id]);
  const [columnsPref, saveColumns] = useColumnPrefs(`columns.${id}`, allColumns, defaultColumns(id));
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

  const raw = useMemo(() => grid.data?.pages.flatMap((p) => p.items ?? []) ?? [], [grid.data]);
  // One display label per copy for the whole page (posters, table, filters, bulk): same-named copies told apart.
  const rawColumns = useMemo(() => copyColumns(raw), [raw]);
  const names = useMemo(() => distinctLabels([...rawColumns, ...(facets.data?.versions ?? [])]), [rawColumns, facets.data]);
  const titles = useMemo(() => raw.map((t) => (t.versions ? { ...t, versions: relabel(t.versions, names) } : t)), [raw, names]);
  const libraries = useMemo(() => relabel(rawColumns, names), [rawColumns, names]);
  const facetsShown = useMemo(() => (facets.data ? { ...facets.data, versions: relabel(facets.data.versions ?? [], names) } : undefined), [facets.data, names]);
  const total = grid.data?.pages[0]?.total;
  const more = !!grid.hasNextPage && !grid.isFetchingNextPage;
  const onMore = useCallback(() => void grid.fetchNextPage(), [grid]);
  const sort = query.sort ?? "name";
  const desc = query.order ? query.order === "desc" : defaultDesc(sort);
  const offered = indexColumns(id, perms.seeFiles);
  const picked = visibleIndexColumns(columnsPref, id, perms.seeFiles).map((c) => offered.find((o) => o.id === c)!);
  // The table shows what it is sorted by: a sort whose column is not chosen (Next release, Latest file) shows that
  // column first, for as long as the sort lasts; the chosen columns are not changed.
  const sortedBy = sortColumn(sort);
  const sortCell = sortedBy && sortedBy !== "title" && !picked.some((c) => c.id === sortedBy) ? offered.find((o) => o.id === sortedBy) : undefined;
  const shown = sortCell ? [sortCell, ...picked] : picked;
  // A sort is asked for in one URL update (separate updates in one event each start from the same render's params, so
  // only the last would stick): the copy goes; a header asking for the sort already shown flips the order, anything
  // else starts in the sort's default order.
  const setSort = (s: string, header = false) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p);
        const same = (n.get("sort") ?? "name") === s && !n.get("sort_version");
        if (header && same) n.set("order", desc ? "asc" : "desc");
        else n.delete("order");
        if (s === "name") n.delete("sort");
        else n.set("sort", s);
        n.delete("sort_version");
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
  const chips = filterChips(params, {
    seeFiles: perms.seeFiles,
    kind: cat.kind,
    names,
    quality: perms.seeFiles ? undefined : resolutionWords,
    provider: providerLabel,
  });
  const removeChip = (c: (typeof chips)[number]) => setParams((p) => withoutChip(p, c), { replace: true });
  const size = titles.reduce((s, t) => s + (t.size_bytes ?? 0), 0);
  const addButton = perms.add ? (
    <Button onClick={() => openOverlay()} data-testid="catalog-add">
      <Plus />
      {ADD_LABEL[id]}
    </Button>
  ) : null;

  return (
    <div className="min-w-0">
      <PageHeader
        title={cat.title}
        description={
          total !== undefined
            ? `${plural(total, "title")}${n ? ` ${pluralWord(total, "matches", "match")} the filters` : ""}${perms.seeFiles && size && !grid.hasNextPage ? ` · ${fmtBytes(size)}` : ""}`
            : " "
        }
        actions={addButton}
      />
      <div className="mb-3 flex flex-wrap items-center gap-1.5 sm:gap-2" data-testid="catalog-toolbar">
        <TitleSearch value={values.q} set={set} className="w-full sm:w-64" />
        {perms.manage ? (
          <Button
            variant={selecting ? "default" : "outline"}
            size="sm"
            aria-pressed={selecting}
            aria-label={selecting ? "Done" : "Select"}
            onClick={() => {
              setSelecting(!selecting);
              setChosen(new Set());
            }}
          >
            <CheckSquare />
            <span className="hidden sm:inline">{selecting ? "Done" : "Select"}</span>
          </Button>
        ) : null}
        {/* On a phone the right-hand controls join Select's row and wrap one by one; from sm they sit at the right. */}
        <div className="contents sm:ml-auto sm:flex sm:min-w-0 sm:flex-wrap sm:items-center sm:justify-end sm:gap-2">
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
          <div role="radiogroup" aria-label="View" className="flex rounded-lg border border-[var(--line)] p-0.5">
            {([["grid", LayoutGrid], ["table", Rows3]] as const).map(([v, Icon]) => (
              <button
                key={v}
                type="button"
                role="radio"
                aria-checked={view === v}
                aria-label={v === "grid" ? "Posters" : "Table"}
                onClick={() => setView(v)}
                className={cn("grid size-8 place-items-center rounded-md text-[var(--ink-3)] transition", view === v && "bg-[color-mix(in_srgb,var(--accent)_22%,transparent)] text-[var(--accent-ink)]")}
              >
                <Icon className="size-4" />
              </button>
            ))}
          </div>
          <span className="flex min-w-0 items-center gap-1">
            <select aria-label="Sort by" className={cn(SELECT, "w-auto min-w-0 max-w-[6rem] sm:max-w-none")} value={sort} onChange={(e) => setSort(e.target.value)}>
              {offeredSorts(perms.seeFiles, id).map((s) => (
                <option key={s.id} value={s.id}>{s.label}</option>
              ))}
            </select>
            <Button
              variant="outline"
              size="icon-sm"
              aria-label={desc ? "Descending" : "Ascending"}
              title={desc ? "Descending" : "Ascending"}
              onClick={() =>
                setParams(
                  (p) => {
                    const n = new URLSearchParams(p);
                    // A sort the account is not offered was replaced by Title: drop it, or the new order is ignored too.
                    if ((n.get("sort") ?? "name") !== query.sort) {
                      n.delete("sort");
                      n.delete("sort_version");
                    }
                    n.set("order", desc ? "asc" : "desc");
                    return n;
                  },
                  { replace: true },
                )
              }
            >
              {desc ? <ArrowDownWideNarrow /> : <ArrowUpNarrowWide />}
            </Button>
          </span>
          <Button variant="outline" size="sm" aria-label={n ? `Filter, ${n} active` : "Filter"} onClick={() => setSheet(true)} data-testid="filters-button">
            <SlidersHorizontal />
            <span className="hidden sm:inline">Filter</span>
            {n ? <span className="rounded-md bg-[color-mix(in_srgb,var(--accent)_20%,transparent)] px-1.5 text-xs text-[var(--accent-ink)]">{n}</span> : null}
          </Button>
        </div>
      </div>
      {chips.length ? (
        <div className="mb-4 flex flex-wrap items-center gap-1.5" data-testid="active-filters">
          {chips.map((c) => (
            <button
              key={`${c.key}/${c.label}`}
              type="button"
              aria-label={`Remove filter: ${c.label}`}
              onClick={() => removeChip(c)}
              className="inline-flex max-w-full items-center gap-1 rounded-[4px] border border-[color-mix(in_srgb,var(--accent)_45%,transparent)] bg-[color-mix(in_srgb,var(--accent)_14%,transparent)] px-2 py-0.5 text-xs font-medium text-[var(--accent-ink)] hover:bg-[color-mix(in_srgb,var(--accent)_24%,transparent)]"
            >
              <span className="truncate">{c.label}</span>
              <X aria-hidden className="size-3 shrink-0" />
            </button>
          ))}
          <button type="button" onClick={clear} className="px-1 text-xs font-medium text-[var(--accent-ink)] hover:underline">
            Clear all
          </button>
        </div>
      ) : (
        <div className="mb-1" />
      )}
      {grid.isPending ? (
        <Loading />
      ) : grid.error ? (
        <ErrorState error={grid.error} />
      ) : titles.length === 0 ? (
        <Card>
          {n ? (
            <Empty>
              <span className="flex flex-col items-center gap-3">
                No titles match these filters.
                <Button variant="outline" size="sm" onClick={clear}>Clear filters</Button>
              </span>
            </Empty>
          ) : (
            <Empty>
              <span className="flex flex-col items-center gap-3">
                {`No ${cat.title.toLowerCase()} yet.`}
                {addButton}
              </span>
            </Empty>
          )}
        </Card>
      ) : view === "table" ? (
        <TitleTable
          titles={titles}
          more={more}
          onMore={onMore}
          seeFiles={perms.seeFiles}
          manage={perms.manage}
          columns={shown}
          sort={{ sort, sortVersion: query.sort_version, desc }}
          onSort={(s) => setSort(s, true)}
          select={select}
        />
      ) : (
        <>
          {select ? (
            <div className="mb-3 flex flex-wrap gap-3 text-sm">
              <button type="button" className="text-[var(--accent-ink)] hover:underline" onClick={() => select.all(true)}>Select all {titles.length} shown</button>
              <button type="button" className="text-[var(--accent-ink)] hover:underline" onClick={() => select.all(false)}>Clear selection</button>
            </div>
          ) : null}
          <PosterGrid titles={titles} more={more} onMore={onMore} library={{ prefs: postersPref, select }} />
        </>
      )}
      {grid.isFetchingNextPage ? <Loading label="Loading more" /> : null}
      {selecting ? <BulkBar titles={chosenTitles} columns={libraries} onClear={() => setChosen(new Set())} /> : null}
      {dialog === "columns" ? <ColumnPicker label="Columns" note="Title always stays. On a phone the chosen columns show as lines under each title." columns={offered} prefs={columnsPref} onChange={saveColumns} onClose={() => setDialog(null)} /> : null}
      {dialog === "posters" ? <PosterOptions catalog={id} prefs={postersPref} seeFiles={perms.seeFiles} onChange={savePosters} onClose={() => setDialog(null)} /> : null}
      <FilterSheet open={sheet} onClose={() => setSheet(false)}>
        <FiltersPanel values={values} set={set} clear={clear} facets={facetsShown} seeFiles={perms.seeFiles} kind={cat.kind} onClose={() => setSheet(false)} />
      </FilterSheet>
    </div>
  );
}
