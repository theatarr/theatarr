import { expect, it } from "vitest";
import { episodesWords } from "./row-bits";

it("episodesWords: a few episodes as they are; more with the count first and runs as ranges", () => {
  expect(episodesWords(["S01E01", "S01E02"])).toBe("S01E01 S01E02");
  expect(episodesWords(Array.from({ length: 11 }, (_, i) => `S01E${String(i + 1).padStart(2, "0")}`))).toBe("11 episodes · S01E01–S01E11");
  expect(episodesWords(["S01E01", "S01E02", "S01E05", "S02E01", "S02E02"])).toBe("5 episodes · S01E01–S01E02 S01E05 S02E01–S02E02");
  expect(episodesWords(undefined)).toBe("");
});
