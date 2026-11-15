// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { HistoryTab } from "./history-tab";

const me = { id: 9, username: "manager", permissions: ["browse", "see_files", "manage"], csrf_token: "t" };
const title = { id: 1, name: "Alpha", year: 2026, kind: "movie" };
const grab = { id: "dl:7", at: "2026-11-12T14:03:00Z", kind: "grab", event: "grabbed", title, version: "movie-web", words: "Grabbed", tone: "info", release: "Alpha.2026.2160p.WEB-DL", quality: "WEBDL-2160p", score: 1500 };
const link = { id: "lib:3", at: "2026-11-11T09:00:00Z", kind: "link", event: "deleted", title, version: "movie-web", words: "Upgraded: 1 file replaced", tone: "info" };

it("shows quality and score in cells, a dash where the event has none, and no day headings; Load more asks for the next cursor", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/titles/facets", reply: {} },
    {
      method: "GET",
      path: "/activity",
      reply: (r: { search: string }) =>
        r.search.includes("cursor=c2") ? { state: "history", admin: true, history: [{ ...link, id: "lib:2", at: "2026-11-01T09:00:00Z" }] } : { state: "history", admin: true, history: [grab, link], next_cursor: "c2" },
    },
  ]);
  renderPage(<HistoryTab admin />, "/activity/history");
  const rows = await screen.findAllByTestId("history-row");
  expect(rows).toHaveLength(2);
  expect(rows[0].querySelector('[data-column="quality"]')?.textContent).toBe("WEBDL-2160p");
  expect(rows[0].querySelector('[data-column="score"]')?.textContent).toBe("1,500");
  expect(rows[1].querySelector('[data-column="quality"]')?.textContent).toBe("—");
  expect(rows[1].querySelector('[data-column="score"]')?.textContent).toBe("—");
  expect(screen.queryByText("Yesterday")).toBeNull();
  expect(document.querySelector("h3")).toBeNull();
  await userEvent.setup().click(screen.getByRole("button", { name: "Load more" }));
  await waitFor(() => expect(screen.getAllByTestId("history-row")).toHaveLength(3));
  expect(fetch.calls("GET", "/activity").some((r) => r.search.includes("cursor=c2"))).toBe(true);
});

const series = { id: 3, name: "Gamma", year: 2019, kind: "series" };
const eps = ["S01E01", "S01E02", "S01E03", "S01E04", "S01E05", "S01E06"];
const rows = [
  { id: "copy:2", at: "2026-11-12T16:00:00Z", kind: "copy", event: "copy_not_cached", title: series, words: "Not cached on AD", tone: "info", provider: "ad", release: "Gamma.S01.2160p.WEB-GRP", reason: "Not cached on AD; our add was deleted" },
  { id: "lib:9", at: "2026-11-12T15:00:00Z", kind: "link", event: "deleted", title: series, version: "tv-2160p", words: "Upgraded: 2 files replaced", tone: "info", episodes: ["S01E01", "S01E02"], files: 2, release: "Gamma.S01.1080p.WEB-OLD", quality: "WEBDL-1080p", score: 900, replaced_by: { release: "Gamma.S01.2160p.WEB-GRP", quality: "WEBDL-2160p", score: 2400 }, replaced: [{ path: "/tv/Gamma/S01E02.mkv", episodes: ["S01E02"] }, { path: "/tv/Gamma/S01E01.mkv", release: "Gamma.S01.1080p.WEB-OLD", quality: "WEBDL-1080p", episodes: ["S01E01"] }], entry: "lib:9" },
  { id: "dl:5:import", at: "2026-11-12T15:00:00Z", kind: "import", event: "imported", title: series, version: "tv-2160p", words: "Imported", tone: "ok", episodes: eps, release: "Gamma.S01.2160p.WEB-GRP", quality: "WEBDL-2160p", score: 2400 },
  { id: "lib:4", at: "2026-10-27T09:00:00Z", kind: "link", event: "adopted", title: series, version: "tv-2160p", words: "Adopted", tone: "ok", episodes: ["S01E07"] },
  { id: "lib:12", at: "2026-10-25T09:00:00Z", kind: "link", event: "deleted", title: series, version: "tv-2160p", words: "Upgraded: 2 files replaced", tone: "info", files: 2, replaced: [{ path: "/tv/Gamma/S02E01.mkv", release: "Gamma.S02E01.WEB" }, { path: "/tv/Gamma/S02E02.mkv", release: "Gamma.S02E02.WEB" }] },
  { id: "dl:6:fail", at: "2026-10-20T09:00:00Z", kind: "failure", event: "failed", title: series, version: "tv-2160p", words: "Failed", tone: "bad", release: "Gamma.S01E08.WEB", reason: "Provider refused: too many active" },
];

it("words rows like Radarr's: one import with its episodes, an upgrade's one row that opens to its files and names what replaced them, unknown for an old adopted file, reasons", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/activity", reply: { state: "history", admin: true, history: rows } },
  ]);
  renderPage(<HistoryTab admin titleId={3} />, "/title/3?tab=history");
  const got = await screen.findAllByTestId("history-row");
  expect(got.map((r) => r.querySelector('[data-testid="history-words"]')?.textContent)).toEqual(["Not cached on AD", "Upgraded: 2 files replaced", "Imported", "Adopted", "Upgraded: 2 files replaced", "Failed"]);
  // The title tab: no filters, no Title column; the episodes under the event, compact, the full list on hover.
  expect(screen.queryByTestId("history-filters")).toBeNull();
  expect(got[0].querySelector('[data-column="title"]')).toBeNull();
  const ep = got[2].querySelector('[data-testid="history-episodes"]');
  expect(ep?.textContent).toBe("6 episodes · S01E01–S01E06");
  expect(ep?.getAttribute("title")).toBe(eps.join(" "));
  expect(got[1].querySelector('[data-column="release"]')?.textContent).toBe("Gamma.S01.1080p.WEB-OLDReplaced by Gamma.S01.2160p.WEB-GRP");
  expect(got[1].querySelector('[data-column="score"]')?.textContent).toBe("900");
  // The files open under the row, across it, in episode order.
  const toggle = got[1].querySelector<HTMLButtonElement>('[data-testid="history-replaced-toggle"]');
  expect(toggle?.textContent).toBe("Show the 2 files");
  expect(screen.queryByTestId("history-replaced")).toBeNull();
  await userEvent.setup().click(toggle!);
  const files = await screen.findByTestId("history-replaced");
  expect(files.closest("tr")?.hasAttribute("data-expanded")).toBe(true);
  expect([...files.querySelectorAll("li")].map((li) => li.textContent)).toEqual([
    "S01E01.mkv Gamma.S01.1080p.WEB-OLD · WEBDL-1080p · S01E01",
    "S01E02.mkv Name not recorded · S01E02",
  ]);
  expect(toggle?.textContent).toBe("Hide the files");
  expect(got[0].querySelector('[data-testid="history-replaced-toggle"]')).toBeNull();
  expect(got[3].querySelector('[data-column="release"]')?.textContent).toBe("Name not recorded");
  expect(got[3].querySelector('[data-column="quality"]')?.textContent).toBe("—");
  // An upgrade over files of two releases names how many, not "Name not recorded".
  expect(got[4].querySelector('[data-column="release"]')?.textContent).toBe("2 releases");
  expect(got[5].querySelector('[data-testid="history-reason"]')?.textContent).toBe("Provider refused: too many active");
  expect(got[0].querySelector('[data-testid="history-reason"]')?.textContent).toBe("Not cached on AD; our add was deleted");
  expect(fetch.calls("GET", "/activity")[0].search).toContain("title_id=3");
});

it("reads an old Other providers bookmark as All events", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/titles/facets", reply: {} },
    { method: "GET", path: "/activity", reply: { state: "history", admin: false, history: [] } },
  ]);
  renderPage(<HistoryTab admin={false} as="user" />, "/activity/history?event=copies");
  expect((await screen.findByTestId("history-event") as HTMLSelectElement).value).toBe("all");
  expect(screen.queryByRole("option", { name: "Other providers" })).toBeNull();
  await screen.findByText("Nothing has happened yet.");
  expect(fetch.calls("GET", "/activity").every((r) => !r.search.includes("events="))).toBe(true);
});

it("says once that no replaced file has a release name", async () => {
  const up = { id: "lib:20", at: "2026-11-12T15:00:00Z", kind: "link", event: "deleted", title: series, version: "tv-2160p", words: "Upgraded: 3 files replaced", tone: "info", files: 3,
    replaced: [{ path: "/tv/Gamma/b.mkv" }, { path: "/tv/Gamma/S01E10.mkv", episodes: ["S01E10"] }, { path: "/tv/Gamma/S01E09.mkv", episodes: ["S01E09"] }] };
  mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/activity", reply: { state: "history", admin: true, history: [up] } },
  ]);
  renderPage(<HistoryTab admin titleId={3} />, "/title/3?tab=history");
  await userEvent.setup().click(await screen.findByRole("button", { name: "Show the 3 files" }));
  const files = await screen.findByTestId("history-replaced");
  expect(files.textContent).not.toContain("Name not recorded");
  expect(files.querySelector("p")?.textContent).toBe("Release names not recorded.");
  expect([...files.querySelectorAll("li")].map((li) => li.textContent)).toEqual(["S01E09.mkv S01E09", "S01E10.mkv S01E10", "b.mkv "]);
});
