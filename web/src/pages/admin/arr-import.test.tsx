// @vitest-environment jsdom
// The one-time import page: Check, the library of the matching kind, the profiles, Preview before Import and the
// report in words. Invented instance, key, paths and titles.
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { ArrImportReport, ArrImportRun } from "@/api/arr-import";
import type { VersionList } from "@/api/settings-versions";
import { mockFetch, renderPage, type RequestRecord } from "@/test/page";
import { ArrImportPage } from "./arr-import";

const record = (key: string, kind: "movie" | "series", name: string) => ({
  id: key.length, key, created_at: "2026-11-01T00:00:00Z", updated_at: "2026-11-01T00:00:00Z", roots: ["/lib/" + key],
  config: { name, mode: "active", kind, anime: null, profile: "hd", library: "/lib/" + key },
});
const versions = {
  source: "db", generation: 3, profiles: {}, format_sets: [], clients: [], indexers: [], providers: [],
  switches: { router_enabled: false, library_enabled: true, notify_enabled: false, versions: [] },
  versions: [record("films", "movie", "Films"), record("shows", "series", "Shows")],
} as unknown as VersionList;

const check = {
  app: "Radarr", version: "5.14.0.9383", titles: 1234, with_files: 1180,
  profiles: [{ id: 1, name: "HD-1080p", titles: 412 }, { id: 2, name: "Ultra-HD", titles: 822 }],
  root_folders: [{ path: "/movies", titles: 1180, readable: false }],
};

const base: ArrImportReport = {
  dry_run: true, app: "Radarr", app_version: "5.14", library: "films", writable: true, tvdb_missing: false, titles: 5,
  added_title: 3, added_library: 1, present: 1, removed: 0, skipped: { profile: 2, no_id: 0, excluded: 0, does_not_fit: 0 },
  failed: 0, files: 4, linkable: 2, linked: 0, existing: 0, files_has_file: 0, not_linked: { not_a_symlink: 2 },
  missing_monitored: 0, problems: [{ title: "Paper Lanterns", path: "/movies/Paper Lanterns/pl.mkv", reason: "not_a_symlink" }],
};

function runOf(body: Record<string, unknown>, report: ArrImportReport): ArrImportRun {
  return {
    id: 1, state: "done", dry_run: body.dry_run as boolean, flavour: body.flavour as string, url: body.url as string,
    library: body.library as string, profiles: body.profiles as number[], paths: (body.paths ?? {}) as Record<string, string>,
    progress: { phase: "importing", done: 5, total: 5 }, report: { ...report, dry_run: body.dry_run as boolean },
    started_at: "2026-11-12T10:00:00Z", finished_at: "2026-11-12T10:00:05Z",
  };
}

/** A server that answers a POST with a finished run of the posted choices, and GET with the last run. */
function server(report: ArrImportReport) {
  let run: ArrImportRun | null = null;
  return mockFetch([
    { method: "GET", path: "/settings/versions", reply: versions },
    { method: "GET", path: "/arr-import", reply: () => ({ run }) },
    { method: "POST", path: "/arr-import/check", reply: check },
    { method: "POST", path: "/arr-import", reply: (req: RequestRecord) => { run = runOf(req.body as Record<string, unknown>, report); return { status: 202, body: { run } }; } },
  ]);
}

async function checked(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText(/Address/), "http://127.0.0.1:7878");
  await user.type(screen.getByLabelText(/API key/), "invented-key");
  await user.click(screen.getByRole("button", { name: "Check" }));
  await screen.findByTestId("arr-check");
}

describe("arr import", () => {
  it("checks the instance, offers libraries of the matching kind and imports only after a Preview of the same choices", async () => {
    const user = userEvent.setup();
    const fetch = server(base);
    renderPage(<ArrImportPage />, "/admin/arr-import");
    await checked(user);
    expect(screen.getByTestId("arr-check").textContent).toBe("Radarr 5.14: 1,234 movies, 1,180 with a file.");
    expect(screen.getByLabelText("HD-1080p (412)")).toBeTruthy();
    const library = screen.getByRole("combobox", { name: /^Library/ });
    expect([...library.querySelectorAll("option")].map((o) => o.value)).toEqual(["films"]);

    const preview = screen.getByRole("button", { name: "Preview" });
    const importButton = screen.getByRole("button", { name: "Import" });
    expect(importButton.hasAttribute("disabled")).toBe(true);
    expect(importButton.getAttribute("title")).toBe("Preview first");
    // No profile ticked: neither Preview nor Import.
    await user.click(screen.getByLabelText("HD-1080p (412)"));
    await user.click(screen.getByLabelText("Ultra-HD (822)"));
    expect(preview.hasAttribute("disabled")).toBe(true);
    await user.click(screen.getByLabelText("HD-1080p (412)"));

    await user.type(screen.getByRole("textbox", { name: /Where does Theatarr see this folder/ }), "/mnt/radarr");
    await user.click(preview);
    expect((await screen.findByTestId("arr-report")).textContent).toContain("Import would add 4 titles to Films: 3 new to Theatarr, 1 already in Theatarr but not in this library. 1 are already in this library and stay as they are.");
    expect(fetch.calls("POST", "/arr-import")[0].body).toEqual({ flavour: "radarr", url: "http://127.0.0.1:7878", api_key: "invented-key", library: "films", profiles: [1], dry_run: true, paths: { "/movies": "/mnt/radarr" } });
    const text = screen.getByTestId("arr-report").textContent;
    expect(text).toContain("Up to 2 files can be linked.");
    expect(text).toContain("Not linkable: 2 ordinary files (for example from Usenet or a local disk).");
    expect(text).toContain("Files that are not linked can leave their titles or episodes missing: if they are monitored, Theatarr looks for them like any other missing title.");
    expect(text).toContain("Skipped: 2 use a quality profile you did not tick.");
    await waitFor(() => expect(importButton.hasAttribute("disabled")).toBe(false));

    // Another choice needs another Preview.
    await user.click(screen.getByLabelText("Ultra-HD (822)"));
    expect(importButton.hasAttribute("disabled")).toBe(true);
    await user.click(screen.getByLabelText("Ultra-HD (822)"));
    expect(importButton.hasAttribute("disabled")).toBe(false);
    await user.click(importButton);
    await waitFor(() => expect(fetch.calls("POST", "/arr-import")).toHaveLength(2));
    expect(fetch.calls("POST", "/arr-import")[1].body).toMatchObject({ dry_run: false, profiles: [1] });
  });

  it("reports what was not linked in words", async () => {
    const user = userEvent.setup();
    const report: ArrImportReport = {
      ...base, linked: 5, existing: 1, files_has_file: 2, missing_monitored: 3, failed: 1,
      not_linked: { not_a_symlink: 2, dangling: 1, no_episodes: 1 },
      problems: [...base.problems, { title: "Hollow Bay", reason: "failed", detail: "TMDb has no such movie" },
        { title: "Tidewater", path: "/tv/Tidewater/S01E01-E02.mkv", reason: "has_file", detail: "kept own files; 1 uncovered episodes (ids, at most 200): [42]" }],
    };
    server(report);
    renderPage(<ArrImportPage />, "/admin/arr-import");
    await checked(user);
    // A real run needs a preview first; the fake server answers both with the same report.
    await user.click(screen.getByRole("button", { name: "Preview" }));
    await screen.findByTestId("arr-report");
    await user.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(screen.getByTestId("arr-report").textContent).toContain("Added 4 titles"));
    const text = screen.getByTestId("arr-report").textContent;
    expect(text).toContain("Added 4 titles to Films (3 new to Theatarr). Linked 5 files; 1 were linked already, and 2 files were left alone because the library has its own file for them.");
    expect(text).toContain("Not linked: 2 ordinary files (for example from Usenet or a local disk) · 1 link whose file is gone from the debrid mount.");
    expect(text).toContain("1 file whose episodes Theatarr could not match.");
    expect(text).toContain("1 title failed (see the list).");
    expect(text).toContain("3 monitored titles still have something missing. Wanted lists what can be searched now.");
    expect(text).toContain("Nothing was changed in Radarr.");
    expect(screen.getByText("Ordinary file (Usenet or local disk)")).toBeTruthy();
    expect(screen.getByText("Failed: TMDb has no such movie")).toBeTruthy();
    expect(screen.getByText("Part of a pack: the library has its own file for some episodes; the others are still missing: kept own files; 1 uncovered episodes (ids, at most 200): [42]")).toBeTruthy();
  });

  it("says first that a Sonarr import without a TVDB key brings no files", async () => {
    const user = userEvent.setup();
    server({ ...base, app: "Sonarr", library: "shows", tvdb_missing: true, linkable: 0, not_linked: { numbering: 4 } });
    renderPage(<ArrImportPage />, "/admin/arr-import");
    await user.selectOptions(await screen.findByRole("combobox", { name: /^Type/ }), "sonarr");
    await checked(user);
    expect([...screen.getByRole("combobox", { name: /^Library/ }).querySelectorAll("option")].map((o) => o.value)).toEqual(["shows"]);
    await user.click(screen.getByRole("button", { name: "Preview" }));
    const report = await screen.findByTestId("arr-report");
    expect(report.querySelector("p")?.textContent).toMatch(/^Theatarr has no TVDB key, so episodes cannot be matched to Sonarr's numbering/);
    expect(report.textContent).not.toContain("can be linked");
  });
});
