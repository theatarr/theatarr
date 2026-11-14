import { expect, it } from "vitest";
import { chosenReleaseRows } from "./release-dates";

it("summarises the chosen first date per type, not every country-less row", () => {
  // A Radarr import then a TMDb refresh: two country-less digital rows; the refresh's is the chosen one.
  const rows = [
    { type: "digital", date: "2026-01-10", country: "", source: "radarr_4k_web" },
    { type: "digital", date: "2026-02-03", country: "", source: "tmdb-art" },
    { type: "theatrical", date: "2025-11-20", country: "US", source: "tmdb" },
    { type: "theatrical", date: "2025-11-20", country: "", source: "tmdb-art" },
    { type: "physical", date: "2026-03-01", country: "DE", source: "tmdb" },
  ];
  const got = chosenReleaseRows(rows, { in_cinemas: "2025-11-20", digital_release: "2026-02-03" });
  expect(got.map((r) => `${r.type} ${r.date} ${r.source}`)).toEqual(["theatrical 2025-11-20 tmdb-art", "digital 2026-02-03 tmdb-art"]);
});

it("picks the refreshed TMDb row over an arr row of the same day, and a country-less row over the US one", () => {
  const rows = [
    { type: "digital", date: "2026-02-03", country: "US", source: "tmdb" },
    { type: "digital", date: "2026-02-03", country: "", source: "radarr_4k_web" },
    { type: "digital", date: "2026-02-03", country: "", source: "tmdb-art" },
    { type: "theatrical", date: "2025-11-20", country: "US", source: "tmdb" },
    { type: "theatrical", date: "2025-11-20", country: "", source: "radarr_4k_web" },
  ];
  const got = chosenReleaseRows(rows, { in_cinemas: "2025-11-20", digital_release: "2026-02-03" });
  expect(got.map((r) => `${r.type} ${r.source}`)).toEqual(["theatrical radarr_4k_web", "digital tmdb-art"]);
});
