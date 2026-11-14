// Page building blocks shared by the screens: header, sections, stat tiles, states, version tags.
import { AlertTriangle, ChevronRight, Inbox, Loader2 } from "lucide-react";
import { useEffect, type ReactNode } from "react";
import { PRODUCT_NAME } from "@/brand";
import { Link, useLocation, useNavigate } from "react-router";
import { itemFor } from "@/components/shell/nav";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Select } from "@/components/ui/select";
import { useLibraryKeyTip, useLibraryName } from "@/lib/library-names";
import { cn } from "@/lib/utils";

export function PageHeader({
  crumb,
  title,
  description,
  actions,
  docTitle: named,
}: {
  crumb?: { label: string; to?: string }[];
  title: ReactNode;
  /** The browser-tab title when the title is not plain text and the nav entry is not the page's name. */
  docTitle?: string;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  // The document title falls back to the nav entry for this page.
  const { pathname } = useLocation();
  const item = itemFor(pathname);
  const docTitle = typeof title === "string" ? title : named ?? item?.label;
  useEffect(() => {
    document.title = docTitle ? `${docTitle} · ${PRODUCT_NAME}` : PRODUCT_NAME;
  }, [docTitle]);
  return (
    <div className="mb-5 flex flex-wrap items-end justify-between gap-x-4 gap-y-3">
      <div className="min-w-0">
        {crumb ? (
          <nav aria-label="Breadcrumb" className="mb-2 flex flex-wrap items-center gap-1.5 text-[12.5px] text-[var(--ink-3)]">
            {[...crumb, ...(typeof title === "string" ? [{ label: title }] : [])].map((c, i, all) => (
              <span key={i} className="flex items-center gap-1.5">
                {i > 0 ? <ChevronRight className="size-3 text-[var(--ink-4)]" aria-hidden /> : null}
                {"to" in c && c.to ? (
                  <Link to={c.to} className="hover:text-[var(--ink)] hover:underline hover:underline-offset-2">{c.label}</Link>
                ) : (
                  <span className={i === all.length - 1 ? "text-[var(--ink-2)]" : undefined}>{c.label}</span>
                )}
              </span>
            ))}
          </nav>
        ) : null}
        <h1 className="min-w-0 break-words font-heading text-[28px] font-medium leading-[1.1] tracking-[-0.015em] text-[var(--ink)] sm:text-[36px]">{title}</h1>
        {description ? <p className="mt-1.5 max-w-[72ch] text-[13.5px] text-[var(--ink-3)]">{description}</p> : null}
      </div>
      {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
    </div>
  );
}

export function Section({ title, aside, children, className }: { title: ReactNode; aside?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cn("mt-8", className)}>
      <div className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-[16px] font-semibold leading-[1.3] text-[var(--ink)]">{title}</h2>
        {aside ? <div className="text-sm text-muted-foreground">{aside}</div> : null}
      </div>
      {children}
    </section>
  );
}

export function Stat({ value, label, tone, testid }: { value: ReactNode; label: ReactNode; tone?: "ok" | "warn" | "bad"; testid?: string }) {
  return (
    <Card className="px-4 py-3.5">
      <div
        data-testid={testid}
        className={cn(
          "font-mono text-[22px] font-medium tabular-nums leading-tight text-[var(--ink)]",
          tone === "ok" && "text-[var(--ok-ink)]",
          tone === "warn" && "text-[var(--warn-ink)]",
          tone === "bad" && "text-[var(--bad-ink)]",
        )}
      >
        {value}
      </div>
      <div className="mt-0.5 text-[12.5px] text-[var(--ink-3)]">{label}</div>
    </Card>
  );
}

export function StatGrid({ children }: { children: ReactNode }) {
  return <div className="grid grid-cols-2 gap-3 sm:grid-cols-[repeat(auto-fit,minmax(160px,1fr))]">{children}</div>;
}

export function Loading({ label = "Loading" }: { label?: string }) {
  return (
    <div className="flex items-center gap-2 py-10 text-[13px] text-[var(--ink-3)]" role="status">
      <Loader2 className="size-4 animate-spin text-[var(--accent)]" />
      {label}…
    </div>
  );
}

export function ErrorState({ error }: { error: unknown }) {
  const msg = error instanceof Error ? error.message : String(error);
  return (
    <div className="flex items-start gap-3 rounded-[5px] border border-l-[3px] border-[var(--line)] border-l-[var(--bad)] bg-[var(--surface)] px-4 py-2.5 text-[13px] text-[var(--ink-2)]">
      <AlertTriangle className="mt-0.5 size-4 shrink-0 text-[var(--bad)]" />
      <div className="min-w-0 [overflow-wrap:anywhere]">
        <div className="font-semibold text-[var(--ink)]">Could not load this</div>
        <div className="mt-0.5 text-[var(--bad-ink)]">{msg}</div>
      </div>
    </div>
  );
}

export function Empty({ children, icon }: { children: ReactNode; icon?: typeof Inbox }) {
  return <EmptyState icon={icon}>{children}</EmptyState>;
}

/** A library as an outlined tag: its name (lib/library-names) with its colour mark; the key is the tooltip and
 * data-vkey (the tooltip only for accounts that see files). label overrides the name (a caller that already holds the response's label). */
export function VersionTag({ k, label, className }: { k: string; label?: string; className?: string }) {
  const nameOf = useLibraryName();
  const tip = useLibraryKeyTip();
  const name = label || nameOf(k);
  return (
    <span
      data-vkey={k}
      title={name === k ? undefined : tip(k)}
      className={cn(
        "inline-flex h-5 items-center gap-1.5 whitespace-nowrap rounded-[3px] px-1.5 text-[11.5px] font-medium leading-none text-[var(--ink-2)] shadow-[inset_0_0_0_1px_var(--line-2)]",
        className,
      )}
    >
      <span className="size-2 rounded-[2px] bg-[var(--vc)]" />
      {name}
    </span>
  );
}

const JOB_TONE: Record<string, "positive" | "negative" | "info" | "warning" | "neutral"> = {
  done: "positive",
  failed: "negative",
  running: "info",
  queued: "neutral",
};
export function JobState({ state }: { state?: string }) {
  return <Badge tone={JOB_TONE[state ?? ""] ?? "neutral"}>{state ?? "—"}</Badge>;
}

export function KV({ items }: { items: [ReactNode, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-5 gap-y-2 text-sm">
      {items.map(([k, v], i) => (
        <div key={i} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0 break-words">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

export function Mono({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cn("font-mono text-[12.5px] tracking-[-0.01em]", className)}>{children}</span>;
}

/** Underlined sub-navigation tabs bound to routes (Scryer's settings tabs look). */
export function RouteTabs({ tabs, active }: { tabs: { id: string; label: string; to: string }[]; active: string }) {
  const navigate = useNavigate();
  return (<>
    <label className="mb-5 flex items-center gap-2 text-[13px] font-medium sm:hidden">Section
      <Select className="min-w-0 flex-1" value={active} onChange={e => { const tab = tabs.find(t => t.id === e.target.value); if (tab) void navigate(tab.to); }}>
        {tabs.map(t => <option key={t.id} value={t.id}>{t.label}</option>)}
      </Select>
    </label>
    <div className="mb-5 hidden gap-5 overflow-x-auto border-b border-[var(--line)] [scrollbar-width:none] sm:flex" role="tablist">
      {tabs.map((t) => (
        <Link
          key={t.id}
          to={t.to}
          role="tab"
          aria-selected={active === t.id}
          data-active={active === t.id}
          className="-mb-px flex h-[38px] items-center whitespace-nowrap border-b-2 border-transparent text-[13.5px] text-[var(--ink-3)] transition-colors hover:text-[var(--ink)] data-[active=true]:border-[var(--ink)] data-[active=true]:font-medium data-[active=true]:text-[var(--ink)]"
        >
          {t.label}
        </Link>
      ))}
    </div>
  </>);
}

export type StripItem = { label: ReactNode; value: ReactNode; tone?: "ok" | "warn" | "bad"; testid?: string };

/** Counts as one compact line (a summary strip), so they do not push the content down on a phone. */
export function Strip({ items, action, className }: { items: StripItem[]; action?: ReactNode; className?: string }) {
  return (
    <Card className={cn("flex flex-wrap items-center gap-x-5 gap-y-1.5 px-4 py-2.5", className)}>
      {items.map((it, i) => (
        <span key={i} className="flex items-baseline gap-1.5 text-[13px] text-muted-foreground">
          <span
            data-testid={it.testid}
            className={cn(
              "font-mono text-[15px] font-medium tabular-nums text-[var(--ink)]",
              it.tone === "ok" && "text-[var(--ok-ink)]",
              it.tone === "warn" && "text-[var(--warn-ink)]",
              it.tone === "bad" && "text-[var(--bad-ink)]",
            )}
          >
            {it.value}
          </span>
          {it.label}
        </span>
      ))}
      {action ? <span className="ml-auto text-sm">{action}</span> : null}
    </Card>
  );
}

/** A folded section: a summary line that opens to its content. */
export function Fold({ title, children, testid, aside }: { title: ReactNode; children: ReactNode; testid?: string; aside?: ReactNode }) {
  return (
    <details className="group mt-5 rounded-[8px] border border-[var(--line)] bg-[var(--surface)]" data-testid={testid}>
      <summary className="flex cursor-pointer list-none items-center gap-2 px-4 py-2.5 text-[13.5px] font-medium text-[var(--ink)] [&::-webkit-details-marker]:hidden">
        <ChevronRight className="size-4 transition-transform group-open:rotate-90" aria-hidden />
        <span className="flex-1">{title}</span>
        {aside}
      </summary>
      <div className="border-t border-border">{children}</div>
    </details>
  );
}
