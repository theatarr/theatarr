// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { renderPage } from "@/test/page";
import { TorrentTabs } from "./torrent-tabs";
it("keeps both torrent tabs reachable from an inventory detail", () => {
 renderPage(<TorrentTabs />, "/admin/torrents/inventory/abc");
 expect(screen.getByRole("tab", { name: "Provider torrents" }).getAttribute("href")).toBe("/admin/torrents");
 expect(screen.getByRole("tab", { name: "Saved inventory" }).getAttribute("href")).toBe("/admin/torrents/inventory");
 expect(screen.getByRole("tab", { name: "Saved inventory" }).getAttribute("aria-selected")).toBe("true");
});
