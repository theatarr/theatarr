import { describe, expect, it } from "vitest";
import { matchesWords, MAX_WORDS, searchWords } from "./words";

const knight = "The.Dark.Knight.2008.UHD.BluRay.2160p.DTS-HD.MA.5.1.DV.HEVC.REMUX-FraMeSToR";

describe("matchesWords", () => {
  it("finds every word, in any order, ignoring case and extra spaces", () => {
    expect(matchesWords("knight frame", knight)).toBe(true);
    expect(matchesWords("frame knight", knight)).toBe(true);
    expect(matchesWords("  KNIGHT   frame ", knight)).toBe(true);
    expect(matchesWords("knight rises", knight)).toBe(false);
  });
  it("matches everything on empty text", () => {
    expect(matchesWords("", knight)).toBe(true);
    expect(matchesWords("   ")).toBe(true);
  });
  it("lets words come from different haystacks, never across them", () => {
    expect(matchesWords("knight 2008", "The Dark Knight", undefined, "2008")).toBe(true);
    expect(matchesWords("kn", "dark", "night")).toBe(false);
  });
  it("uses at most MAX_WORDS words", () => {
    expect(searchWords("a ".repeat(15))).toHaveLength(MAX_WORDS);
  });
});
