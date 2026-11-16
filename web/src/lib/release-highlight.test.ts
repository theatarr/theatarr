import { describe, expect, it } from "vitest";
import { highlightRelease } from "./release-highlight";

/** The coloured parts of a name as "kind:text". */
const facts = (name: string) => highlightRelease(name).filter((s) => s.kind).map((s) => `${s.kind}:${s.text}`);

const NAMES: [string, string[]][] = [
  [
    "The.Lord.of.the.Rings.The.Rings.of.Power.S02.UHD.BluRay.2160p.TrueHD.Atmos.7.1.DV.HDR10P.HEVC.REMUX-FraMeSToR",
    ["res:UHD", "source:BluRay", "res:2160p", "audio:TrueHD", "audio:Atmos.7.1", "hdr:DV", "hdr:HDR10P", "source:REMUX"],
  ],
  [
    "Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX",
    ["res:2160p", "source:WEB-DL", "audio:DDP5.1", "audio:Atmos", "hdr:DV", "hdr:HDR"],
  ],
  // The title's Web is plain; the source after the resolution is coloured.
  ["Madame.Web.2024.1080p.WEB-DL.DDP5.1.H.264-GRP", ["res:1080p", "source:WEB-DL", "audio:DDP5.1"]],
  [
    "The Rings Of Power S02 COMPLETE 2160p WEB DL DV HDR10 PLUS ENG LATINO HINDI DDP5 1 Atmos",
    ["res:2160p", "source:WEB DL", "hdr:DV", "hdr:HDR10 PLUS", "audio:DDP5 1", "audio:Atmos"],
  ],
  ["Show.S01E02.1080p.AMZN.WEB.h264-GRP", ["res:1080p", "source:WEB"]],
  ["Movie.2007.DVDRip.XviD-GRP", ["source:DVDRip"]],
  [
    "Frosty the Snowman 1969 UHD BluRay 2160p DTS-HD MA 5 1 HEVC REMUX-FraMeSToR",
    ["res:UHD", "source:BluRay", "res:2160p", "audio:DTS-HD MA 5 1", "source:REMUX"],
  ],
  ["Movie.2019.1080p.BluRay.DD5.1.HDR10+.x264-GRP", ["res:1080p", "source:BluRay", "audio:DD5.1", "hdr:HDR10+"]],
  // A title's Opus is plain; the codec after the resolution is coloured.
  ["Magnum.Opus.2022.1080p.WEB-DL.Opus.2.0.H.264-GRP", ["res:1080p", "source:WEB-DL", "audio:Opus.2.0"]],
  ["Mr.Hollands.Opus.1995.DVDRip.XviD-GRP", ["source:DVDRip"]],
  ["Some.Show.S03E04.Episode.Name.x265-GRP", []],
];

describe("highlightRelease", () => {
  it.each(NAMES)("colours the facts of %s", (name, want) => {
    expect(facts(name)).toEqual(want);
  });

  it.each(NAMES)("joins back to %s", (name) => {
    expect(highlightRelease(name).map((s) => s.text).join("")).toBe(name);
  });

  it("gives a name without facts as one plain segment", () => {
    expect(highlightRelease("Some.Show.S03E04.Episode.Name.x265-GRP")).toEqual([{ text: "Some.Show.S03E04.Episode.Name.x265-GRP" }]);
  });

  it("leaves DD without a channel count, MA and X alone, and keeps loose digits out of a channel count", () => {
    expect(facts("Show.DD.MA.X.2160p.AAC.2019")).toEqual(["res:2160p", "audio:AAC"]);
  });
});
