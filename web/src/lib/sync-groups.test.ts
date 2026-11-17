import { expect, it } from "vitest";
import { namesList, syncDeleteNote, syncedBy, syncNote } from "./sync-groups";

it("lists the partners a ticked library brings, not one ticked itself", () => {
  const opts = [{ key: "a", synced: ["b", "c"] }, { key: "b", synced: ["a"] }, { key: "c" }];
  expect(syncedBy(opts, (k) => k === "a")).toEqual({ b: "a", c: "a" });
  expect(syncedBy(opts, (k) => k === "a" || k === "c")).toEqual({ b: "a" });
  expect(syncedBy(opts, () => false)).toEqual({});
});

it("words the note for a library in a group, and nothing for one outside", () => {
  const name = (k: string) => k.toUpperCase();
  expect(syncNote([["a", "b", "c"]], "a", name)).toBe("Titles added to A are also added to B and C (in sync).");
  expect(syncNote([["a", "b"]], "c", name)).toBe("");
  expect(syncNote([["a", "b", "c"]], "a", name, ["a", "b"])).toBe("Titles added to A are also added to C (in sync).");
  expect(syncNote([["a", "b"]], "a", name, ["a", "b"])).toBe("");
  expect(namesList(["x", "y", "z"])).toBe("x, y and z");
});

it("words a connected app's delete note for its libraries' partners outside it, and nothing without one", () => {
  const name = (k: string) => k.toUpperCase();
  expect(syncDeleteNote([["a", "b", "c"]], ["a"], name)).toBe("A delete from this app also removes the title from B and C (in sync), with their files when the app asks for its files to be deleted.");
  expect(syncDeleteNote([["a", "b", "c"], ["d", "e"]], ["a", "b", "d"], name)).toBe("A delete from this app also removes the title from C and E (in sync), with their files when the app asks for its files to be deleted.");
  expect(syncDeleteNote([["a", "b"]], ["a", "b"], name)).toBe("");
  expect(syncDeleteNote([["a", "b"]], ["c"], name)).toBe("");
});
