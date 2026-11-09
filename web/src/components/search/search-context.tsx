// The search overlay and the add sheet are mounted once, in the shell; any screen opens them through this context
// (the top bar, the `/` and Ctrl-K shortcuts, the /search page, a title page's "Add versions"). An account without
// the add permission gets the request sheet instead: its "Add" is a request (docs/design/users.md); one that may do
// neither gets no add or request control at all.
import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import type { AddTarget } from "@/api/add";
import { AddSheet } from "@/components/add/add-sheet";
import { RequestSheet } from "@/components/request/request-sheet";
import { usePerms } from "@/api/library";
import { SearchOverlay } from "./search-overlay";

type SearchUI = {
  overlayOpen: boolean;
  openOverlay: (q?: string) => void;
  closeOverlay: () => void;
  openAdd: (t: AddTarget) => void;
  /** The account adds directly (true) or requests (false). */
  canAdd: boolean;
  /** Without add: the account may request; with neither, search only finds and opens titles. */
  canRequest: boolean;
};

const Ctx = createContext<SearchUI | null>(null);

export function useSearchUI(): SearchUI {
  const v = useContext(Ctx);
  if (!v) throw new Error("useSearchUI outside SearchProvider");
  return v;
}

export function SearchProvider({ children }: { children: ReactNode }) {
  const [overlay, setOverlay] = useState<{ open: boolean; q: string }>({ open: false, q: "" });
  const [target, setTarget] = useState<AddTarget | null>(null);
  const openOverlay = useCallback((q?: string) => setOverlay({ open: true, q: q ?? "" }), []);
  const closeOverlay = useCallback(() => setOverlay((o) => ({ ...o, open: false })), []);
  const openAdd = useCallback((t: AddTarget) => setTarget(t), []);
  const perms = usePerms();
  const canAdd = perms.add;
  const canRequest = perms.request;
  const value = useMemo(
    () => ({ overlayOpen: overlay.open, openOverlay, closeOverlay, openAdd, canAdd, canRequest }),
    [overlay.open, openOverlay, closeOverlay, openAdd, canAdd, canRequest],
  );
  const sheetKey = target ? `${target.kind}:${target.tmdb_id ?? ""}:${target.title_id ?? ""}` : "";
  return (
    <Ctx.Provider value={value}>
      {children}
      {overlay.open ? <SearchOverlay initial={overlay.q} onClose={closeOverlay} onAdd={openAdd} /> : null}
      {target && canAdd ? <AddSheet key={sheetKey} target={target} onClose={() => setTarget(null)} /> : null}
      {target && !canAdd && canRequest ? <RequestSheet key={sheetKey} target={target} onClose={() => setTarget(null)} /> : null}
    </Ctx.Provider>
  );
}
