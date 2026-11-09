// @vitest-environment jsdom
import { fireEvent, screen } from "@testing-library/react";
import { useLocation } from "react-router";
import { expect, it } from "vitest";
import { renderPage, mockFetch } from "@/test/page";
import schema from "@/components/settings/testdata/schema.json";
import { overview } from "@/components/settings/fixtures";
import { settingsIndex, findSettings } from "@/lib/settings-search";
import type { SettingsSchema } from "@/api/settings";
import { SettingsHome } from "./home";
const index = settingsIndex(schema as unknown as SettingsSchema);
it("finds rclone, router and VPN with familiar names and ignores record keys", () => {
 expect(findSettings(index, "rclone")[0].href).toBe("/admin/settings/libraries/files#mount");
 expect(findSettings(index, "router")[0].href).toBe("/admin/settings/downloads");
 expect(findSettings(index, "vpn").some(r => r.href === "/admin/settings/indexers/vpn")).toBe(true);
 expect(findSettings(index, "movie-web")).toEqual([]);
});
function Location() { const loc = useLocation(); return <output data-testid="location">{loc.pathname}{loc.hash}</output>; }
it("Enter opens the first setting result", async () => {
 mockFetch([{ method: "GET", path: "/settings/schema", reply: schema }, { method: "GET", path: "/settings", reply: overview() }, { method: "GET", path: "/me", reply: { permissions: ["admin", "users"] } }]);
 renderPage(<><SettingsHome /><Location /></>, "/admin/settings");
 const input = screen.getByRole("searchbox", { name: "Find a setting" });
 fireEvent.change(input, { target: { value: "rclone" } });
 fireEvent.keyDown(input, { key: "Enter" });
 expect(screen.getByTestId("location").textContent).toBe("/admin/settings/libraries/files#mount");
});

it("finds Theatarr backups and restore and keeps torrent backups", () => {
 for (const query of ["backup", "restore", "theatarr backup"]) {
  expect(findSettings(index, query)).toContainEqual(expect.objectContaining({ href: "/admin/system/backup", label: "Theatarr backups and restore" }));
 }
 expect(findSettings(index, "backup").some(r => r.href === "/admin/system/backup#torrent-backups")).toBe(true);
});

it("counts each tile in the singular for one", async () => {
 const o = overview(); o.switches.versions = [o.switches.versions[0]];
 mockFetch([{ method: "GET", path: "/settings/schema", reply: schema }, { method: "GET", path: "/settings", reply: o }, { method: "GET", path: "/me", reply: { permissions: ["admin", "users"] } }]);
 renderPage(<SettingsHome />, "/admin/settings");
 expect(await screen.findByText("1 library")).toBeTruthy();
 expect(screen.getByText("0 indexers")).toBeTruthy();
 expect(screen.queryByText("1 libraries")).toBeNull();
});
