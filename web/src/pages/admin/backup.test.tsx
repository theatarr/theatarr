// @vitest-environment jsdom
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import schema from "@/components/settings/testdata/schema.json";
import { BackupPage } from "./backup";
it("renders the four backup sections in order and saves the torrent schedule", async () => {
 const config = { scan_roots: ["/other"], backup: { every: "6h", keep: 12 } };
 const fetch = mockFetch([
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 4, sections: [{ key: "torrents", config }] } },
  { method: "PUT", path: "/settings/sections/torrents", reply: {} },
  { method: "GET", path: "/system/backups", reply: { enabled: true, hour: 3, keep: 14, sets: [], settings_source: "db", last_restore: null } },
  { method: "GET", path: "/torrents/backups", reply: { backups: [], every_s: 0, keep: 12 } },
  { method: "GET", path: "/torrents/report", reply: { providers: [] } },
  { method: "GET", path: "/status", reply: { counts: { title: 10 }, schema: { current: 103, target: 103 } } },
  { method: "GET", path: "/jobs", reply: { items: [] } },
  { method: "GET", path: "/health", reply: { checks: [] } },
 ]);
 renderPage(<BackupPage />, "/admin/system/backup");
 await screen.findByLabelText("Backup interval");
 const names = ["Theatarr backups", "Torrent-list backups", "Settings export", "Database details"];
 const headings = screen.getAllByRole("heading").map(h => h.textContent).filter(h => names.includes(h!));
 expect(headings).toEqual(names);
 expect(screen.getByText(/Scheduled backups are off/)).toBeTruthy();
 expect(screen.getByText(/A review copy of your settings without credentials. Not a backup./)).toBeTruthy();
 await waitFor(() => expect(document.getElementById("backup-check")).toBeTruthy());
 fireEvent.change(screen.getByLabelText("Backup interval"), { target: { value: "24h" } });
 fireEvent.click(screen.getByRole("button", { name: "Save" }));
 await waitFor(() => expect(fetch.calls("PUT", "/settings/sections/torrents")).toHaveLength(1));
 expect(fetch.calls("PUT", "/settings/sections/torrents")[0].body).toEqual({ generation: 4, config: { ...config, backup: { every: "24h", keep: 12 } } });
});
