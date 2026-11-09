import { describe, expect, it } from "vitest";
import { renameButton } from "./rename";

const idle = { isPending: false, isError: false, error: null };

describe("rename button", () => {
  it("never says the names match when the preview failed", () => {
    const b = renameButton({ isPending: false, isError: true, error: new Error("the library rename operation is unavailable"), data: undefined }, idle);
    expect(b.label).toBe("Rename unavailable");
    expect(b.disabled).toBe(true);
    expect(b.error).toContain("the library rename operation is unavailable");
  });

  it("says it is checking while the preview loads", () => {
    const b = renameButton({ isPending: true, isError: false, error: null, data: undefined }, idle);
    expect(b.label).toBe("Checking names…");
    expect(b.disabled).toBe(true);
  });

  it("says the names match only after a successful empty preview", () => {
    const b = renameButton({ isPending: false, isError: false, error: null, data: { files: [], skipped: 1 } }, idle);
    expect(b).toMatchObject({ label: "Names match", disabled: true, title: "1 link skipped", error: "" });
  });

  it("offers the rename and shows a failed apply", () => {
    const data = { files: [{}, {}], skipped: 0 };
    expect(renameButton({ isPending: false, isError: false, error: null, data }, idle)).toMatchObject({ label: "Rename (2)", disabled: false });
    expect(renameButton({ isPending: false, isError: false, error: null, data }, { isPending: true, isError: false, error: null })).toMatchObject({
      label: "Renaming…",
      disabled: true,
    });
    const failed = renameButton({ isPending: false, isError: false, error: null, data }, { isPending: false, isError: true, error: new Error("write refused") });
    expect(failed.error).toBe("Rename failed: write refused");
    expect(failed.disabled).toBe(false);
  });
});
