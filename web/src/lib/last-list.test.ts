import { describe, expect, it } from "vitest";
import { listToReturnTo, noteLocation } from "./last-list";

describe("the list a removal returns to", () => {
  it("is the list the title was opened from, not an older list", () => {
    noteLocation("/movies?availability=available");
    noteLocation("/title/5");
    noteLocation("/title/5?tab=history"); // a tab of the same title keeps it
    expect(listToReturnTo("movie")).toBe("/movies?availability=available");
    // Movies, then Search, then a series: back to the series list, not the old Movies filter.
    noteLocation("/movies?availability=missing");
    noteLocation("/search?q=lanterns");
    noteLocation("/title/9");
    expect(listToReturnTo("series")).toBe("/series");
    expect(listToReturnTo("series", true)).toBe("/anime");
  });
});
