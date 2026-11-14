// @vitest-environment jsdom
import { createElement } from "react";
import { screen, waitFor } from "@testing-library/react";
import { mockFetch, renderPage } from "@/test/page";
import { SystemBadge } from "./app-shell";
import { setupLinkShown, setupUnfinished, type SetupState } from "@/api/setup";
import { describe, expect, it, vi } from "vitest";
import { flatItems, itemFor, itemActive, moreFor, navFor, tabFor, TABS, type NavGroup } from "./nav";
import { permsOf } from "@/api/library";

// The permission bits of each role (internal/auth/perms.go, docs/design/users.md).
const ROLES = {
  user: permsOf(["browse", "request"]),
  manager: permsOf(["browse", "request", "auto_approve", "add", "manage_requests", "see_files", "manage", "own_keys"]),
  admin: permsOf(["browse", "request", "auto_approve", "add", "manage_requests", "see_files", "manage", "own_keys", "admin", "users"]),
  usersOnly: permsOf(["browse", "request", "users"]),
};

const paths = (groups: NavGroup[]) => flatItems(groups).map((i) => i.to);
const ADMIN_PAGES = ["/admin", "/admin", ...["tasks", "backup", "errors", "logs"].map(p => `/admin/system/${p}`), "/admin/reviews", "/admin/torrents", "/admin/settings",
 ...["libraries", "profiles", "formats", "quality-sync", "indexers", "downloads", "notifications", "plex", "metadata", "apps"].map(p => `/admin/settings/${p}`),
 "/admin/users", "/admin/advanced", ...["config", "history", "limits"].map(p => `/admin/advanced/${p}`)];
const MAIN = ["/", "/movies", "/series", "/anime", "/requests", "/activity", "/now-playing", "/calendar"];
// Managers and admins also have Collections in Library.
const MAIN_MANAGE = MAIN.flatMap(p => p === "/anime" ? [p, "/collections"] : [p]);
const MORE = ["/activity", "/now-playing", "/calendar"];

describe("navigation by role", () => {
  it("users and managers see the main entries and no Admin; managers also Wanted", () => {
    // Managers also have Wanted, after Activity.
    const withWanted = (l: string[]) => l.flatMap(p => p === "/activity" ? [p, "/wanted"] : [p]);
    for (const [p, main, more] of [[ROLES.user, MAIN, MORE], [ROLES.manager, withWanted(MAIN_MANAGE), withWanted(MORE)]] as const) {
      const g = navFor(p);
      expect(g.map((x) => x.id)).toEqual(["main"]);
      expect(paths(g)).toEqual(main);
      expect(paths(moreFor(p))).toEqual(more);
    }
  });
  it("admins see the main entries, then Admin with every page", () => {
    const g = navFor(ROLES.admin);
    expect(g.map((x) => x.id)).toEqual(["main", "admin"]);
    expect(g[0].items.map((i) => i.label)).toEqual(["Home", "Library", "Requests", "Activity", "Wanted", "Now playing", "Calendar"]);
    expect(g[1].items.map((i) => i.label)).toEqual(["System", "Reviews", "Torrents", "Settings", "Users & access", "Advanced"]);
    expect(paths(g)).toEqual([...MAIN_MANAGE.flatMap(p => p === "/activity" ? [p, "/wanted"] : [p]), ...ADMIN_PAGES]);
    expect(paths(moreFor(ROLES.admin)).sort()).toEqual(["/activity", "/wanted", "/now-playing", "/calendar", ...ADMIN_PAGES.slice(1)].sort());
  });
  it("an account with only the users bit sees Users & access alone", () => {
    expect(paths(navFor(ROLES.usersOnly))).toEqual([...MAIN, "/admin/users"]);
    expect(paths(moreFor(ROLES.usersOnly))).toEqual([...MORE, "/admin/users"]);
  });
  it("phone tabs are the same for everyone", () => {
    expect(TABS.map((t) => t.label)).toEqual(["Home", "Browse", "Search", "Requests", "More"]);
  });
});

describe("active entries", () => {
  it("maps paths to tabs", () => {
    expect(tabFor("/")).toBe("home");
    expect(tabFor("/anime")).toBe("browse");
    expect(tabFor("/series")).toBe("browse");
    expect(tabFor("/collections")).toBe("browse");
    expect(tabFor("/title/515")).toBe("browse");
    expect(tabFor("/title/515/releases")).toBe("browse");
    expect(tabFor("/discover/missing")).toBe("browse");
    expect(tabFor("/search")).toBe("search");
    expect(tabFor("/requests/all")).toBe("requests");
    expect(tabFor("/wanted/cutoff")).toBe("more");
    expect(tabFor("/activity/history")).toBe("more");
    expect(tabFor("/activity/journal/dl:3")).toBe("more");
    expect(tabFor("/wanted/manual-import")).toBe("more");
    expect(tabFor("/now-playing")).toBe("more");
    expect(tabFor("/admin/system/logs")).toBe("more");
    expect(tabFor("/title/4")).toBe("browse");
  });
  it("finds the deepest item; Status only on /admin itself", () => {
    expect(itemFor("/admin")?.label).toBe("Status");
    expect(itemFor("/admin/system/tasks")?.label).toBe("Tasks");
    expect(itemFor("/activity/journal/dl:3")?.label).toBe("Activity");
    expect(itemFor("/wanted/manual-import")?.label).toBe("Wanted");
    expect(itemFor("/admin/advanced")?.label).toBe("Advanced");
    expect(itemFor("/movies")?.label).toBe("Movies");
    expect(itemFor("/title/3")).toBeUndefined();
  });
});

it("shows Setup at the top only while unfinished in both menus", () => {
 for (const groups of [navFor, moreFor]) {
  expect(groups({ ...ROLES.admin, setupUnfinished: true }).find(g => g.id === "admin")?.items[0].label).toBe("Setup");
  expect(paths(groups({ ...ROLES.admin, setupUnfinished: false }))).not.toContain("/admin/setup");
 }
});
it("opens System children only on Status and System pages and keeps child badges", () => {
 const system = navFor(ROLES.admin)[1].items[0];
 expect(system.badge).toBe("system");
 expect(system.childrenWhenActive).toBe(true);
 expect(system.end).toBe(true);
 for (const path of ["/admin", "/admin/system/logs"]) expect(itemActive(path, system)).toBe(true);
 expect(itemActive("/admin/reviews", system)).toBe(false);
 expect(system.children?.find(c => c.label === "Tasks")?.badge).toBe("jobs");
 expect(system.children?.find(c => c.label === "Errors")?.badge).toBe("errors");
});

vi.mock("@/api/events", () => ({ useLive: () => ({ jobCounts: { attention: 3, failed: 10 } }) }));
it("excludes job-sourced errors from the sum of errors and attention jobs in the System badge", async () => {
 const fetch = mockFetch([{ method: "GET", path: "/errors", reply: { counts: { new: 4 }, total: 2 } }]);
 renderPage(createElement(SystemBadge));
 await waitFor(() => expect(screen.getByTestId("system-badge").textContent).toBe("5"));
 expect(fetch.calls("GET", "/errors")[0].search).toContain("source=job");
});
it("uses first_run or missing setup steps", () => {
 expect(setupUnfinished(undefined)).toBe(false);
 for (const [first_run, missing, expected] of [[false, [], false], [true, [], true], [false, ["provider"], true]] as const) {
  expect(setupUnfinished({ first_run, missing: [...missing] } as SetupState)).toBe(expected);
 }
});
it("keeps the Setup link until an install goes live or passes readiness, and never brings it back for a live one", () => {
 const nothingMissing = { first_run: false, missing: [] } as unknown as SetupState;
 // A fresh install at 4 of 6: libraries in Shadow, the preflight failed or never ran.
 expect(setupLinkShown(nothingMissing, false, false)).toBe(true);
 // Shadow by choice: a full preflight without failures ends the reminder; activation is not required.
 expect(setupLinkShown(nothingMissing, false, true)).toBe(false);
 // Set up long ago: a library is Active. Its readiness step is open after every restart, and that alone shows nothing.
 expect(setupLinkShown(nothingMissing, true, false)).toBe(false);
 // The libraries have not loaded (or failed to): nothing new shows.
 expect(setupLinkShown(nothingMissing, undefined, false)).toBe(false);
 expect(setupLinkShown(undefined, false, false)).toBe(false);
 // Something the install needs is missing: shown as before, live or not.
 expect(setupLinkShown({ first_run: false, missing: ["no debrid mount"] } as unknown as SetupState, true, true)).toBe(true);
});

it("phone sections keep all children visible as vertical lists and preserve badges", () => {
 const groups = moreFor(ROLES.admin);
 expect(groups.filter(g => g.id !== "more" && g.id !== "admin").map(g => g.label)).toEqual(["System", "Settings", "Users & access", "Advanced"]);
 expect(new Set(paths(groups)).size).toBe(paths(groups).length);
 expect(groups.find(g => g.label === "System")?.items.find(i => i.label === "Status")?.badge).toBe("system");
 expect(groups.flatMap(g => g.items).every(i => !i.children && !i.childrenWhenActive)).toBe(true);
 expect(groups.find(g => g.label === "System")?.items.find(i => i.label === "Tasks")?.badge).toBe("jobs");
 expect(groups.find(g => g.label === "System")?.items.find(i => i.label === "Errors")?.badge).toBe("errors");
 expect(groups.find(g => g.id === "admin")?.items.find(i => i.label === "Reviews")?.badge).toBe("reviews");
});
