import { ADVANCED_SETTINGS_PAGES, SETTINGS_PAGES } from "@/lib/settings-map";
// The navigation (docs/design/ui.md). Desktop: Home, Library (Movies, Series, Anime; Collections for managers), Requests,
// Activity, Wanted (managers), Now playing, Calendar, then Admin as its own group for accounts with admin pages (Users &
// access needs the users permission, the rest admin). System, Settings and Advanced reveal their children only while
// open. The Decision journal is a page of System; Manual import a tab of Wanted (admin only). Import lists are a
// Settings page; a manager, who has no Settings, finds them in Admin. Phone: bottom tabs Home, Browse (Movies, Series,
// Anime), Activity, Requests, More (search stays in the header); More lists Wanted, Now playing, Calendar and the admin
// pages the account may open, top level only: System, Settings and Advanced open their pages on a tap. The server
// enforces the permissions; this only hides what would fail.
import {
  Activity,
  Bell,
  Radar,
  BookOpen,
  Bug,
  Boxes,
  CalendarDays,
  ClipboardCheck,
  Database,
  FileText,
  Film,
  Gauge,
  GitCompareArrows,
  HardDrive,
  Home,
  Import,
  Inbox,
  KeyRound,
  ListVideo,
  Library,
  LayoutGrid,
  ListChecks,
  ListTodo,
  MonitorPlay,
  type LucideIcon,
  ScrollText,
  Search,
  Sparkles,
  SlidersHorizontal,
  Tv,
  Wrench,
} from "lucide-react";

export type NavNeed = "admin" | "users" | "manage";
export type NavBadge = "requests" | "jobs" | "errors" | "system" | "reviews";
export type NavItem = {
  /** The page; a parent without one only groups its children (Library). */
  to?: string;
  label: string;
  icon: LucideIcon;
  need?: NavNeed;
  badge?: NavBadge;
  /** Active on this exact path only (Home, Admin overview). */
  end?: boolean;
  activeFor?: string[];
  setupOnly?: boolean;
  children?: NavItem[];
  /** Show the children only while the parent’s pages are open (Settings and Advanced). */
  childrenWhenActive?: boolean;
  /** Not for admins: they reach the page elsewhere (a manager's Import lists; admins have them under Settings). */
  unlessAdmin?: boolean;
  /** A group of the phone's More sheet: its children show once tapped open. */
  startCollapsed?: boolean;
};
export type NavGroup = { id: string; label?: string; items: NavItem[] };

export const LIBRARY: NavItem[] = [
  { to: "/movies", label: "Movies", icon: Film },
  { to: "/series", label: "Series", icon: Tv },
  { to: "/anime", label: "Anime", icon: Sparkles },
  { to: "/collections", label: "Collections", icon: Library, need: "manage" },
];

const ACTIVITY: NavItem = { to: "/activity", label: "Activity", icon: Activity };
const CALENDAR: NavItem = { to: "/calendar", label: "Calendar", icon: CalendarDays };
const NOW_PLAYING: NavItem = { to: "/now-playing", label: "Now playing", icon: MonitorPlay };

const SETTINGS_ICONS: Record<string, LucideIcon> = { libraries: HardDrive, profiles: SlidersHorizontal, formats: ListChecks, "quality-sync": GitCompareArrows, indexers: Radar, downloads: Import, notifications: Bell, plex: MonitorPlay, metadata: Database, apps: Boxes };
export const SETTINGS: NavItem[] = SETTINGS_PAGES.filter(p => !p.system).map(p => ({ to: p.route, label: p.label, icon: SETTINGS_ICONS[p.id], need: "admin" }));

export const ADVANCED: NavItem[] = [
  ...["config", "history", "limits"].map(id => { const page = ADVANCED_SETTINGS_PAGES.find(p => p.id === id)!; return { to: page.route, label: page.label, icon: FileText, need: "admin" as const }; }),
];

const ADMIN: NavGroup = {
  id: "admin",
  label: "Admin",
  items: [
    { to: "/admin/setup", label: "Setup", icon: ClipboardCheck, need: "admin", setupOnly: true },
    { to: "/admin", label: "System", icon: Gauge, need: "admin", end: true, activeFor: ["/admin/system"], badge: "system", childrenWhenActive: true, children: [
      { to: "/admin", label: "Status", icon: Gauge, end: true },
      { to: "/admin/system/tasks", label: "Tasks", icon: ListTodo, badge: "jobs" },
      { to: "/admin/system/backup", label: "Backup", icon: Database },
      { to: "/admin/system/errors", label: "Errors", icon: Bug, badge: "errors" },
      { to: "/admin/system/logs", label: "Logs", icon: ScrollText },
      { to: "/admin/system/journal", label: "Decision journal", icon: BookOpen },
    ] },
    { to: "/admin/reviews", label: "Reviews", icon: ListChecks, need: "admin", badge: "reviews" },
    { to: "/admin/torrents", label: "Torrents", icon: HardDrive, need: "admin" },
    { to: "/admin/settings", label: "Settings", icon: SlidersHorizontal, need: "admin", children: SETTINGS, childrenWhenActive: true },
    { to: "/admin/users", label: "Users & access", icon: KeyRound, need: "users" },
    { to: "/admin/settings/import-lists", label: "Import lists", icon: ListVideo, need: "manage", unlessAdmin: true },
    { to: "/admin/advanced", label: "Advanced", icon: Wrench, need: "admin", children: ADVANCED, childrenWhenActive: true },
  ],
};

const WANTED: NavItem = { to: "/wanted", label: "Wanted", icon: Search, need: "manage" };

export const NAV: NavGroup[] = [
  {
    id: "main",
    items: [
      { to: "/", label: "Home", icon: Home, end: true },
      { label: "Library", icon: LayoutGrid, children: LIBRARY },
      { to: "/requests", label: "Requests", icon: Inbox, badge: "requests" },
      ACTIVITY,
      WANTED,
      NOW_PLAYING,
      CALENDAR,
    ],
  },
  ADMIN,
];

export type Can = { admin: boolean; users: boolean; manage?: boolean; setupUnfinished?: boolean };

export function allowed(items: NavItem[], can: Can): NavItem[] {
  const out: NavItem[] = [];
  for (const it of items) {
    if (it.need && !can[it.need]) continue;
    if (it.setupOnly && !can.setupUnfinished) continue;
    if (it.unlessAdmin && can.admin) continue;
    if (it.children) {
      const children = allowed(it.children, can);
      if (children.length === 0 && !it.to) continue;
      out.push({ ...it, children });
    } else {
      out.push(it);
    }
  }
  return out;
}

function groupsFor(groups: NavGroup[], can: Can): NavGroup[] {
  return groups.map((g) => ({ ...g, items: allowed(g.items, can) })).filter((g) => g.items.length > 0);
}

/** The sidebar groups and items an account sees. */
export function navFor(can: Can): NavGroup[] {
  return groupsFor(NAV, can);
}

/** The phone tabs; Browse and More open sheets. Search is the header's. */
export type Tab = { id: "home" | "browse" | "activity" | "requests" | "more"; label: string; icon: LucideIcon; to?: string; badge?: NavBadge };
export const TABS: Tab[] = [
  { id: "home", label: "Home", icon: Home, to: "/" },
  { id: "browse", label: "Browse", icon: BookOpen },
  { id: "activity", label: "Activity", icon: Activity, to: "/activity" },
  { id: "requests", label: "Requests", icon: Inbox, to: "/requests", badge: "requests" },
  { id: "more", label: "More", icon: SlidersHorizontal },
];

/** The label of a group's own page inside its More group (System's is its Status child). */
const OWN_PAGE: Record<string, string> = { Settings: "All settings", Advanced: "Advanced overview" };

/** What the phone's More sheet holds: Wanted, Now playing and Calendar, then the admin pages the account may open, top
 *  level only. System, Settings and Advanced are groups that open on a tap, their own page first. */
export function moreFor(can: Can): NavGroup[] {
  const admin = allowed(ADMIN.items, can).map((it): NavItem => {
    if (!it.to || !it.children?.length) return it;
    const own = it.children.some((c) => c.to === it.to) ? [] : [{ to: it.to, label: OWN_PAGE[it.label] ?? it.label, icon: it.icon, need: it.need }];
    return {
      label: it.label, icon: it.icon, badge: it.badge, startCollapsed: true,
      children: [...own, ...it.children].map((c) => (c.to === it.to && it.badge ? { ...c, badge: it.badge } : c)),
    };
  });
  return [{ id: "more", items: allowed([WANTED, NOW_PLAYING, CALENDAR], can) }, ...(admin.length ? [{ id: "admin", label: "Admin", items: admin }] : [])];
}

/** The tab a path belongs to on the phone (More for everything the tabs do not hold). */
export function tabFor(pathname: string): Tab["id"] {
  if (pathname === "/") return "home";
  // A title page, a Home "See all" list or the search page belongs to the library the user browses.
  if (LIBRARY.some((i) => isActive(pathname, i.to!)) || isActive(pathname, "/title") || isActive(pathname, "/discover") || isActive(pathname, "/search")) return "browse";
  if (isActive(pathname, "/activity")) return "activity";
  if (isActive(pathname, "/requests")) return "requests";
  if (isActive(pathname, "/wanted") || isActive(pathname, "/now-playing") || isActive(pathname, "/calendar") || isActive(pathname, "/admin")) return "more";
  return "home";
}

export function isActive(pathname: string, to: string, end = false): boolean {
  if (to === "/" || end) return pathname === to;
  return pathname === to || pathname.startsWith(to + "/");
}

/** Whether an item or one of its children is the current page. */
export function itemActive(pathname: string, it: NavItem): boolean {
  if (it.activeFor?.some(prefix => isActive(pathname, prefix))) return true;
  if (it.to && isActive(pathname, it.to, it.end)) return true;
  return (it.children ?? []).some((c) => itemActive(pathname, c));
}

/** Every page item, children included (page headers take their icon from the deepest match). */
export function flatItems(groups: NavGroup[] = NAV): NavItem[] {
  const out: NavItem[] = [];
  const walk = (items: NavItem[]) => {
    for (const it of items) {
      if (it.to) out.push(it);
      if (it.children) walk(it.children);
    }
  };
  for (const g of groups) walk(g.items);
  return out;
}

/** The nav item for a path: the longest page path that is active. */
export function itemFor(pathname: string): NavItem | undefined {
  return flatItems().reverse()
    .filter((i) => isActive(pathname, i.to!, i.end))
    .sort((a, b) => b.to!.length - a.to!.length)[0];
}
