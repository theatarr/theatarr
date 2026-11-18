import { describe, expect, it } from "vitest";
import { audioLines, languageName, mediaLine, mediaRows, subsList } from "./media";

describe("mediaLine", () => {
  it("cuts a long subtitle list to four languages and a count", () => {
    const subtitles = "eng/ara/zho/hrv/ces/dan/eng";
    expect(mediaLine({ resolution: "1920x1080", subtitles })).toBe("1920x1080 · subtitles English, Arabic, Chinese, Croatian +2");
    expect(subsList(subtitles)).toBe("English, Arabic, Chinese, Croatian, Czech, Danish");
    expect(mediaLine({ subtitles: "eng/spa" })).toBe("subtitles English, Spanish");
  });

  it("names audio languages and keeps codes it does not know", () => {
    expect(mediaLine({ audio_codec: "EAC3", audio_channels: 6, audio_languages: "jpn/eng" })).toBe("EAC3 6.0 · Japanese, English");
    expect(languageName("fre")).toBe("French");
    // Each language once, first-seen order, however many tracks carry it (T35).
    expect(mediaLine({ audio_languages: "rus/rus/rus/rus/rus/rus/rus/eng/ru" })).toBe("Russian, English");
    expect(languageName("und")).toBe("und");
    expect(languageName("qqq")).toBe("qqq");
  });
});

describe("mediaRows (the Media group)", () => {
  const thirty = ["eng", "ara", "zho", "hrv", "ces", "dan", "nld", "fin", "fre", "ger", "ell", "heb", "hin", "hun", "ind", "ita", "jpn", "kor", "msa", "nor", "pol", "por", "ron", "rus", "spa", "swe", "tha", "tur", "ukr", "vie", "en"];

  it("lists every subtitle language once by name, in full", () => {
    const rows = Object.fromEntries(mediaRows({ subtitles: thirty.join("/") }));
    const subs = rows.Subtitles[0].split(", ");
    // Thirty languages; "en" after "eng" is English again and reads once.
    expect(subs).toHaveLength(30);
    expect(subs[0]).toBe("English");
    expect(subs.at(-1)).toBe("Vietnamese");
    expect(rows.Subtitles[0]).not.toContain("+");
  });

  it("shows any stored bit depth in the Video row, the compact line only above 8", () => {
    const m = { resolution: "1920x1080", video_codec: "x264", video_bit_depth: 8 };
    expect(mediaRows(m)).toEqual([["Video", ["1920x1080 · x264 8-bit"]]]);
    expect(mediaLine(m)).toBe("1920x1080 · x264");
  });

  it("gives only the rows the file has, the video in one line", () => {
    expect(mediaRows({ resolution: "3840x2160", video_codec: "x265", video_bit_depth: 10, video_dynamic_range: "HDR10", run_time: "2:03:04" }))
      .toEqual([["Video", ["3840x2160 · x265 10-bit · HDR10"]], ["Runtime", ["2:03:04"]]]);
    expect(mediaRows(null)).toEqual([]);
    expect(mediaRows({})).toEqual([]);
  });

  it("puts several probed audio tracks each on a line, the codec alone without a language, and no languages row beside them", () => {
    const m = { audio_codec: "TrueHD Atmos", audio_channels: 7.1, audio_languages: "eng/jpn",
      audio_tracks: [{ codec: "TrueHD Atmos", channels: 7.1, language: "eng" }, { codec: "FLAC", channels: 2, language: "jpn" }, { codec: "AC3", channels: 5.1 }] };
    expect(audioLines(m)).toEqual(["English · TrueHD Atmos 7.1", "Japanese · FLAC 2.0", "AC3 5.1"]);
    expect(mediaRows(m).map(([k]) => k)).toEqual(["Audio"]);
  });

  it("reads one probed track like several, a taken-over file's first track with the languages row", () => {
    const one = { audio_codec: "AAC", audio_channels: 2, audio_languages: "jpn", audio_tracks: [{ codec: "AAC", channels: 2, language: "jpn" }] };
    expect(mediaRows(one)).toEqual([["Audio", ["Japanese · AAC 2.0"]]]);
    // "und" or no language is no language.
    expect(audioLines({ audio_tracks: [{ codec: "AC3", channels: 5.1, language: "und" }, { codec: "AAC", channels: 2, language: "UND" }, { codec: "DTS", channels: 6, language: " " }] }))
      .toEqual(["AC3 5.1", "AAC 2.0", "DTS 6.0"]);
    expect(mediaRows({ audio_codec: "EAC3", audio_channels: 5.1, audio_languages: "eng/en/fre" })).toEqual([["Audio", ["EAC3 5.1"]], ["Audio languages", ["English, French"]]]);
  });

  it("gives a taken-over file's track count with its first track, and one decimal for channels", () => {
    expect(audioLines({ audio_codec: "TrueHD Atmos", audio_channels: 7.1, audio_streams: 13 })).toEqual(["13 tracks; first: TrueHD Atmos 7.1"]);
    expect(audioLines({ audio_codec: "Opus", audio_channels: 2 })).toEqual(["Opus 2.0"]);
    expect(audioLines({ audio_codec: "AAC", audio_channels: 1 })).toEqual(["AAC 1.0"]);
  });

  it("names regional language tags", () => {
    expect(languageName("pt-BR")).toBe("Brazilian Portuguese");
    expect(languageName("es-419")).toBe("Latin American Spanish");
  });
});
