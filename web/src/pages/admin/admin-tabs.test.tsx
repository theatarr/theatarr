// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { renderPage } from "@/test/page";
import { ArrTabs } from "./arr-tabs";
import { TorrentTabs } from "./torrent-tabs";
it("keeps migration steps reachable and selects Comparison on a detail", () => {
 renderPage(<ArrTabs />, "/admin/advanced/arr/comparison/3");
 for (const [name, suffix] of [["Instances", ""], ["Comparison", "/comparison"], ["Import runs", "/imports"]]) {
  const tab = screen.getByRole("tab", { name });
  expect(tab.getAttribute("href")).toBe(`/admin/advanced/arr${suffix}`);
  expect(tab.getAttribute("aria-selected")).toBe(String(name === "Comparison"));
 }
});
it("keeps both torrent tabs reachable from an inventory detail", () => {
 renderPage(<TorrentTabs />, "/admin/torrents/inventory/abc");
 expect(screen.getByRole("tab", { name: "Provider torrents" }).getAttribute("href")).toBe("/admin/torrents");
 expect(screen.getByRole("tab", { name: "Saved inventory" }).getAttribute("href")).toBe("/admin/torrents/inventory");
 expect(screen.getByRole("tab", { name: "Saved inventory" }).getAttribute("aria-selected")).toBe("true");
});
