// Per-user choices kept in this browser (docs/design/ui.md "Library views"): localStorage under
// theatarr.library.<what>.<user>. The key and read/write helpers and the column order helpers were moved unchanged from
// lib/copies.ts (which re-exports them); normalizeColumnPrefs and useColumnPrefs keep any table's chosen columns.
import { useState } from "react";
import { useAuth } from "@/api/auth";

/** Move a column one place up (-1) or down (+1) in the order. */
export function moveColumn<P extends { order: string[] }>(p: P, id: P["order"][number], by: -1 | 1): P {
  const order = [...p.order];
  const i = order.indexOf(id);
  const j = i + by;
  if (i < 0 || j < 0 || j >= order.length) return p;
  [order[i], order[j]] = [order[j], order[i]];
  return { ...p, order };
}

/** Swap two columns' places in the order (the picker moves a column past the next one it lists, skipping columns it
 *  does not list). */
export function swapColumns<P extends { order: string[] }>(p: P, a: P["order"][number], b: P["order"][number]): P {
  const order = [...p.order];
  const i = order.indexOf(a);
  const j = order.indexOf(b);
  if (i < 0 || j < 0) return p;
  [order[i], order[j]] = [order[j], order[i]];
  return { ...p, order };
}

// ---- Preferences ----

/** The localStorage key of a per-user library preference: theatarr.library.<what>.<user>. */
export function prefKey(what: string, user: number | string | undefined): string {
  return `theatarr.library.${what}.${user ?? "anon"}`;
}

export function readPref<T>(what: string, user: number | string | undefined, normalize: (v: unknown) => T): T {
  try {
    const raw = window.localStorage.getItem(prefKey(what, user));
    return normalize(raw ? JSON.parse(raw) : undefined);
  } catch {
    return normalize(undefined);
  }
}

export function writePref(what: string, user: number | string | undefined, value: unknown) {
  try {
    window.localStorage.setItem(prefKey(what, user), JSON.stringify(value));
  } catch {
    // not kept
  }
}

// ---- Chosen columns of a table ----

/** A column a table offers in its picker. */
export type ColumnDef<T extends string> = { id: T; label: string; hint: string };

/** A table's column choice: every column in its order, and the ones shown. */
export type ColumnPrefs<T extends string> = { order: T[]; shown: T[] };

/** The normalizer for a table's stored column choice (pass it to readPref): unknown ids dropped; columns added since
 *  the choice was stored are appended and hidden; nothing stored gives the defs' order and defaultShown. */
export function normalizeColumnPrefs<T extends string>(defs: readonly ColumnDef<T>[], defaultShown: readonly T[]): (v: unknown) => ColumnPrefs<T> {
  const known = (x: unknown): x is T => defs.some((c) => c.id === x);
  return (v) => {
    const o = (v && typeof v === "object" ? v : {}) as { order?: unknown; shown?: unknown };
    if (!Array.isArray(o.order) || !Array.isArray(o.shown)) return { order: defs.map((c) => c.id), shown: defaultShown.filter(known) };
    const order = [...new Set(o.order.filter(known))];
    const stored = new Set(order);
    const shown = [...new Set(o.shown.filter(known))].filter((id) => stored.has(id));
    for (const c of defs) if (!stored.has(c.id)) order.push(c.id);
    return { order, shown };
  };
}

/** A table's column choice for the signed-in user, kept under theatarr.library.<what>.<user>: [prefs, save]. */
export function useColumnPrefs<T extends string>(what: string, defs: readonly ColumnDef<T>[], defaultShown: readonly T[]): [ColumnPrefs<T>, (p: ColumnPrefs<T>) => void] {
  const uid = useAuth().data?.id;
  const [saved, setSaved] = useState<{ key: string; prefs: ColumnPrefs<T> } | null>(null);
  const key = prefKey(what, uid);
  const prefs = saved?.key === key ? saved.prefs : readPref(what, uid, normalizeColumnPrefs(defs, defaultShown));
  const save = (p: ColumnPrefs<T>) => {
    setSaved({ key, prefs: p });
    writePref(what, uid, p);
  };
  return [prefs, save];
}
