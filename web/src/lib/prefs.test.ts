// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { normalizeColumnPrefs, readPref, writePref, type ColumnDef } from "./prefs";

type Id = "status" | "quality" | "size";
const defs: ColumnDef<Id>[] = [
  { id: "status", label: "Status", hint: "" },
  { id: "quality", label: "Quality", hint: "" },
];
const later: ColumnDef<Id>[] = [...defs, { id: "size", label: "Size", hint: "" }];

afterEach(() => window.localStorage.clear());

describe("normalizeColumnPrefs", () => {
  it("gives the defaults when nothing is stored", () => {
    expect(normalizeColumnPrefs(defs, ["status"])(undefined)).toEqual({ order: ["status", "quality"], shown: ["status"] });
  });

  it("round-trips a choice through the browser and keeps a column added later hidden", () => {
    writePref("test-columns", 7, { order: ["quality", "status"], shown: ["quality"] });
    expect(window.localStorage.getItem("theatarr.library.test-columns.7")).not.toBeNull();
    const p = readPref("test-columns", 7, normalizeColumnPrefs(later, ["status", "size"]));
    expect(p).toEqual({ order: ["quality", "status", "size"], shown: ["quality"] });
  });

  it("drops unknown ids", () => {
    expect(normalizeColumnPrefs(defs, [])({ order: ["bogus", "quality"], shown: ["bogus", "quality"] })).toEqual({ order: ["quality", "status"], shown: ["quality"] });
  });
});
