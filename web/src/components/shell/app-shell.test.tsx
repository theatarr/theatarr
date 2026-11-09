// @vitest-environment jsdom
import { act, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { AppShell, DownloadsSignal } from "./app-shell";
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

const versions = (router_enabled: boolean, router?: string) => ({ method: "GET", path: "/settings/versions", reply: { versions: [], switches: { router_enabled, sources: router ? { router } : {} } } });

it("says new downloads are off in the top bar, linking to the switch", async () => {
  mockFetch([versions(false)]);
  renderPage(<DownloadsSignal />);
  const signal = await screen.findByTestId("downloads-signal");
  expect(signal.getAttribute("href")).toBe("/admin/settings/downloads");
  // One wording at every width: the phone bar says the same as the desktop one.
  expect(signal.textContent).toBe("New downloads off");
  expect(signal.dataset.forced).toBeUndefined();
});

it("says when the environment forces new downloads off", async () => {
  mockFetch([versions(false, "forced off by environment")]);
  renderPage(<DownloadsSignal />);
  const signal = await screen.findByTestId("downloads-signal");
  expect(signal.textContent).toBe("New downloads locked off");
  expect(signal.dataset.forced).toBe("true");
});

it("shows nothing while new downloads are on", async () => {
  const fetch = mockFetch([versions(true)]);
  renderPage(<DownloadsSignal />);
  await waitFor(() => expect(fetch.calls("GET", "/settings/versions")).toHaveLength(1));
  await act(async () => {});
  expect(screen.queryByTestId("downloads-signal")).toBeNull();
});

it("never asks for the switches without admin", async () => {
  const fetch = mockFetch([versions(false)]);
  vi.stubGlobal("matchMedia", () => ({ matches: true, addEventListener: () => {}, removeEventListener: () => {} }));
  renderPage(<AppShell perms={{ ownKeys: false, admin: false, seeFiles: false, manage: false, add: false, request: false, manageRequests: false, users: false }}><p>Page</p></AppShell>);
  await act(async () => {});
  expect(screen.queryByTestId("downloads-signal")).toBeNull();
  expect(fetch.calls("GET", "/settings/versions")).toHaveLength(0);
});
