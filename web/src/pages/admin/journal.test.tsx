// @vitest-environment jsdom
import { screen, within } from "@testing-library/react";
import { Route, Routes } from "react-router";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { JournalList, JournalEntryPage } from "./journal";

it("explains a run verdict and links its download", async () => {
  mockFetch([
    { method: "GET", path: "/journal/71", reply: { id: "run:21", kind: "search", related: ["dl:8"] } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  renderPage(<Routes><Route path="/activity/journal/:id" element={<JournalEntryPage />} /></Routes>, "/activity/journal/71");
  expect(await screen.findByText("Records your verdict only. To block the release, use its download in Activity.")).toBeTruthy();
  expect(screen.queryByText(/Wrong on an active version/)).toBeNull();
  expect(screen.getByRole("link", { name: "View download" }).getAttribute("href")).toBe("/activity/journal/dl%3A8");
  const crumb = within(screen.getByRole("navigation", { name: "Breadcrumb" }));
  expect(crumb.getByRole("link", { name: "Activity" }).getAttribute("href")).toBe("/activity");
  expect(crumb.getByRole("link", { name: "Decision journal" }).getAttribute("href")).toBe("/activity/journal");
});

it("keeps the decision kind and library inline under the title for when the columns hide on phones", async () => {
  mockFetch([
    { method: "GET", path: "/journal", reply: { items: [{ id: "run:21", kind: "search", source: "router", at: "2026-11-07T10:00:00Z", version: "tv-2160p", title: { id: 3, name: "Ferrous Bay" }, summary: "Grabbed" }] } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  renderPage(<JournalList />, "/activity/journal");
  const inline = await screen.findByTestId("journal-inline");
  expect(within(inline).getByText("search")).toBeTruthy();
  expect(within(inline).getByText("tv-2160p")).toBeTruthy();
});

it("wraps a long release name in the summary so a phone page doesn't widen", async () => {
  mockFetch([
    { method: "GET", path: "/journal/dl%3A2", reply: { id: "dl:2", kind: "grab", summary: "grabbed Cinderhook.2024.2160p.UHD.BluRay.REMUX.DV.HDR10.HEVC.TrueHD.Atmos.7.1-CINDRA" } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  renderPage(<Routes><Route path="/activity/journal/:id" element={<JournalEntryPage />} /></Routes>, "/activity/journal/dl:2");
  expect((await screen.findByText(/^grabbed Cinderhook/)).className).toContain("[overflow-wrap:anywhere]");
});

it("numbers a runner-up from its place when the entry records no rank", async () => {
  mockFetch([
    { method: "GET", path: "/journal/sg%3A11", reply: { id: "sg:11", kind: "shadow", reasoning: { basis: "shadow", results: 2, accepted: 2,
      chosen: { rank: 0, release: "Bees.2024.1080p-ONE", quality: "WEBDL-1080p", score: 900, accepted: true },
      runners_up: [{ rank: 0, release: "Bees.2024.PROPER.1080p-ONE", quality: "WEBDL-1080p", score: 700, accepted: true }] } } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  renderPage(<Routes><Route path="/activity/journal/:id" element={<JournalEntryPage />} /></Routes>, "/activity/journal/sg:11");
  expect(await screen.findByText(/^#2 Bees\.2024\.PROPER/)).toBeTruthy();
  expect(screen.queryByText(/^#0 /)).toBeNull();
});
