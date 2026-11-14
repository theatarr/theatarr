import { describe, expect, it } from "vitest";
import { mediaLine, subsList } from "./media";

describe("mediaLine", () => {
  it("cuts a long subtitle list to four languages and a count", () => {
    const subtitles = "eng/ara/zho/hrv/ces/dan/eng";
    expect(mediaLine({ resolution: "1920x1080", subtitles })).toBe("1920x1080 · subs eng/ara/zho/hrv +2");
    expect(subsList(subtitles)).toBe("eng/ara/zho/hrv/ces/dan");
    expect(mediaLine({ subtitles: "eng/spa" })).toBe("subs eng/spa");
  });
});
