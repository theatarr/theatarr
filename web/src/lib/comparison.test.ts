import { describe, expect, it } from "vitest";
import { resultsSummary } from "./comparison";

describe("comparison search results summary", () => {
  it("says how many were kept and shown next to the total found", () => {
    expect(resultsSummary(33, 2, 2)).toBe("33 found · best 2 kept and shown · 2 accepted");
    expect(resultsSummary(3, 3, 1, "anime")).toBe("3 found, all shown · 1 accepted · anime");
    expect(resultsSummary(0, 0, 0)).toBe("0 found, all shown · 0 accepted");
  });
});
