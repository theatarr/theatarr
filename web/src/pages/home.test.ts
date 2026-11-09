import { expect, it } from "vitest";
import { librarySummary } from "./home";

it("counts titles across libraries, not each title's place in each library", () => {
  expect(librarySummary(1204, 6)).toBe("1,204 titles across 6 libraries.");
  expect(librarySummary(1, 1)).toBe("1 title across 1 library.");
  expect(librarySummary(0, undefined)).toBe("0 titles across 0 libraries.");
});
