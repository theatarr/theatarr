import { describe, expect, it } from "vitest";
import { choiceOf, closedGates, downloadReason, downloadSettings, globalDownloadsOff, linksOff, stateWords, verdict, withChoice } from "./version-grab";

describe("closedGates and verdict", () => {
  for (const mode of ["off", "shadow", "active"]) {
    for (const paused of [false, true]) {
      for (const pause_forced of [false, true]) {
        if (pause_forced && !paused) continue;
        it(`${mode}, paused ${paused}, forced ${pause_forced}`, () => {
          const state = { mode, paused, pause_forced };
          const gates = closedGates(state).map((g) => g.text).join("\n");
          const open = mode === "active" && !paused;
          // Every closed gate is listed, so fixing one never hides another.
          expect(gates === "").toBe(open);
          expect(gates.includes("Watch only: Theatarr searches, downloads and links nothing for this library")).toBe(mode === "shadow");
          expect(gates.includes("not in the saved settings")).toBe(mode === "off");
          expect(gates.includes("Paused by the environment (THEATARR_FORCE_OFF)")).toBe(pause_forced);
          expect(gates.includes("Paused: turn Pause off at the top of the page")).toBe(paused && !pause_forced);
          const word = verdict(state).word;
          expect(word === "Downloads on").toBe(open);
          if (mode === "shadow") expect(word).toBe("Watch only");
          else if (mode === "active" && paused) expect(word).toBe("Paused");
          else if (!open) expect(word).toBe("Downloads off");
        });
      }
    }
  }
});

it("never says a Watch-only library records what it would download", () => {
  const text = [closedGates({ mode: "shadow", paused: false, pause_forced: false }).map((g) => g.text).join(" "), stateWords({ mode: "shadow", paused: false, pause_forced: false }).text].join(" ");
  expect(text).not.toMatch(/record|would/);
});

it("takes the verdict from the server's will_grab", () => {
  const gates = { mode: "active", paused: false, pause_forced: false };
  expect(verdict({ ...gates, will_grab: false }).word).toBe("Downloads off");
  expect(closedGates({ ...gates, will_grab: false }).map((g) => g.text)).toEqual(["Automatic downloads are paused by the server"]);
  expect(verdict({ ...gates, mode: "shadow", will_grab: false }).word).toBe("Watch only");
});

it("marks links off separately from the download verdict", () => {
  const on = { mode: "active", paused: false, pause_forced: false, will_grab: true };
  expect(linksOff({ ...on, library_writes: false })).toBe(true);
  expect(linksOff({ ...on, library_writes: true })).toBe(false);
  expect(verdict({ ...on, library_writes: false }).word).toBe("Downloads on");
  // A Watch-only library writes no links by design, and Pause says it for itself: no mark.
  expect(linksOff({ ...on, mode: "shadow", will_grab: false, library_writes: false })).toBe(false);
  expect(linksOff({ ...on, paused: true, will_grab: false, library_writes: false })).toBe(false);
  expect(closedGates({ ...on, library_writes: false }).map((g) => g.text)).toEqual(["This library writes no links: new files won't be linked"]);
});

it("never says downloads are on while a download gate is closed", () => {
  for (const gate of [{ paused: true }, { mode: "shadow" }, { will_grab: false }, { paused: true, pause_forced: true }]) {
    expect(verdict({ mode: "active", paused: false, pause_forced: false, ...gate }).word).not.toBe("Downloads on");
  }
});

it("links each gate to where it is changed; Pause gates carry no link", () => {
  const gates = closedGates({ mode: "shadow", paused: true, pause_forced: false }, "movie-web");
  expect(gates.map((g) => g.to)).toEqual(["/admin/settings/libraries?edit=movie-web", undefined]);
});

it("points a paused Live library at Pause, everything else at Libraries", () => {
  expect(downloadSettings({ mode: "off", paused: true, pause_forced: true, will_grab: false })).toEqual({ label: "Settings › Libraries", to: "/admin/settings/libraries" });
  expect(downloadSettings({ mode: "shadow", paused: true, pause_forced: false, will_grab: false })).toEqual({ label: "Settings › Libraries", to: "/admin/settings/libraries" });
  expect(downloadSettings({ mode: "active", paused: true, pause_forced: false, will_grab: false })).toEqual({ label: "Pause, at the top of the page" });
  expect(downloadSettings({ mode: "active", paused: true, pause_forced: true, will_grab: false })).toEqual({ label: "THEATARR_FORCE_OFF" });
});

it("words search notes as the verdict and keeps other server reasons", () => {
  const c = (grab_state: object, search_reason = "Paused: nothing is searched or downloaded") => ({ mode: "active", can_edit: true, live_releases: false, can_search: false, search_reason, grab_state }) as Parameters<typeof downloadReason>[0];
  const on = { mode: "active", paused: false, pause_forced: false, will_grab: false };
  expect(downloadReason(c({ ...on, paused: true }))).toBe("Paused: nothing is searched, downloaded or linked");
  expect(downloadReason(c({ ...on, paused: true, pause_forced: true }))).toBe("Paused by the environment (THEATARR_FORCE_OFF)");
  expect(downloadReason(c({ ...on, mode: "shadow" }, "Watch only: nothing is searched or downloaded for this library"))).toBe("Watch only: Theatarr searches, downloads and links nothing for this library");
  expect(downloadReason(c(on))).toBe("Downloads off");
  // A server hold with every displayed gate open keeps the server's reason beside the verdict.
  expect(downloadReason(c(on, "provider budget exhausted until 12:00"))).toBe("Downloads off (provider budget exhausted until 12:00)");
  expect(downloadReason(c({ ...on, mode: "off" }, "not in the policy"))).toBe("not in the policy");
});

it("finds the global state once from any library", () => {
  expect(globalDownloadsOff([])).toBeUndefined();
  expect(globalDownloadsOff([undefined, { mode: "active", paused: true, pause_forced: false }])).toBe("off");
  expect(globalDownloadsOff([{ mode: "shadow", paused: true, pause_forced: true }])).toBe("locked");
  expect(globalDownloadsOff([{ mode: "shadow", paused: false, pause_forced: false }])).toBeUndefined();
});

describe("the Watch only / Live control", () => {
  const live = { mode: "active", paused: false, pause_forced: false };
  it.each([
    [{ mode: "shadow" }, "Watch only"],
    [{ mode: "shadow", paused: true }, "Watch only"],
    [{}, "Live"],
    [{ paused: true }, "Live, paused"],
    [{ paused: true, pause_forced: true }, "Live, paused"],
    [{ library_writes: false }, "Live, but new files won't be linked"],
    [{ will_grab: false }, "Live, downloads held by the server"],
    [{ mode: "off" }, "Not set up"],
  ])("%j reads %s", (over, text) => {
    expect(stateWords({ ...live, ...over }).text).toBe(text);
  });

  it("maps each stored mode to its choice", () => {
    expect(choiceOf("shadow")).toBe("watch");
    expect(choiceOf("active")).toBe("live");
    expect(choiceOf("off")).toBeUndefined();
  });

  it("changes only mode", () => {
    const base = { mode: "shadow", kind: "movie" };
    expect(withChoice(base, "live")).toEqual({ mode: "active", kind: "movie" });
    expect(withChoice(withChoice(base, "live"), "watch")).toEqual(base);
    expect(withChoice(base, "watch")).toBe(base);
  });
});
