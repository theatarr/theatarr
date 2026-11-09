import { describe, expect, it } from "vitest";
import { closedGates, downloadReason, downloadSettings, globalDownloadsOff, linksOff, verdict } from "./version-grab";

describe("closedGates and verdict", () => {
  for (const mode of ["off", "shadow", "active"]) {
    for (const grab of [false, true]) {
      for (const router_enabled of [false, true]) {
        for (const router_forced_off of [false, true]) {
          it(`${mode}, grab ${grab}, router ${router_enabled}, forced ${router_forced_off}`, () => {
            const state = { mode, grab, router_enabled, router_forced_off, library_enabled: true };
            const gates = closedGates(state).map((g) => g.text).join("\n");
            const open = mode === "active" && grab && router_enabled && !router_forced_off;
            // Every closed gate is listed, so fixing one never hides another.
            expect(gates === "").toBe(open);
            expect(gates.includes("Mode is Shadow")).toBe(mode === "shadow");
            expect(gates.includes("not in the saved settings")).toBe(mode === "off");
            expect(gates.includes("Allow downloads for this library is off")).toBe(!grab);
            expect(gates.includes("locked off by the environment")).toBe(router_forced_off);
            expect(gates.includes("Allow new downloads is off")).toBe(!router_enabled && !router_forced_off);
            const word = verdict(state).word;
            expect(word === "Downloads on").toBe(open);
            if (mode === "shadow") expect(word).toBe("Preview only");
            else if (mode === "active" && router_forced_off) expect(word).toBe("Locked off");
            else if (mode === "active" && !router_enabled) expect(word).toBe("Paused globally");
            else if (!open) expect(word).toBe("Downloads off");
          });
        }
      }
    }
  }
});

it("takes the verdict from the server's will_grab", () => {
  const gates = { mode: "active", grab: true, router_enabled: true, router_forced_off: false };
  expect(verdict({ ...gates, will_grab: false }).word).toBe("Downloads off");
  expect(closedGates({ ...gates, will_grab: false }).map((g) => g.text)).toEqual(["Automatic downloads are paused by the server"]);
  expect(verdict({ ...gates, grab: false, will_grab: true }).word).toBe("Downloads on");
  expect(verdict({ ...gates, mode: "shadow", will_grab: false }).word).toBe("Preview only");
});

it("marks links off separately from the download verdict", () => {
  const on = { mode: "active", grab: true, router_enabled: true, router_forced_off: false, will_grab: true };
  expect(verdict({ ...on, library_enabled: false }).word).toBe("Downloads on");
  expect(linksOff({ ...on, library_enabled: false })).toBe(true);
  expect(linksOff({ ...on, library_enabled: true, library_writes: false })).toBe(true);
  expect(linksOff({ ...on, library_enabled: true })).toBe(false);
  // A shadow library writes no links by design: no mark.
  expect(linksOff({ ...on, mode: "shadow", will_grab: false, library_enabled: true, library_writes: false })).toBe(false);
  expect(closedGates({ ...on, library_enabled: false }).map((g) => g.text)).toEqual(["Write library links is off: new files won't be linked"]);
  expect(closedGates({ ...on, will_grab: false, grab: false, library_enabled: false }).map((g) => g.text)).toEqual(["Allow downloads for this library is off", "Write library links is off: new files won't be linked"]);
});

it("never says downloads are on while a download gate is closed", () => {
  for (const gate of [{ router_enabled: false }, { grab: false }, { mode: "shadow" }, { will_grab: false }, { router_forced_off: true }]) {
    expect(verdict({ mode: "active", grab: true, router_enabled: true, router_forced_off: false, library_enabled: false, ...gate }).word).not.toBe("Downloads on");
  }
});

it("links each gate to where it is changed", () => {
  const gates = closedGates({ mode: "shadow", grab: false, router_enabled: false, router_forced_off: false, library_enabled: false }, "movie-web");
  expect(gates.map((g) => g.to)).toEqual(["/admin/settings/libraries?edit=movie-web", "/admin/settings/libraries?edit=movie-web", "/admin/settings/downloads", "/admin/settings/libraries"]);
  expect(closedGates({ mode: "active", grab: false, router_enabled: true, router_forced_off: false })[0].to).toBe("/admin/settings/libraries");
});

it("links a removed library to Libraries even when downloads are forced off", () => {
  // The server reports a library gone from the policy as mode off; its reason ("not in the policy") wins over force-off.
  expect(downloadSettings({ mode: "off", grab: false, router_enabled: false, router_forced_off: true, will_grab: false })).toEqual({ label: "Settings › Libraries", to: "/admin/settings/libraries" });
  // A shadow library under force-off reads Preview only, so its link goes where Shadow is changed: Libraries.
  expect(downloadSettings({ mode: "shadow", grab: true, router_enabled: true, router_forced_off: true, will_grab: false })).toEqual({ label: "Settings › Libraries", to: "/admin/settings/libraries" });
  // An active library locked off or paused globally links to Download clients.
  expect(downloadSettings({ mode: "active", grab: true, router_enabled: true, router_forced_off: true, will_grab: false })).toEqual({ label: "Settings › Download clients", to: "/admin/settings/downloads" });
  expect(downloadSettings({ mode: "active", grab: true, router_enabled: false, router_forced_off: false, will_grab: false })).toEqual({ label: "Settings › Download clients", to: "/admin/settings/downloads" });
});

it("words search notes as the verdict and keeps other server reasons", () => {
  const c = (grab_state: object, search_reason = "router.enabled is false") => ({ mode: "active", can_edit: true, live_releases: false, can_search: false, search_reason, grab_state }) as Parameters<typeof downloadReason>[0];
  const on = { mode: "active", grab: true, router_enabled: true, router_forced_off: false, will_grab: false };
  expect(downloadReason(c({ ...on, grab: false }))).toBe("Downloads off (Allow downloads for this library is off)");
  expect(downloadReason(c(on))).toBe("Downloads off");
  // A server hold with every displayed gate open keeps the server's reason beside the verdict.
  expect(downloadReason(c(on, "provider budget exhausted until 12:00"))).toBe("Downloads off (provider budget exhausted until 12:00)");
  expect(downloadReason(c({ ...on, mode: "off" }, "not in the policy"))).toBe("not in the policy");
});

it("finds the global state once from any library", () => {
  expect(globalDownloadsOff([])).toBeUndefined();
  expect(globalDownloadsOff([undefined, { mode: "active", grab: true, router_enabled: false, router_forced_off: false }])).toBe("off");
  expect(globalDownloadsOff([{ mode: "shadow", grab: true, router_enabled: true, router_forced_off: true }])).toBe("locked");
  expect(globalDownloadsOff([{ mode: "shadow", grab: true, router_enabled: true, router_forced_off: false }])).toBeUndefined();
});
