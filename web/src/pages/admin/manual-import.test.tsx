// @vitest-environment jsdom
import { createElement as h } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import type { Schemas } from "@/api/client";
import { ApiError } from "@/api/client";
import { importRow, listedWords, listingError, ManualImportFiles, ManualImportPage } from "./manual-import";

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
  it("says what a listing found, and a readable folder without video files as such", () => {
    expect(listedWords(0, "folder")).toBe("No video files in this folder");
    expect(listedWords(1, "folder")).toBe("1 candidate file");
    expect(listedWords(2, "downloadId")).toBe("2 candidate files");
  });
  it("says the server's reason for an empty listing, and points an unknown download hash at Add by magnet or hash", () => {
    expect(listingError(new ApiError(404, "No download with this hash in this library"), "downloadId")).toBe("No download with this hash in this library. To add a new magnet or hash, open the title's Last search and use Add by magnet or hash.");
    expect(listingError(new ApiError(409, "Its files cannot be listed now: they are not on the mount yet"), "downloadId")).toBe("Its files cannot be listed now: they are not on the mount yet");
    expect(listingError(new ApiError(422, "The folder cannot be read: no such file or directory"), "folder")).toBe("The folder cannot be read: no such file or directory");
  });
});

describe("manual import page", () => {
  const movieOptions = { ...options, flavour: "radarr", titles: [{ id: 100001, name: "Alpha", episodes: [] }] };
  const movieCandidate = { ...candidate, series: undefined, episodes: undefined, movie: { id: 100001, title: "Alpha" }, path: "/mount/Alpha/a.mkv" };
  const page = (listings: unknown[]) => {
    let n = 0;
    const api = mockFetch([
      { method: "GET", path: "/policy", reply: { versions: [{ key: "movie-web" }] } },
      { method: "GET", path: "/manualimport/options", reply: movieOptions },
      { method: "GET", path: "/manualimport", reply: () => listings[Math.min(n++, listings.length - 1)] },
    ]);
    renderPage(h(ManualImportPage));
    return api;
  };
  const pick = async () => {
    const library = await screen.findByRole("combobox", { name: "Library" });
    await waitFor(() => expect(library.querySelectorAll("option")).toHaveLength(2));
    fireEvent.change(library, { target: { value: "movie-web" } });
  };
  const listFiles = async () => {
    const button = screen.getByRole("button", { name: "List files" });
    await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(button);
  };

  it("clears the old result on a source change and keeps each source's own value", async () => {
    page([json({ error: "No download with this hash in this library" }, 404)]);
    await pick();
    fireEvent.change(screen.getByRole("combobox", { name: "Source" }), { target: { value: "downloadId" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Download hash" }), { target: { value: "abc" } });
    await listFiles();
    expect((await screen.findByRole("alert")).textContent).toContain("No download with this hash in this library. To add a new magnet");
    fireEvent.change(screen.getByRole("combobox", { name: "Source" }), { target: { value: "folder" } });
    expect(screen.queryByRole("alert")).toBeNull();
    expect((screen.getByRole("textbox", { name: "Folder under the mount" }) as HTMLInputElement).value).toBe("");
    fireEvent.change(screen.getByRole("combobox", { name: "Source" }), { target: { value: "downloadId" } });
    expect((screen.getByRole("textbox", { name: "Download hash" }) as HTMLInputElement).value).toBe("abc");
  });

  it("clears the old error when the library changes", async () => {
    page([json({ error: "The folder cannot be read: permission denied" }, 422)]);
    await pick();
    fireEvent.change(screen.getByRole("textbox", { name: "Folder under the mount" }), { target: { value: "/mount/Alpha" } });
    await listFiles();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toBe("The folder cannot be read: permission denied");
    // It looks like an error, not like instructions.
    expect(alert.className).toContain("bg-[var(--bad-soft)]");
    expect(alert.className).toContain("text-[var(--bad-ink)]");
    fireEvent.change(screen.getByRole("combobox", { name: "Library" }), { target: { value: "" } });
    expect(screen.queryByRole("alert")).toBeNull();
    // Without an error nothing is drawn where it was.
    expect(alert.className).toBe("");
  });

  it("leaves no candidates of an earlier listing when a later one fails", async () => {
    page([[movieCandidate], json({ error: "The folder cannot be read: permission denied" }, 422)]);
    await pick();
    fireEvent.change(screen.getByRole("textbox", { name: "Folder under the mount" }), { target: { value: "/mount/Alpha" } });
    await listFiles();
    expect(await screen.findByRole("button", { name: "Import selected files" })).toBeTruthy();
    await listFiles();
    expect((await screen.findByRole("alert")).textContent).toBe("The folder cannot be read: permission denied");
    expect(screen.queryByRole("button", { name: "Import selected files" })).toBeNull();
  });
});
