// /search?q=&kind=: every result for a query, the library as rows and TMDb as a poster grid with Add (docs/design/ui.md,
// Search). The overlay shows the first few of each; Enter or "See all" lands here. The URL is the state: the box
// follows it (a new search from the overlay replaces what was here), and typing writes it back once the typing settles.
// The kind goes to the server before the limit, so "Series" is every matching series, not the series among the
// first page of everything; the library part pages with "Show more" and counts with library_total.
import { Loader2, Search } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { useDebounced, useLibrarySearchPages, useSearch, type SearchKind } from "@/api/add";
import { usePerms } from "@/api/library";
import { PageHeader } from "@/components/page";
import { useSearchUI } from "@/components/search/search-context";
import {
  GroupHeader,
  LibraryResultCard,
  SearchEmpty,
  SectionError,
  SectionLoading,
  SectionNote,
  TmdbResultCard,
  searchProblem,
  searchWords,
  tmdbNote,
  TmdbNote,
} from "@/components/search/search-results";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

const LIBRARY_PAGE = 50;

export function SearchPage() {
  const [params, setParams] = useSearchParams();
  const urlQ = (params.get("q") ?? "").trim();
  const kindParam = params.get("kind");
  const kind: SearchKind = kindParam === "movie" || kindParam === "series" ? kindParam : "";
  const { openAdd, canAdd, canRequest } = useSearchUI();
  const words = searchWords(canAdd, canRequest);

  // The box: what is typed, written to the URL once it settles. A URL change that did not come from the box (the
  // overlay's "See all", back and forward) replaces it.
  const [draft, setDraft] = useState(urlQ);
  const written = useRef(urlQ);
  useEffect(() => {
    if (urlQ === written.current) return;
    written.current = urlQ;
    setDraft(urlQ);
  }, [urlQ]);
  const settled = useDebounced(draft, 300).trim();
  useEffect(() => {
    if (settled === written.current) return;
    written.current = settled;
    setParams(
      (p) => {
        const n = new URLSearchParams(p);
        if (settled) n.set("q", settled);
        else n.delete("q");
        return n;
      },
      { replace: true },
    );
  }, [settled, setParams]);

  const query = urlQ;
  const lib = useLibrarySearchPages(query, kind, LIBRARY_PAGE);
  const tm = useSearch(query, "tmdb", 40, kind);
  const libItems = lib.data?.pages.flatMap((p) => p.library) ?? [];
  const libTotal = lib.data?.pages[0]?.library_total;
  const hits = tm.data?.tmdb ?? [];
  const note = tmdbNote(tm.data?.tmdb_state);
  const perms = usePerms();
  const libProblem = searchProblem(lib.error);
  const tmProblem = searchProblem(tm.error, tm.data, "tmdb");
  const setKind = (k: SearchKind) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p);
        if (k) n.set("kind", k);
        else n.delete("kind");
        return n;
      },
      { replace: true },
    );
  return (
    <div>
      <PageHeader title="Search" description={words.page} />
      <form
        className="mb-4 flex h-11 max-w-2xl items-center gap-3 rounded-[5px] border border-[var(--control)] bg-[var(--surface)] focus-within:border-[var(--line-focus)] focus-within:ring-[3px] focus-within:ring-[var(--accent-soft)] px-3.5"
        onSubmit={(e) => e.preventDefault()}
        role="search"
      >
        <Search className="size-[18px] shrink-0 text-[var(--ink-3)]" />
        <input
          type="search"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder="Search movies, series and anime"
          aria-label="Search movies, series and anime"
          data-testid="search-page-input"
          autoFocus
          autoComplete="off"
          className="h-10 min-w-0 flex-1 border-0 bg-transparent text-[16px] text-[var(--ink)] caret-[var(--accent)] outline-none placeholder:text-[var(--ink-3)]"
        />
      </form>
      <div className="mb-6 flex gap-5 border-b border-[var(--line)]" role="group" aria-label="Kind">
        {([["", "All"], ["movie", "Movies"], ["series", "Series"]] as [SearchKind, string][]).map(([k, label]) => (
          <button
            key={k}
            type="button"
            onClick={() => setKind(k)}
            aria-pressed={kind === k}
            className={cn("-mb-px h-[38px] border-b-2 border-transparent text-[13.5px] text-[var(--ink-3)] transition-colors outline-none hover:text-[var(--ink)] focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--line-focus)]", kind === k && "border-[var(--ink)] font-medium text-[var(--ink)]")}
          >
            {label}
          </button>
        ))}
      </div>
      {!query ? (
        <SearchEmpty title="Find something to watch" description="Library matches show at once; TMDb results follow. Press / anywhere to search from the top bar." />
      ) : (
        <div className="space-y-8">
          <section data-testid="search-page-library">
            <GroupHeader title="In your library" note="already in the collection" count={libProblem ? undefined : libTotal} />
            {libProblem ? (
              <SectionError problem={libProblem} onRetry={() => void lib.refetch()} retrying={lib.isFetching} testid="search-library-error" />
            ) : lib.isPending ? (
              <SectionLoading label="Searching the library" />
            ) : libItems.length === 0 ? (
              <SectionNote>No {kind === "movie" ? "movie" : kind === "series" ? "series" : "title"} in the library matches “{query}”.</SectionNote>
            ) : (
              <>
                <div className="grid grid-cols-1 gap-3 lg:grid-cols-2 2xl:grid-cols-3">
                  {libItems.map((t) => <LibraryResultCard key={t.id} t={t} onAdd={openAdd} />)}
                </div>
                {lib.hasNextPage ? (
                  <div className="mt-3 flex items-center gap-3">
                    <Button variant="outline" size="sm" onClick={() => void lib.fetchNextPage()} disabled={lib.isFetchingNextPage} data-testid="search-library-more">
                      {lib.isFetchingNextPage ? <Loader2 className="animate-spin" /> : null}
                      Show more
                    </Button>
                    <span className="text-xs text-[var(--ink-3)]">
                      {libItems.length} of {libTotal}
                    </span>
                  </div>
                ) : null}
              </>
            )}
          </section>
          <section data-testid="search-page-tmdb">
            <GroupHeader title={words.tmdb} note="movies and series" count={tm.data?.tmdb_state === "ok" && !tmProblem ? hits.length : undefined} />
            {query.length < 2 ? (
              <SectionNote>Type at least two characters to search TMDb.</SectionNote>
            ) : tmProblem ? (
              <SectionError problem={tmProblem} onRetry={() => void tm.refetch()} retrying={tm.isFetching} testid="search-tmdb-error" admin={perms.admin} />
            ) : tm.isPending ? (
              <SectionLoading label="Searching TMDb" />
            ) : note ? (
              <TmdbNote state={tm.data?.tmdb_state} admin={perms.admin} />
            ) : hits.length === 0 ? (
              <SectionNote>TMDb has nothing for “{query}”.</SectionNote>
            ) : (
              <div className="grid grid-cols-3 gap-3 sm:grid-cols-4 sm:gap-4 md:grid-cols-5 lg:grid-cols-6 xl:grid-cols-7 2xl:grid-cols-8">
                {hits.map((h) => <TmdbResultCard key={`${h.media_type}:${h.tmdb_id}`} h={h} onAdd={openAdd} />)}
              </div>
            )}
          </section>
        </div>
      )}
    </div>
  );
}
