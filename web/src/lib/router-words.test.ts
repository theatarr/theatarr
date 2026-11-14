import { expect, it } from "vitest";
import { attemptMessageWords } from "./router-words";

it("words the attempt messages that name a setting by its key", () => {
  expect(attemptMessageWords("min_gap 20s since the last add")).toBe("Waiting for the 20 s minimum gap between adds");
  expect(attemptMessageWords("min_gap 1m0s since the last add")).toBe("Waiting for the 1 min minimum gap between adds");
  expect(attemptMessageWords("adds_per_minute 5 used")).toBe("All 5 adds a minute are used; waiting for the next minute");
  expect(attemptMessageWords("uncached_adds_per_hour 10 used")).toBe("All 10 uncached adds an hour are used; waiting for the next hour");
  expect(attemptMessageWords("known uncached here and cached_only is on")).toBe("Skipped: known not cached here, and Only add cached releases is on");
  expect(attemptMessageWords("the provider's cache check says uncached")).toBe("the provider's cache check says uncached");
  expect(attemptMessageWords(undefined)).toBe("");
});
