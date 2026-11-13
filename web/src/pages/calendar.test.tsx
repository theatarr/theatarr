// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it } from "vitest";
import { CalendarPageView } from "./calendar";
import { mockFetch, renderPage } from "@/test/page";
import type { Schemas } from "@/api/client";
import { monthDays, ymd } from "@/lib/calendar";

type Entry = Schemas["CalendarEntry"];

const day = (n: number) => {
  const d = new Date();
  return ymd(new Date(d.getFullYear(), d.getMonth(), n));
};
const title = (id: number, name: string, kind: string) => ({ id, name, kind, anime: false, poster: "" }) as Entry["title"];
// Day 1 is never in the future, day 28 only on the 28th: the missing episode is past, the movie is out at home.
const items: Entry[] = [
  { date: day(1), kind: "episode", type: "episode", title: title(3, "Gamma", "series"), episode_id: 41, season: 1, number: 2, monitored: true, available: false, acquiring: false },
  { date: day(1), kind: "movie", type: "digital", title: title(1, "Alpha", "movie"), monitored: true, available: true, acquiring: false },
];

beforeEach(() => window.localStorage.setItem("theatarr.calendar.view", "agenda"));
afterEach(() => window.localStorage.clear());

const routes = (permissions: string[]) => [
  { method: "GET", path: "/auth/me", reply: { id: 1, permissions } },
  { method: "GET", path: "/calendar", reply: { from: day(1), to: day(28), items } },
];

it("links an episode to its episode window, colours by status and shows the legend", async () => {
  mockFetch(routes(["manage", "see_files"]));
  renderPage(<CalendarPageView />, "/calendar");
  const gamma = (await screen.findByText("Gamma")).closest("a")!;
  expect(gamma.getAttribute("href")).toBe("/title/3?tab=episodes&episode=41");
  expect(gamma.dataset.tone).toBe("bad");
  const alpha = screen.getByText("Alpha").closest("a")!;
  expect(alpha.getAttribute("href")).toBe("/title/1");
  expect(alpha.dataset.tone).toBe("ok");
  expect(screen.getByTestId("cal-legend").textContent).toBe("DownloadedDownloadingMissingUpcoming or in cinemasNot monitored");
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
