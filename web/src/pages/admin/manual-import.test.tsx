import { createElement as h } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Schemas } from "@/api/client";
import { importRow, listedWords, ManualImportFiles } from "./manual-import";

const candidate: Schemas["ManualImportCandidate"] = {
  id: 1, path: "/mount/Show/file.mkv", size: 123,
  series: { id: 100010, title: "Show" }, episodes: [{ id: 102 }],
  quality: { quality: { id: 3, name: "WEBDL-1080p" }, revision: { version: 2, real: 1 } },
  languages: [{ id: 2, name: "French" }], releaseGroup: "CHOSEN", indexerFlags: 4,
  customFormatScore: 321, rejections: [{ type: "permanent", reason: "Not an upgrade" }],
};
const options: Schemas["ManualImportOptions"] = {
  flavour: "sonarr", titles: [{ id: 100010, name: "Show", episodes: [{ id: 102, name: "Second", season: 1, number: 2 }] }],
  qualities: [{ id: 3, name: "WEBDL-1080p" }], languages: [{ id: 2, name: "French" }],
};

describe("manual import", () => {
  it("keeps the client choices and revision when preparing a reviewed candidate", () => {
    const row = importRow(candidate);
    expect(row.selected).toBe(true);
    expect(row.choice).toMatchObject({ path: candidate.path, seriesId: 100010, episodeIds: [102],
      quality: candidate.quality, languages: candidate.languages, releaseGroup: "CHOSEN", indexerFlags: 4 });
  });
  it("renders decisions and labelled per-file editors, disabled during submission", () => {
    const html = renderToStaticMarkup(h(ManualImportFiles, { rows: [importRow(candidate)], options, disabled: true, change: () => {} }));
    for (const label of ["Title", "Episodes", "Quality", "Revision", "Languages", "Release group", "Not an upgrade", "Needs review", "Format score: 321"]) expect(html).toContain(label);
    expect(html).toContain('aria-label="Import /mount/Show/file.mkv"');
    expect(html).toContain('<fieldset disabled=""');
    expect(html).toContain('value="102" selected=""');
    expect(html).toContain('value="CHOSEN"');
  });
  it("uses the themed select for single choices", () => {
    const html = renderToStaticMarkup(h(ManualImportFiles, { rows: [importRow(candidate)], options, disabled: false, change: () => {} }));
    expect(html).toContain('data-slot="select"');
  });
  it("points an unknown download hash at Add by magnet or hash", () => {
    expect(listedWords(0, "downloadId")).toBe("No download of Theatarr's has this hash. To add a new magnet or hash, open the title's Last search and use Add by magnet or hash.");
    expect(listedWords(0, "folder")).toBe("0 candidate file(s)");
    expect(listedWords(2, "downloadId")).toBe("2 candidate file(s)");
  });
});
