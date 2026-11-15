// What the live stream's download and title events do (docs/design/ui.md, live updates). A download's change of state
// refreshes the activity lists (Home's Continue row among them), the calendar, its title and the library lists, whose
// copies show the acquisition stage, and toasts. Progress alone refreshes quietly only what shows progress: the queue
// and the Continue row at most every 5 s per download, its title and the lists at most every 20 s.
import type { QueryClient } from "@tanstack/react-query";
import { toast, type ToastTone } from "@/components/toaster";
import type { DownloadEvent, TitleEvent } from "./activity";
import { qk, titleChanged } from "./query-keys";
import { libraryLabel } from "@/lib/library-label";
import { titleWithYear } from "@/lib/format";

const TONE: Record<string, ToastTone> = {
  imported: "ok",
  ready: "ok",
  failed: "bad",
};

/**
 * The toast for a download event, or null when nothing worth a toast changed (progress only) or a removal ended it. The library's name
 * (nameOf, lib/library-names) is for accounts that see files; others get the words alone (docs/design/users.md).
 */
export function downloadToast(ev: DownloadEvent, seeFiles = true, nameOf: (key: string) => string = libraryLabel): { title: string; body: string; tone: ToastTone } | null {
  const changed = ev.previous ? ev.previous !== ev.state : true;
  if (!changed && ev.state !== "ready") return null;
  if (!ev.previous && ev.state === "downloading") return null;
  // A removal ended it: it is in History only, not in the queue the toast would open.
  if (ev.removed) return null;
  const name = titleWithYear(ev.title.name, ev.title.year);
  return { title: name, body: seeFiles && ev.version ? `${ev.words} · ${nameOf(ev.version)}` : ev.words, tone: TONE[ev.state] ?? "info" };
}

/** How often progress alone refreshes a title and the library lists. */
export const PROGRESS_REFRESH_MS = 20_000;
const lastTitle = new Map<string, number>();
let lastLists = 0;

/** Whether a download event refreshes its title and the lists now (and notes when it did); changed: stateChanged. */
export function libraryRefresh(ev: Pick<DownloadEvent, "title">, changed: boolean, now: number): { title: boolean; lists: boolean } {
  const key = String(ev.title?.id ?? "");
  const title = !!key && (changed || now - (lastTitle.get(key) ?? -Infinity) >= PROGRESS_REFRESH_MS);
  const lists = changed || now - lastLists >= PROGRESS_REFRESH_MS;
  if (title) lastTitle.set(key, now);
  if (lists) lastLists = now;
  return { title, lists };
}

/** How often progress alone refreshes the queue and the Continue row, per download. */
export const PROGRESS_LIVE_MS = 5_000;
const lastProgress = new Map<number, number>();
const lastState = new Map<number, string>();

/** Whether ev's state differs from the last one seen for its download (the first event of a download counts). The
 * server names previous only on a change, so a progress event cannot tell by itself. */
export function stateChanged(ev: Pick<DownloadEvent, "id" | "state">): boolean {
  const before = lastState.get(ev.id);
  lastState.set(ev.id, ev.state);
  return before !== ev.state;
}

export function onDownloadEvent(qc: QueryClient, raw: string, now: number = Date.now(), seeFiles = true, nameOf?: (key: string) => string) {
  let ev: DownloadEvent;
  try {
    ev = JSON.parse(raw) as DownloadEvent;
  } catch {
    return;
  }
  const changed = stateChanged(ev);
  if (changed) {
    lastProgress.delete(ev.id);
    qc.invalidateQueries({ queryKey: qk.activity.all });
    qc.invalidateQueries({ queryKey: qk.calendar.all });
  } else if (now - (lastProgress.get(ev.id) ?? -Infinity) >= PROGRESS_LIVE_MS) {
    lastProgress.set(ev.id, now);
    qc.invalidateQueries({ queryKey: [...qk.activity.all, "now"] });
    qc.invalidateQueries({ queryKey: [...qk.activity.all, "continue"] });
  }
  const r = libraryRefresh(ev, changed, now);
  if (r.title) qc.invalidateQueries({ queryKey: qk.title(ev.title.id) });
  if (r.lists) qc.invalidateQueries({ queryKey: qk.titles.all });
  const t = downloadToast(ev, seeFiles, nameOf);
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
  if (ev.reason === "versions") {
    qc.invalidateQueries({ queryKey: qk.activity.all });
    // A removal's debrid clean-up ends with this event: the torrent lists follow.
    qc.invalidateQueries({ queryKey: qk.torrents.all });
  }
}
