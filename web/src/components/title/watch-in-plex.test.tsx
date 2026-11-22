// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { plexHandover, WatchInPlex } from "./watch-in-plex";

afterEach(() => vi.restoreAllMocks());

const versions = [
  { id: 1, key: "movie-remux", label: "4K Remux", has_file: true },
  { id: 2, key: "movie-web", label: "4K WEB", has_file: true },
  { id: 3, key: "movie-hd", label: "HD", has_file: false },
];

it("offers one button per library with a file, asks Plex only on the tap, and opens the web link", async () => {
  const api = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse"] } },
    { method: "GET", path: "/titles/7/plex", reply: { found: true, web_url: "https://app.plex.tv/desktop/#!/x", app_url: "plex://preplay/?x" } },
  ]);
  const tab = { opener: window as unknown, location: { href: "" }, close: vi.fn() };
  // The tab opens at the tap, before the lookup answers (a slow answer would lose the tap's permission to open it).
  let asked = -1;
  const open = vi.spyOn(window, "open").mockImplementation(() => {
    asked = api.calls("GET", "/titles/7/plex").length;
    return tab as never;
  });
  renderPage(<WatchInPlex titleId={7} versions={versions as never} />);
  const box = await screen.findByTestId("watch-in-plex");
  const buttons = within(box).getAllByRole("button");
  expect(buttons.map((b) => b.textContent)).toEqual(["Watch in Plex · 4K Remux", "Watch in Plex · 4K WEB"]);
  expect(api.calls("GET", "/titles/7/plex")).toHaveLength(0);
  await userEvent.setup().click(buttons[1]);
  // A blank tab, cut from this page, then sent to Plex Web: no fallback link when it opened.
  await waitFor(() => expect(tab.location.href).toBe("https://app.plex.tv/desktop/#!/x"));
  expect(open).toHaveBeenCalledWith("", "_blank");
  expect(asked).toBe(0);
  expect(tab.opener).toBeNull();
  expect(tab.close).not.toHaveBeenCalled();
  expect(screen.queryByRole("link", { name: "Open in Plex" })).toBeNull();
  expect(api.calls("GET", "/titles/7/plex").map((c) => c.search)).toEqual(["?version=movie-web"]);
});

it("says the server's sentence in place when Plex has not got it, and closes the tab it opened", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse"] } },
    { method: "GET", path: "/titles/7/plex", reply: { found: false, message: "This library has no Plex section." } },
  ]);
  const tab = { opener: window as unknown, location: { href: "" }, close: vi.fn() };
  vi.spyOn(window, "open").mockImplementation(() => tab as never);
  renderPage(<WatchInPlex titleId={7} versions={[versions[0]] as never} />);
  await userEvent.setup().click(await screen.findByRole("button", { name: "Watch in Plex" }));
  expect((await screen.findByTestId("watch-in-plex-message")).textContent).toBe("This library has no Plex section.");
  expect(tab.close).toHaveBeenCalled();
  expect(tab.location.href).toBe("");
  expect(screen.queryByRole("link")).toBeNull();
});

it("hands over to the Plex app on iOS and keeps the Plex Web link beneath the button", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse"] } },
    { method: "GET", path: "/titles/7/plex", reply: { found: true, web_url: "https://app.plex.tv/desktop/#!/x", app_url: "plex://preplay/?x" } },
  ]);
  vi.spyOn(navigator, "userAgent", "get").mockReturnValue("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15");
  const toApp = vi.spyOn(plexHandover, "toApp").mockImplementation(() => {});
  const open = vi.spyOn(window, "open").mockImplementation(() => null);
  renderPage(<WatchInPlex titleId={7} versions={[versions[0]] as never} />);
  await userEvent.setup().click(await screen.findByRole("button", { name: "Watch in Plex" }));
  const link = await screen.findByRole("link", { name: "Open in Plex web" });
  expect(link.getAttribute("href")).toBe("https://app.plex.tv/desktop/#!/x");
  expect(toApp).toHaveBeenCalledWith("plex://preplay/?x");
  expect(open).not.toHaveBeenCalled();
});

it("shows Open in Plex in place when the browser refuses the new tab", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse"] } },
    { method: "GET", path: "/titles/7/plex", reply: { found: true, web_url: "https://app.plex.tv/desktop/#!/x", app_url: "plex://preplay/?x" } },
  ]);
  vi.spyOn(window, "open").mockImplementation(() => null);
  renderPage(<WatchInPlex titleId={7} versions={[versions[0]] as never} />);
  await userEvent.setup().click(await screen.findByRole("button", { name: "Watch in Plex" }));
  const link = await screen.findByRole("link", { name: "Open in Plex" });
  expect(link.getAttribute("href")).toBe("https://app.plex.tv/desktop/#!/x");
  expect(link.getAttribute("target")).toBe("_blank");
});

it("wraps a long library name inside its button", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse"] } }]);
  const long = [{ ...versions[0], label: "2160pRemuxDolbyVisionHDR10PlusHEVC" }, versions[1]];
  renderPage(<WatchInPlex titleId={7} versions={long as never} />);
  const label = await screen.findByText("Watch in Plex · 2160pRemuxDolbyVisionHDR10PlusHEVC");
  expect(label.className.split(" ")).toEqual(expect.arrayContaining(["min-w-0", "[overflow-wrap:anywhere]"]));
  expect(label.closest("button")!.className.split(" ")).toContain("max-w-full");
});
