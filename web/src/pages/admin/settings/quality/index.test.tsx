// @vitest-environment jsdom
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { overview } from "@/components/settings/fixtures";
import { expectNamedControls, json, mockFetch, renderPage, type RequestRecord } from "@/test/page";
import { QualityPanel, type QualityRecord } from ".";

const list = (records: QualityRecord[], generation = 4) => ({ generation, records });
const profile: QualityRecord = {
  id: 5, key: "web", config: {
    qualities: ["1080p", "2160p", { group: "HD", qualities: ["HDTV-1080p", "HDTV-2160p"] }],
    quality_order: ["1080p", "2160p", { group: "HD", qualities: ["HDTV-1080p", "HDTV-2160p"] }],
    cutoff: "1080p", formats: { "web-id": 5 }, min_score: -1, cutoff_score: 10, min_upgrade_step: 2, upgrades: true,
  },
};
const implementations = [
  { implementation: "ReleaseTitleSpecification", flavours: ["radarr", "sonarr"], fields: [{ name: "value", type: "string" }] },
  { implementation: "IndexerFlagSpecification", flavours: ["radarr"], fields: [{ name: "flag", type: "integer" }, { name: "sceneOnly", type: "boolean" }] },
  { implementation: "SonarrOnlySpecification", flavours: ["sonarr"], fields: [{ name: "season", type: "integer" }] },
];
const baseRoutes = (entity: string, records: QualityRecord[]) => [
  { method: "GET", path: "/settings", reply: overview() },
  { method: "GET", path: `/settings/${entity}`, reply: list(records) },
  { method: "GET", path: "/settings/formats/schema", reply: implementations },
];

afterEach(() => {
  vi.useRealTimers();
});

describe("quality editors", () => {
  it("edits allowed qualities, quality/group order, cutoff and scores, then saves the exact profile config", async () => {
    const fetch = mockFetch([
      ...baseRoutes("profiles", [profile]),
      { method: "PUT", path: "/settings/profiles/5", reply: json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<QualityPanel entity="profiles" />);
    await user.click(await screen.findByRole("button", { name: "Edit web" }));
    expectNamedControls();

    await user.click(screen.getByRole("button", { name: "Move 2160p up" }));
    await user.type(screen.getByRole("textbox", { name: "New quality or group name" }), "UHD");
    await user.type(screen.getByRole("textbox", { name: "Group members (comma separated; empty for one quality)" }), "2160p, 2160p Remux");
    await user.click(screen.getByRole("button", { name: "Add quality or group" }));
    await user.click(screen.getByRole("button", { name: "Move UHD up" }));
    await user.click(screen.getByRole("checkbox", { name: "Allow 1080p" }));

    const cutoff = screen.getByRole("combobox", { name: "Quality cutoff" }) as HTMLSelectElement;
    expect(Array.from(cutoff.options).map((option) => option.value)).not.toContain("1080p");
    await user.selectOptions(cutoff, "UHD");
    const score = screen.getByRole("spinbutton", { name: "web-id" });
    await user.clear(score);
    await user.type(score, "25");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(fetch.calls("PUT", "/settings/profiles/5")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/profiles/5")[0].body).toEqual({
      generation: 4,
      key: "web",
      config: {
        qualities: ["2160p", { group: "UHD", qualities: ["2160p", "2160p Remux"] }, { group: "HD", qualities: ["HDTV-1080p", "HDTV-2160p"] }],
        quality_order: ["2160p", "1080p", { group: "UHD", qualities: ["2160p", "2160p Remux"] }, { group: "HD", qualities: ["HDTV-1080p", "HDTV-2160p"] }],
        cutoff: "UHD", formats: { "web-id": 25 }, min_score: -1, cutoff_score: 10, min_upgrade_step: 2, upgrades: true,
      },
    });
    expect(fetch.calls("PUT", "/settings/profiles/5")[0].headers["x-csrf-token"]).toBe("ui-test-csrf");
  });

  it("confirms a profile change that widens an active version, then saves it again with the token", async () => {
    const effect = "Profile web, used by active version movie-web, raises cutoff_score from 10 to 500: Theatarr may upgrade existing files until their format score reaches it.";
    let puts = 0;
    const fetch = mockFetch([
      ...baseRoutes("profiles", [profile]),
      { method: "PUT", path: "/settings/profiles/5", reply: () => ++puts === 1
        ? json({ error: "explicit confirmation required", effects: [{ version: "movie-web", effect }], confirmation: { generation: 5, token: "profile-confirmation", effects: [{ version: "movie-web", effect }] } }, 422)
        : json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<QualityPanel entity="profiles" />);
    await user.click(await screen.findByRole("button", { name: "Edit web" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    const dialog = await screen.findByRole("dialog", { name: "Confirm what this change enables" });
    expect(within(dialog).getByText(effect)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "Confirm and save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/profiles/5")).toHaveLength(2));
    const [first, second] = fetch.calls("PUT", "/settings/profiles/5");
    expect(first.headers["x-confirmation-token"]).toBeUndefined();
    expect(second.headers["x-confirmation-token"]).toBe("profile-confirmation");
    expect(second.body).toEqual(first.body);
  });

  it("uses the API implementation schema, toggles specifications, imports arr JSON and links to the matching export", async () => {
    const original = {
      id: 12, key: "web-format", config: { flavour: "radarr", trash_id: "old-id", include_when_renaming: false },
      specification: { name: "Old format", includeCustomFormatWhenRenaming: false, specifications: [{ name: "Title", implementation: "ReleaseTitleSpecification", negate: false, required: false, fields: { value: "WEB" } }] },
    } as QualityRecord;
    const imported = {
      name: "Imported format", includeCustomFormatWhenRenaming: true, trash_id: "new-id",
      specifications: [{ name: "Flag", implementation: "IndexerFlagSpecification", negate: true, required: true, fields: { flag: 7, sceneOnly: true } }],
    };
    const fetch = mockFetch([
      ...baseRoutes("formats", [original]),
      { method: "PUT", path: "/settings/formats/12", reply: json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<QualityPanel entity="formats" />);
    const exportLink = await screen.findByRole("link", { name: "Export arr JSON" });
    expect(exportLink.getAttribute("href")).toBe("/api/theatarr/v1/settings/formats/12/export");
    expect(exportLink.getAttribute("download")).toBe("web-format.json");
    await user.click(screen.getByRole("button", { name: "Edit web-format" }));
    expectNamedControls();

    expect(screen.getByRole("textbox", { name: "value" })).toBeTruthy();
    const implementation = screen.getByRole("combobox", { name: "Implementation" });
    await user.selectOptions(implementation, "IndexerFlagSpecification");
    expect(screen.getByRole("spinbutton", { name: "flag" })).toBeTruthy();
    expect(screen.getByRole("checkbox", { name: "sceneOnly" })).toBeTruthy();
    expect(screen.queryByRole("spinbutton", { name: "season" })).toBeNull();
    await user.click(screen.getByRole("checkbox", { name: "Negate" }));
    await user.click(screen.getByRole("checkbox", { name: "Required" }));
    expect((screen.getByRole("checkbox", { name: "Negate" }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("checkbox", { name: "Required" }) as HTMLInputElement).checked).toBe(true);
    await user.click(screen.getByRole("button", { name: "Add specification" }));
    const second = screen.getByRole("group", { name: "Specification 2" });
    await user.click(within(second).getByRole("button", { name: "Remove specification" }));

    fireEvent.change(screen.getByRole("textbox", { name: "Import arr custom format JSON (replaces this draft)" }), { target: { value: JSON.stringify(imported) } });
    expect((screen.getByRole("textbox", { name: "Format name" }) as HTMLInputElement).value).toBe("Imported format");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/formats/12")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/formats/12")[0].body).toEqual({
      generation: 4, key: "web-format", config: { flavour: "radarr", trash_id: "new-id", include_when_renaming: true }, specification: imported,
    });
  });

  it("saves a format set with the edited format references", async () => {
    const set: QualityRecord = { id: 7, key: "web-set", config: { flavour: "radarr", formats: { "WEB Tier 01": { ref: "web" } } } };
    const fetch = mockFetch([
      ...baseRoutes("format_sets", [set]),
      { method: "PUT", path: "/settings/format_sets/7", reply: json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<QualityPanel entity="format_sets" />);
    await user.click(await screen.findByRole("button", { name: "Edit web-set" }));
    const formats = screen.getByRole("textbox", { name: /Formats by display name/ });
    fireEvent.change(formats, { target: { value: JSON.stringify({ "WEB Tier 01": { ref: "web" }, "WEB Tier 02": { ref: "web-2" } }) } });
    await user.selectOptions(screen.getByRole("combobox", { name: "Flavour" }), "sonarr");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/format_sets/7")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/format_sets/7")[0].body).toEqual({
      generation: 4, key: "web-set", config: { flavour: "sonarr", formats: { "WEB Tier 01": { ref: "web" }, "WEB Tier 02": { ref: "web-2" } } },
    });
  });

  it("debounces naming preview, aborts a superseded request, and shows the sample names", async () => {
    let previewCount = 0;
    let firstSignal: AbortSignal | undefined;
    const fetch = mockFetch([
      ...baseRoutes("naming", []),
      { method: "POST", path: "/settings/naming/preview", reply: (request: RequestRecord) => {
        previewCount++;
        if (previewCount === 1) {
          firstSignal = request.signal;
          return new Promise((_resolve, reject) => request.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true }));
        }
        return json({ generation: 4, source: "db", folder: "Movies/New title", file: "New title (2026).mkv" });
      } },
    ]);
    renderPage(<QualityPanel entity="naming" />);
    const add = await screen.findByRole("button", { name: "Add library naming" });
    vi.useFakeTimers();
    fireEvent.click(add);
    fireEvent.change(screen.getByRole("textbox", { name: "Library key" }), { target: { value: "movie-web" } });
    await act(async () => { await vi.advanceTimersByTimeAsync(400); });
    expect(fetch.calls("POST", "/settings/naming/preview")).toHaveLength(1);
    fireEvent.change(screen.getByRole("textbox", { name: "File name" }), { target: { value: " - {Quality}" } });
    expect(firstSignal?.aborted).toBe(true);
    await act(async () => { await vi.advanceTimersByTimeAsync(400); await Promise.resolve(); });
    expect(fetch.calls("POST", "/settings/naming/preview")).toHaveLength(2);
    expect(fetch.calls("POST", "/settings/naming/preview")[1].body).toMatchObject({ generation: 4, version: "movie-web", naming: { file: " - {Quality}" } });
    expect(screen.getByText("Folder: Movies/New title")).toBeTruthy();
    expect(screen.getByText("File: New title (2026).mkv")).toBeTruthy();
    vi.useRealTimers();
  });

  it("offers no copy notice and no naming taken from an arr or a library", async () => {
    mockFetch([...baseRoutes("naming", [{ id: 2, key: "movie-remux-mm", config: { colon: "dash" } }]), { method: "POST", path: "/settings/naming/preview", reply: json({ folder: "f", file: "f.mkv" }) }]);
    const user = userEvent.setup();
    renderPage(<QualityPanel entity="naming" />);
    await user.click(await screen.findByRole("button", { name: "Edit movie-remux-mm" }));
    expect(await screen.findByRole("textbox", { name: "File name" })).toBeTruthy();
    expect(screen.queryByRole("textbox", { name: "Naming from arr" })).toBeNull();
    expect(screen.queryByRole("textbox", { name: "Naming from library" })).toBeNull();
    expect(screen.queryByTestId("copy-naming")).toBeNull();
  });

  it("shows a naming preview refusal in the editor", async () => {
    const fetch = mockFetch([
      ...baseRoutes("naming", []),
      { method: "POST", path: "/settings/naming/preview", reply: json({ error: "naming preview unavailable" }, 503) },
    ]);
    renderPage(<QualityPanel entity="naming" />);
    const add = await screen.findByRole("button", { name: "Add library naming" });
    vi.useFakeTimers();
    fireEvent.click(add);
    fireEvent.change(screen.getByRole("textbox", { name: "Library key" }), { target: { value: "movie-web" } });
    await act(async () => { await vi.advanceTimersByTimeAsync(400); await Promise.resolve(); });
    expect(screen.getByRole("alert")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("naming preview unavailable");
    expect(fetch.calls("POST", "/settings/naming/preview")).toHaveLength(1);
    vi.useRealTimers();
  });

  it("rebases on the latest record after 409 and explains when that record was deleted", async () => {
    let reads = 0;
    let writes = 0;
    const remote = { ...profile, config: { ...profile.config, format_set: "remote-set" } };
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview() },
      { method: "GET", path: "/settings/profiles", reply: () => json(list(++reads === 1 ? [profile] : [remote], reads === 1 ? 4 : 5)) },
      { method: "GET", path: "/settings/formats/schema", reply: implementations },
      { method: "PUT", path: "/settings/profiles/5", reply: () => ++writes === 1
        ? json({ error: "settings changed since this generation was read; reload and retry", generation: 5 }, 409)
        : json({ generation: 6 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<QualityPanel entity="profiles" />);
    await user.click(await screen.findByRole("button", { name: "Edit web" }));
    await user.type(screen.getByRole("textbox", { name: "Format set key" }), "local-set");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByRole("alert");
    await user.click(screen.getByRole("button", { name: "Reload, discard my changes" }));
    await waitFor(() => expect((screen.getByRole("textbox", { name: "Format set key" }) as HTMLInputElement).value).toBe("remote-set"));
    expect(screen.getByRole("heading", { name: /Edit profiles/ })).toBeTruthy();
    expect(fetch.calls("PUT", "/settings/profiles/5")[0].body).toMatchObject({ generation: 4, config: { format_set: "local-set" } });
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/profiles/5")).toHaveLength(2));
    expect(fetch.calls("PUT", "/settings/profiles/5")[1].body).toMatchObject({ generation: 5, config: { format_set: "remote-set" } });
  });

  it("keeps the editor open with an explanation when a 409 reload finds the record deleted", async () => {
    let reads = 0;
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview() },
      { method: "GET", path: "/settings/profiles", reply: () => json(list(++reads === 1 ? [profile] : [], reads === 1 ? 4 : 5)) },
      { method: "GET", path: "/settings/formats/schema", reply: implementations },
      { method: "PUT", path: "/settings/profiles/5", reply: json({ error: "settings changed since this generation was read; reload and retry", generation: 5 }, 409) },
    ]);
    const user = userEvent.setup();
    renderPage(<QualityPanel entity="profiles" />);
    await user.click(await screen.findByRole("button", { name: "Edit web" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByRole("alert");
    await user.click(screen.getByRole("button", { name: "Reload, keep my changes" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("This record was deleted"));
    expect(fetch.calls("GET", "/settings/profiles")).toHaveLength(2);
  });

  it("shows the server uses list when deleting an in-use record", async () => {
    const fetch = mockFetch([
      ...baseRoutes("profiles", [profile]),
      { method: "DELETE", path: "/settings/profiles/5", reply: json({ error: "record is in use", uses: ["version movie-web", "format set web-set"] }, 422) },
    ]);
    const user = userEvent.setup();
    renderPage(<QualityPanel entity="profiles" />);
    await user.click(await screen.findByRole("button", { name: "Edit web" }));
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Confirm delete" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("record is in use"));
    expect(screen.getByText(/version movie-web/)).toBeTruthy();
    expect(screen.getByText(/format set web-set/)).toBeTruthy();
    expect(fetch.calls("DELETE", "/settings/profiles/5")[0].body).toMatchObject({ generation: 4, key: "web" });
  });
});
