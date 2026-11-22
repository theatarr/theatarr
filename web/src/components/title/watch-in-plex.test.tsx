// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { WatchInPlex } from "./watch-in-plex";

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
  const open = vi.spyOn(window, "open").mockImplementation(() => null);
  renderPage(<WatchInPlex titleId={7} versions={versions as never} />);
  const box = await screen.findByTestId("watch-in-plex");
  const buttons = within(box).getAllByRole("button");
  expect(buttons.map((b) => b.textContent)).toEqual(["Watch in Plex · 4K Remux", "Watch in Plex · 4K WEB"]);
  expect(api.calls("GET", "/titles/7/plex")).toHaveLength(0);
  await userEvent.setup().click(buttons[1]);
  await waitFor(() => expect(open).toHaveBeenCalledWith("https://app.plex.tv/desktop/#!/x", "_blank", "noopener"));
  expect(api.calls("GET", "/titles/7/plex").map((c) => c.search)).toEqual(["?version=movie-web"]);
});

it("says the server's sentence in place when Plex has not got it", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["browse"] } },
    { method: "GET", path: "/titles/7/plex", reply: { found: false, message: "Not found in Plex yet" } },
  ]);
  const open = vi.spyOn(window, "open").mockImplementation(() => null);
  renderPage(<WatchInPlex titleId={7} versions={[versions[0]] as never} />);
  await userEvent.setup().click(await screen.findByRole("button", { name: "Watch in Plex" }));
  expect((await screen.findByTestId("watch-in-plex-message")).textContent).toBe("Not found in Plex yet");
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

