import { describe, expect, it } from "vitest";
import { renameError } from "./rename";

const ok = { isError: false, error: null };

describe("rename errors", () => {
  it("says why the preview failed", () => {
    expect(renameError({ isError: true, error: new Error("the library rename operation is unavailable") }, ok)).toBe(
      "Rename preview failed: the library rename operation is unavailable");
  });
  it("puts a failed rename first, and says nothing when neither failed", () => {
    expect(renameError({ isError: true, error: new Error("mount down") }, { isError: true, error: new Error("write refused") })).toBe("Rename failed: write refused");
    expect(renameError(ok, { isError: true, error: "x" })).toBe("Rename failed: unknown error");
    expect(renameError(ok, ok)).toBe("");
  });
});
