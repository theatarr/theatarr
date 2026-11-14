// @vitest-environment jsdom
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { AppShell } from "./app-shell";
vi.mock("@/components/search/search-context", () => ({ useSearchUI: () => ({ openOverlay: vi.fn() }) }));
vi.mock("./account-menu", () => ({ AccountMenu: () => null, ThemeSwitch: () => null }));
vi.mock("@/api/useSetupProgress", () => ({ useSetupProgress: () => ({ unfinished: false }) }));

it("exposes one navigation at a time and updates it at the desktop breakpoint", () => {
  mockFetch([]);
  const listeners = new Set<() => void>();
  const media = { matches: false, addEventListener: (_: string, fn: () => void) => listeners.add(fn), removeEventListener: (_: string, fn: () => void) => listeners.delete(fn) };
  vi.stubGlobal("matchMedia", () => media);
  renderPage(<AppShell perms={{ ownKeys: false, admin: false, seeFiles: false, manage: false, add: false, request: false, manageRequests: false, users: false }}><p>Page</p></AppShell>);
  expect(screen.getByRole("navigation", { name: "Tabs" })).toBeTruthy();
  expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();
  expect(document.querySelector('[data-slot="sidebar"]')).toBeNull();
  act(() => { media.matches = true; listeners.forEach(fn => fn()); });
  expect(screen.getByRole("navigation", { name: "Main" })).toBeTruthy();
  expect(screen.queryByRole("navigation", { name: "Tabs" })).toBeNull();
  expect(document.querySelector('[data-testid="bottom-tabs"]')).toBeNull();
});

const versions = { method: "GET", path: "/settings/versions", reply: { versions: [], switches: { paused: true, pause_forced: false, versions: [] } } };

it("never asks for the switches without admin", async () => {
  const fetch = mockFetch([versions]);
  vi.stubGlobal("matchMedia", () => ({ matches: true, addEventListener: () => {}, removeEventListener: () => {} }));
  renderPage(<AppShell perms={{ ownKeys: false, admin: false, seeFiles: false, manage: false, add: false, request: false, manageRequests: false, users: false }}><p>Page</p></AppShell>);
  await act(async () => {});
  expect(fetch.calls("GET", "/settings/versions")).toHaveLength(0);
  // Nor Pause: it needs the admin permission that changes settings.
  expect(screen.queryByRole("switch", { name: "Pause" })).toBeNull();
  expect(fetch.calls("GET", "/settings")).toHaveLength(0);
});

it("shows Pause to an admin in the header, disabled while settings are read-only and locked when forced", async () => {
  const o = { source: "db", writable: true, generation: 3, switches: { paused: true, pause_forced: false, versions: [] } };
  mockFetch([versions, { method: "GET", path: "/settings", reply: o }]);
  vi.stubGlobal("matchMedia", () => ({ matches: true, addEventListener: () => {}, removeEventListener: () => {} }));
  const admin = { ownKeys: false, admin: true, seeFiles: false, manage: false, add: false, request: false, manageRequests: false, users: false };
  const first = renderPage(<AppShell perms={admin}><p>Page</p></AppShell>);
  const header = document.querySelector<HTMLElement>('[data-slot="root-header"]')!;
  await waitFor(() => expect(header.querySelector('[role="switch"][aria-label="Pause"]')).toBeTruthy());
  expect(header.querySelector<HTMLInputElement>('[role="switch"]')!.disabled).toBe(false);
  first.unmount();
  mockFetch([versions, { method: "GET", path: "/settings", reply: { ...o, writable: false } }]);
  renderPage(<AppShell perms={admin}><p>Page</p></AppShell>);
  expect((await screen.findByRole("switch", { name: "Pause" }) as HTMLInputElement).disabled).toBe(true);
});

it("offers Skip to content as the first stop for Tab, moving focus to the page", async () => {
  mockFetch([]);
  vi.stubGlobal("matchMedia", () => ({ matches: true, addEventListener: () => {}, removeEventListener: () => {} }));
  renderPage(<AppShell perms={{ ownKeys: false, admin: false, seeFiles: false, manage: false, add: false, request: false, manageRequests: false, users: false }}><p>Page</p></AppShell>);
  const user = userEvent.setup();
  await user.tab();
  const skip = screen.getByRole("link", { name: "Skip to content" });
  expect(document.activeElement).toBe(skip);
  await user.click(skip);
  expect(document.activeElement).toBe(document.getElementById("main"));
  expect(document.getElementById("main")?.textContent).toBe("Page");
});

it("phone: tabs Home, Browse, Activity, Requests, More; More opens System only on a tap; a household account gets no Admin", async () => {
  mockFetch([]);
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} }));
  window.localStorage.clear();
  const admin = { ownKeys: true, admin: true, seeFiles: true, manage: true, add: true, request: true, manageRequests: true, users: true };
  const first = renderPage(<AppShell perms={admin}><p>Page</p></AppShell>);
  const tabs = screen.getByRole("navigation", { name: "Tabs" });
  expect([...tabs.querySelectorAll("[data-tab]")].map((t) => t.textContent)).toEqual(["Home", "Browse", "Activity", "Requests", "More"]);
  expect(tabs.querySelector('[data-tab="activity"]')?.getAttribute("href")).toBe("/activity");
  const user = userEvent.setup();
  await user.click(screen.getByTestId("more-tab"));
  const system = await screen.findByRole("button", { name: /System/ });
  expect(system.getAttribute("aria-expanded")).toBe("false");
  expect(screen.queryByRole("link", { name: "Tasks" })).toBeNull();
  expect(screen.getByRole("link", { name: "Wanted" })).toBeTruthy();
  await user.click(system);
  expect(screen.getByRole("link", { name: /Tasks/ }).getAttribute("href")).toBe("/admin/system/tasks");
  expect(screen.getByRole("link", { name: "Decision journal" }).getAttribute("href")).toBe("/admin/system/journal");
  first.unmount();
  window.localStorage.clear();
  const household = { ownKeys: false, admin: false, seeFiles: false, manage: false, add: false, request: true, manageRequests: false, users: false };
  renderPage(<AppShell perms={household}><p>Page</p></AppShell>);
  await user.click(screen.getByTestId("more-tab"));
  const sheet = await screen.findByRole("dialog", { name: "More" });
  expect([...sheet.querySelectorAll("a[href]")].map((a) => a.getAttribute("href")).filter((h) => h !== "/")).toEqual(["/now-playing", "/calendar"]);
});
