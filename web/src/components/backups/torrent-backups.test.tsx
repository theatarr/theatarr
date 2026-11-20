// @vitest-environment jsdom
import { fireEvent, screen, within } from "@testing-library/react";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { Toaster } from "@/components/toaster";
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
 // It holds no torrents: nothing to download either, and its words read left like the other notes.
 expect(within(failed).queryByRole("link", { name: "Download" })).toBeNull();
 expect(within(failed).queryByRole("link", { name: "DMM" })).toBeNull();
 const column = within(failed).getAllByText("Failed: no provider could be read").find((s) => !s.hasAttribute("data-testid"))!.closest("td")!;
 expect(column.className).toContain("text-left");
 expect(column.className).not.toContain("text-right");
 expect(within(partial).getByRole("link", { name: "Download" })).toBeTruthy();
 expect(within(partial).getByRole("link", { name: "DMM" })).toBeTruthy();
 expect(within(failed).queryByTestId("torrent-backup-unreadable")).toBeNull();
 expect(within(partial).getByTestId("torrent-backup-unreadable").textContent).toContain("unreadable");
 expect(within(partial).getByTestId("torrent-backup-unreadable").className).toContain("md:hidden");
 expect(within(partial).getByRole("button", { name: "Restore…" })).toBeTruthy();
 // An account listed with zero torrents is a good backup.
 expect(within(empty).queryByTestId("torrent-backup-failed")).toBeNull();
 expect(within(empty).getByRole("button", { name: "Restore…" })).toBeTruthy();
});

it("Back up now says the torrent backup is queued and links System › Tasks, where it ends", async () => {
 mockFetch([
  { method: "GET", path: "/torrents/backups", reply: { backups: [], every_s: 0, keep: 30 } },
  { method: "GET", path: "/torrents/report", reply: { providers: [] } },
  { method: "POST", path: "/torrents/backups", reply: { job: 41 } },
 ]);
 renderPage(<><TorrentBackups /><Toaster /></>);
 fireEvent.click(await screen.findByTestId("backup-now"));
 const title = await screen.findByText("Torrent backup queued");
 expect(title.closest("a")?.getAttribute("href")).toBe("/admin/system/tasks");
 expect(screen.getByText("System › Tasks shows how it ends.")).toBeTruthy();
});

it("the restore preview says once per unavailable account how many it skips and why, beside a valid target", async () => {
 const reason = "rd: gateway: backing off after 5 failures in a row, last: HTTP 503";
 const skip = (hash: string) => ({ target: "rd", source: "rd", hash, name: `Skip.${hash[0]}`, bytes: 1, files: 1, selection: "all", action: "skip_target", reason });
 mockFetch([
  { method: "GET", path: "/torrents/backups", reply: { backups: [{ id: "backup", providers: [], torrents: 4 }] } },
  { method: "GET", path: "/torrents/report", reply: { providers: [] } },
  { method: "POST", path: "/torrents/restore/preview", reply: { backup_id: "backup", items: [skip("a".repeat(40)), skip("b".repeat(40)), skip("c".repeat(40)),
   { target: "tb", source: "tb", hash: "d".repeat(40), name: "Readd.Movie", bytes: 1, files: 1, selection: "all", action: "readd" }], counts: { skip_target: 3, readd: 1 }, readd: 1, resume: 0, bytes: 1, confirmation_token: "r1" } },
 ]);
 renderPage(<TorrentBackups />);
 fireEvent.click(await screen.findByRole("button", { name: "Restore…" }));
 const line = await screen.findByTestId("restore-skipped");
 expect(line.textContent).toBe("3 torrents skipped: RD is not asked for now: paused after 5 failed requests in a row (last: HTTP 503).");
 expect(screen.getAllByTestId("restore-skipped")).toHaveLength(1);
 expect(screen.queryByText(/skip_target/)).toBeNull();
 expect(screen.getByText("Readd.Movie").closest("li")?.textContent).toBe("Re-add Readd.Movie");
 expect(screen.queryByText("readd")).toBeNull();
});
