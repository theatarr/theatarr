// @vitest-environment jsdom
// The real route table for the admin pages that live under Activity and Wanted: who may open them, and "View as a user".
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { lazyPage, routes } from "./router";
import { browser, RELOAD_GUARD, RouteError } from "./components/route-error";
import { mockFetch } from "@/test/page";

vi.mock("@/api/events", () => ({ LiveProvider: ({ children }: { children: ReactNode }) => children, useLive: () => ({}) }));
vi.mock("./components/shell/app-shell", () => ({ AppShell: ({ children }: { children: ReactNode }) => children, Logo: () => null }));

const ADMIN = ["browse", "request", "see_files", "manage", "admin", "users"];
const MANAGER = ["browse", "request", "see_files", "manage"];
const USER = ["browse", "request"];

function open(path: string, permissions: string[]) {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { kind: "session", id: 1, username: "someone", permissions, csrf_token: "t" } },
    { method: "GET", path: "/activity", reply: { state: "now", admin: true, total: 0, now: [] } },
    { method: "GET", path: "/wanted", reply: { items: [], titles: {}, libraries: {}, total: 0, counts: {}, limit: 50, offset: 0, enabled: true, downloads_enabled: true, interval: "15m0s", per_hour: 20, again_after: "24h0m0s", missing: true, cutoff_unmet: "quality", searched_last_hour: 0 } },
    { method: "GET", path: "/status", reply: { versions: [] } },
    { method: "GET", path: "/policy", reply: { versions: [] } },
    { method: "GET", path: "/journal/dl%3A4", reply: { id: "dl:4", kind: "grab", summary: "Grabbed" } },
    { method: "GET", path: "/journal", reply: { items: [] } },
    { method: "GET", path: "/lists", reply: { lists: [], libraries: {}, can_sync: true } },
  ]);
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>);
  const at = () => router.state.location.pathname + router.state.location.search + router.state.location.hash;
  return { fetch, at };
}

it("opens a journal entry under System for an admin, and sends its old Activity address there", async () => {
  const entry = open("/activity/journal/dl%3A4?x=1", ADMIN);
  expect(await screen.findByTestId("journal-summary")).toBeTruthy();
  expect(entry.at()).toBe("/admin/system/journal/dl%3A4?x=1");
  expect(entry.fetch.calls("GET", "/journal/dl%3A4")).toHaveLength(1);
});

it("opens the journal list under System and sends /activity/journal there with its filter", async () => {
  const page = open("/activity/journal?suspect=1", ADMIN);
  await waitFor(() => expect(page.at()).toBe("/admin/system/journal?suspect=1"));
  expect(await screen.findByRole("heading", { name: "Decision journal" })).toBeTruthy();
  await waitFor(() => expect(page.fetch.calls("GET", "/journal")[0]?.search).toContain("suspect=true"));
});

it("opens Manual import as a tab of Wanted for an admin", async () => {
  const page = open("/wanted/manual-import", ADMIN);
  expect((await screen.findByRole("tab", { name: "Manual import" })).getAttribute("aria-selected")).toBe("true");
  await waitFor(() => expect(page.fetch.calls("GET", "/policy")).toHaveLength(1));
});

it("sends an admin viewing as a user from Manual import to Wanted, in that view, without its tab or its requests", async () => {
  const { fetch, at } = open("/wanted/manual-import?as=user", ADMIN);
  await waitFor(() => expect(at()).toBe("/wanted?as=user"));
  expect((await screen.findByRole("tab", { name: "Missing" })).getAttribute("aria-selected")).toBe("true");
  expect(screen.queryByRole("tab", { name: "Manual import" })).toBeNull();
  expect(screen.queryByRole("button", { name: "List files" })).toBeNull();
  expect(fetch.requests.some((r) => r.path === "/policy" || r.path.startsWith("/manualimport"))).toBe(false);
});

it.each(["/admin/system/journal/dl%3A4", "/admin/system/journal", "/wanted/manual-import", "/wanted/manual-import?as=user"])("refuses %s to a manager without an admin request", async (path) => {
  const { fetch, at } = open(path, MANAGER);
  expect(await screen.findByText(/not for your account/i)).toBeTruthy();
  expect(at()).toBe(path);
  expect(fetch.requests.every((r) => r.path === "/auth/me")).toBe(true);
});

it.each([["/collections/lists", MANAGER], ["/collections/lists", ADMIN], ["/admin/settings/import-lists", MANAGER]])("opens Import lists in Settings from %s for a manager and an admin", async (path, permissions) => {
  const page = open(path, permissions);
  expect(await screen.findByRole("heading", { name: "Import lists" })).toBeTruthy();
  expect(page.at()).toBe("/admin/settings/import-lists");
  await waitFor(() => expect(page.fetch.calls("GET", "/lists")).toHaveLength(1));
});

it.each(["/admin/settings/import-lists", "/collections/lists", "/admin/system/journal"])("refuses %s to a household account", async (path) => {
  const { fetch } = open(path, USER);
  expect(await screen.findByText(/not for your account/i)).toBeTruthy();
  expect(fetch.requests.every((r) => r.path === "/auth/me")).toBe(true);
});

it("sends an old anchor of a moved settings panel to its new page", async () => {
  const page = open("/admin/torrents#scan-roots", MANAGER);
  await waitFor(() => expect(page.at()).toBe("/admin/settings/downloads/torrents#scan-roots"));
  // Still an admin page: a manager is refused there, as on the old one.
  expect(await screen.findByText(/not for your account/i)).toBeTruthy();
});

it("opens Wanted for a manager, without the Manual import tab", async () => {
  const { fetch } = open("/wanted", MANAGER);
  expect((await screen.findByRole("tab", { name: "Missing" })).getAttribute("aria-selected")).toBe("true");
  await waitFor(() => expect(fetch.calls("GET", "/wanted")).toHaveLength(1));
  expect(screen.queryByRole("tab", { name: "Manual import" })).toBeNull();
  expect(screen.queryByText(/not for your account/i)).toBeNull();
});

// A new install: the root URL leads to Create your admin account, and creating it leads to Setup, not an empty Home.
it("leads a new install from / through Create your admin account to Setup", async () => {
  sessionStorage.clear();
  let created = false;
  const user = (await import("@testing-library/user-event")).default.setup();
  mockFetch([
    { method: "GET", path: "/auth/me", reply: () => created ? { kind: "session", id: 1, username: "admin", permissions: ADMIN, csrf_token: "t" } : { status: 401, body: { error: "authentication required" } } },
    { method: "GET", path: "/auth/first-account", reply: () => ({ needs_account: !created, password_pending: false }) },
    { method: "GET", path: "/auth/plex", reply: { enabled: false } },
    { method: "POST", path: "/auth/first-account", reply: () => { created = true; return { username: "admin", csrf_token: "c" }; } },
    { method: "GET", path: "/setup", reply: { first_run: true, settings_source: "db" } },
  ]);
  const router = createMemoryRouter(routes, { initialEntries: ["/"] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>);
  await user.type(await screen.findByLabelText("Password"), "a long enough password");
  await user.type(screen.getByLabelText("Confirm password"), "a long enough password");
  await user.click(screen.getByRole("button", { name: "Create account" }));
  await waitFor(() => expect(router.state.location.pathname).toBe("/admin/setup"));
  await new Promise((r) => setTimeout(r, 50));
  expect(router.state.location.pathname).toBe("/admin/setup");
});

// System > Status does not wait for the Setup check: a slow GET /setup leaves the page rendered.
it("renders Status for an admin while the Setup check has not answered", async () => {
  sessionStorage.clear();
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { kind: "session", id: 1, username: "admin", permissions: ADMIN, csrf_token: "t" } },
    { method: "GET", path: "/setup", reply: () => new Promise(() => {}) },
  ]);
  const router = createMemoryRouter(routes, { initialEntries: ["/admin"] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>);
  expect(await screen.findByRole("heading", { name: "Status" })).toBeTruthy();
  expect(router.state.location.pathname).toBe("/admin");
});

// A tab opened before an update asks for screen files that are gone: one reload, then the error screen.
describe("a screen that cannot be loaded", () => {
  const here = () => window.location.pathname + window.location.search + window.location.hash;
  const failing = () => Promise.reject(new TypeError("Failed to fetch dynamically imported module: /assets/activity-old.js"));
  function show(element: ReactNode) {
    const router = createMemoryRouter([{ path: "/", element, errorElement: <RouteError /> }], { initialEntries: ["/"] });
    render(<RouterProvider router={router} />);
  }
  afterEach(() => { cleanup(); vi.restoreAllMocks(); sessionStorage.clear(); });

  it("gives every root route the error screen", () => {
    for (const r of routes) expect(r.errorElement).toBeTruthy();
  });

  it("reloads once when the import fails and notes the address", async () => {
    sessionStorage.clear();
    const reload = vi.spyOn(browser, "reload").mockImplementation(() => {});
    const Page = lazyPage(failing);
    show(<Page />);
    await waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
    expect(JSON.parse(sessionStorage.getItem(RELOAD_GUARD)!).to).toBe(here());
    expect(screen.getByRole("status").textContent).toContain("Loading");
    expect(screen.queryByTestId("route-error")).toBeNull();
  });

  it("shows the error screen with Reload when the import fails again just after a reload", async () => {
    sessionStorage.setItem(RELOAD_GUARD, JSON.stringify({ to: here(), at: Date.now() - 2000 }));
    const reload = vi.spyOn(browser, "reload").mockImplementation(() => {});
    const Page = lazyPage(failing);
    show(<Page />);
    expect(await screen.findByRole("heading", { name: "This page could not be loaded." })).toBeTruthy();
    expect(screen.getByText("Theatarr may have been updated or is restarting.")).toBeTruthy();
    expect(screen.queryByText(/activity-old/)).toBeNull();
    expect(reload).not.toHaveBeenCalled();
    screen.getByRole("button", { name: "Reload" }).click();
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("shows the error screen with the message for an error thrown while rendering", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    function Broken(): ReactNode { throw new Error("Something broke"); }
    show(<Broken />);
    expect(await screen.findByRole("heading", { name: "This page could not be loaded." })).toBeTruthy();
    expect(screen.getByText("Something broke")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Reload" })).toBeTruthy();
  });
});
