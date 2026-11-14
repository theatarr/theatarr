// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { BackupPage } from "./backup";
it("renders the four backup sections in order and shows the torrent schedule as text linked to Settings", async () => {
 const fetch = mockFetch([
  { method: "GET", path: "/system/backups", reply: { enabled: true, hour: 3, keep: 14, sets: [], settings_source: "db", last_restore: null } },
  { method: "GET", path: "/torrents/backups", reply: { backups: [], every_s: 21600, keep: 12 } },
  { method: "GET", path: "/torrents/report", reply: { providers: [] } },
  { method: "GET", path: "/status", reply: { counts: { title: 10 }, schema: { current: 103, target: 103 } } },
  { method: "GET", path: "/jobs", reply: { items: [] } },
  { method: "GET", path: "/health", reply: { checks: [] } },
 ]);
 renderPage(<BackupPage />, "/admin/system/backup");
 expect((await screen.findByTestId("torrent-backup-schedule")).textContent).toContain("Scheduled every 6 h · keeps the newest 12");
 const names = ["Theatarr backups", "Torrent-list backups", "Settings export", "Database details"];
 const headings = screen.getAllByRole("heading").map(h => h.textContent).filter(h => names.includes(h!));
 expect(headings).toEqual(names);
 expect(screen.getByText(/A review copy of your settings without credentials. Not a backup./)).toBeTruthy();
 // No form here: the schedule is changed in Settings, which asks before unsaved changes are left.
 expect(screen.getByTestId("torrent-backup-edit").getAttribute("href")).toBe("/admin/settings/downloads/torrents#torrent-backups");
 expect(screen.queryByLabelText("Backup interval")).toBeNull();
 await waitFor(() => expect(document.getElementById("backup-check")).toBeTruthy());
 expect(fetch.calls("GET", "/settings/sections")).toHaveLength(0);
});
