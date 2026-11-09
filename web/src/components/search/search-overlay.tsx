// The global search overlay (opened by the top bar's box, `/`, Ctrl-K or Cmd-K) with two groups, the
// library (instant) and TMDb (debounced; through the server, cached a day). Enter or "See all" opens /search.
// Overlay layout and classes adapted from Scryer (github.com/scryer-media/scryer) apps/scryer-web/components/root/
// root-header.tsx @ 2ef7757c, GPL-3.0: the dimmed backdrop, the panel, the bordered input row with ESC and close, the
// sections. It runs in our Modal (components/ui/modal.tsx: focus trap, inert page, focus restore, scroll lock), not
// Radix Dialog, whose scroll lock injects a <style> element the CSP refuses.
import { ArrowRight, Eraser, Search, X } from "lucide-react";
import { useRef, useState } from "react";
import { useNavigate } from "react-router";
import { useDebounced, useSearch, type AddTarget } from "@/api/add";
import { usePerms } from "@/api/library";
import { buttonVariants } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { cn } from "@/lib/utils";
import { useSearchUI } from "./search-context";
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
} from "./search-results";

export function SearchOverlay({ initial, onClose, onAdd }: { initial: string; onClose: () => void; onAdd: (t: AddTarget) => void }) {
  const [q, setQ] = useState(initial);
  const dq = useDebounced(q, 250);
  const lib = useSearch(dq, "library", 6);
  const tm = useSearch(dq, "tmdb", 14);
  const navigate = useNavigate();
  const { canAdd, canRequest } = useSearchUI();
  const words = searchWords(canAdd, canRequest);
  const input = useRef<HTMLInputElement>(null);
  const seeAll = () => {
    const t = q.trim();
    onClose();
    navigate(t ? `/search?q=${encodeURIComponent(t)}` : "/search");
  };
  const add = (t: AddTarget) => {
    onAdd(t);
  };
  const query = dq.trim();
  const libItems = lib.data?.library ?? [];
  const hits = tm.data?.tmdb ?? [];
  const note = tmdbNote(tm.data?.tmdb_state);
  const perms = usePerms();
  const libProblem = searchProblem(lib.error);
  const tmProblem = searchProblem(tm.error, tm.data, "tmdb");
  return (
    <Modal
      label="Search"
      onClose={onClose}
      initialFocus={input}
      testid="search-overlay"
      className="z-[60] flex flex-col items-center sm:px-5 sm:pb-7 sm:pt-[4.5rem]"
      backdropSlot="global-search-overlay"
      backdropClassName="animate-in fade-in-0"
      panelSlot="global-search-panel"
      panelClassName="flex h-dvh w-full flex-col overflow-hidden border-[var(--line)] animate-in slide-in-from-top-3 sm:h-auto sm:max-h-[calc(100dvh-7rem)] sm:w-[min(920px,calc(100vw-2.5rem))] sm:rounded-[8px] sm:border sm:shadow-[var(--shadow-2)]"
    >
        <div className="flex items-center gap-2.5 border-b border-[var(--line)] px-3 py-3 sm:gap-[13px] sm:px-[18px] sm:py-4">
          <form
            className="relative flex h-11 min-w-0 flex-1 items-center gap-3 rounded-[5px] border border-[var(--control)] bg-[var(--surface)] focus-within:border-[var(--line-focus)] focus-within:ring-[3px] focus-within:ring-[var(--accent-soft)] px-3 sm:px-3.5"
            onSubmit={(e) => {
              e.preventDefault();
              seeAll();
            }}
          >
            <Search className="size-[18px] shrink-0 text-[var(--ink-3)]" />
            <input
              ref={input}
              type="search"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="Search movies, series and anime"
              aria-label="Search movies, series and anime"
              data-testid="search-input"
              autoComplete="off"
              data-1p-ignore="true"
              data-lpignore="true"
              className="block h-10 min-w-0 flex-1 appearance-none border-0 bg-transparent px-0 text-[16px] text-[var(--ink)] caret-[var(--accent)] outline-none placeholder:text-[var(--ink-3)] sm:text-[17px] [&::-webkit-search-cancel-button]:hidden"
            />
            {q ? (
              <button
                type="button"
                aria-label="Clear"
                onClick={() => {
                  setQ("");
                  input.current?.focus();
                }}
                className={cn(buttonVariants({ variant: "ghost", size: "icon-sm" }))}
              >
                <Eraser className="size-3.5" />
              </button>
            ) : null}
          </form>
          <kbd className="hidden rounded-[3px] border border-b-2 border-[var(--line-2)] bg-[var(--bg)] px-[5px] py-0.5 text-[11px] font-medium leading-none text-[var(--ink-3)] sm:inline">Esc</kbd>
          <button
            type="button"
            aria-label="Close search"
            onClick={onClose}
            className={cn(buttonVariants({ variant: "ghost", size: "icon-sm" }), "shrink-0")}
          >
            <X className="size-4" />
          </button>
        </div>
        <div data-slot="global-search-results" className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-3 sm:p-[18px]">
          {!query ? (
            <SearchEmpty title="Search your library and TMDb" description={words.empty} />
          ) : (
            <div className="space-y-6">
              <section data-testid="search-group-library">
                <GroupHeader title="In your library" note="already in the collection" count={lib.data && !libProblem ? lib.data.library_total : undefined} />
                {libProblem ? (
                  <SectionError problem={libProblem} onRetry={() => void lib.refetch()} retrying={lib.isFetching} testid="search-library-error" />
                ) : lib.isPending ? (
                  <SectionLoading label="Searching the library" />
                ) : libItems.length === 0 ? (
                  <SectionNote>No title in the library matches “{query}”.</SectionNote>
                ) : (
                  <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
                    {libItems.map((t) => (
                      <LibraryResultCard key={t.id} t={t} onOpen={onClose} onAdd={add} />
                    ))}
                  </div>
                )}
              </section>
              <section data-testid="search-group-tmdb">
                <GroupHeader title={words.tmdb} note="movies and series" count={tm.data?.tmdb_state === "ok" ? hits.length : undefined} />
                {query.length < 2 ? (
                  <SectionNote>Type at least two characters to search TMDb.</SectionNote>
                ) : tmProblem ? (
                  <SectionError problem={tmProblem} onRetry={() => void tm.refetch()} retrying={tm.isFetching} testid="search-tmdb-error" />
                ) : tm.isPending ? (
                  <SectionLoading label="Searching TMDb" />
                ) : note ? (
                  <TmdbNote state={tm.data?.tmdb_state} admin={perms.admin} onOpen={onClose} />
                ) : hits.length === 0 ? (
                  <SectionNote>TMDb has nothing for “{query}”.</SectionNote>
                ) : (
                  <div className="-mx-1 grid auto-cols-[118px] grid-flow-col gap-3 overflow-x-auto overscroll-x-contain px-1 pb-2 [scrollbar-width:thin] sm:auto-cols-[128px]">
                    {hits.map((h) => (
                      <TmdbResultCard key={`${h.media_type}:${h.tmdb_id}`} h={h} onAdd={add} onOpen={onClose} />
                    ))}
                  </div>
                )}
              </section>
            </div>
          )}
        </div>
        <div className="flex items-center justify-between gap-3 border-t border-[var(--line)] px-3 py-2.5 text-xs text-[var(--ink-3)] sm:px-[18px]">
          <span className="hidden sm:inline">Enter for every result · Esc to close</span>
          <button
            type="button"
            onClick={seeAll}
            data-testid="search-see-all"
            className="ml-auto flex items-center gap-1.5 rounded-[3px] font-medium text-[var(--accent-ink)] outline-none hover:underline focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--line-focus)]"
          >
            See all results
            <ArrowRight className="size-3.5" />
          </button>
        </div>
    </Modal>
  );
}
