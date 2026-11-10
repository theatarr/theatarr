// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { WantedTabs } from "./wanted-tabs";
import { ManualImportPage } from "@/pages/admin/manual-import";
import { mockFetch, renderPage } from "@/test/page";

const href = (name: string) => screen.getByRole("tab", { name }).getAttribute("href");

it("keeps the library filter on Missing and Cutoff unmet, not on Manual import", () => {
  renderPage(<WantedTabs active="cutoff" query="?version=movie-web" />, "/wanted/cutoff?version=movie-web");
  expect(href("Missing")).toBe("/wanted?version=movie-web");
  expect(href("Cutoff unmet")).toBe("/wanted/cutoff?version=movie-web");
  expect(href("Manual import")).toBe("/wanted/manual-import");
  expect(screen.getByRole("tab", { name: "Cutoff unmet" }).getAttribute("aria-selected")).toBe("true");
});

it("shows Manual import as the selected tab of Wanted", async () => {
  mockFetch([{ method: "GET", path: "/policy", reply: { versions: [] } }]);
  renderPage(<ManualImportPage />, "/wanted/manual-import");
  expect((await screen.findByRole("tab", { name: "Manual import" })).getAttribute("aria-selected")).toBe("true");
  expect(href("Missing")).toBe("/wanted");
  expect(screen.getByRole("heading", { name: "Wanted" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "List files" })).toBeTruthy();
});
