// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useLocation } from "react-router";
import { CalendarPageView } from "./calendar";
import { mockFetch, renderPage } from "@/test/page";
import type { Schemas } from "@/api/client";
import { monthDays, ymd } from "@/lib/calendar";

type Entry = Schemas["CalendarEntry"];

const day = (n: number) => {
  const d = new Date(new Date().getFullYear(), new Date().getMonth(), 1);
  return ymd(new Date(d.getFullYear(), d.getMonth(), n));
};
const title = (id: number, name: string, kind: string) => ({ id, name, kind, anime: false, poster: "" }) as Entry["title"];
// Day 1 is never in the future, day 28 only on the 28th: the missing episode is past, the movie is out at home.
const items: Entry[] = [
  { date: day(1), kind: "episode", type: "episode", title: title(3, "Gamma", "series"), episode_id: 41, season: 1, number: 2, monitored: true, available: false, acquiring: false },
  { date: day(1), kind: "movie", type: "digital", title: title(1, "Alpha", "movie"), monitored: true, available: true, acquiring: false },
];

// Today is the 1st of this month at noon unless a test says otherwise: the agenda of this month starts at today.
const real = new Date();
beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"], now: new Date(real.getFullYear(), real.getMonth(), 1, 12) });
  window.localStorage.setItem("theatarr.calendar.view", "agenda");
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  window.localStorage.clear();
});

function LocationProbe() {
  const loc = useLocation();
  return <span data-testid="location">{loc.pathname + loc.search}</span>;
}

const routes = (permissions: string[]) => [
  { method: "GET", path: "/auth/me", reply: { id: 1, permissions } },
  { method: "GET", path: "/calendar", reply: { from: day(1), to: day(28), items } },
];

// T3: text that matters is not cut on a phone: no line clamp or ellipsis below the sm width (desktop keeps it).
const cutOnPhone = (el: Element) => el.className.split(/\s+/).some((c) => c === "truncate" || /^line-clamp-\d/.test(c));

it("wraps an agenda line's title, episode and time in full on a phone", async () => {
  mockFetch(routes(["manage", "see_files"]));
  renderPage(<CalendarPageView />, "/calendar");
  await screen.findByText("Gamma");
  const lines = screen.getAllByTestId("agenda-item").flatMap((a) => [...a.querySelectorAll("div")]);
  expect(lines.length).toBeGreaterThan(0);
  expect(lines.filter(cutOnPhone)).toEqual([]);
});

it("links an episode to its episode window, colours by status and shows the legend", async () => {
  mockFetch(routes(["manage", "see_files"]));
  renderPage(<CalendarPageView />, "/calendar");
  const gamma = (await screen.findByText("Gamma")).closest("a")!;
  expect(gamma.getAttribute("href")).toBe("/title/3?tab=episodes&episode=41");
  expect(gamma.dataset.tone).toBe("bad");
  const alpha = screen.getByText("Alpha").closest("a")!;
  expect(alpha.getAttribute("href")).toBe("/title/1");
  expect(alpha.dataset.tone).toBe("ok");
  expect(screen.getByTestId("cal-legend").textContent).toBe("AvailableGetting readyMissingUpcoming or in cinemasNot monitored");
});

it("gives an account without see files the household words, no red missing", async () => {
  mockFetch(routes([]));
  renderPage(<CalendarPageView />, "/calendar");
  const gamma = (await screen.findByText("Gamma")).closest("a")!;
  expect(gamma.dataset.tone).toBe("neutral");
  expect(screen.getByText("Not available")).toBeTruthy();
  expect(screen.queryByText("Missing")).toBeNull();
  expect(screen.getByTestId("cal-legend").textContent).toBe("AvailableGetting ready, upcoming or not available yet");
});

it("counts the month shown in both views, not the grid's days of the neighbouring months", async () => {
  const user = userEvent.setup();
  const now = new Date();
  const grid = monthDays(now.getFullYear(), now.getMonth()).map(ymd);
  const before = day(0); // the last day of the month before
  const after = ymd(new Date(now.getFullYear(), now.getMonth() + 1, 1));
  const ep = (id: number, date: string, name: string): Entry => ({ ...items[0], episode_id: id, date, title: title(id, name, "series") });
  const reply = { from: grid[0], to: grid.at(-1), items: [ep(51, before, "Before"), ep(52, day(10), "Inside"), ep(53, after, "After")] };
  mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["see_files"] } }, { method: "GET", path: "/calendar", reply }]);
  renderPage(<CalendarPageView />, "/calendar");
  expect(await screen.findByText("Inside")).toBeTruthy();
  const series = () => screen.getByRole("button", { name: /^Series/ }).textContent;
  expect(series()).toBe("Series1");
  await user.click(screen.getByRole("button", { name: "Month" }));
  // The grid still shows a neighbouring day's entry when that day is on it.
  const shown = [before, after].filter((d) => grid.includes(d));
  for (const d of shown) expect(await screen.findByText(d === before ? "Before" : "After")).toBeTruthy();
  expect(series()).toBe("Series1");
});

it("shows one movie's releases of a day as one entry, with its status words", async () => {
  const odyssey = { date: day(1), kind: "movie", title: title(7, "The Odyssey", "movie"), monitored: true, available: true, partial: true, acquiring: false };
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["manage", "see_files"] } },
    { method: "GET", path: "/calendar", reply: { from: day(1), to: day(28), items: [{ ...odyssey, type: "digital" }, { ...odyssey, type: "physical" }] } },
  ]);
  renderPage(<CalendarPageView />, "/calendar");
  await screen.findByText("The Odyssey");
  const rows = screen.getAllByTestId("agenda-item");
  expect(rows).toHaveLength(1);
  expect(rows[0].textContent).toContain("Digital + Physical");
  expect(rows[0].textContent).toContain("Partly available");
});

const nov = (n: number) => `2026-11-${String(n).padStart(2, "0")}`;
const ep = (id: number, date: string, name: string, kind = "series", monitored = true): Entry =>
  ({ ...items[0], episode_id: id, date, monitored, title: title(id, name, kind) });

it("starts this month's agenda at today, with Show earlier, and Today goes back there", async () => {
  vi.setSystemTime(new Date(2026, 10, 14, 12));
  const user = userEvent.setup();
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["see_files"] } },
    { method: "GET", path: "/calendar", reply: { from: nov(1), to: nov(30), items: [ep(1, nov(3), "Early"), ep(2, nov(14), "Now"), ep(3, nov(20), "Later")] } },
  ]);
  renderPage(<CalendarPageView />, "/calendar");
  expect(await screen.findByText("Now")).toBeTruthy();
  expect(screen.getByText("Later")).toBeTruthy();
  expect(screen.queryByText("Early")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Show earlier (1 day)" }));
  expect(screen.getByText("Early")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Today" }));
  expect(screen.queryByText("Early")).toBeNull();
  // Another month is shown whole.
  await user.click(screen.getByRole("button", { name: "Previous month" }));
  expect(screen.queryByRole("button", { name: /Show earlier/ })).toBeNull();
});

it("keeps the month, the toggles and Monitored only in the URL, and shows titles not monitored greyed", async () => {
  vi.setSystemTime(new Date(2026, 10, 14, 12));
  const user = userEvent.setup();
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["see_files"] } },
    { method: "GET", path: "/calendar", reply: { from: "2026-11-30", to: "2027-01-01", items: [
      { ...items[1], date: "2026-12-05", title: title(1, "Alpha", "movie") },
      { ...items[1], date: "2026-12-06", title: title(2, "Quiet", "movie"), monitored: false, available: false },
      ep(5, "2026-12-07", "Gamma")] } },
  ]);
  renderPage(<><CalendarPageView /><LocationProbe /></>, "/calendar?month=2026-12&types=movie");
  expect((await screen.findByTestId("cal-title")).textContent).toBe("December 2026");
  expect(await screen.findByText("Alpha")).toBeTruthy();
  // Titles no library monitors are asked for and shown greyed, as on Home's Upcoming row.
  // (The app has the account before the page renders; here the first request may go before it arrives.)
  await waitFor(() => expect(fetch.calls("GET", "/calendar").at(-1)?.search).toBe("?from=2026-11-30&to=2027-01-01&unmonitored=1"));
  expect(screen.getByText("Quiet").closest("a")!.className).toContain("opacity-60");
  expect(screen.queryByText("Gamma")).toBeNull();
  expect(screen.getByRole("button", { name: /^Series/ }).getAttribute("aria-pressed")).toBe("false");
  await user.click(screen.getByLabelText("Monitored only"));
  expect(screen.queryByText("Quiet")).toBeNull();
  expect(screen.getByTestId("location").textContent).toBe("/calendar?month=2026-12&types=movie&monitored=1");
  await user.click(screen.getByRole("button", { name: /^Series/ }));
  expect(screen.getByText("Gamma")).toBeTruthy();
  expect(screen.getByTestId("location").textContent).toBe("/calendar?month=2026-12&types=movie%2Cseries&monitored=1");
  await user.click(screen.getByRole("button", { name: "Next month" }));
  expect(screen.getByTestId("location").textContent).toBe("/calendar?month=2027-01&types=movie%2Cseries&monitored=1");
  await user.click(screen.getByRole("button", { name: "Today" }));
  expect(screen.getByTestId("location").textContent).toBe("/calendar?types=movie%2Cseries&monitored=1");
});

it("says the filters hide this month's entries instead of claiming the month is empty", async () => {
  vi.setSystemTime(new Date(2026, 10, 14, 12));
  const user = userEvent.setup();
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: [] } },
    { method: "GET", path: "/calendar", reply: { from: "2026-11-30", to: "2027-01-01", items: [{ ...items[1], date: "2026-12-05" }, ep(5, "2026-12-07", "Gamma")] } },
  ]);
  renderPage(<CalendarPageView />, "/calendar?month=2026-12&types=anime");
  expect(await screen.findByText("No entries match these filters this month.", { exact: false })).toBeTruthy();
  expect(screen.queryByText("Nothing is released or airs in this month.")).toBeNull();
  // A household account follows Home's Upcoming row too: titles not monitored are asked for, with Monitored only.
  expect(screen.getByLabelText("Monitored only")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Show all" }));
  expect(screen.getByText("Gamma")).toBeTruthy();
});

it("brings a busy day's list into view and focus on +N more", async () => {
  vi.setSystemTime(new Date(2026, 10, 14, 12));
  window.localStorage.setItem("theatarr.calendar.view", "month");
  const user = userEvent.setup();
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["see_files"] } },
    { method: "GET", path: "/calendar", reply: { from: nov(1), to: nov(30), items: [1, 2, 3, 4].map((n) => ep(n, nov(17), `Show ${n}`)) } },
  ]);
  renderPage(<CalendarPageView />, "/calendar");
  await user.click(await screen.findByRole("button", { name: "+1 more" }));
  const panel = screen.getByTestId("cal-day-panel");
  expect(document.activeElement).toBe(panel);
  expect(screen.getAllByTestId("agenda-item")).toHaveLength(4);
});

it("folds a series' consecutive episodes of a day into one month slot; the day's list keeps each episode", async () => {
  vi.setSystemTime(new Date(2026, 10, 20, 12));
  window.localStorage.setItem("theatarr.calendar.view", "month");
  const user = userEvent.setup();
  const drop = [1, 2, 3].map((n): Entry => ({ ...ep(60 + n, nov(17), "Drop"), title: title(6, "Drop", "series"), season: 2, number: n, available: n === 1 }));
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["see_files"] } },
    { method: "GET", path: "/calendar", reply: { from: nov(1), to: nov(30), items: [...drop, ep(71, nov(17), "Other"), ep(72, nov(17), "Third")] } },
  ]);
  renderPage(<CalendarPageView />, "/calendar");
  const chip = (await screen.findByText("S02E01–E03")).closest("a")!;
  expect(chip.getAttribute("href")).toBe("/title/6?tab=episodes&episode=61");
  // One slot is still missing two episodes: the folded chip says so.
  expect(chip.dataset.tone).toBe("bad");
  expect(chip.getAttribute("title")).toContain("S02E01 · Available");
  expect(chip.getAttribute("title")).toContain("S02E03 · Missing");
  // Three slots: the fold, Other and Third; no "+N more".
  expect(screen.getAllByTestId("cal-event")).toHaveLength(3);
  expect(screen.queryByRole("button", { name: /more$/ })).toBeNull();
  await user.click(screen.getByRole("button", { name: /: 5 items$/ }));
  const list = screen.getAllByTestId("agenda-item");
  expect(list).toHaveLength(5);
  expect(list.slice(0, 3).map((a) => a.getAttribute("href"))).toEqual(["/title/6?tab=episodes&episode=61", "/title/6?tab=episodes&episode=62", "/title/6?tab=episodes&episode=63"]);
  expect(list[0].dataset.tone).toBe("ok");
  expect(list[1].dataset.tone).toBe("bad");
});

it("folds a series' same-day episodes of one season into one Agenda line, as Month does", async () => {
  const pair = [5, 6].map((n): Entry => ({ date: day(1), kind: "episode", type: "episode", title: title(8, "Hacks", "series"), episode_id: 80 + n, season: 3, number: n, episode: `Part ${n}`, monitored: true, available: n === 5, acquiring: false }));
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["see_files"] } },
    { method: "GET", path: "/calendar", reply: { from: day(1), to: day(28), items: [...pair, items[1]] } },
  ]);
  renderPage(<CalendarPageView />, "/calendar");
  const line = (await screen.findByText("Hacks")).closest("a")!;
  expect(screen.getAllByTestId("agenda-item")).toHaveLength(2);
  expect(line.textContent).toContain("S03E05–E06");
  expect(line.getAttribute("href")).toBe("/title/8?tab=episodes&episode=85");
  // The line's colour is the one most in need of a look, as the month's chip.
  expect(line.dataset.tone).toBe("bad");
});

it("keeps a season's episodes of a day in one slot when Monitored only leaves a gap, with one dot on the phone, and a time only when they share it", async () => {
  vi.setSystemTime(new Date(2026, 10, 20, 12));
  window.localStorage.setItem("theatarr.calendar.view", "month");
  const drop = [1, 2, 3].map((n): Entry => ({ ...ep(80 + n, nov(17), "Gap"), title: title(8, "Gap", "series"), season: 1, number: n, monitored: n !== 2,
    at: `2026-11-17T${n === 3 ? "21" : "20"}:00:00Z` }));
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["see_files"] } },
    { method: "GET", path: "/calendar", reply: { from: nov(1), to: nov(30), items: [...drop, ep(91, nov(17), "Other"), ep(92, nov(17), "Third")] } },
  ]);
  renderPage(<CalendarPageView />, "/calendar?monitored=1");
  const chip = (await screen.findByText("S01E01, E03")).closest("a")!;
  expect(screen.getAllByTestId("cal-event")).toHaveLength(3);
  expect(screen.queryByRole("button", { name: /more$/ })).toBeNull();
  // E01 and E03 air at different times: no time on the chip, each in the tooltip.
  expect(chip.querySelector(".theatarr-cal-event-time")).toBeNull();
  const lines = chip.getAttribute("title")!.split("\n");
  expect(lines[1]).toMatch(/^S01E01 · .+ · Missing$/);
  expect(lines[2]).toMatch(/^S01E03 · .+ · Missing$/);
  expect(lines[1]).not.toBe(lines[2].replace("E03", "E01"));
  // The phone dots follow the same slots: three, not four.
  const cell = document.querySelector('[data-day="2026-11-17"]')!;
  expect(cell.querySelectorAll(".theatarr-cal-dot")).toHaveLength(3);
});

it("does not say no entries match while earlier ones of the month are behind Show earlier", async () => {
  vi.setSystemTime(new Date(2026, 10, 14, 12));
  const movie = { ...items[1], date: nov(3), title: title(1, "Alpha", "movie") };
  const routes = (later: Entry[]) => [
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["see_files"] } },
    { method: "GET", path: "/calendar", reply: { from: nov(1), to: nov(30), items: [movie, ...later] } },
  ];
  // Nothing later this month at all.
  mockFetch(routes([]));
  const first = renderPage(<CalendarPageView />, "/calendar?types=movie");
  expect(await screen.findByText("Nothing more this month.")).toBeTruthy();
  expect(screen.queryByText(/No entries match/)).toBeNull();
  expect(screen.getByRole("button", { name: "Show earlier (1 day)" })).toBeTruthy();
  first.unmount();
  // A later entry the toggles hide.
  mockFetch(routes([ep(9, nov(20), "Later")]));
  renderPage(<CalendarPageView />, "/calendar?types=movie");
  expect(await screen.findByText("No more entries this month match these filters.", { exact: false })).toBeTruthy();
  expect(screen.queryByText(/No entries match/)).toBeNull();
});

it("shows a household account titles not monitored greyed, with its own words", async () => {
  vi.setSystemTime(new Date(2026, 10, 14, 12));
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: [] } },
    { method: "GET", path: "/calendar", reply: { from: nov(1), to: nov(30), items: [ep(4, nov(18), "Quiet", "series", false)] } },
  ]);
  renderPage(<CalendarPageView />, "/calendar");
  const quiet = (await screen.findByText("Quiet")).closest("a")!;
  expect(fetch.calls("GET", "/calendar").every((c) => c.search.endsWith("&unmonitored=1"))).toBe(true);
  expect(quiet.className).toContain("opacity-60");
  expect(quiet.textContent).not.toContain("Not monitored");
});

it("says a day's entries are hidden by the filters instead of nothing on this day", async () => {
  vi.setSystemTime(new Date(2026, 10, 14, 12));
  window.localStorage.setItem("theatarr.calendar.view", "month");
  const user = userEvent.setup();
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["see_files"] } },
    { method: "GET", path: "/calendar", reply: { from: nov(1), to: nov(30), items: [ep(4, nov(18), "Quiet", "series", false)] } },
  ]);
  renderPage(<CalendarPageView />, "/calendar?monitored=1");
  await user.click(await screen.findByRole("button", { name: /^Wednesday, November 18/ }));
  expect(screen.getByText("This day's entries are hidden by the filters.", { exact: false })).toBeTruthy();
  expect(screen.queryByText("Nothing on this day.")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Show all" }));
  expect(screen.getAllByText("Quiet").length).toBeGreaterThan(0);
  await user.click(screen.getByRole("button", { name: /^Thursday, November 19/ }));
  expect(screen.getByText("Nothing on this day.")).toBeTruthy();
});

it("starts the month's weeks on the browser language's first day and fetches the whole grid", async () => {
  vi.spyOn(navigator, "language", "get").mockReturnValue("de-DE");
  window.localStorage.setItem("theatarr.calendar.view", "month");
  const fetch = mockFetch(routes(["see_files"]));
  const { container } = renderPage(<CalendarPageView />, "/calendar");
  await screen.findByText("Gamma");
  expect([...container.querySelectorAll(".theatarr-cal-head .hidden")].map((h) => h.textContent)).toEqual(["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"]);
  const now = new Date();
  const grid = monthDays(now.getFullYear(), now.getMonth(), 1).map(ymd);
  expect(grid.length % 7).toBe(0);
  const q = new URLSearchParams(fetch.calls("GET", "/calendar")[0].search);
  expect(q.get("from")! <= grid[0] && q.get("to")! >= grid.at(-1)!).toBe(true);
});
