// @vitest-environment jsdom
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Link, Route, Routes } from "react-router";
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
  expect(document.title).toMatch(/^Decision journal · /);
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
  expect(within(inline).getByText("Search")).toBeTruthy();
  // The library by its name, the key as its mark's data-vkey.
  expect(within(inline).getByText("4K").closest("[data-vkey]")?.getAttribute("data-vkey")).toBe("tv-2160p");
  expect(within(inline).queryByText("tv-2160p")).toBeNull();
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
    { method: "GET", path: "/journal/dl%3A11", reply: { id: "dl:11", kind: "grab", reasoning: { basis: "search", results: 2, accepted: 2,
      chosen: { rank: 0, release: "Bees.2024.1080p-ONE", quality: "WEBDL-1080p", score: 900, accepted: true },
      runners_up: [{ rank: 0, release: "Bees.2024.PROPER.1080p-ONE", quality: "WEBDL-1080p", score: 700, accepted: true }] } } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  renderPage(<Routes><Route path="/activity/journal/:id" element={<JournalEntryPage />} /></Routes>, "/activity/journal/dl:11");
  expect(await screen.findByText(/^#2 Bees\.2024\.PROPER/)).toBeTruthy();
  expect(screen.queryByText(/^#0 /)).toBeNull();
});

it("says how entries appear when the journal is empty, and keeps the short text under a filter", async () => {
  mockFetch([
    { method: "GET", path: "/journal", reply: { items: [] } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  const first = renderPage(<JournalList />, "/activity/journal");
  expect(await screen.findByText(/No entries yet\. One appears each time a Live library judges a release/)).toBeTruthy();
  first.unmount();
  renderPage(<JournalList />, "/activity/journal?kind=grab");
  expect(await screen.findByText("No entries.")).toBeTruthy();
});

it("words kinds, sources and flags, keeping the codes as tooltips", async () => {
  mockFetch([
    { method: "GET", path: "/journal", reply: { items: [{ id: "lnk:4", kind: "research", source: "theatarr", at: "2026-11-07T10:00:00Z", title: { id: 3, name: "Ferrous Bay" }, summary: "s", flags: [{ code: "dead_link", severity: "warn", reason: "gone" }] }] } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  renderPage(<JournalList />, "/activity/journal");
  const row = await screen.findByTestId("journal-row");
  expect(within(row).getAllByText("Search after a failure")[0].getAttribute("title")).toBe("research");
  expect(within(row).getByText("Theatarr's search").getAttribute("title")).toBe("theatarr");
  expect(within(row).getAllByText("Dead link")[0].getAttribute("title")).toBe("dead_link");
  expect(within(row).queryByText("dead_link")).toBeNull();
  expect(screen.getByRole("option", { name: "All kinds" })).toBeTruthy();
});

it("keeps the title box in step with the URL when a link drops the filter", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/journal", reply: { items: [] } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  renderPage(<><Link to="/activity/journal">Decision journal</Link><JournalList /></>, "/activity/journal?title=Anora");
  const box = (await screen.findByLabelText("Title")) as HTMLInputElement;
  expect(box.value).toBe("Anora");
  await userEvent.setup().click(screen.getByRole("link", { name: "Decision journal" }));
  await screen.findByText(/No entries yet/);
  expect(box.value).toBe("");
  expect(fetch.calls("GET", "/journal").at(-1)!.search).not.toContain("title=");
});
