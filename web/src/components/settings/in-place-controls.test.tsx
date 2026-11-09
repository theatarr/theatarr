// @vitest-environment jsdom
import { useState } from "react";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { SectionPanel } from "./section-panel";
import schema from "./testdata/schema.json";
import { SETTINGS_PANELS } from "@/lib/settings-map";
function BackupSchedulePanel() { return <SectionPanel {...SETTINGS_PANELS.find(p => p.id === "torrent-backups")!} />; }
const schemaRoute = { method: "GET", path: "/settings/schema", reply: schema };
import { RcloneConnectionForm } from "./rclone-connection";
import type { SettingsSection } from "@/api/settings";
const library: SettingsSection = { key: "library", live: true, config: { mount: { path: "/mount", rclone_rc: { budget: "rc-budget" } }, other: true }, yaml: "", secrets: [] };
it("patches only the backup fields of a fresh torrent section", async () => {
 const config = { scan_roots: ["/other"], backup: { every: "6h", keep: 12, future: true }, unknown: 7 };
 const fetch = mockFetch([schemaRoute, { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 9, sections: [{ key: "torrents", config }] } }, { method: "PUT", path: "/settings/sections/torrents", reply: {} }]);
 renderPage(<BackupSchedulePanel />);
 await waitFor(() => expect((screen.getByLabelText("Backup interval") as HTMLSelectElement).disabled).toBe(false));
 fireEvent.change(screen.getByLabelText("Backup interval"), { target: { value: "24h" } }); fireEvent.change(screen.getByLabelText("Backups to keep"), { target: { value: "30" } }); fireEvent.click(screen.getByRole("button", { name: "Save" }));
 await waitFor(() => expect(fetch.calls("PUT", "/settings/sections/torrents")).toHaveLength(1));
 expect(fetch.calls("PUT", "/settings/sections/torrents")[0].body).toEqual({ generation: 9, config: { ...config, backup: { every: "24h", keep: 30, future: true } } });
});
it("shows an unconfigured schedule editable, with no policy-files note", async () => {
 mockFetch([schemaRoute, { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 0, sections: [] } }]); renderPage(<BackupSchedulePanel />);
 await waitFor(() => expect((screen.getByLabelText("Backup interval") as HTMLSelectElement).disabled).toBe(false));
 expect(screen.queryByText(/Read-only:/)).toBeNull(); expect(screen.queryByText(/policy files/)).toBeNull();
});
it("tests typed rclone credentials and saves them beside the parent config", async () => {
 const fetch = mockFetch([{ method: "POST", path: "/nowplaying/rclone/test", reply: { ok: true, version: "v1.70" } }, { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 11, sections: [library] } }, { method: "PUT", path: "/settings/sections/library", reply: {} }]);
 renderPage(<RcloneConnectionForm section={library} readOnly={false} />);
 fireEvent.change(screen.getByLabelText("rclone URL"), { target: { value: "http://host:5572" } }); fireEvent.change(screen.getByLabelText("rclone username"), { target: { value: "user" } }); fireEvent.change(screen.getByLabelText("rclone password"), { target: { value: "password" } });
 fireEvent.click(screen.getByRole("button", { name: "Test connection" })); expect(await screen.findByText("Connected · v1.70")).toBeTruthy();
 expect(fetch.calls("POST", "/nowplaying/rclone/test")[0].body).toEqual({ url: "http://host:5572", username: "user", password: "password", use_stored_username: false, use_stored_password: false });
 fireEvent.click(screen.getByRole("button", { name: "Save" })); await waitFor(() => expect(fetch.calls("PUT", "/settings/sections/library")).toHaveLength(1));
 expect(fetch.calls("PUT", "/settings/sections/library")[0].body).toEqual({ generation: 11, config: { mount: { path: "/mount", rclone_rc: { budget: "rc-budget", url: "http://host:5572" } }, other: true }, secrets: { "mount.rclone_rc.username": "user", "mount.rclone_rc.password": "password" } });
});
const savedRC: SettingsSection = { ...library, config: { mount: { rclone_rc: { url: "http://saved:5572" } } }, secrets: [{ path: "mount.rclone_rc.username", set: true }, { path: "mount.rclone_rc.password", set: true }] };
it("password-only test uses stored username independently", async () => {
 const fetch = mockFetch([{ method: "POST", path: "/nowplaying/rclone/test", reply: { ok: true } }]);
 renderPage(<RcloneConnectionForm section={savedRC} readOnly={false} />);
 fireEvent.click(screen.getByRole("button", { name: "Edit" }));
 fireEvent.change(screen.getByLabelText("rclone password"), { target: { value: "new-password" } });
 fireEvent.click(screen.getByRole("button", { name: "Test connection" }));
 await screen.findByText("Connected");
 expect(fetch.calls("POST", "/nowplaying/rclone/test")[0].body).toMatchObject({ password: "new-password", use_stored_username: true, use_stored_password: false });
});
it("changed rclone URL blocks until credentials re-entered or removed", async () => {
 const fetch = mockFetch([{ method: "GET", path: "/settings/sections", reply: { source: "db", generation: 1, sections: [savedRC] } }, { method: "PUT", path: "/settings/sections/library", reply: {} }]);
 renderPage(<RcloneConnectionForm section={savedRC} readOnly={false} />);
 fireEvent.click(screen.getByRole("button", { name: "Edit" }));
 fireEvent.change(screen.getByLabelText("rclone URL"), { target: { value: "http://new:5572" } });
 for (const name of ["Save", "Test connection"]) expect((screen.getByRole("button", { name }) as HTMLButtonElement).disabled).toBe(true);
 fireEvent.change(screen.getByLabelText("rclone username"), { target: { value: "user" } });
 expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);
 fireEvent.change(screen.getByLabelText("rclone password"), { target: { value: "pass" } });
 expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(false);
 fireEvent.change(screen.getByLabelText("rclone password"), { target: { value: "" } });
 fireEvent.click(screen.getByLabelText("Remove saved credentials"));
 fireEvent.click(screen.getByRole("button", { name: "Save" }));
 await waitFor(() => expect(fetch.calls("PUT", "/settings/sections/library")).toHaveLength(1));
 expect(fetch.calls("PUT", "/settings/sections/library")[0].body).toMatchObject({ clear_secrets: ["mount.rclone_rc.username", "mount.rclone_rc.password"] });
});
it("hides unset rclone when read-only and keeps the configured line read-only", () => {
 const view = renderPage(<RcloneConnectionForm section={library} readOnly />);
 expect(screen.queryByText("Connect rclone")).toBeNull();
 view.unmount(); renderPage(<RcloneConnectionForm section={savedRC} readOnly />);
 expect(screen.getByText(/rclone: connected/)).toBeTruthy();
 expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
});
it("schedule Off removes every and saved notice survives a parent render", async () => {
 const config = { backup: { every: "6h0m0s", keep: 12, future: true } };
 const fetch = mockFetch([schemaRoute, { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 9, sections: [{ key: "torrents", config }] } }, { method: "PUT", path: "/settings/sections/torrents", reply: {} }]);
 function Refresh() { const [, setRevision] = useState(0); return <><BackupSchedulePanel /><button onClick={() => setRevision(1)}>Refetch</button></>; }
 renderPage(<Refresh />);
 await waitFor(() => expect((screen.getByLabelText("Backup interval") as HTMLSelectElement).disabled).toBe(false));
 fireEvent.change(screen.getByLabelText("Backup interval"), { target: { value: "" } }); fireEvent.click(screen.getByRole("button", { name: "Save" }));
 await screen.findByText("Saved.");
 expect(fetch.calls("PUT", "/settings/sections/torrents")[0].body).toMatchObject({ config: { backup: { keep: 12, future: true } } });
 expect((fetch.calls("PUT", "/settings/sections/torrents")[0].body as {config: {backup: object}}).config.backup).not.toHaveProperty("every");
 fireEvent.click(screen.getByRole("button", { name: "Refetch" }));
 expect(screen.getByText("Saved.")).toBeTruthy();
});
