// A library's name and colour mark, the same on every screen (docs/design/ui.md, "Library names"). The names come
// from the server (GET auth/me `libraries`, internal/web/library.go libraryNames): the configured name, else a
// default by kind and profile, with same-named libraries told apart by words, never by key. Every account gets
// them; none contains a key. The key stays the identifier: data-vkey, and the tooltip for accounts that see files
// (a household account never sees a key).
import { createContext, useCallback, useContext, useEffect, useMemo, type ReactNode } from "react";
import { libraryLabel } from "./library-label";

export type LibraryName = { key: string; label: string };

const Names = createContext<{ names: ReadonlyMap<string, string>; keys: boolean }>({ names: new Map(), keys: false });

/** How many colour marks the palette has (--v-0 … in styles/globals.css). */
export const MARKS = 10;

/** Each library's colour mark: its position among the configured keys in key order, so five libraries get five
 * different marks and a reload gives the same ones. A key not configured has no mark (the accent colour). */
export function libraryMarks(keys: readonly string[]): Map<string, number> {
  return new Map([...new Set(keys)].sort().map((k, i) => [k, i % MARKS]));
}

/** The style rules that give each configured library's data-vkey its mark (--vc). */
export function markRules(keys: readonly string[]): string {
  return [...libraryMarks(keys)].map(([k, i]) => `[data-vkey=${JSON.stringify(k)}]{--vc:var(--v-${i})}`).join("\n");
}

/** keys: the account sees files, so a library's key may show as its tooltip. */
/** Puts the mark rules on the page as a constructed style sheet: the page's CSP (style-src 'self') refuses an inline
 * <style>, not a sheet built through the CSSOM. Returns the cleanup. Without constructable sheets (old browsers,
 * tests) the marks stay the accent. */
function adoptRules(rules: string): (() => void) | undefined {
  if (!rules || typeof CSSStyleSheet === "undefined" || !("adoptedStyleSheets" in document)) return undefined;
  const sheet = new CSSStyleSheet();
  sheet.replaceSync(rules);
  document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  return () => { document.adoptedStyleSheets = document.adoptedStyleSheets.filter((s) => s !== sheet); };
}

export function LibraryNamesProvider({ libraries, keys = false, children }: { libraries?: readonly LibraryName[] | null; keys?: boolean; children: ReactNode }) {
  const value = useMemo(() => ({ names: new Map((libraries ?? []).map((l) => [l.key, l.label])), keys }), [libraries, keys]);
  const rules = useMemo(() => markRules([...value.names.keys()]), [value]);
  useEffect(() => adoptRules(rules), [rules]);
  return <Names.Provider value={value}>{children}</Names.Provider>;
}

/** A function that names a library key: the server's name, else the fallback given (a settings page's own guess from
 * the stored library), else the default its key suggests. */
export function useLibraryName(): (key: string, fallback?: string) => string {
  const { names } = useContext(Names);
  return useCallback((key: string, fallback?: string) => names.get(key) ?? (fallback || libraryLabel(key)), [names]);
}

/** A library key as a tooltip: the key for an account that sees files, nothing for the others. */
export function useLibraryKeyTip(): (key: string) => string | undefined {
  const { keys } = useContext(Names);
  return useCallback((key: string) => (keys ? key : undefined), [keys]);
}
