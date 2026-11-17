// Movies, Series and Anime (docs/design/ui.md "Library views"): posters or a table across the full width. The toolbar
// has the title search and Select (managers) on the left; Columns or Poster options, the view switch, Sort and Filter on
// the right. Filters open in a sheet at every width and show as removable chips under the toolbar. Filters, sort and a
// picked view (?view=grid|table) live in the URL, so links such as /movies?availability=missing&view=table open that
// list as it was; without ?view= the list opens in the view last picked, which is kept per list and per user in this
// browser, as are the table's columns and the poster options (lib/prefs.ts). Select (managers) feeds the bulk bar;
// "Select all N matches" loads the remaining pages of the same query first.
// Anime has All / Movies / Series (?kind=); Series and Anime have a Monitored shortcut (?monitored=true) in the toolbar.
import { ArrowDownWideNarrow, ArrowUpNarrowWide, CheckSquare, Columns3, Eye, LayoutGrid, Loader2, Plus, Rows3, Settings2, SlidersHorizontal, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { useAuth } from "@/api/auth";
import { useFacets, usePerms, useTitleGrid } from "@/api/library";
import { BulkBar } from "@/components/library/bulk-monitor";
import { FiltersPanel, SELECT, TitleSearch, type FilterValues } from "@/components/library/filters";
import { PosterGrid } from "@/components/library/poster-grid";
import { SavedFilters } from "@/components/library/saved-filters";
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
import { activeFilters, applySaved, CATALOGS, catalogKind, FILTER_KEYS, filterChips, gridQuery, offeredSorts, savedQuery, viewOf, withoutChip, type CatalogId, type ListView } from "@/lib/library";
import { normalizePosters, type PosterPrefs } from "@/lib/poster-options";
import { prefKey, readPref, useColumnPrefs, useListPref } from "@/lib/prefs";
import { resolutionWords } from "@/lib/upgrade";
import { cn } from "@/lib/utils";

/** The view kept before views were per list; read when a list has none of its own. */
const OLD_VIEW_KEY = "theatarr.catalog.view";

type View = ListView;

/** The selection, and the list query a "Select all N matches" is loading the pages of (null: none). */
type SelectionState = { chosen: ReadonlySet<number>; loadingFor: string | null };
type SelectionStep =
  | { t: "set"; chosen: ReadonlySet<number> }
  | { t: "toggle"; id: number; on: boolean }
  | { t: "start"; key: string }
  | { t: "loaded"; key: string; ids: number[] | null }
  | { t: "cancel"; unless: string };

/** Any change to the selection cancels a pending select-all; its answer applies only while it is still pending. */
function selectionStep(s: SelectionState, a: SelectionStep): SelectionState {
  switch (a.t) {
    case "set":
      return { chosen: a.chosen, loadingFor: null };
    case "toggle": {
      const chosen = new Set(s.chosen);
      if (a.on) chosen.add(a.id);
      else chosen.delete(a.id);
      return { chosen, loadingFor: null };
    }
    case "start":
      return { ...s, loadingFor: a.key };
    case "loaded":
      if (s.loadingFor !== a.key) return s;
      return { chosen: a.ids ? new Set(a.ids) : s.chosen, loadingFor: null };
    case "cancel":
      return s.loadingFor === null || s.loadingFor === a.unless ? s : { ...s, loadingFor: null };
  }
}

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
  // Anime's All / Movies / Series (?kind=): the list, its counts and its filters follow the kind shown.
  const kind = catalogKind(id, params);
  const facets = useFacets(kind, cat.anime);
  const [sheet, setSheet] = useState(false);
  const uid = useAuth().data?.id;
  const [remembered, remember] = useListPref<View>(`view.${id}`, uid, () => readView(id, uid));
  const view = viewOf(params, remembered);
  const [postersPref, savePosters] = useListPref<PosterPrefs>(`posters.${id}`, uid, () => readPosters(id, uid));
  const allColumns = useMemo(() => listColumns(id), [id]);
  const [columnsPref, saveColumns] = useColumnPrefs(`columns.${id}`, allColumns, defaultColumns(id));
  const [dialog, setDialog] = useState<"columns" | "posters" | null>(null);
  const [selecting, setSelecting] = useState(false);
  const [{ chosen, loadingFor }, pick] = useReducer(selectionStep, { chosen: new Set<number>(), loadingFor: null });
  const loadingAll = loadingFor !== null;
  // The pending select-all as the paging loop reads it between pages.
  const pending = useRef<string | null>(null);
  useEffect(() => {
    pending.current = loadingFor;
  }, [loadingFor]);
  // A selection belongs to one catalog.
  useEffect(() => {
    setSelecting(false);
    pick({ t: "set", chosen: new Set() });
  }, [id]);

  const set = useCallback(
    (key: string, value: string | undefined, drop?: string[]) =>
      setParams(
        (p) => {
          const n = new URLSearchParams(p);
          if (value === undefined || value === "") n.delete(key);
          else n.set(key, value);
          drop?.forEach((k) => n.delete(k));
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
  // Anime's Movies or Series segment: the columns and their words follow the kind shown; the stored choice is Anime's.
  const offered = indexColumns(id, perms.seeFiles, kind);
  const picked = visibleIndexColumns(columnsPref, id, perms.seeFiles, kind).map((c) => offered.find((o) => o.id === c)!);
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
        toggle: (tid, on) => pick({ t: "toggle", id: tid, on }),
        all: (on) => pick({ t: "set", chosen: on ? new Set(titles.map((t) => t.id ?? 0)) : new Set() }),
      }
    : undefined;
  // Select all N matches: the remaining pages of the same query, then every title of it. Changing the selection,
  // leaving select mode or changing the list's query meanwhile cancels it: its answer then changes nothing.
  const queryKey = JSON.stringify(query);
  const selectAllMatches = async () => {
    pick({ t: "start", key: queryKey });
    pending.current = queryKey;
    let r = await grid.fetchNextPage();
    // One page at a time, and only while it is still pending: a cancel stops the paging.
    while (r.hasNextPage && !r.isError && pending.current === queryKey) r = await grid.fetchNextPage();
    const ids = r.isError ? null : (r.data?.pages ?? []).flatMap((p) => p.items ?? []).map((t) => t.id ?? 0);
    pick({ t: "loaded", key: queryKey, ids });
  };
  useEffect(() => pick({ t: "cancel", unless: queryKey }), [queryKey]);
  const moreMatches = total !== undefined && total > titles.length;
  const chosenTitles = titles.filter((t) => chosen.has(t.id ?? 0));
  const n = activeFilters(params);
  const chips = filterChips(params, {
    seeFiles: perms.seeFiles,
    kind,
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
            : // A no-break space keeps the line's height while the count loads, so the toolbar does not move.
              "\u00a0"
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
              pick({ t: "set", chosen: new Set() });
            }}
          >
            <CheckSquare />
            <span className="hidden sm:inline">{selecting ? "Done" : "Select"}</span>
          </Button>
        ) : null}
        {cat.anime ? (
          <div role="radiogroup" aria-label="Kind" className="flex rounded-lg border border-[var(--line)] p-0.5" data-testid="anime-kind">
            {([[undefined, "All"], ["movie", "Movies"], ["series", "Series"]] as const).map(([k, label]) => (
              <button key={label} type="button" role="radio" aria-checked={kind === k} onClick={() =>
                  // Series status is for series: switching to Movies drops it.
                  setParams(
                    (p) => {
                      const n = new URLSearchParams(p);
                      if (k) n.set("kind", k);
                      else n.delete("kind");
                      if (k === "movie") n.delete("series_status");
                      return n;
                    },
                    { replace: true },
                  )
                }
                className={cn("h-8 rounded-md px-2.5 text-[13px] font-medium text-[var(--ink-3)] transition", kind === k && "bg-[color-mix(in_srgb,var(--accent)_22%,transparent)] text-[var(--accent-ink)]")}>
                {label}
              </button>
            ))}
          </div>
        ) : null}
        {/* Series and Anime: a visible shortcut to the monitored titles (nothing is hidden by default). */}
        {id !== "movies" ? (
          <Button variant={values.monitored === "true" ? "default" : "outline"} size="sm" aria-pressed={values.monitored === "true"}
            onClick={() => set("monitored", values.monitored === "true" ? undefined : "true")} data-testid="monitored-shortcut">
            <Eye />
            Monitored
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
                onClick={() => {
                  remember(v);
                  set("view", v);
                }}
                className={cn("grid size-8 place-items-center rounded-md text-[var(--ink-3)] transition", view === v && "bg-[color-mix(in_srgb,var(--accent)_22%,transparent)] text-[var(--accent-ink)]")}
              >
                <Icon className="size-4" />
              </button>
            ))}
          </div>
          {/* On a phone Sort takes the rest of its row (a new one if needed), so the chosen label shows whole. */}
          <span className="flex min-w-0 items-center gap-1 max-sm:min-w-[14rem] max-sm:flex-1">
            <select aria-label="Sort by" className={cn(SELECT, "w-auto min-w-0 max-sm:flex-1")} value={sort} onChange={(e) => setSort(e.target.value)}>
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
          <Button variant="outline" size="sm" className="sm:min-w-[6.5rem]" aria-label={n ? `Filter, ${n} active` : "Filter"} onClick={() => setSheet(true)} data-testid="filters-button">
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
      ) : (
        <>
          {select ? (
            <div className="mb-3 flex flex-wrap items-center gap-3 text-sm" data-testid="select-line">
              <button type="button" className="text-[var(--accent-ink)] hover:underline" onClick={() => select.all(true)}>Select all {titles.length} shown</button>
              {moreMatches || loadingAll ? (
                <button type="button" className="inline-flex items-center gap-1.5 text-[var(--accent-ink)] hover:underline disabled:no-underline disabled:opacity-70" disabled={loadingAll}
                  onClick={() => void selectAllMatches()} data-testid="select-all-matches">
                  {loadingAll ? <Loader2 className="size-3.5 animate-spin" /> : null}
                  Select all {total} matches
                </button>
              ) : null}
              <button type="button" className="text-[var(--accent-ink)] hover:underline" onClick={() => select.all(false)}>Clear selection</button>
            </div>
          ) : null}
          {view === "table" ? (
            <TitleTable
              titles={titles}
              more={more && !loadingAll}
              onMore={onMore}
              seeFiles={perms.seeFiles}
              manage={perms.manage}
              columns={shown}
              sort={{ sort, sortVersion: query.sort_version, desc }}
              onSort={(s) => setSort(s, true)}
              select={select}
            />
          ) : (
            <PosterGrid titles={titles} more={more && !loadingAll} onMore={onMore} library={{ prefs: postersPref, select, sort, catalog: id }} />
          )}
        </>
      )}
      {grid.isFetchingNextPage ? <Loading label="Loading more" /> : null}
      {selecting && loadingAll ? (
        <section aria-label="Bulk edit" data-testid="bulk-loading"
          className="sticky bottom-[calc(env(safe-area-inset-bottom)+4.5rem)] z-30 mt-4 flex items-center gap-2 rounded-xl border border-[var(--th-baccent)] bg-[var(--surface)] p-3 text-sm shadow-[var(--shadow-2)] md:bottom-3">
          <Loader2 className="size-4 animate-spin" />
          <span role="status">Loading all {total} matches…</span>
        </section>
      ) : selecting ? <BulkBar titles={chosenTitles} columns={libraries} kinds={kind ? [kind] : ["movie", "series"]} anime={cat.anime} onClear={() => pick({ t: "set", chosen: new Set() })} /> : null}
      {dialog === "columns" ? <ColumnPicker label="Columns" note="Title always stays. On a phone the chosen columns show as lines under each title." columns={offered} prefs={columnsPref} onChange={saveColumns} onClose={() => setDialog(null)} /> : null}
      {dialog === "posters" ? <PosterOptions catalog={id} prefs={postersPref} seeFiles={perms.seeFiles} onChange={savePosters} onClose={() => setDialog(null)} /> : null}
      <FilterSheet open={sheet} onClose={() => setSheet(false)}>
        <FiltersPanel values={values} set={set} clear={clear} facets={facetsShown} seeFiles={perms.seeFiles} kind={kind} onClose={() => setSheet(false)}
          saved={<SavedFilters list={id} uid={uid} current={savedQuery(params, cat.anime)} apply={(q) => setParams((p) => applySaved(p, q, cat.anime), { replace: true })} />} />
      </FilterSheet>
    </div>
  );
}
