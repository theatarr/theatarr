// @vitest-environment jsdom
import { act, screen, waitFor } from "@testing-library/react";
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
