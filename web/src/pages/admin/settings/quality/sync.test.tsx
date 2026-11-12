// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { overview } from "@/components/settings/fixtures";
import { json, mockFetch, renderPage } from "@/test/page";
import { QualityPanel } from ".";
import { SyncSources } from "./sync";

const preview = {
  id: 0, generation: 4, source: "trash-main", commit: "abc123", state: "pending",
  added: [{ key: "New Format", trash_id: "trash-new", reason: "new guide entry" }],
  spec_changed: [{ key: "WEB Tier 01", trash_id: "trash-web", reason: "definition changed" }],
  retired: [{ key: "Retired Format", trash_id: "trash-old", reason: "removed upstream" }],
  profiles_changed: ["web"], scores: [{ profile: "web", format: "WEB Tier 01", before: 5, after: 10 }],
  held: [{ profile: "web", format: "WEB Tier 02", ours: 1300, last: 1400, trash: 1450 }],
  impact: {
    files: 3, changed: 2, score_changed: 2, cutoff_changed: 1, upgrade_changed: 1,
    examples: [{ title: "Example Movie", version: "movie-web", file: "Example.Movie.1080p.mkv", before_score: 5, after_score: 10, before_cutoff_met: false, after_cutoff_met: true, before_upgrade: false, after_upgrade: true }],
  },
};
const sources = { generation: 4, records: [{ id: 9, key: "trash-main", config: { kind: "trash", flavour: "radarr" } }] };

describe("TRaSH sync", () => {
  it("previews a source as a dry run: changes, kept scores and the effect on files, with nothing to approve", async () => {
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview() },
      { method: "GET", path: "/settings/sync_sources", reply: sources },
      { method: "GET", path: "/settings/formats/schema", reply: [] },
      { method: "GET", path: "/settings/sync/changes", reply: [] },
      { method: "GET", path: "/settings/sync/runs", reply: [] },
      { method: "POST", path: "/settings/sync/preview", reply: preview },
    ]);
    const user = userEvent.setup();
    renderPage(<QualityPanel entity="sync_sources" />);
    await user.click(await screen.findByRole("button", { name: "Preview" }));
    expect(await screen.findByText("Added: 1")).toBeTruthy();
    expect(screen.getByText("No runs yet.")).toBeTruthy();
    expect(fetch.calls("POST", "/settings/sync/preview")[0].body).toMatchObject({ generation: 4, key: "trash-main" });
    for (const text of [
      "New Format new guide entry", "Specifications changed: 1", "WEB Tier 01 definition changed",
      "Retired (retained): 1", "Retired Format removed upstream", "web / WEB Tier 01: 5 → 10",
      "Kept yours (the source changed them)", "web / WEB Tier 02: yours 1300 · TRaSH now 1450", "Effect on existing files",
      "3 files evaluated; 2 scores, 1 cutoff states and 1 upgrade eligibility states would change.",
      "A preview saves nothing. The next run applies these changes.",
    ]) expect(screen.getByText(text)).toBeTruthy();
    const example = screen.getAllByRole("listitem").find((item) => item.textContent?.includes("Example Movie / movie-web: Example.Movie.1080p.mkv"));
    expect(example?.textContent).toContain("Score 5 → 10; cutoff met false → true; upgrade eligible false → true");
    expect(screen.queryByRole("button", { name: /Approve/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Keep our/ })).toBeNull();
    expect(screen.queryByText(/updates waiting/)).toBeNull();
  });

  it("lists the update log with counts, and an entry expands to its changes", async () => {
    const entry = { ...preview, id: 7, state: "applied", applied_at: Date.UTC(2026, 10, 12), commit: "c".repeat(40), held_fields: [{ profile: "household-web", field: "min_score", ours: 7, last: 5, upstream: 9 }],
      fields: [{ profile: "household-web", field: "sizes/Remux-2160p", before: null, after: { min: 187.4, preferred: 0, max: 0 } }] };
    mockFetch([
      { method: "GET", path: "/settings", reply: overview() },
      { method: "GET", path: "/settings/sync_sources", reply: sources },
      { method: "GET", path: "/settings/formats/schema", reply: [] },
      { method: "GET", path: "/settings/sync/changes", reply: [entry] },
      { method: "GET", path: "/settings/sync/runs", reply: [] },
    ]);
    const user = userEvent.setup();
    renderPage(<QualityPanel entity="sync_sources" />);
    const line = await screen.findByText(/trash-main · cccccccccccc: /);
    expect(line.textContent).toContain(": 1 formats updated, 1 added, 1 scores changed, 1 profile values changed, 2 kept yours");
    await user.click(line);
    expect(screen.getByText("No longer in TRaSH (kept as it is): 1")).toBeTruthy();
    expect(screen.getByText("household-web / min_score: yours 7 · source now 9")).toBeTruthy();
    expect(screen.getByText("household-web / sizes/Remux-2160p: not set → min 187.4 · preferred unlimited · max unlimited MB/min")).toBeTruthy();
  });

  it("shows a tracked source's branch and last good commit, and the editor defaults a tracked source to weekly with no mode", async () => {
    const tracked = { id: 9, key: "trash-guides", config: { kind: "trash", flavour: "radarr", origin: "github", repo: "TRaSH-Guides/Guides", branch: "master", schedule: "168h", enabled: true } };
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview() },
      { method: "GET", path: "/settings/sync_sources", reply: { generation: 4, records: [tracked] } },
      { method: "GET", path: "/settings/formats/schema", reply: [] },
      { method: "GET", path: "/settings/sync/changes", reply: [] },
      { method: "GET", path: "/settings/sync/runs", reply: [
        { id: 3, source: "trash-guides", started_at: 3, commit: "", outcome: "error", error: "could not read the newest commit", review_id: null },
        { id: 2, source: "trash-guides", started_at: 2, commit: "b".repeat(40), outcome: "applied", error: "", review_id: 7 },
      ] },
      { method: "POST", path: "/settings/sync_sources", reply: json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<QualityPanel entity="sync_sources" />);
    const line = await screen.findByTestId("sync-source-trash-guides");
    await waitFor(() => expect(line.textContent).toContain("last commit bbbbbbbbbbbb"));
    expect(line.textContent).toContain("trash · tracks TRaSH-Guides/Guides master · last commit");
    expect(line.textContent).toContain("last run failed: could not read the newest commit");

    await user.click(screen.getByRole("button", { name: "Add sync sources" }));
    await user.type(screen.getByLabelText("Key"), "weekly");
    await user.selectOptions(screen.getByLabelText(/TRaSH origin/), "github");
    expect(screen.queryByLabelText("Sync mode")).toBeNull();
    expect((screen.getByLabelText("Schedule (default 168h, weekly)") as HTMLInputElement).value).toBe("168h");
    expect(screen.queryByLabelText(/Pinned git archive URL/)).toBeNull();
    expect(screen.getByLabelText("Tracked branch (default master)")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: /^Save/ }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/sync_sources")).toHaveLength(1));
    const body = fetch.calls("POST", "/settings/sync_sources")[0].body as { config: Record<string, unknown> };
    expect(body.config).toMatchObject({ origin: "github", schedule: "168h" });
    expect(body.config.mode).toBeUndefined();
    expect(body.config.archive_url).toBeUndefined();
  });

  it("offers the quality sizes switch for a Recyclarr source, on by default", async () => {
    mockFetch([
      { method: "GET", path: "/settings", reply: overview() },
      { method: "GET", path: "/settings/sync_sources", reply: { generation: 4, records: [] } },
      { method: "GET", path: "/settings/formats/schema", reply: [] },
      { method: "GET", path: "/settings/sync/changes", reply: [] },
      { method: "GET", path: "/settings/sync/runs", reply: [] },
    ]);
    const user = userEvent.setup();
    renderPage(<QualityPanel entity="sync_sources" />);
    await user.click(await screen.findByRole("button", { name: "Add sync sources" }));
    expect(screen.queryByLabelText(/Apply quality sizes/)).toBeNull();
    await user.selectOptions(screen.getByLabelText("Source"), "recyclarr");
    expect((screen.getByLabelText(/Apply quality sizes/) as HTMLInputElement).checked).toBe(true);
  });
});

it("does not show an empty log while loading or failed", async () => {
  mockFetch([
    { method: "GET", path: "/settings/sync/changes", reply: () => new Promise(() => {}) },
    { method: "GET", path: "/settings/sync/runs", reply: [] },
  ]);
  const view = renderPage(<SyncSources records={[]} generation={1} onChanged={async () => {}} />);
  expect(screen.getByText("Loading")).toBeTruthy();
  expect(screen.queryByText("No updates applied yet.")).toBeNull();
  view.unmount();
  mockFetch([
    { method: "GET", path: "/settings/sync/changes", reply: json({ error: "sync failed" }, 500) },
    { method: "GET", path: "/settings/sync/runs", reply: [] },
  ]);
  renderPage(<SyncSources records={[]} generation={1} onChanged={async () => {}} />);
  expect(await screen.findByText(/sync failed/)).toBeTruthy();
  expect(screen.queryByText("No updates applied yet.")).toBeNull();
});

it.each([409, 503])("says why quality sync is unavailable on %s", async (status) => {
  mockFetch([
    { method: "GET", path: "/settings/sync/changes", reply: json({ error: "off" }, status) },
    { method: "GET", path: "/settings/sync/runs", reply: [] },
  ]);
  renderPage(<SyncSources records={[]} generation={1} onChanged={async () => {}} />);
  expect(await screen.findByText(status === 409 ? "Quality sync is not available while settings come from YAML" : "Quality sync is not running")).toBeTruthy();
});
