// Pieces shared by the Activity screen and Home's Continue row: the progress bar, the step track
// (queued → adding → downloading → download complete → linked) and a download's tone. The bar follows Scryer's
// ActivityProgressBar (github.com/scryer-media/scryer apps/scryer-web/components/views/activity-progress-bar.tsx
// @ 2ef7757c, GPL-3.0): a rounded muted track with a coloured fill; the fill width is a 5 % class step, since the
// CSP refuses inline styles.
import { Check, X } from "lucide-react";
import { cn } from "@/lib/utils";
import type { ActivityDownload } from "@/api/activity";

const WIDTH = [
  "w-0", "w-[5%]", "w-[10%]", "w-[15%]", "w-[20%]", "w-[25%]", "w-[30%]", "w-[35%]", "w-[40%]", "w-[45%]", "w-[50%]",
  "w-[55%]", "w-[60%]", "w-[65%]", "w-[70%]", "w-[75%]", "w-[80%]", "w-[85%]", "w-[90%]", "w-[95%]", "w-full",
];

/** The width class for a percentage, rounded to the nearest 5 %. */
export function widthClass(pct: number): string {
  const i = Math.round(Math.min(Math.max(pct, 0), 100) / 5);
  return WIDTH[i];
}

export type DownloadTone = "active" | "ok" | "warn" | "bad" | "idle";

export function toneOf(d: { state: string; step: string; steps?: { state: string }[]; import_state?: string }): DownloadTone {
  if (d.state === "failed" || d.steps?.some((s) => s.state === "failed")) return "bad";
  if (d.import_state === "partial") return "warn";
  if (d.state === "imported") return "ok";
  if (d.state === "ready" || d.state === "importing") return "ok";
  if (d.state === "queued") return "idle";
  return "active";
}

const FILL: Record<DownloadTone, string> = {
  active: "bg-[var(--accent)]",
  ok: "bg-[var(--ok-ink)]",
  warn: "bg-[var(--warn)]",
  bad: "bg-[var(--bad-ink)]",
  idle: "bg-[var(--ink-3)]",
};

export function ProgressBar({ pct, tone, className }: { pct: number; tone: DownloadTone; className?: string }) {
  return (
    <div
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(pct)}
      className={cn("h-1.5 w-full overflow-hidden rounded-full bg-muted/90", className)}
    >
      <div className={cn("h-full rounded-full transition-[width] duration-500", FILL[tone], widthClass(pct))} />
    </div>
  );
}

/** The five steps as dots joined by a line; the current one pulses, a failed one is red. */
export function StepTrack({ steps, compact = false }: { steps: ActivityDownload["steps"]; compact?: boolean }) {
  return (
    <ol className="flex w-full items-center" aria-label="Steps">
      {steps.map((s, i) => (
        <li key={s.key} className={cn("flex items-start", i > 0 && "flex-1")} aria-current={s.state === "current" ? "step" : undefined}>
          {i > 0 ? (
            <span
              aria-hidden
              className={cn("mx-1 mt-[9px] h-0.5 flex-1 rounded-full", s.state === "todo" ? "bg-border" : "bg-[color-mix(in_srgb,var(--accent)_55%,transparent)]")}
            />
          ) : null}
          <span className="flex flex-col items-center gap-1">
            <span
              className={cn(
                "grid size-5 place-items-center rounded-full border text-[10px]",
                s.state === "done" && "border-transparent bg-[var(--accent)] text-[var(--on-accent)]",
                s.state === "current" && "border-[var(--accent)] bg-[color-mix(in_srgb,var(--accent)_18%,transparent)] text-[var(--accent-ink)] ring-4 ring-[color-mix(in_srgb,var(--accent)_15%,transparent)]",
                s.state === "failed" && "border-transparent bg-[var(--bad-solid)] text-[var(--on-solid)]",
                s.state === "todo" && "border-border bg-card text-muted-foreground",
              )}
            >
              {s.state === "done" ? <Check className="size-3" /> : s.state === "failed" ? <X className="size-3" /> : null}
              {s.state === "current" ? <span className="size-1.5 animate-pulse rounded-full bg-[var(--accent)]" /> : null}
            </span>
            {compact ? <span className="sr-only">{s.label}</span> : (
              <span
                className={cn(
                  "hidden text-[11px] font-medium sm:block",
                  s.state === "current" ? "text-foreground" : "text-muted-foreground",
                )}
              >
                {s.label}
              </span>
            )}
          </span>
        </li>
      ))}
    </ol>
  );
}

export function kindLabel(kind: string): string {
  return kind === "series" ? "Series" : kind === "anime" ? "Anime" : "Movie";
}
