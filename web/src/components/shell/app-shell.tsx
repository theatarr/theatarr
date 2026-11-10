// The app shell: sidebar (Home, Library, Requests, Activity, Calendar; then Admin), top bar (search, the system
// signals, account), bottom tabs at phone width (Home, Browse, Search, Requests, More) with Browse and More as sheets
// (components/shell/nav.ts holds what each account sees). The look: a
// sidebar on the paper with a hairline, the current page a sheet with a 3 px accent rule, counts as plain mono
// figures (amber when they ask for you), the current section's pages under it as in Sonarr v5's PageSidebar; the
// top bar names what is off for everyone ("New downloads off", "VPN down") once, so rows need not repeat it. The
// phone tab bar follows Seerr's MobileMenu (MIT), a bottom bar with a More list. The sheet is our own
// Modal (components/ui/modal.tsx): Radix Dialog's scroll lock injects a <style> element, which the CSP refuses.
import { useMinWidth } from "@/components/library/copy-bits";
import { ChevronDown, Search, X } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link, NavLink, useLocation } from "react-router";
import { PRODUCT_NAME } from "@/brand";
import { cn } from "@/lib/utils";
import { AccountMenu, ThemeSwitch } from "./account-menu";
import { allowed, isActive, itemActive, LIBRARY, moreFor, navFor, tabFor, TABS, type NavBadge, type NavGroup, type NavItem } from "./nav";
import { useReviewCounts } from "@/api/reviews";
import { useSetupProgress } from "@/api/useSetupProgress";
import { useVersions } from "@/api/settings-versions";
import { settingHref } from "@/lib/settings-map";
import { useLive } from "@/api/events";
import { attentionCount } from "@/lib/jobs";
import type { Perms } from "@/api/library";
import { useRequestSummary } from "@/api/requests";
import { useNewErrorCount, useNewNonJobErrorCount } from "@/api/errors";
import { StatusDot } from "./status-dot";
import { useSearchUI } from "@/components/search/search-context";
import { Modal } from "@/components/ui/modal";
import { topModal } from "@/lib/modal-stack";

const NAV_BUTTON =
  "relative flex h-8 w-full items-center gap-2.5 rounded-[5px] px-2 text-[13.5px] text-[var(--ink-2)] transition-colors hover:bg-[var(--hover)] hover:text-[var(--ink)] [&>svg]:text-[var(--ink-3)] data-[active=true]:bg-[var(--surface)] data-[active=true]:font-medium data-[active=true]:text-[var(--ink)] data-[active=true]:shadow-[inset_0_0_0_1px_var(--line)] data-[active=true]:before:absolute data-[active=true]:before:inset-y-[7px] data-[active=true]:before:-left-3 data-[active=true]:before:w-[3px] data-[active=true]:before:rounded-r-[2px] data-[active=true]:before:bg-[var(--accent)] data-[active=true]:before:content-[''] data-[active=true]:[&>svg]:text-[var(--accent-ink)]";

export function Logo({ className }: { className?: string }) {
  return (
    <div className={cn("flex items-center gap-2.5", className)}>
      <img src="/icon.svg" alt="" className="size-[26px] shrink-0" />
      <span className="font-heading text-[20px] font-semibold leading-none tracking-[-0.01em] text-[var(--ink)]">{PRODUCT_NAME}</span>
    </div>
  );
}

const COLLAPSED_KEY = "theatarr.nav.collapsed";

function readCollapsed(): string[] {
  try {
    return JSON.parse(window.localStorage.getItem(COLLAPSED_KEY) ?? "[]") as string[];
  } catch {
    return [];
  }
}

/** A nav count: a plain mono figure, amber because each one asks for you. */
const COUNT = "ml-auto font-mono text-[12px] font-medium leading-none tabular-nums text-[var(--warn-ink)]";

/** The Requests badge: pending requests for managers, your open ones for everyone else. */
function RequestsBadge() {
  const s = useRequestSummary();
  const n = s.data?.badge ?? 0;
  if (n <= 0) return null;
  return (
    <span data-testid="requests-badge" className={COUNT}>
      {n > 99 ? "99+" : n}
    </span>
  );
}

/** The Jobs badge (admins): failed jobs that still need attention, from the live stream. */
function JobsBadge() {
  const live = useLive();
  const n = attentionCount(live.jobCounts);
  if (n <= 0) return null;
  return (
    <span data-testid="jobs-badge" className={COUNT}>
      {n > 99 ? "99+" : n}
    </span>
  );
}

/** The Errors badge (admins): error inbox rows not looked at yet ("new"). */
function ErrorsBadge() {
  const n = useNewErrorCount().data ?? 0;
  if (n <= 0) return null;
  return (
    <span data-testid="errors-badge" className={COUNT}>
      {n > 99 ? "99+" : n}
    </span>
  );
}

export function SystemBadge() {
  const live = useLive();
  const n = attentionCount(live.jobCounts) + (useNewNonJobErrorCount().data ?? 0);
  return n > 0 ? <span data-testid="system-badge" className={COUNT}>{n > 99 ? "99+" : n}</span> : null;
}

export function ReviewsBadge() {
  const counts = useReviewCounts();
  return counts.total > 0 || counts.decisionsMore ? <span data-testid="reviews-badge" className={COUNT}>{counts.total > 99 ? "99+" : counts.decisionsMore ? counts.total ? `${counts.total}+` : "?" : counts.total}</span> : null;
}

function NavBadgeFor({ badge }: { badge?: NavBadge }) {
  if (badge === "reviews") return <ReviewsBadge />;
  if (badge === "system") return <SystemBadge />;
  if (badge === "requests") return <RequestsBadge />;
  if (badge === "jobs") return <JobsBadge />;
  if (badge === "errors") return <ErrorsBadge />;
  return null;
}

function useCollapsed() {
  const [collapsed, setCollapsed] = useState<string[]>(readCollapsed);
  const toggle = (id: string) => {
    const next = collapsed.includes(id) ? collapsed.filter((c) => c !== id) : [...collapsed, id];
    setCollapsed(next);
    try {
      window.localStorage.setItem(COLLAPSED_KEY, JSON.stringify(next));
    } catch {
      // not kept
    }
  };
  return [collapsed, toggle] as const;
}

function NavEntry({ it, pathname, onNavigate, collapsed, toggle, depth = 0, sub = false }: {
  it: NavItem;
  pathname: string;
  onNavigate?: () => void;
  collapsed: string[];
  toggle: (id: string) => void;
  depth?: number;
  /** A page's own pages (System, Settings, Advanced): indented under the parent's label, without an icon. */
  sub?: boolean;
}) {
  const kids = it.children ?? [];
  const key = `item:${it.label}`;
  const open = it.childrenWhenActive ? itemActive(pathname, it) : !collapsed.includes(key);
  const inner = (
    <>
      {sub ? null : <it.icon className="size-4 shrink-0" />}
      <span className="min-w-0 flex-1 truncate">{it.label}</span>
      <NavBadgeFor badge={it.badge} />
    </>
  );
  return (
    <li>
      {it.to ? (
        <NavLink
          to={it.to}
          end={it.end}
          onClick={onNavigate}
          data-active={isActive(pathname, it.to, it.end || kids.length > 0)}
          className={cn(NAV_BUTTON, sub ? "h-[30px] pl-[34px] text-[13px]" : depth > 0 && "pl-6")}
        >
          {inner}
          {kids.length > 0 ? <ChevronDown className={cn("size-3.5 transition-transform", !open && "-rotate-90")} aria-hidden /> : null}
        </NavLink>
      ) : (
        <button type="button" onClick={() => toggle(key)} aria-expanded={open} className={cn(NAV_BUTTON, "text-left")} data-nav-parent={it.label}>
          {inner}
          <ChevronDown className={cn("size-3.5 transition-transform", !open && "-rotate-90")} aria-hidden />
        </button>
      )}
      {kids.length > 0 && open ? (
        <ul className="mt-px space-y-px">
          {kids.map((c) => (
            <NavEntry key={c.to ?? c.label} it={c} pathname={pathname} onNavigate={onNavigate} collapsed={collapsed} toggle={toggle} depth={depth + 1} sub={!!it.to} />
          ))}
        </ul>
      ) : null}
    </li>
  );
}

function NavGroups({ groups, onNavigate }: { groups: NavGroup[]; onNavigate?: () => void }) {
  const { pathname } = useLocation();
  const [collapsed, toggle] = useCollapsed();
  return (
    <nav aria-label="Main" className="flex flex-col gap-1">
      {groups.map((g) => (
        <div key={g.id} data-nav-group={g.id}>
          {g.label ? (
            <button
              type="button"
              onClick={() => toggle(g.id)}
              aria-expanded={!collapsed.includes(g.id)}
              className="mt-3 flex w-full items-center justify-between rounded-[3px] px-2 py-1 text-left text-[11px] font-semibold uppercase tracking-[0.09em] text-[var(--ink-3)] hover:text-[var(--ink)]"
            >
              {g.label}
              <ChevronDown className={cn("size-3.5 transition-transform", collapsed.includes(g.id) && "-rotate-90")} />
            </button>
          ) : null}
          <ul className={cn("space-y-px", g.label && collapsed.includes(g.id) && "hidden")}>
            {g.items.map((it) => (
              <NavEntry key={it.to ?? it.label} it={it} pathname={pathname} onNavigate={onNavigate} collapsed={collapsed} toggle={toggle} />
            ))}
          </ul>
        </div>
      ))}
    </nav>
  );
}

function SidebarBody({ onNavigate, groups }: { onNavigate?: () => void; groups: NavGroup[] }) {
  return (
    <div data-slot="sidebar-inner" className="flex h-full flex-col bg-[var(--bg)]">
      <div className="px-5 pb-5 pt-[18px]">
        <Link to="/" onClick={onNavigate} aria-label={`${PRODUCT_NAME} home`} className="inline-block rounded-[3px]">
          <Logo />
        </Link>
      </div>
      <div data-slot="sidebar-content" className="min-h-0 flex-1 overflow-y-auto px-3 pb-3">
        <NavGroups groups={groups} onNavigate={onNavigate} />
      </div>
      <div className="border-t border-dashed border-[var(--line)] px-3 py-3">
        <ThemeSwitch />
      </div>
    </div>
  );
}

/** The search box: opens the search overlay (components/search). */
/** The top bar's search control: a bordered control, so its border is the 3:1 control colour (lib/contrast.test.ts). */
export const SEARCH_TRIGGER =
  "flex h-8 w-full min-w-[38px] max-w-[440px] items-center gap-2 rounded-[5px] border border-[var(--control)] bg-[var(--surface)] px-2.5 text-left text-[13px] text-[var(--ink-3)] transition-colors hover:border-[var(--ink-3)] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--line-focus)]";

function SearchTrigger() {
  const { openOverlay } = useSearchUI();
  const ref = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) return;
      if (topModal()) return;
      if (e.key === "/" || ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k")) {
        e.preventDefault();
        openOverlay();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [openOverlay]);
  return (
    <button
      ref={ref}
      type="button"
      data-testid="search-trigger"
      onClick={() => openOverlay()}
      aria-keyshortcuts="/ Control+K Meta+K"
      aria-label="Search movies, series and anime"
      className={SEARCH_TRIGGER}
    >
      <Search className="size-4 shrink-0" aria-hidden />
      <span className="min-w-0 flex-1 truncate">Search movies, series and anime</span>
      <kbd className="hidden rounded-[3px] border border-b-2 border-[var(--line-2)] bg-[var(--bg)] px-[5px] py-0.5 text-[11px] font-medium leading-none text-[var(--ink-3)] md:inline">
        /
      </kbd>
    </button>
  );
}

/** The top bar's "New downloads off" (admins): the global switch (Settings › Download clients), said once here so
 * rows need not repeat it. Nothing shows while new downloads are on. */
export function DownloadsSignal() {
  const sw = useVersions().data?.switches;
  if (!sw) return null;
  const forced = sw.sources?.router === "forced off by environment";
  if (sw.router_enabled && !forced) return null;
  const label = forced ? "New downloads locked off" : "New downloads off";
  const detail = forced
    ? "New downloads are forced off by the environment (THEATARR_FORCE_OFF) for every library."
    : "New downloads are off for every library. Settings › Download clients.";
  return (
    <Link
      to={settingHref("switches", "router_enabled") ?? "/admin/settings/downloads"}
      data-testid="downloads-signal"
      data-forced={forced || undefined}
      title={detail}
      aria-label={`${label}. ${detail}`}
      className="flex h-8 shrink-0 items-center gap-1.5 rounded-[5px] px-2 text-[12.5px] text-[var(--ink-2)] transition-colors hover:bg-[var(--hover)] hover:text-[var(--ink)]"
    >
      <span aria-hidden className={cn("size-2 shrink-0 rounded-full border-[1.5px]", forced ? "border-[var(--bad)]" : "border-[var(--ink-3)]")} />
      <span className="whitespace-nowrap">{label}</span>
    </Link>
  );
}

function BottomTabs({ sheet, onSheet }: { sheet: "browse" | "more" | null; onSheet: (s: "browse" | "more") => void }) {
  const { pathname } = useLocation();
  const current = sheet ?? tabFor(pathname);
  const tab =
    "relative flex flex-1 flex-col items-center gap-1 py-1.5 text-[11px] font-medium text-[var(--ink-3)] data-[active=true]:text-[var(--accent-ink)] data-[active=true]:before:absolute data-[active=true]:before:inset-x-[28%] data-[active=true]:before:-top-1 data-[active=true]:before:h-[2px] data-[active=true]:before:bg-[var(--accent)] data-[active=true]:before:content-['']";
  return (
    <nav
      aria-label="Tabs"
      data-testid="bottom-tabs"
      className="fixed inset-x-0 bottom-0 z-40 flex border-t border-[var(--line)] bg-[var(--surface)] px-1 pb-[calc(0.25rem+env(safe-area-inset-bottom))] pt-1 md:hidden"
    >
      {TABS.map((t) => {
        const body = (
          <>
            <t.icon className="size-5" />
            {t.label}
            {t.badge ? <span className="absolute right-[calc(50%-1.5rem)] top-0 [&>span]:rounded-[3px] [&>span]:bg-[var(--warn-soft)] [&>span]:px-1 [&>span]:py-0.5"><NavBadgeFor badge={t.badge} /></span> : null}
          </>
        );
        return t.to ? (
          <Link key={t.id} to={t.to} data-active={current === t.id} data-tab={t.id} className={tab}>
            {body}
          </Link>
        ) : (
          <button
            key={t.id}
            type="button"
            onClick={() => onSheet(t.id as "browse" | "more")}
            data-active={current === t.id}
            data-tab={t.id}
            aria-haspopup="dialog"
            aria-expanded={sheet === t.id}
            data-testid={`${t.id}-tab`}
            className={tab}
          >
            {body}
          </button>
        );
      })}
    </nav>
  );
}

/** The phone sheets: Browse (Movies, Series, Anime) rises from the tab bar; More slides in from the side. */
function MobileSheet({ sheet, onClose, perms }: { sheet: "browse" | "more" | null; onClose: () => void; perms: Perms }) {
  const setup = useSetupProgress(perms.admin);
  if (!sheet) return null;
  const browse = sheet === "browse";
  return (
    <Modal
      label={browse ? "Browse" : "More"}
      onClose={onClose}
      className="z-50 md:hidden"
      closeFrom={768}
      backdropClassName="bg-[var(--scrim)] animate-in fade-in-0"
      panelSlot="sheet-content"
      panelClassName={
        browse
          ? "absolute inset-x-0 bottom-0 rounded-t-[12px] border-t border-[var(--line)] bg-[var(--surface)] px-3 pb-[calc(1.5rem+env(safe-area-inset-bottom))] pt-4 shadow-[var(--shadow-2)] animate-in slide-in-from-bottom"
          : "absolute inset-y-0 left-0 flex w-[min(300px,86vw)] flex-col border-r border-[var(--line)] bg-[var(--bg)] shadow-[var(--shadow-2)] animate-in slide-in-from-left"
      }
    >
      <button
        type="button"
        onClick={onClose}
        aria-label="Close"
        className="absolute right-3 top-4 z-10 grid size-8 place-items-center rounded-[5px] text-[var(--ink-3)] hover:bg-[var(--hover)] hover:text-[var(--ink)]"
      >
        <X className="size-4" />
      </button>
      {browse ? (
        <div data-testid="browse-sheet">
          <p className="px-2 pb-2 text-[11px] font-semibold uppercase tracking-[0.09em] text-[var(--ink-3)]">Browse</p>
          <NavGroups groups={[{ id: "browse", items: allowed(LIBRARY, perms) }]} onNavigate={onClose} />
        </div>
      ) : (
        <SidebarBody onNavigate={onClose} groups={moreFor({ ...perms, setupUnfinished: setup.unfinished })} />
      )}
    </Modal>
  );
}

export function AppShell({ children, perms }: { children: ReactNode; perms: Perms }) {
  const desktop = useMinWidth(768);
  const setup = useSetupProgress(perms.admin);
  const [sheet, setSheet] = useState<"browse" | "more" | null>(null);
  const { pathname } = useLocation();
  useEffect(() => setSheet(null), [pathname]);
  return (
    <div data-slot="root-app-frame" className="flex min-h-dvh">
      {desktop ? <aside
        data-slot="sidebar"
        className="sticky top-0 hidden h-dvh w-[var(--side)] shrink-0 border-r border-[var(--line)] md:block"
      >
        <SidebarBody groups={navFor({ ...perms, setupUnfinished: setup.unfinished })} />
      </aside> : null}
      <div className="flex min-w-0 flex-1 flex-col">
        <header
          data-slot="root-header"
          className="sticky top-0 z-30 border-b border-[var(--line)] bg-[color-mix(in_srgb,var(--bg)_88%,transparent)] backdrop-blur-[8px]"
        >
          <div className="flex h-[var(--top)] w-full items-center gap-3 px-4 sm:px-6 lg:px-8">
            <div className="min-w-0 flex-1">
              <SearchTrigger />
            </div>
            <div className="flex shrink-0 items-center justify-end gap-1 sm:gap-2">
              {perms.admin ? <DownloadsSignal /> : null}
              {perms.admin ? <StatusDot /> : null}
              <AccountMenu />
            </div>
          </div>
        </header>
        <main data-slot="root-main-scroll" className="min-w-0 flex-1 px-4 pb-28 pt-6 sm:px-6 md:pb-12 lg:px-8">
          {children}
        </main>
      </div>
      {!desktop ? <BottomTabs sheet={sheet} onSheet={(s) => setSheet((cur) => (cur === s ? null : s))} /> : null}
      <MobileSheet sheet={sheet} onClose={() => setSheet(null)} perms={perms} />
    </div>
  );
}
