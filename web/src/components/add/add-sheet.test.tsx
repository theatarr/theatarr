// @vitest-environment jsdom
// The add drawer's library rows (audit 2): one checkbox per library, identity and state on their own rows, the
// library state from grab_state, the global notice said once, and "n will download" from the ticked rows' will_grab.
// Invented keys and names.
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import { AddSheet } from "./add-sheet";

const gates = { grab: true, router_enabled: true, router_forced_off: false, library_enabled: true, library_writes: true };
const long = "A library name that is long enough to wrap at four hundred p";
const versions = [
  { key: "movie-web", label: long, mode: "active", grab_state: { ...gates, mode: "active", will_grab: true } },
  { key: "movie-remux", label: "Remux", mode: "active", grab_state: { ...gates, mode: "active", grab: false, will_grab: false } },
  { key: "movie-1080", label: "1080p", mode: "shadow", grab_state: { ...gates, mode: "shadow", will_grab: false, library_writes: false } },
  { key: "movie-4k", label: "4K", mode: "active", grab_state: { ...gates, mode: "active", will_grab: true } },
  { key: "movie-hdr", label: "HDR", mode: "active", grab_state: { ...gates, mode: "active", will_grab: true, library_writes: false } },
  { key: "movie-dv", label: "Dolby Vision", mode: "shadow", grab_state: { ...gates, mode: "shadow", will_grab: false, library_writes: false } },
  { key: "movie-sd", label: "SD", mode: "active", grab_state: { ...gates, mode: "active", will_grab: true } },
].map((v) => ({ ...v, kind: "movie", enable: "", fits: true, default: v.key !== "movie-sd", on: false, searchable: v.grab_state.will_grab }));
const options = { kind: "movie", anime: false, router_enabled: true, episodes_known: false, can_search: true, seasons: [], versions };

it("lists every library with its state, counts what will download and sends the same add", async () => {
  expect(long).toHaveLength(60);
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "add"] } },
    { method: "GET", path: "/add/options", reply: options },
    { method: "POST", path: "/titles", reply: json({ created: true, new: ["movie-web"], title: { id: 9, name: "Alpha", versions: [] } }) },
  ]);
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", year: 2024, tmdb_id: 7 }} onClose={() => {}} />);
  const list = await screen.findByTestId("add-versions");
  expect(within(list).getAllByRole("checkbox")).toHaveLength(7);
  const web = screen.getByTestId("add-version-movie-web");
  expect(within(web).getByText(long)).toBeTruthy();
  expect(within(web).getByText("movie-web")).toBeTruthy();
  expect(within(web).getByText("Downloads on")).toBeTruthy();
  expect(within(screen.getByTestId("add-version-movie-remux")).getByText("Downloads off")).toBeTruthy();
  expect(within(screen.getByTestId("add-version-movie-1080")).getByText("Preview only")).toBeTruthy();
  expect(within(screen.getByTestId("add-version-movie-hdr")).getByTestId("links-off")).toBeTruthy();
  // The checkbox is named by the library and described by its state.
  const box = within(web).getByRole("checkbox", { name: new RegExp(long) });
  expect(document.getElementById(box.getAttribute("aria-describedby") ?? "")?.textContent).toContain("Downloads on");
  // Six ticked by default; movie-web, movie-4k and movie-hdr will download.
  expect(screen.getByTestId("add-count").textContent).toBe("6 libraries · 3 will download");
  await user.click(within(screen.getByTestId("add-version-movie-sd")).getByRole("checkbox"));
  expect(screen.getByTestId("add-count").textContent).toBe("7 libraries · 4 will download");
  await user.click(box);
  expect(screen.getByTestId("add-count").textContent).toBe("6 libraries · 3 will download");
  expect(screen.queryByTestId("downloads-off-notice")).toBeNull();
  await user.click(screen.getByTestId("add-submit"));
  await waitFor(() => expect(fetch.calls("POST", "/titles")).toHaveLength(1));
  expect(fetch.calls("POST", "/titles")[0].body).toEqual({ kind: "movie", tmdb_id: 7, title: "Alpha", year: 2024, anime: false, versions: ["movie-remux", "movie-1080", "movie-4k", "movie-hdr", "movie-dv", "movie-sd"], search: false });
});

it("turns Search now on from its switch or the row's words and sends it with the add", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "add"] } },
    { method: "GET", path: "/add/options", reply: options },
    { method: "POST", path: "/titles", reply: json({ created: true, new: ["movie-web"], title: { id: 9, name: "Alpha", versions: [] } }) },
  ]);
  const user = userEvent.setup();
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", year: 2024, tmdb_id: 7 }} onClose={() => {}} />);
  const sw = await screen.findByRole("switch", { name: "Search now" });
  expect(sw.getAttribute("aria-checked")).toBe("false");
  await user.click(screen.getByText("Search now"));
  expect(sw.getAttribute("aria-checked")).toBe("true");
  await user.click(sw);
  expect(sw.getAttribute("aria-checked")).toBe("false");
  await user.click(sw);
  await user.click(screen.getByTestId("add-submit"));
  await waitFor(() => expect(fetch.calls("POST", "/titles")).toHaveLength(1));
  expect((fetch.calls("POST", "/titles")[0].body as { search: boolean }).search).toBe(true);
});

it("says new downloads are off once, not on every row", async () => {
  const off = versions.map((v) => ({ ...v, searchable: false, grab_state: { ...v.grab_state, router_enabled: false, will_grab: false } }));
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["add"] } },
    { method: "GET", path: "/add/options", reply: { ...options, router_enabled: false, versions: off } },
  ]);
  renderPage(<AddSheet target={{ kind: "movie", name: "Alpha", tmdb_id: 7 }} onClose={() => {}} />);
  expect((await screen.findAllByTestId("downloads-off-notice"))).toHaveLength(1);
  expect(screen.queryByTestId("add-no-search")).toBeNull();
  expect(within(screen.getByTestId("add-version-movie-web")).getByText("Paused globally")).toBeTruthy();
  expect(screen.getByTestId("add-count").textContent).toBe("6 libraries · 0 will download");
  // Not an admin: no settings link.
  expect(within(screen.getByTestId("downloads-off-notice")).queryByRole("link")).toBeNull();
});
