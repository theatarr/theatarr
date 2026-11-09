// @vitest-environment jsdom
import { fireEvent, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { TorrentBackups } from "./torrent-backups";

it("links completed restore guidance to Torrents", async () => {
 mockFetch([
  { method: "GET", path: "/torrents/backups", reply: { backups: [{ id: "backup", providers: [], torrents: 1 }] } },
  { method: "GET", path: "/torrents/report", reply: { providers: [] } },
  { method: "POST", path: "/torrents/restore/preview", reply: { items: [], readd: 1 } },
  { method: "POST", path: "/torrents/restore", reply: { job: 12 } },
 ]);
 renderPage(<TorrentBackups />);
 fireEvent.click(await screen.findByRole("button", { name: "Restore…" }));
 fireEvent.click(await screen.findByRole("button", { name: "Restore 1" }));
 expect(await screen.findByText("Restore queued as job #12.")).toBeTruthy();
 expect(screen.getByRole("link", { name: "Torrents" }).getAttribute("href")).toBe("/admin/torrents");
 expect(screen.getByText(/shows each re-add as it happens/)).toBeTruthy();
});
