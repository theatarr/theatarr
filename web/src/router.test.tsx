// @vitest-environment jsdom
// The real route table for the admin pages that live under Activity and Wanted: who may open them, and "View as a user".
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { expect, it, vi } from "vitest";
import { routes } from "./router";
import { mockFetch } from "@/test/page";

vi.mock("@/api/events", () => ({ LiveProvider: ({ children }: { children: ReactNode }) => children, useLive: () => ({}) }));
vi.mock("./components/shell/app-shell", () => ({ AppShell: ({ children }: { children: ReactNode }) => children, Logo: () => null }));

const ADMIN = ["browse", "request", "see_files", "manage", "admin", "users"];
const MANAGER = ["browse", "request", "see_files", "manage"];

function open(path: string, permissions: string[]) {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { kind: "session", id: 1, username: "someone", permissions, csrf_token: "t" } },
    { method: "GET", path: "/activity", reply: { state: "now", admin: true, total: 0, now: [] } },
    { method: "GET", path: "/wanted", reply: { items: [], titles: {}, libraries: {}, total: 0, counts: {}, limit: 50, offset: 0, enabled: true, downloads_enabled: true, interval: "15m0s", per_hour: 20, again_after: "24h0m0s", missing: true, cutoff_unmet: "quality", searched_last_hour: 0 } },
    { method: "GET", path: "/status", reply: { versions: [] } },
    { method: "GET", path: "/policy", reply: { versions: [] } },
    { method: "GET", path: "/journal/dl%3A4", reply: { id: "dl:4", kind: "grab", summary: "Grabbed" } },
  ]);
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>);
  const at = () => router.state.location.pathname + router.state.location.search;
  return { fetch, at };
}

it("opens a journal entry and Manual import for an admin", async () => {
  const entry = open("/activity/journal/dl%3A4", ADMIN);
  expect(await screen.findByTestId("journal-summary")).toBeTruthy();
  expect(entry.fetch.calls("GET", "/journal/dl%3A4")).toHaveLength(1);
});

it("opens Manual import as a tab of Wanted for an admin", async () => {
  const page = open("/wanted/manual-import", ADMIN);
  expect((await screen.findByRole("tab", { name: "Manual import" })).getAttribute("aria-selected")).toBe("true");
  expect(page.fetch.calls("GET", "/policy")).toHaveLength(1);
});

it("sends an admin viewing as a user from a journal entry to Activity, in that view, without reading the journal", async () => {
  const { fetch, at } = open("/activity/journal/dl%3A4?as=user", ADMIN);
  await waitFor(() => expect(at()).toBe("/activity?as=user"));
  expect(await screen.findByRole("button", { name: "Back to the admin view" })).toBeTruthy();
  expect(screen.queryByRole("tab", { name: "Decision journal" })).toBeNull();
  expect(fetch.requests.some((r) => r.path.startsWith("/journal") || r.path === "/status")).toBe(false);
});

it("sends an admin viewing as a user from Manual import to Wanted, in that view, without its tab or its requests", async () => {
  const { fetch, at } = open("/wanted/manual-import?as=user", ADMIN);
  await waitFor(() => expect(at()).toBe("/wanted?as=user"));
  expect((await screen.findByRole("tab", { name: "Missing" })).getAttribute("aria-selected")).toBe("true");
  expect(screen.queryByRole("tab", { name: "Manual import" })).toBeNull();
  expect(screen.queryByRole("button", { name: "List files" })).toBeNull();
  expect(fetch.requests.some((r) => r.path === "/policy" || r.path.startsWith("/manualimport"))).toBe(false);
});

it.each(["/activity/journal/dl%3A4", "/wanted/manual-import", "/activity/journal/dl%3A4?as=user", "/wanted/manual-import?as=user"])("refuses %s to a manager without an admin request", async (path) => {
  const { fetch, at } = open(path, MANAGER);
  expect(await screen.findByText(/not for your account/i)).toBeTruthy();
  expect(at()).toBe(path);
  expect(fetch.requests.every((r) => r.path === "/auth/me")).toBe(true);
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
