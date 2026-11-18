// @vitest-environment jsdom
import { fireEvent, screen, within } from "@testing-library/react";
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

it("shows a backup no provider could be read as failed, at every width, with no Restore; a partial one names its unreadable providers on a phone too", async () => {
 mockFetch([
  { method: "GET", path: "/torrents/backups", reply: { backups: [
   { id: "failed", reason: "manual", torrents: 0, bytes: 0, providers: [{ provider: "rd", ok: false, torrents: 0 }, { provider: "tb", ok: false, torrents: 0 }] },
   { id: "partial", reason: "scheduled", torrents: 3, bytes: 30, providers: [{ provider: "rd", ok: true, torrents: 3 }, { provider: "tb", ok: false, torrents: 0 }] },
   { id: "empty", reason: "scheduled", torrents: 0, bytes: 0, providers: [{ provider: "rd", ok: true, torrents: 0 }] },
  ] } },
  { method: "GET", path: "/torrents/report", reply: { providers: [] } },
 ]);
 renderPage(<TorrentBackups />);
 const rows = await screen.findAllByTestId("torrent-backup-row");
 const [failed, partial, empty] = rows;
 // The phone line under the date and the desktop column both say it.
 expect(within(failed).getByTestId("torrent-backup-failed").textContent).toBe("Failed: no provider could be read");
 expect(within(failed).getAllByText("Failed: no provider could be read")).toHaveLength(2);
 expect(within(failed).queryByRole("button", { name: "Restore…" })).toBeNull();
 expect(within(failed).queryByTestId("torrent-backup-unreadable")).toBeNull();
 expect(within(partial).getByTestId("torrent-backup-unreadable").textContent).toContain("unreadable");
 expect(within(partial).getByTestId("torrent-backup-unreadable").className).toContain("md:hidden");
 expect(within(partial).getByRole("button", { name: "Restore…" })).toBeTruthy();
 // An account listed with zero torrents is a good backup.
 expect(within(empty).queryByTestId("torrent-backup-failed")).toBeNull();
 expect(within(empty).getByRole("button", { name: "Restore…" })).toBeTruthy();
});
