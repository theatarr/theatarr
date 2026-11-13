import { SETTINGS_PAGES } from "./lib/settings-map";
import { lazy, Suspense, type ComponentType, type ReactNode } from "react";
import { createBrowserRouter, Navigate, Outlet, useLocation, useSearchParams, type RouteObject } from "react-router";
import { LEGACY_ROUTES, legacyTarget } from "./lib/legacy-routes";
import { useAuth } from "./api/auth";
import { ApiError } from "./api/client";
import { LiveProvider } from "./api/events";
import { SearchProvider } from "./components/search/search-context";
import { AppShell, Logo } from "./components/shell/app-shell";
import { Forbidden, NotFound } from "./pages/library";
import { Home } from "./pages/home";
import { Catalog } from "./pages/catalog";
import { TitlePage } from "./pages/title";
import { Login, PlexPopup } from "./pages/login";
import { permsOf } from "./api/library";
import { SearchPage } from "./pages/search";
import { DiscoverPage } from "./pages/discover";
import { Loading } from "./components/page";
import { useSetup } from "./api/setup";


// The admin screens load on first use, so the library screens stay small.
function lazyPage(load: () => Promise<ComponentType>) {
  const C = lazy(async () => ({ default: await load() }));
  return function LazyPage() {
    return (
      <Suspense fallback={<Loading />}>
        <C />
      </Suspense>
    );
  };
}
const SettingsPage = lazyPage(() => import("./pages/admin/settings/pages").then(m => m.SettingsPage));
const AdvancedSettingsPage = lazyPage(() => import("./pages/admin/settings/pages").then(m => m.AdvancedSettingsPage));
const SettingsHome = lazyPage(() => import("./pages/admin/settings/home").then(m => m.SettingsHome));
const SetupPage = lazyPage(() => import("./pages/admin/setup").then((m) => m.SetupPage));
const ArrImportPage = lazyPage(() => import("./pages/admin/arr-import").then((m) => m.ArrImportPage));
const Overview = lazyPage(() => import("./pages/admin/overview").then((m) => m.Overview));
const JobsPage = lazyPage(() => import("./pages/admin/jobs").then((m) => m.JobsPage));
const ErrorsPage = lazyPage(() => import("./pages/admin/errors").then((m) => m.ErrorsPage));
const TorrentsPage = lazyPage(() => import("./pages/admin/torrents").then((m) => m.TorrentsPage));
const AdvancedPage = lazyPage(() => import("./pages/admin/advanced").then((m) => m.AdvancedPage));
const BackupPage = lazyPage(() => import("./pages/admin/backup").then((m) => m.BackupPage));
const JournalEntryPage = lazyPage(() => import("./pages/admin/journal").then((m) => m.JournalEntryPage));
const ReviewsPage = lazyPage(() => import("./pages/admin/reviews").then((m) => m.ReviewsPage));
const MetadataFranchise = lazyPage(() => import("./pages/admin/anime-mappings").then((m) => m.MetadataFranchise));
const Registry = lazyPage(() => import("./pages/admin/registry").then((m) => m.Registry));
const ManualImport = lazyPage(() => import("./pages/admin/manual-import").then((m) => m.ManualImportPage));
const RegistryContent = lazyPage(() => import("./pages/admin/registry").then((m) => m.RegistryContent));
const UsersPage = lazyPage(() => import("./pages/admin/users").then((m) => m.UsersAdmin));
const RequestsPage = lazyPage(() => import("./pages/requests").then((m) => m.RequestsPage));
const ProfilePage = lazyPage(() => import("./pages/profile").then((m) => m.Profile));
const Logs = lazyPage(() => import("./pages/admin/logs").then((m) => m.Logs));
const TitleReleases = lazyPage(() => import("./pages/title-releases").then((m) => m.TitleReleasesPage));
const WantedPage = lazyPage(() => import("./pages/wanted").then((m) => m.WantedPage));
const ActivityPage = lazyPage(() => import("./pages/activity").then((m) => m.ActivityPageView));
const NowPlayingPage = lazyPage(() => import("./pages/now-playing").then((m) => m.NowPlayingPage));
const CollectionsPage = lazyPage(() => import("./pages/collections").then((m) => m.CollectionsPage));
const ListsPage = lazyPage(() => import("./pages/lists").then((m) => m.ListsPage));
const CalendarPage = lazyPage(() => import("./pages/calendar").then((m) => m.CalendarPageView));

function Splash() {
  return (
    <div className="grid min-h-dvh place-items-center">
      <Logo className="animate-pulse" />
    </div>
  );
}

function RequireAuth() {
  const me = useAuth();
  const loc = useLocation();
  if (me.isPending) return <Splash />;
  if (me.error) {
    if (me.error instanceof ApiError && me.error.status === 401) {
      return <Navigate to={`/login?next=${encodeURIComponent(loc.pathname + loc.search)}`} replace />;
    }
    return (
      <div className="grid min-h-dvh place-items-center p-6 text-sm text-muted-foreground">
        Could not reach the server: {String(me.error.message)}
      </div>
    );
  }
  if (me.data?.kind !== "session") return <Navigate to="/login" replace />;
  const perms = permsOf(me.data?.permissions);
  return (
    <LiveProvider enabled seeFiles={perms.seeFiles}>
      <SearchProvider>
        <AppShell perms={perms}>
          <Outlet />
        </AppShell>
      </SearchProvider>
    </LiveProvider>
  );
}

/** The admin screens: the API refuses them to other accounts; the UI says so instead of showing errors. */
function RequireAdmin({ need = "admin" }: { need?: "admin" | "users" | "manage" }) {
  const me = useAuth();
  const p = permsOf(me.data?.permissions);
  if (!p[need]) return <Forbidden />;
  return <Outlet />;
}

/**
 * An admin page under Activity or Wanted: in "View as a user" (?as=user) it is not opened, as Activity's own
 * admin tabs are not; the admin goes to the parent page, still in that view.
 */
function NotAsUser({ to, children }: { to: string; children: ReactNode }) {
  const [sp] = useSearchParams();
  if (sp.get("as") === "user") return <Navigate to={`${to}?as=user`} replace />;
  return children;
}

/**
 * /admin: Status for admins; an account that only manages users lands on Users & access. A fresh install (no
 * version yet, settings in the database) leads to Setup once per browser session.
 */
function AdminHome() {
  const me = useAuth();
  const p = permsOf(me.data?.permissions);
  const setup = useSetup(p.admin && !setupShown());
  if (p.admin && setup.isLoading) return <Loading />;
  if (p.admin && setup.data?.first_run && setup.data.settings_source === "db" && !setupShown()) {
    sessionStorage.setItem(SETUP_SHOWN, "1");
    return <Navigate to="/admin/setup" replace />;
  }
  if (p.admin) return <Overview />;
  if (p.users) return <Navigate to="/admin/users" replace />;
  return <Forbidden />;
}

const SETUP_SHOWN = "theatarr.setup-shown";
const setupShown = () => sessionStorage.getItem(SETUP_SHOWN) === "1";

/** An old admin URL: on to where it lives now, keeping the query and hash (lib/legacy-routes.ts). */
function LegacyRedirect() {
  const loc = useLocation();
  return <Navigate to={legacyTarget(loc.pathname, loc.search, loc.hash) ?? "/admin"} replace />;
}

/** The route table (exported for router.test.tsx). */
export const routes: RouteObject[] = [
  { path: "/login", element: <Login /> },
  { path: "/login/plex/loading", element: <PlexPopup /> },
  { path: "/login/plex/done", element: <PlexPopup done /> },
  {
    element: <RequireAuth />,
    children: [
      { path: "/", element: <Home /> },
      { path: "/movies", element: <Catalog key="movies" id="movies" /> },
      { path: "/series", element: <Catalog key="series" id="series" /> },
      { path: "/anime", element: <Catalog key="anime" id="anime" /> },
      { path: "/title/:id", element: <TitlePage /> },
      { path: "/search", element: <SearchPage /> },
      { path: "/discover/:row", element: <DiscoverPage /> },
      { path: "/title/:id/releases", element: <TitleReleases /> },
      { path: "/calendar", element: <CalendarPage /> },
      { path: "/collections", element: <CollectionsPage /> },
      { path: "/collections/lists", element: <ListsPage /> },
      { path: "/activity", element: <ActivityPage /> },
      { path: "/activity/:tab", element: <ActivityPage /> },
      { path: "/now-playing", element: <NowPlayingPage /> },
      { path: "/requests", element: <RequestsPage /> },
      { path: "/requests/:tab", element: <RequestsPage /> },
      { path: "/me", element: <ProfilePage /> },
      ...LEGACY_ROUTES.map(([path]) => ({ path, element: <LegacyRedirect /> })),
      { path: "/admin", element: <AdminHome /> },
      {
        element: <RequireAdmin need="users" />,
        children: [{ path: "/admin/users", element: <UsersPage /> }],
      },
      {
        element: <RequireAdmin need="manage" />,
        children: [
          { path: "/wanted", element: <WantedPage /> },
          { path: "/wanted/cutoff", element: <WantedPage /> },
        ],
      },
      {
        element: <RequireAdmin />,
        children: [
          { path: "/wanted/manual-import", element: <NotAsUser to="/wanted"><ManualImport /></NotAsUser> },
          { path: "/activity/journal/:id", element: <NotAsUser to="/activity"><JournalEntryPage /></NotAsUser> },
          { path: "/admin/setup", element: <SetupPage /> },
          { path: "/admin/arr-import", element: <ArrImportPage /> },
          { path: "/admin/system/tasks", element: <JobsPage /> },
          { path: "/admin/system/errors", element: <ErrorsPage /> },
          { path: "/admin/settings", element: <SettingsHome /> },
          ...SETTINGS_PAGES.filter(p => !p.system).flatMap(p => [...new Set([p.route, ...p.tabs.map(t => t.route)])].map(path => ({ path, element: <SettingsPage key={path} /> }))),
          { path: "/admin/settings/profiles/format_sets", element: <SettingsPage /> },
          ...["config", "config/sections", "config/import", "history", "limits"].map(path => ({ path: `/admin/advanced/${path}`, element: <AdvancedSettingsPage /> })),
          { path: "/admin/torrents", element: <TorrentsPage /> },
          ...["", "/quality", "/anime", "/indexers", "/decisions"].map(tab => ({ path: `/admin/reviews${tab}`, element: <ReviewsPage /> })),
          { path: "/admin/reviews/anime/:slug", element: <MetadataFranchise /> },
          { path: "/admin/advanced", element: <AdvancedPage /> },
          { path: "/admin/torrents/inventory", element: <Registry /> },
          { path: "/admin/torrents/inventory/:hash", element: <RegistryContent /> },
          { path: "/admin/system/logs", element: <Logs /> },
          { path: "/admin/system/backup", element: <BackupPage /> },
        ],
      },
      { path: "*", element: <NotFound /> },
    ],
  },
];

export const router = createBrowserRouter(routes);
