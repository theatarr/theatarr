// Toasts for live events (Scryer turns its subscription updates into toasts; Seerr shows them bottom right). A tiny
// store outside React, so the event stream can push without a context; the Toaster renders the queue. No portal,
// no inline style: a fixed stack in the corner, above the phone tab bar.
import { CheckCircle2, Info, TriangleAlert, X, XCircle } from "lucide-react";
import { useEffect, useSyncExternalStore } from "react";
import { Link } from "react-router";
import { cn } from "@/lib/utils";

export type ToastTone = "ok" | "info" | "warn" | "bad";
export type Toast = { id: number; title: string; body?: string; tone: ToastTone; to?: string; image?: string };

let seq = 0;
let toasts: Toast[] = [];
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());

/** Shows a toast for a few seconds. At most four are on screen; the oldest goes first. */
export function toast(t: Omit<Toast, "id">): number {
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
  useEffect(() => {
    const h = window.setTimeout(() => dismiss(t.id), 6000);
    return () => window.clearTimeout(h);
  }, [t.id]);
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
        </div>
        {t.body ? <div className="mt-0.5 text-[13px] text-muted-foreground">{t.body}</div> : null}
      </div>
    </>
  );
  return (
    <div
      role="status"
      data-testid="toast"
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

export function Toaster() {
  const list = useSyncExternalStore(subscribe, () => toasts, () => toasts);
  if (list.length === 0) return null;
  return (
    <div
      aria-live="polite"
      className="pointer-events-none fixed inset-x-4 bottom-20 z-50 flex flex-col items-stretch gap-2 sm:inset-x-auto sm:right-5 sm:bottom-5 sm:w-[360px] md:bottom-5"
    >
      {list.map((t) => (
        <ToastCard key={t.id} t={t} />
      ))}
    </div>
  );
}
