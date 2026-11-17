// @vitest-environment jsdom
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import { focusManager } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import { fmtTime } from "@/lib/format";
import { EpisodeHistoryRows, HistoryTab } from "./history-tab";

const me = { id: 9, username: "manager", permissions: ["browse", "see_files", "manage"], csrf_token: "t" };
const title = { id: 1, name: "Alpha", year: 2026, kind: "movie" };
const grab = { id: "dl:7", at: "2026-11-12T14:03:00Z", kind: "grab", event: "grabbed", title, version: "movie-web", words: "Grabbed", tone: "info", release: "Alpha.2026.2160p.WEB-DL", quality: "WEBDL-2160p", score: 1500 };
const link = { id: "lib:3", at: "2026-11-11T09:00:00Z", kind: "link", event: "deleted", title, version: "movie-web", words: "Upgraded", tone: "ok" };

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

it("shows one row per download: an import or a failure says when it was grabbed under its words, an upgrade in its opened details", async () => {
  const imported = { id: "dl:7:import", at: "2026-11-12T14:05:00Z", kind: "import", event: "imported", title, version: "movie-web", words: "Imported", tone: "ok", release: "Alpha.2026.2160p.WEB-DL", grabbed_at: "2026-11-12T14:03:00Z" };
  const upgraded = { ...link, grabbed_at: "2026-11-11T08:00:00Z", replaced: [{ path: "/movies/Alpha/old.mkv", release: "Alpha.2026.1080p.WEB" }] };
  const failed = { id: "dl:8:fail", at: "2026-11-12T13:00:00Z", kind: "failure", event: "failed", title, version: "movie-web", words: "Failed", tone: "bad", grabbed_at: "2026-11-12T12:50:00Z" };
  mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/titles/facets", reply: {} },
    { method: "GET", path: "/activity", reply: { state: "history", admin: true, history: [grab, imported, failed, upgraded] } },
  ]);
  renderPage(<HistoryTab admin />, "/activity/history");
  const got = await screen.findAllByTestId("history-row");
  expect(got.map((r) => r.querySelector('[data-testid="history-words"]')?.textContent)).toEqual(["Grabbed", "Imported", "Failed", "Upgraded"]);
  expect(got.map((r) => r.querySelector('[data-testid="history-grabbed"]')?.textContent ?? null)).toEqual([
    null,
    `Grabbed ${fmtTime("2026-11-12T14:03:00Z")}`,
    `Grabbed ${fmtTime("2026-11-12T12:50:00Z")}`,
    null,
  ]);
  // The upgrade's grab time is in its details, above the replaced files.
  expect(screen.queryByTestId("history-replaced")).toBeNull();
  await userEvent.setup().click(got[3].querySelector<HTMLButtonElement>('[data-testid="history-replaced-toggle"]')!);
  const details = await screen.findByTestId("history-replaced");
  expect(details.querySelector('[data-testid="history-grabbed"]')?.textContent).toBe(`Grabbed ${fmtTime("2026-11-11T08:00:00Z")}`);
  expect(screen.getAllByTestId("history-grabbed")).toHaveLength(3);
});

const series = { id: 3, name: "Gamma", year: 2019, kind: "series" };
const eps = ["S01E01", "S01E02", "S01E03", "S01E04", "S01E05", "S01E06"];
const rows = [
  { id: "copy:2", at: "2026-11-12T16:00:00Z", kind: "copy", event: "copy_not_cached", title: series, words: "Not cached on AD", tone: "info", provider: "ad", release: "Gamma.S01.2160p.WEB-GRP", reason: "Not cached on AD; our add was deleted" },
  { id: "lib:9", at: "2026-11-12T15:00:00Z", kind: "link", event: "deleted", title: series, version: "tv-2160p", words: "Upgraded", tone: "ok", episodes: eps, files: 2, release: "Gamma.S01.2160p.WEB-GRP", quality: "WEBDL-2160p", score: 2400, replaced: [{ path: "/tv/Gamma/S01E02.mkv", episodes: ["S01E02"] }, { path: "/tv/Gamma/S01E01.mkv", release: "Gamma.S01.1080p.WEB-OLD", quality: "WEBDL-1080p", score: 900, episodes: ["S01E01"] }], entry: "dl:5:import" },
  { id: "lib:5", at: "2026-11-01T09:00:00Z", kind: "link", event: "adopted", title: series, version: "tv-2160p", words: "Adopted", tone: "ok", episodes: ["S01E07"], files: 1, release: "Gamma.S01E07.2160p.WEB-NEW", quality: "WEBDL-2160p", score: 1800, replaced: [{ path: "/tv/Gamma/S01E07.old.mkv", release: "Gamma.S01E07.1080p.WEB-OLD", quality: "WEBDL-1080p", score: 600, episodes: ["S01E07"] }] },
  { id: "lib:4", at: "2026-10-27T09:00:00Z", kind: "link", event: "adopted", title: series, version: "tv-2160p", words: "Adopted", tone: "ok", episodes: ["S01E08"], reason: "From Sonarr", source: "Sonarr_anime_zurg" },
  { id: "lib:12", at: "2026-10-25T09:00:00Z", kind: "link", event: "deleted", title: series, version: "tv-2160p", words: "Upgraded", tone: "ok", files: 2, release: "Gamma.S02.WEB-NEW", replaced: [{ path: "/tv/Gamma/S02E01.mkv", release: "Gamma.S02E01.WEB" }, { path: "/tv/Gamma/S02E02.mkv", release: "Gamma.S02E02.WEB" }] },
  { id: "dl:6:fail", at: "2026-10-20T09:00:00Z", kind: "failure", event: "failed", title: series, version: "tv-2160p", words: "Failed", tone: "bad", release: "Gamma.S01E08.WEB", reason: "Provider refused: too many active" },
];

/** A replaced file's labelled parts ("File: S01E01.mkv"). */
const parts = (li: Element) => [...li.querySelectorAll("dt")].map((dt) => `${dt.textContent}: ${dt.nextElementSibling?.textContent}`);
const text = (el: Element | null | undefined) => el?.textContent?.replace(/\u00a0/g, " ");

it("words rows like Radarr's: an upgrade's one row with the new release and what it replaced, opening to the files labelled; an adoption that replaced a file says so; unknown for an old adopted file, reasons", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/activity", reply: { state: "history", admin: true, history: rows } },
  ]);
  renderPage(<HistoryTab admin titleId={3} />, "/title/3?tab=history");
  const got = await screen.findAllByTestId("history-row");
  expect(got.map((r) => r.querySelector('[data-testid="history-words"]')?.textContent)).toEqual(["Not cached on AD", "Upgraded", "Adopted", "Adopted", "Upgraded", "Failed"]);
  // The title tab: no filters, no Title column; the episodes under the event, compact, the full list on hover.
  expect(screen.queryByTestId("history-filters")).toBeNull();
  expect(got[0].querySelector('[data-column="title"]')).toBeNull();
  const ep = got[1].querySelector('[data-testid="history-episodes"]');
  expect(ep?.textContent).toBe("6 episodes · S01E01–S01E06");
  expect(ep?.getAttribute("title")).toBe(eps.join(" "));
  // The upgrade's columns are the new release's; the line under it says what went.
  expect(text(got[1].querySelector('[data-column="release"]'))).toBe("Gamma.S01.2160p.WEB-GRPReplaced 2 files");
  expect(got[1].querySelector('[data-column="quality"]')?.textContent).toBe("WEBDL-2160p");
  expect(got[1].querySelector('[data-column="score"]')?.textContent).toBe("2,400");
  // The files open under the row, across it, in episode order, each part labelled.
  const toggle = got[1].querySelector<HTMLButtonElement>('[data-testid="history-replaced-toggle"]');
  expect(toggle?.textContent).toBe("Show the 2 replaced files");
  expect(screen.queryByTestId("history-replaced")).toBeNull();
  await userEvent.setup().click(toggle!);
  const files = await screen.findByTestId("history-replaced");
  expect(files.closest("tr")?.hasAttribute("data-expanded")).toBe(true);
  expect(files.querySelector('[data-testid="history-replaced-head"]')?.textContent).toBe("Replaced files");
  expect([...files.querySelectorAll("li")].map(parts)).toEqual([
    ["File: S01E01.mkv", "Release: Gamma.S01.1080p.WEB-OLD", "Quality: WEBDL-1080p", "Score: 900", "Episodes: S01E01"],
    ["File: S01E02.mkv", "Release: Name not recorded", "Episodes: S01E02"],
  ]);
  expect(files.querySelector('[title="/tv/Gamma/S01E01.mkv"]')?.textContent).toBe("S01E01.mkv");
  expect(toggle?.textContent).toBe("Hide the replaced files");
  expect(got[0].querySelector('[data-testid="history-replaced-toggle"]')).toBeNull();
  // An adoption that replaced the file before: the new file's facts, the old one's under them; what Adopted means.
  const line = got[2].querySelector('[data-testid="history-replaced-line"]');
  expect(text(line)).toBe("Replaced: Gamma.S01E07.1080p.WEB-OLD · WEBDL-1080p · 600");
  expect(got[2].querySelector('[data-column="score"]')?.textContent).toBe("1,800");
  expect(got[2].querySelector('[data-testid="history-words"]')?.parentElement?.getAttribute("title")).toBe("Taken over from Sonarr/Radarr's library; Theatarr did not download it.");
  expect(got[3].querySelector('[data-column="release"]')?.textContent).toBe("File name not recorded");
  expect(got[3].querySelector('[data-column="quality"]')?.textContent).toBe("—");
  // An adopted file never scored: a dash that says so (a recorded 0 would read 0); where it came from, the instance on
  // hover.
  expect(got[3].querySelector('[data-testid="history-unscored"]')?.getAttribute("title")).toBe("Adopted file; not scored");
  const from = got[3].querySelector('[data-testid="history-reason"]');
  expect(from?.textContent).toBe("From Sonarr");
  expect(from?.getAttribute("title")).toBe("Sonarr_anime_zurg");
  expect(got[3].querySelector('[data-testid="history-replaced-line"]')).toBeNull();
  expect(text(got[4].querySelector('[data-column="release"]'))).toBe("Gamma.S02.WEB-NEWReplaced 2 files");
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
  const up = { id: "lib:20", at: "2026-11-12T15:00:00Z", kind: "link", event: "deleted", title: series, version: "tv-2160p", words: "Upgraded", tone: "ok", files: 3,
    replaced: [{ path: "/tv/Gamma/b.mkv" }, { path: "/tv/Gamma/S01E10.mkv", episodes: ["S01E10"] }, { path: "/tv/Gamma/S01E09.mkv", episodes: ["S01E09"] }] };
  mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/activity", reply: { state: "history", admin: true, history: [up] } },
  ]);
  renderPage(<HistoryTab admin titleId={3} />, "/title/3?tab=history");
  await userEvent.setup().click(await screen.findByRole("button", { name: "Show the 3 replaced files" }));
  const files = await screen.findByTestId("history-replaced");
  expect(files.textContent).not.toContain("Name not recorded");
  expect([...files.querySelectorAll("p")].map((p) => p.textContent)).toEqual(["Replaced files", "Release names not recorded."]);
  expect([...files.querySelectorAll("li")].map(parts)).toEqual([["File: S01E09.mkv", "Episodes: S01E09"], ["File: S01E10.mkv", "Episodes: S01E10"], ["File: b.mkv"]]);
});

it("shows the episode window's rows in the same table and words, without the episode it is about or a Library column", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: me }]);
  const up = { ...rows[1], id: "lib:30", episodes: ["S01E02"], replaced: [{ path: "/tv/Gamma/S01E02.mkv", release: "Gamma.S01E02.1080p.WEB-OLD", quality: "WEBDL-1080p", score: 900, episodes: ["S01E02"] }] };
  const pack = { ...grab, id: "dl:31", title: series, episodes: ["S01E01", "S01E02"] };
  renderPage(<EpisodeHistoryRows events={[up, pack] as never} episode="S01E02" />);
  const got = await screen.findAllByTestId("history-row");
  expect(got.map((r) => r.querySelector('[data-testid="history-words"]')?.textContent)).toEqual(["Upgraded", "Grabbed"]);
  expect(text(got[0].querySelector('[data-testid="history-replaced-line"]'))).toBe("Replaced: Gamma.S01E02.1080p.WEB-OLD · WEBDL-1080p · 900");
  expect(got[0].querySelector('[data-testid="history-replaced-toggle"]')?.textContent).toBe("Show the replaced file");
  expect(got[0].querySelector('[data-testid="history-episodes"]')).toBeNull();
  expect(got[1].querySelector('[data-testid="history-episodes"]')?.textContent).toBe("S01E01 S01E02");
  expect(got[0].querySelector('[data-column="library"]')).toBeNull();
  expect(got[0].querySelector('[data-column="date"]')).toBeTruthy();
});

it("keeps the last rows with a notice and Retry when a refresh fails; the full error only with no rows", async () => {
  let fail = false;
  mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/activity", reply: () => (fail ? json({ error: "boom" }, 500) : { state: "history", admin: true, history: [grab] }) },
  ]);
  renderPage(<HistoryTab admin titleId={1} />, "/title/1?tab=history");
  expect(await screen.findAllByTestId("history-row")).toHaveLength(1);
  fail = true;
  focusManager.setFocused(false);
  focusManager.setFocused(true);
  const notice = await screen.findByTestId("stale-notice");
  expect(notice.textContent).toMatch(/^Could not refresh\. Showing the list from .+\.Retry$/);
  expect(screen.getAllByTestId("history-row")).toHaveLength(1);
  expect(screen.queryByText("Could not load this")).toBeNull();
  fail = false;
  await userEvent.setup().click(within(notice).getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(screen.queryByTestId("stale-notice")).toBeNull());
  cleanup();
  mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/activity", reply: json({ error: "boom" }, 500) },
  ]);
  renderPage(<HistoryTab admin titleId={1} />, "/title/1?tab=history");
  expect(await screen.findByText("Could not load this")).toBeTruthy();
  expect(screen.queryByTestId("stale-notice")).toBeNull();
});

it("keeps the rows when Load more fails, says so, and Retry reads the next page again", async () => {
  let fail = true;
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: me },
    { method: "GET", path: "/titles/facets", reply: {} },
    {
      method: "GET",
      path: "/activity",
      reply: (r: { search: string }) =>
        !r.search.includes("cursor=c2") ? { state: "history", admin: true, history: [grab, link], next_cursor: "c2" } : fail ? json({ error: "boom" }, 500) : { state: "history", admin: true, history: [{ ...link, id: "lib:2", at: "2026-11-01T09:00:00Z" }] },
    },
  ]);
  renderPage(<HistoryTab admin />, "/activity/history");
  expect(await screen.findAllByTestId("history-row")).toHaveLength(2);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Load more" }));
  const notice = await screen.findByTestId("stale-notice");
  expect(notice.textContent).toBe("Could not load more.Retry");
  expect(screen.getAllByTestId("history-row")).toHaveLength(2);
  expect(screen.queryByText(/Could not refresh/)).toBeNull();
  fail = false;
  await user.click(within(notice).getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(screen.getAllByTestId("history-row")).toHaveLength(3));
  expect(screen.queryByTestId("stale-notice")).toBeNull();
  expect(fetch.calls("GET", "/activity").filter((r) => r.search.includes("cursor=c2"))).toHaveLength(2);
});
