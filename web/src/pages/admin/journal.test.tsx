// @vitest-environment jsdom
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Link, Route, Routes } from "react-router";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { JournalList, JournalEntryPage } from "./journal";

it("links a search's download and has no verdict", async () => {
  mockFetch([
    { method: "GET", path: "/journal/71", reply: { id: "run:21", kind: "search", related: ["dl:8"] } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  renderPage(<Routes><Route path="/admin/system/journal/:id" element={<JournalEntryPage />} /></Routes>, "/admin/system/journal/71");
  expect((await screen.findByRole("link", { name: "View download" })).getAttribute("href")).toBe("/admin/system/journal/dl%3A8");
  expect(document.title).toMatch(/^Decision journal · /);
  const crumb = within(screen.getByRole("navigation", { name: "Breadcrumb" }));
  expect(crumb.getByRole("link", { name: "System" }).getAttribute("href")).toBe("/admin");
  expect(crumb.getByRole("link", { name: "Decision journal" }).getAttribute("href")).toBe("/admin/system/journal");
  expect(screen.queryByRole("button", { name: "Ok" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Wrong" })).toBeNull();
});

it("keeps the decision kind and library inline under the title for when the columns hide on phones", async () => {
  mockFetch([
    { method: "GET", path: "/journal", reply: { items: [{ id: "run:21", kind: "search", source: "router", at: "2026-11-07T10:00:00Z", version: "tv-2160p", title: { id: 3, name: "Ferrous Bay" }, summary: "Grabbed" }] } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  renderPage(<JournalList />, "/admin/system/journal");
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
  renderPage(<Routes><Route path="/admin/system/journal/:id" element={<JournalEntryPage />} /></Routes>, "/admin/system/journal/dl:2");
  expect((await screen.findByText(/^grabbed Cinderhook/)).className).toContain("[overflow-wrap:anywhere]");
});

it("numbers a runner-up from its place when the entry records no rank", async () => {
  mockFetch([
    { method: "GET", path: "/journal/dl%3A11", reply: { id: "dl:11", kind: "grab", reasoning: { basis: "search", results: 2, accepted: 2,
      chosen: { rank: 0, release: "Bees.2024.1080p-ONE", quality: "WEBDL-1080p", score: 900, accepted: true },
      runners_up: [{ rank: 0, release: "Bees.2024.PROPER.1080p-ONE", quality: "WEBDL-1080p", score: 700, accepted: true }] } } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  renderPage(<Routes><Route path="/admin/system/journal/:id" element={<JournalEntryPage />} /></Routes>, "/admin/system/journal/dl:11");
  expect(await screen.findByText(/^#2 Bees\.2024\.PROPER/)).toBeTruthy();
  expect(screen.queryByText(/^#0 /)).toBeNull();
});

it("says how entries appear when the journal is empty, and keeps the short text under a filter", async () => {
  mockFetch([
    { method: "GET", path: "/journal", reply: { items: [] } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  const first = renderPage(<JournalList />, "/admin/system/journal");
  expect(await screen.findByText(/No entries yet\. One appears each time a library judges a release/)).toBeTruthy();
  first.unmount();
  renderPage(<JournalList />, "/admin/system/journal?kind=grab");
  expect(await screen.findByText("No entries.")).toBeTruthy();
});

it("words kinds, sources and flags, keeping the codes as tooltips", async () => {
  mockFetch([
    { method: "GET", path: "/journal", reply: { items: [{ id: "lnk:4", kind: "research", source: "theatarr", at: "2026-11-07T10:00:00Z", title: { id: 3, name: "Ferrous Bay" }, summary: "s", flags: [{ code: "repeated_failures", severity: "warn", reason: "3 failures" }] }] } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  renderPage(<JournalList />, "/admin/system/journal");
  const row = await screen.findByTestId("journal-row");
  expect(within(row).getAllByText("Search after a failure")[0].getAttribute("title")).toBe("research");
  expect(within(row).getByText("Theatarr").getAttribute("title")).toBe("theatarr");
  expect(within(row).getAllByText("Repeated failures")[0].getAttribute("title")).toBe("repeated_failures");
  expect(within(row).queryByText("repeated_failures")).toBeNull();
  expect(screen.getByRole("option", { name: "All kinds" })).toBeTruthy();
});

it("keeps the title box in step with the URL when a link drops the filter", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/journal", reply: { items: [] } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  renderPage(<><Link to="/admin/system/journal">Decision journal</Link><JournalList /></>, "/admin/system/journal?title=Anora");
  const box = (await screen.findByLabelText("Title")) as HTMLInputElement;
  expect(box.value).toBe("Anora");
  await userEvent.setup().click(screen.getByRole("link", { name: "Decision journal" }));
  await screen.findByText(/No entries yet/);
  expect(box.value).toBe("");
  expect(fetch.calls("GET", "/journal").at(-1)!.search).not.toContain("title=");
});

it("starts at the newest page when the filter comes back to one paged before", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/journal", reply: { items: [{ id: "dl:1", kind: "grab", source: "theatarr", at: "2026-11-07T10:00:00Z", title: { id: 3, name: "Ferrous Bay" }, summary: "s" }], next_cursor: "c1" } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  renderPage(<JournalList />, "/admin/system/journal");
  await screen.findByTestId("journal-row");
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Older" }));
  await user.click(screen.getByTestId("journal-suspect"));
  await user.click(screen.getByRole("button", { name: "All" }));
  await screen.findByTestId("journal-row");
  expect(fetch.calls("GET", "/journal").at(-1)!.search).not.toContain("cursor=");
});

it("lists each line by its label with the file paths as the summary's tooltip, and the entry page names the paths", async () => {
  mockFetch([
    { method: "GET", path: "/journal", reply: { items: [{ id: "lib:6", kind: "link", label: "Removed", source: "library", at: "2026-11-15T10:00:00Z",
      version: "tv-2160p", title: { id: 3, name: "Lanterns" }, summary: "Removed Lanterns - S01E08.mkv · copy removed by mgr · moved to the trash",
      paths: ["/tv/Lanterns (2026)/Season 01/Lanterns - S01E08.mkv"] }] } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  const page = renderPage(<JournalList />, "/admin/system/journal");
  const summary = await screen.findByTestId("journal-row-summary");
  expect(summary.textContent).toBe("Removed Lanterns - S01E08.mkv · copy removed by mgr · moved to the trash");
  expect(summary.getAttribute("title")).toBe("/tv/Lanterns (2026)/Season 01/Lanterns - S01E08.mkv");
  expect(within(screen.getByTestId("journal-row")).getAllByText("Removed").length).toBeGreaterThan(0);
  page.unmount();
  mockFetch([
    { method: "GET", path: "/journal/lib%3A6", reply: { id: "lib:6", kind: "link", label: "Removed", summary: "Removed Lanterns - S01E08.mkv",
      paths: ["/tv/Lanterns (2026)/Season 01/Lanterns - S01E08.mkv"],
      after: [{ at: "2026-11-15T10:00:01Z", kind: "action", text: "Removed by mgr", ref: "act:4" }] } },
    { method: "GET", path: "/status", reply: {} },
  ]);
  renderPage(<Routes><Route path="/admin/system/journal/:id" element={<JournalEntryPage />} /></Routes>, "/admin/system/journal/lib:6");
  expect((await screen.findByTestId("journal-paths")).textContent).toBe("/tv/Lanterns (2026)/Season 01/Lanterns - S01E08.mkv");
  expect(screen.getByRole("link", { name: "Removed by mgr" }).getAttribute("href")).toBe("/admin/system/journal/act%3A4");
});
