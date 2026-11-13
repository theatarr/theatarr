// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { renderPage } from "@/test/page";
import { ArrTabs } from "./arr-tabs";
import { TorrentTabs } from "./torrent-tabs";
it("keeps the migration's Instances tab, without Import runs or Comparison", () => {
 renderPage(<ArrTabs />, "/admin/advanced/arr");
 const tab = screen.getByRole("tab", { name: "Instances" });
 expect(tab.getAttribute("href")).toBe("/admin/advanced/arr");
 expect(tab.getAttribute("aria-selected")).toBe("true");
 expect(screen.queryByRole("tab", { name: "Import runs" })).toBeNull();
 expect(screen.queryByRole("tab", { name: "Comparison" })).toBeNull();
});
it("keeps both torrent tabs reachable from an inventory detail", () => {
 renderPage(<TorrentTabs />, "/admin/torrents/inventory/abc");
 expect(screen.getByRole("tab", { name: "Provider torrents" }).getAttribute("href")).toBe("/admin/torrents");
 expect(screen.getByRole("tab", { name: "Saved inventory" }).getAttribute("href")).toBe("/admin/torrents/inventory");
 expect(screen.getByRole("tab", { name: "Saved inventory" }).getAttribute("aria-selected")).toBe("true");
});
