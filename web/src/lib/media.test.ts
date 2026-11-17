import { describe, expect, it } from "vitest";
import { languageName, mediaLine, subsList } from "./media";

describe("mediaLine", () => {
  it("cuts a long subtitle list to four languages and a count", () => {
    const subtitles = "eng/ara/zho/hrv/ces/dan/eng";
    expect(mediaLine({ resolution: "1920x1080", subtitles })).toBe("1920x1080 · subtitles English, Arabic, Chinese, Croatian +2");
    expect(subsList(subtitles)).toBe("English, Arabic, Chinese, Croatian, Czech, Danish");
    expect(mediaLine({ subtitles: "eng/spa" })).toBe("subtitles English, Spanish");
  });

  it("names audio languages and keeps codes it does not know", () => {
    expect(mediaLine({ audio_codec: "EAC3", audio_channels: 6, audio_languages: "jpn/eng" })).toBe("EAC3 6 · Japanese, English");
    expect(languageName("fre")).toBe("French");
    // Each language once, first-seen order, however many tracks carry it (T35).
    expect(mediaLine({ audio_languages: "rus/rus/rus/rus/rus/rus/rus/eng/ru" })).toBe("Russian, English");
    expect(languageName("und")).toBe("und");
    expect(languageName("qqq")).toBe("qqq");
  });
});
