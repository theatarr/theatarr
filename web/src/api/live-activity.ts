// What the live stream's download and title events do (docs/design/ui.md, live updates): refresh the activity, the Continue row,
// the calendar and the title queries, and toast a download's change of state. Progress alone refreshes quietly. A
// download event also refreshes its title and the library lists, whose copies show the acquisition stage: at once on
// a change of state, and at most every 20 s for progress alone.
import type { QueryClient } from "@tanstack/react-query";
import { toast, type ToastTone } from "@/components/toaster";
import type { DownloadEvent, TitleEvent } from "./activity";
import { qk, titleChanged } from "./query-keys";

const TONE: Record<string, ToastTone> = {
  imported: "ok",
  ready: "ok",
  failed: "bad",
};

/**
 * The toast for a download event, or null when nothing worth a toast changed (progress only). The library key is for
 * accounts that see files; others get the words alone (docs/design/users.md).
 */
export function downloadToast(ev: DownloadEvent, seeFiles = true): { title: string; body: string; tone: ToastTone } | null {
  const changed = ev.previous ? ev.previous !== ev.state : true;
  if (!changed && ev.state !== "ready") return null;
  if (!ev.previous && ev.state === "downloading") return null;
  const name = ev.title.year ? `${ev.title.name} (${ev.title.year})` : ev.title.name;
  return { title: name, body: seeFiles && ev.version ? `${ev.words} · ${ev.version}` : ev.words, tone: TONE[ev.state] ?? "info" };
}

/** How often progress alone refreshes a title and the library lists. */
export const PROGRESS_REFRESH_MS = 20_000;
const lastTitle = new Map<string, number>();
let lastLists = 0;

/** Whether a download event refreshes its title and the lists now (and notes when it did). */
export function libraryRefresh(ev: Pick<DownloadEvent, "state" | "previous" | "title">, now: number): { title: boolean; lists: boolean } {
  const changed = ev.previous ? ev.previous !== ev.state : true;
  const key = String(ev.title?.id ?? "");
  const title = !!key && (changed || now - (lastTitle.get(key) ?? -Infinity) >= PROGRESS_REFRESH_MS);
  const lists = changed || now - lastLists >= PROGRESS_REFRESH_MS;
  if (title) lastTitle.set(key, now);
  if (lists) lastLists = now;
  return { title, lists };
}

export function onDownloadEvent(qc: QueryClient, raw: string, now: number = Date.now(), seeFiles = true) {
  let ev: DownloadEvent;
  try {
    ev = JSON.parse(raw) as DownloadEvent;
  } catch {
    return;
  }
  qc.invalidateQueries({ queryKey: qk.activity.all });
  const r = libraryRefresh(ev, now);
  if (r.title) qc.invalidateQueries({ queryKey: qk.title(ev.title.id) });
  if (r.lists) qc.invalidateQueries({ queryKey: qk.titles.all });
  const t = downloadToast(ev, seeFiles);
  if (t) toast({ ...t, to: "/activity", image: ev.title.poster });
}

export function onTitleEvent(qc: QueryClient, raw: string) {
  let ev: TitleEvent;
  try {
    ev = JSON.parse(raw) as TitleEvent;
  } catch {
    return;
  }
  for (const queryKey of titleChanged(ev.id)) qc.invalidateQueries({ queryKey });
  qc.invalidateQueries({ queryKey: qk.calendar.all });
  if (ev.reason === "versions") qc.invalidateQueries({ queryKey: qk.activity.all });
}
