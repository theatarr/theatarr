import { describe, expect, it } from "vitest";
import { closedGates, downloadReason, downloadSettings, globalDownloadsOff, linksOff, stateWords, verdict } from "./version-grab";

describe("closedGates and verdict", () => {
  for (const paused of [false, true]) {
    for (const pause_forced of [false, true]) {
      if (pause_forced && !paused) continue;
      it(`paused ${paused}, forced ${pause_forced}`, () => {
        const state = { paused, pause_forced };
        const gates = closedGates(state).map((g) => g.text).join("\n");
        const open = !paused;
        // Every closed gate is listed, so fixing one never hides another.
        expect(gates === "").toBe(open);
        expect(gates.includes("Paused by the environment (THEATARR_FORCE_OFF)")).toBe(pause_forced);
        expect(gates.includes("Paused: turn Pause off at the top of the page")).toBe(paused && !pause_forced);
        const word = verdict(state).word;
        expect(word === "Downloads on").toBe(open);
        if (paused) expect(word).toBe("Paused");
      });
    }
  }
});

it("takes the verdict from the server's will_grab", () => {
  const gates = { paused: false, pause_forced: false };
  expect(verdict({ ...gates, will_grab: false }).word).toBe("Downloads off");
  expect(closedGates({ ...gates, will_grab: false }).map((g) => g.text)).toEqual(["Automatic downloads are paused by the server"]);
});

it("marks links off separately from the download verdict", () => {
  const on = { paused: false, pause_forced: false, will_grab: true };
  expect(linksOff({ ...on, library_writes: false })).toBe(true);
  expect(linksOff({ ...on, library_writes: true })).toBe(false);
  expect(verdict({ ...on, library_writes: false }).word).toBe("Downloads on");
  // Pause says it for itself: no mark.
  expect(linksOff({ ...on, paused: true, will_grab: false, library_writes: false })).toBe(false);
  expect(closedGates({ ...on, library_writes: false }).map((g) => g.text)).toEqual(["This library writes no links: new files won't be linked"]);
});

it("never says downloads are on while a download gate is closed", () => {
  for (const gate of [{ paused: true }, { will_grab: false }, { paused: true, pause_forced: true }]) {
    expect(verdict({ paused: false, pause_forced: false, ...gate }).word).not.toBe("Downloads on");
  }
});

it("links each gate to where it is changed; Pause gates carry no link", () => {
  const gates = closedGates({ paused: false, pause_forced: false, will_grab: false, library_writes: false }, "movie-web");
  expect(gates.map((g) => g.to)).toEqual(["/admin/settings/libraries?edit=movie-web", "/admin/settings/libraries?edit=movie-web"]);
  expect(closedGates({ paused: true, pause_forced: false }, "movie-web").map((g) => g.to)).toEqual([undefined]);
});

it("points a paused library at Pause, everything else at Libraries", () => {
  expect(downloadSettings({ paused: false, pause_forced: false, will_grab: false })).toEqual({ label: "Settings › Libraries", to: "/admin/settings/libraries" });
  expect(downloadSettings({ paused: true, pause_forced: false, will_grab: false })).toEqual({ label: "Pause, at the top of the page" });
  expect(downloadSettings({ paused: true, pause_forced: true, will_grab: false })).toEqual({ label: "THEATARR_FORCE_OFF" });
});

it("words search notes as the verdict and keeps other server reasons", () => {
  const c = (grab_state: object, search_reason = "Paused: searches, new grabs and imports stop for every library; Interactive search can still look up releases") => ({ can_edit: true, live_releases: false, can_search: false, search_reason, grab_state }) as Parameters<typeof downloadReason>[0];
  const on = { paused: false, pause_forced: false, will_grab: false };
  expect(downloadReason(c({ ...on, paused: true }))).toBe("Paused: searches, new grabs and imports stop for every library; Interactive search can still look up releases");
  expect(downloadReason(c({ ...on, paused: true, pause_forced: true }))).toBe("Paused by the environment (THEATARR_FORCE_OFF)");
  expect(downloadReason(c(on))).toBe("Downloads off");
  // A server hold with every displayed gate open keeps the server's reason beside the verdict.
  expect(downloadReason(c(on, "provider budget exhausted until 12:00"))).toBe("Downloads off (provider budget exhausted until 12:00)");
  expect(downloadReason(c(on, "not in the policy"))).toBe("Downloads off (not in the policy)");
});

it("finds the global state once from any library", () => {
  expect(globalDownloadsOff([])).toBeUndefined();
  expect(globalDownloadsOff([undefined, { paused: true, pause_forced: false }])).toBe("off");
  expect(globalDownloadsOff([{ paused: true, pause_forced: true }])).toBe("locked");
  expect(globalDownloadsOff([{ paused: false, pause_forced: false }])).toBeUndefined();
});

describe("the library's state in words", () => {
  const on = { paused: false, pause_forced: false };
  it.each([
    [{}, "Downloads on"],
    [{ paused: true }, "Paused"],
    [{ paused: true, pause_forced: true }, "Paused"],
    [{ library_writes: false }, "New files won't be linked"],
    [{ will_grab: false }, "Downloads held by the server"],
  ])("%j reads %s", (over, text) => {
    expect(stateWords({ ...on, ...over }).text).toBe(text);
  });

  it("reads Not set up without a running state", () => {
    expect(stateWords(undefined).text).toBe("Not set up");
  });
});
