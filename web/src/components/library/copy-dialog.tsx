// A title's copies in one dialog (docs/design/ui.md "Library views"), opened from the library table: per library its
// current file, state, acquisition stage with its bar, and the monitor switch (managers). The movie copy dialog of the design; series get it too. It reads the grid's row only, and its one
// write is the copy's monitoring through the bulk route: searching and grabbing stay on the title page.
import { X } from "lucide-react";
import { useRef } from "react";
import { Link } from "react-router";
import { useBulkMonitor } from "@/api/copies";
import type { TitleRow } from "@/api/library";
import { VersionStatus } from "@/components/version-status";
import { toast } from "@/components/toaster";
import { ProgressBar } from "@/components/activity-bits";
import { MonitorSwitch } from "@/components/title/manage-bits";
import { Button } from "@/components/ui/button";
import { errText } from "@/components/ui/confirm-dialog";
import { Modal } from "@/components/ui/modal";
import { copyBar, copyStatus } from "@/lib/copies";
import { fmtBytes } from "@/lib/format";
import { upcomingRelease } from "@/lib/library";
import { resolutionWords } from "@/lib/upgrade";
import { cn } from "@/lib/utils";
import { CopyBarLine, CopyDot, StageLink } from "./copy-bits";

type Version = NonNullable<TitleRow["versions"]>[number];

/** A copy's monitor switch: one item through POST /titles/monitor. */
export function CopyMonitor({ t, v }: { t: TitleRow; v: Version }) {
  const mon = useBulkMonitor();
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
      <MonitorSwitch
        size="sm"
        on={!!v.monitored}
        label={`Monitor ${t.name} ${v.label}`}
        disabled={mon.isPending}
        onChange={(on) =>
          mon.mutate(
            { title_ids: [t.id!], versions: [v.key!], monitored: on },
            {
              onSuccess: (r) => toast({ title: t.name ?? "", body: r.message, tone: r.changed ? "ok" : "info" }),
              onError: (e) => toast({ title: t.name ?? "", body: errText(e), tone: "bad" }),
            },
          )
        }
      />
      {v.monitored ? "Monitored" : "Not monitored"}
    </span>
  );
}

const TONE = {
  positive: "text-[var(--ok-ink)]",
  warning: "text-[var(--warn-ink)]",
  negative: "text-[var(--bad-ink)]",
  neutral: "text-muted-foreground",
} as const;

export function CopyDialog({ t, seeFiles, manage, onClose }: { t: TitleRow; seeFiles: boolean; manage: boolean; onClose: () => void }) {
  const close = useRef<HTMLButtonElement>(null);
  const vs = t.versions ?? [];
  return (
    <Modal
      label={`${t.name} libraries`}
      onClose={onClose}
      initialFocus={close}
      testid="copy-dialog"
      className="z-[85] flex items-end justify-center sm:items-center sm:p-6"
      backdropClassName="absolute inset-0 bg-[var(--scrim)] backdrop-blur-sm animate-in fade-in-0"
      panelClassName="relative flex max-h-[88dvh] w-full flex-col rounded-t-[8px] border border-[var(--line)] bg-[var(--surface)] shadow-[var(--shadow-2)] sm:max-w-2xl sm:rounded-[8px]"
    >
      <div className="relative border-b border-[var(--line)] p-4 pr-12">
        <h2 className="text-lg font-semibold text-[var(--ink)] [overflow-wrap:anywhere]">{t.name}</h2>
        <p className="text-sm text-muted-foreground">
          {vs.length === 1 ? "1 library" : `${vs.length} libraries`}
        </p>
        <button ref={close} type="button" onClick={onClose} aria-label="Close" className="absolute right-3 top-3 grid size-8 place-items-center rounded-lg text-[var(--ink-3)] hover:bg-[var(--hover)]">
          <X className="size-4" />
        </button>
      </div>
      <div className="grid min-h-0 flex-1 gap-3 overflow-y-auto p-4 text-sm sm:grid-cols-2">
        {vs.map((v) => {
          const st = copyStatus(v, t.kind, !seeFiles, upcomingRelease(t));
          const q = seeFiles ? v.quality : resolutionWords(v.quality);
          const a = v.acquisition;
          return (
            <section key={v.key} className="min-w-0 space-y-2 rounded-xl border border-[var(--line)] p-3" data-copy={v.key}>
              <h3 className="flex min-w-0 items-center gap-1.5 font-semibold">
                <CopyDot vkey={v.key} />
                <span className="truncate">{v.label}</span>
              </h3>
              {v.control?.grab_state ? <VersionStatus state={v.control.grab_state} showMode /> : null}
              <div>
                <div className="text-[11px] font-bold uppercase tracking-[0.05em] text-[var(--ink-3)]">Current file</div>
                <div>{v.has_file ? q || "Held" : "No current file"}</div>
                {seeFiles && v.size_bytes ? <div className="text-xs text-muted-foreground">{fmtBytes(v.size_bytes)}{v.files && v.files > 1 ? ` · ${v.files} files` : ""}</div> : null}
              </div>
              <div className={cn("font-medium", TONE[st.tone])}>{st.label}</div>
              {a ? (
                <div className="space-y-1">
                  <StageLink a={a} />
                  {a.stage === "downloading" && a.progress !== undefined ? <ProgressBar pct={a.progress} tone="active" /> : null}
                  {a.upgrade ? <p className="text-xs text-muted-foreground">The current file stays available until the new one is linked.</p> : null}
                </div>
              ) : null}
              {t.kind === "series" ? <CopyBarLine vkey={v.key} label={v.label ?? v.key ?? ""} bar={copyBar(v, t.kind)} /> : null}
              {manage ? <CopyMonitor t={t} v={v} /> : null}
            </section>
          );
        })}
      </div>
      <div className="flex flex-wrap items-center justify-end gap-2 border-t border-[var(--line)] p-3">
        <span className="mr-auto text-xs text-[var(--ink-3)]">Search, releases and files are on the title page.</span>
        <Button size="sm" variant="outline" asChild>
          <Link to={`/title/${t.id}`}>Open title page</Link>
        </Button>
      </div>
    </Modal>
  );
}
