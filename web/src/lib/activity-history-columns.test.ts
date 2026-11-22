import { describe, expect, it } from "vitest";
import { eventMeaning } from "./activity-history-columns";

describe("eventMeaning", () => {
  it("words Blocked as what happened then: an unblocked release keeps its History row", () => {
    expect(eventMeaning("Blocked")).toBe("This release was put on the library's blocklist at the time.");
    expect(eventMeaning("Blocked")).not.toContain(" is on ");
  });

  it("words the playing outcomes as they are: no put-back, and no promise of further searches", () => {
    expect(eventMeaning("Earlier copy put back")).toBe("");
    const none = eventMeaning("Stopped working, no other copy yet");
    expect(none).toContain("RSS");
    expect(none).not.toContain("searches go on");
  });
});
