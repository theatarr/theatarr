// Toasts for live events (Scryer turns its subscription updates into toasts; Seerr shows them bottom right). A tiny
// store outside React, so the event stream can push without a context; the Toaster renders the queue. No portal,
// no inline style: a fixed stack in the corner, above the phone tab bar.
import { CheckCircle2, Info, TriangleAlert, X, XCircle } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { Link } from "react-router";
import { modalOpen, subscribeModals, topModal } from "@/lib/modal-stack";
import { cn } from "@/lib/utils";

export type ToastTone = "ok" | "info" | "warn" | "bad";
/** duration: how long it stays (ms), 6 s when unset; longer for a notice whose link is the point. */
/** count: how many times the same toast arrived (a repeat bumps it instead of stacking a copy). */
export type Toast = { id: number; title: string; body?: string; tone: ToastTone; to?: string; image?: string; duration?: number; count?: number };

let seq = 0;
let toasts: Toast[] = [];
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());

/** Shows a toast for a few seconds. At most four are on screen; the oldest goes first. The same toast again (same
 * words, tone and link) while one is on screen counts on that one ("×2") and starts its time again. */
export function toast(t: Omit<Toast, "id" | "count">): number {
  const same = toasts.find((x) => x.title === t.title && (x.body ?? "") === (t.body ?? "") && x.tone === t.tone && (x.to ?? "") === (t.to ?? ""));
  if (same) {
    toasts = toasts.map((x) => (x === same ? { ...x, count: (x.count ?? 1) + 1 } : x));
    emit();
    return same.id;
  }
  const id = ++seq;
  toasts = [...toasts, { ...t, id }].slice(-4);
  emit();
  return id;
}

export function dismiss(id: number) {
  toasts = toasts.filter((t) => t.id !== id);
  emit();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

const ICON = { ok: CheckCircle2, info: Info, warn: TriangleAlert, bad: XCircle } as const;
const TONE: Record<ToastTone, string> = {
  ok: "text-[var(--ok)]",
  info: "text-[var(--accent)]",
  warn: "text-[var(--warn)]",
  bad: "text-[var(--bad)]",
};

function ToastCard({ t }: { t: Toast }) {
  // Hovered or focused, it stays; leaving it (or a repeat) gives it its full time again.
  const [hovered, setHovered] = useState(false);
  const [focused, setFocused] = useState(false);
  const held = hovered || focused;
  useEffect(() => {
    if (held) return;
    const h = window.setTimeout(() => dismiss(t.id), t.duration ?? 6000);
    return () => window.clearTimeout(h);
  }, [t.id, t.duration, t.count, held]);
  const Icon = ICON[t.tone];
  const body = (
    <>
      {t.image ? (
        <img src={t.image} alt="" className="aspect-[2/3] w-9 shrink-0 rounded-md object-cover ring-1 ring-[var(--line-2)]" />
      ) : (
        <Icon className={cn("mt-0.5 size-5 shrink-0", TONE[t.tone])} />
      )}
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5 text-[13.5px] font-semibold text-[var(--ink)]">
          {t.image ? <Icon className={cn("size-4 shrink-0", TONE[t.tone])} /> : null}
          <span className="truncate">{t.title}</span>
          {(t.count ?? 1) > 1 ? <span className="shrink-0 rounded-[3px] bg-[var(--hover)] px-1 text-[11.5px] font-medium tabular-nums text-[var(--ink-2)]" data-testid="toast-count" aria-label={`${t.count} times`}>×{t.count}</span> : null}
        </div>
        {t.body ? <div className="mt-0.5 text-[13px] text-muted-foreground">{t.body}</div> : null}
      </div>
    </>
  );
  return (
    <div
      role="status"
      data-testid="toast"
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      onFocus={() => setFocused(true)}
      onBlur={(e) => { if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setFocused(false); }}
      className="pointer-events-auto flex w-full items-start gap-3 rounded-[8px] border border-[var(--line)] bg-[var(--surface)] p-3 text-[var(--ink)] shadow-[var(--shadow-2)] animate-in fade-in slide-in-from-bottom-2"
    >
      {t.to ? (
        <Link to={t.to} onClick={() => dismiss(t.id)} className="flex min-w-0 flex-1 items-start gap-3 outline-none">
          {body}
        </Link>
      ) : (
        <div className="flex min-w-0 flex-1 items-start gap-3">{body}</div>
      )}
      <button
        type="button"
        aria-label="Dismiss"
        onClick={() => dismiss(t.id)}
        className="-m-1 rounded-md p-1 text-muted-foreground hover:bg-[var(--hover)] hover:text-foreground"
      >
        <X className="size-4" />
      </button>
    </div>
  );
}

/** The toasts, above every dialog and sheet (z-[70] to z-[85], with their scrims) and below the skip link
 *  (z-[100]); the stack lets clicks through, each toast takes its own. While a modal is open the stack sits at the top
 *  of the screen (centred from sm up, clear of a side sheet), so it never covers a dialog's or sheet's buttons at the
 *  bottom; on a phone (toastTop) it sits above the top modal, over the backdrop, when there is room there (a short,
 *  bottom-aligned confirmation), else just below its header row (the row with its Close), so neither Close nor a button
 *  row is under it. */
export function Toaster() {
  const list = useSyncExternalStore(subscribe, () => toasts, () => toasts);
  const top = useSyncExternalStore(subscribeModals, modalOpen, () => false);
  const stack = useRef<HTMLDivElement>(null);
  const shown = list.length > 0;
  useLayoutEffect(() => {
    const el = stack.current;
    if (!el || !top) return;
    // A custom property set through the CSSOM (the CSP refuses style attributes, not this).
    const place = () => {
      const { panelTop, headerBottom } = modalGeometry();
      el.style.setProperty("--toast-top", `${toastTop(panelTop, headerBottom, el.offsetHeight)}px`);
    };
    place();
    // Again once a sliding sheet has settled.
    const t = setTimeout(place, 350);
    window.addEventListener("resize", place);
    return () => {
      clearTimeout(t);
      window.removeEventListener("resize", place);
    };
  }, [top, list.length]);
  if (!shown) return null;
  return (
    <div
      ref={stack}
      aria-live="polite"
      data-placement={top ? "top" : "bottom"}
      data-testid="toasts"
      className={cn(
        "pointer-events-none fixed z-[95] flex flex-col items-stretch gap-2",
        top
          ? "inset-x-4 top-[var(--toast-top,1rem)] sm:inset-x-auto sm:top-[calc(1rem+env(safe-area-inset-top))] sm:left-1/2 sm:w-[360px] sm:-translate-x-1/2"
          : "inset-x-4 bottom-20 sm:inset-x-auto sm:right-5 sm:bottom-5 sm:w-[360px] md:bottom-5",
      )}
    >
      {list.map((t) => (
        <ToastCard key={t.id} t={t} />
      ))}
    </div>
  );
}

/** The margin (px) around the toast stack at the top of a phone screen. */
export const TOAST_MARGIN = 16;

/** Where the toast stack's top goes on a phone while a modal is open (px from the viewport top): at the margin when the
 *  stack (height px) and its margins fit above the modal's panel (top edge panelTop), so the whole dialog stays clear;
 *  else just below the modal's header row (its bottom edge headerBottom), keeping Close reachable. */
export function toastTop(panelTop: number, headerBottom: number, height: number): number {
  if (panelTop >= height + 2 * TOAST_MARGIN) return TOAST_MARGIN;
  return Math.max(headerBottom + 8, TOAST_MARGIN);
}

/** The top modal's panel top edge and its header row's bottom edge (the row holding its Close button, else the panel's
 *  first child), px from the viewport top; 0 for what is not there. */
export function modalGeometry(): { panelTop: number; headerBottom: number } {
  const root = topModal() as unknown as Element | undefined;
  const panel = root?.querySelector?.('[aria-modal="true"]');
  if (!panel) return { panelTop: 0, headerBottom: 0 };
  const head = panel.querySelector('[aria-label="Close"]')?.parentElement ?? panel.firstElementChild;
  return { panelTop: Math.round(panel.getBoundingClientRect().top), headerBottom: head ? Math.round(head.getBoundingClientRect().bottom) : 0 };
}
