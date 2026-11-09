// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { overview } from "@/components/settings/fixtures";
import { json, mockFetch, renderPage } from "@/test/page";
import { ReviewsPage } from "../../reviews";
import { QualityPanel } from ".";

const pending = {
  id: 42, generation: 4, source: "trash-main", commit: "abc123", state: "pending",
  added: [{ key: "New Format", trash_id: "trash-new", reason: "new guide entry" }],
  spec_changed: [{ key: "WEB Tier 01", trash_id: "trash-web", reason: "definition changed" }],
  retired: [{ key: "Retired Format", trash_id: "trash-old", reason: "removed upstream" }],
  profiles_changed: ["web"], scores: [{ profile: "web", format: "WEB Tier 01", before: 5, after: 10 }],
  impact: {
    files: 3, changed: 2, score_changed: 2, cutoff_changed: 1, upgrade_changed: 1,
    examples: [{ title: "Example Movie", version: "movie-web", file: "Example.Movie.1080p.mkv", before_score: 5, after_score: 10, before_cutoff_met: false, after_cutoff_met: true, before_upgrade: false, after_upgrade: true }],
  },
};

describe("TRaSH sync review", () => {
  it("shows added, changed, retired, score and decision impact, then approves the reviewed generation", async () => {
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview() },
      { method: "GET", path: "/settings/sync_sources", reply: { generation: 4, records: [{ id: 9, key: "trash-main", config: { kind: "trash", flavour: "radarr", mode: "review" } }] } },
      { method: "GET", path: "/settings/formats/schema", reply: [] },
      { method: "GET", path: "/settings/sync/reviews", reply: [pending] },
      { method: "GET", path: "/settings/sync/runs", reply: [] },
      { method: "POST", path: "/settings/sync/reviews/42/apply", reply: json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<ReviewsPage />, "/admin/reviews/quality");
    await user.click(await screen.findByRole("button", { name: "Review changes" }));

    for (const text of [
      "Added: 1", "New Format new guide entry", "Specifications changed: 1", "WEB Tier 01 definition changed",
      "Retired (retained): 1", "Retired Format removed upstream", "web / WEB Tier 01: 5 → 10",
      "3 files evaluated; 2 scores, 1 cutoff states and 1 upgrade eligibility states would change.",
    ]) expect(screen.getByText(text)).toBeTruthy();
    const example = screen.getAllByRole("listitem").find((item) => item.textContent?.includes("Example Movie / movie-web: Example.Movie.1080p.mkv"));
    expect(example?.textContent).toContain("Score 5 → 10; cutoff met false → true; upgrade eligible false → true");

    await user.click(screen.getByRole("button", { name: "Approve this preview" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/sync/reviews/42/apply")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/sync/reviews/42/apply")[0].body).toEqual({ generation: 4 });
    expect(screen.getByTestId("quality-sync-announcer").textContent).toContain("Sync action finished.");
  });

  it("confirms a review that widens an active version before applying it with the token", async () => {
    const effect = "Profile web, used by active version movie-web, turns upgrades on: Theatarr may replace existing links with better releases up to the cutoff \"WEB 2160p\" and cutoff_score 10000.";
    let applies = 0;
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview() },
      { method: "GET", path: "/settings/sync_sources", reply: { generation: 4, records: [] } },
      { method: "GET", path: "/settings/formats/schema", reply: [] },
      { method: "GET", path: "/settings/sync/reviews", reply: [pending] },
      { method: "GET", path: "/settings/sync/runs", reply: [] },
      { method: "POST", path: "/settings/sync/reviews/42/apply", reply: () => ++applies === 1
        ? json({ error: "explicit confirmation required", effects: [{ version: "movie-web", effect }], confirmation: { generation: 5, token: "review-confirmation", effects: [{ version: "movie-web", effect }] } }, 422)
        : json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<ReviewsPage />, "/admin/reviews/quality");
    await user.click(await screen.findByRole("button", { name: "Review changes" }));
    await user.click(screen.getByRole("button", { name: "Approve this preview" }));
    const dialog = await screen.findByRole("dialog", { name: "Confirm what this change enables" });
    expect(within(dialog).getByText(effect)).toBeTruthy();
    await waitFor(() => expect(screen.getByTestId("quality-sync-announcer").textContent).toContain("Confirmation needed"));
    expect(screen.getByTestId("quality-sync-announcer").textContent).not.toContain("Sync action finished.");
    await user.click(within(dialog).getByRole("button", { name: "Confirm and save" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/sync/reviews/42/apply")).toHaveLength(2));
    expect(fetch.calls("POST", "/settings/sync/reviews/42/apply")[1].headers["x-confirmation-token"]).toBe("review-confirmation");
  });

  it("lists held scores with take or keep per row and sends each choice at the current generation", async () => {
    const held = {
      ...pending, id: 43, kind: "scores", added: [], spec_changed: [], retired: [], profiles_changed: [], scores: [],
      impact: { files: 0, changed: 0, score_changed: 0, cutoff_changed: 0, upgrade_changed: 0, examples: [] },
      held: [
        { profile: "anime", format: "Anime BD Tier 01", ours: 1300, last: 1400, trash: 1450 },
        { profile: "anime", format: "Anime Raws", ours: null, last: null, trash: -10000 },
      ],
    };
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview({ generation: 6 }) },
      { method: "GET", path: "/settings/sync_sources", reply: { generation: 6, records: [] } },
      { method: "GET", path: "/settings/formats/schema", reply: [] },
      { method: "GET", path: "/settings/sync/reviews", reply: [held] },
      { method: "GET", path: "/settings/sync/runs", reply: [] },
      { method: "POST", path: "/settings/sync/reviews/43/scores", reply: json({ generation: 7 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<ReviewsPage />, "/admin/reviews/quality");
    expect(await screen.findByText(/2 held score\(s\)/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Review held scores" }));
    expect(screen.getByText("anime / Anime BD Tier 01: ours 1300 · TRaSH 1400 → 1450")).toBeTruthy();
    expect(screen.getByText("anime / Anime Raws: ours not scored · TRaSH -10000 (first run after binding)")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Approve this preview" })).toBeNull();

    await user.click(screen.getByRole("button", { name: "Take TRaSH's score for anime / Anime BD Tier 01" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/sync/reviews/43/scores")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/sync/reviews/43/scores")[0].body).toEqual({ generation: 6, decisions: [{ profile: "anime", format: "Anime BD Tier 01", choice: "take" }] });
    await waitFor(() => expect(screen.queryByText("anime / Anime BD Tier 01: ours 1300 · TRaSH 1400 → 1450")).toBeNull());
    await user.click(screen.getByRole("button", { name: "Keep our score for anime / Anime Raws" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/sync/reviews/43/scores")).toHaveLength(2));
    expect((fetch.calls("POST", "/settings/sync/reviews/43/scores")[1].body as { decisions: unknown[] }).decisions).toEqual([{ profile: "anime", format: "Anime Raws", choice: "keep" }]);
    await waitFor(() => expect(screen.queryByRole("button", { name: "Close review" })).toBeNull());
  });

  it("shows a tracked source's branch, mode and last good commit, and the editor defaults a tracked source to weekly auto definitions", async () => {
    const tracked = { id: 9, key: "trash-guides", config: { kind: "trash", flavour: "radarr", origin: "github", repo: "TRaSH-Guides/Guides", branch: "master", mode: "auto_definitions", schedule: "168h", enabled: true } };
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview() },
      { method: "GET", path: "/settings/sync_sources", reply: { generation: 4, records: [tracked] } },
      { method: "GET", path: "/settings/formats/schema", reply: [] },
      { method: "GET", path: "/settings/sync/reviews", reply: [] },
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
    expect(line.textContent).toContain("trash · tracks TRaSH-Guides/Guides master · mode auto_definitions");
    expect(line.textContent).toContain("last run failed: could not read the newest commit");

    await user.click(screen.getByRole("button", { name: "Add sync sources" }));
    await user.type(screen.getByLabelText("Key"), "weekly");
    await user.selectOptions(screen.getByLabelText(/TRaSH origin/), "github");
    expect((screen.getByLabelText("Sync mode") as HTMLSelectElement).value).toBe("auto_definitions");
    expect((screen.getByLabelText("Schedule (default 168h, weekly)") as HTMLInputElement).value).toBe("168h");
    expect(screen.queryByLabelText(/Pinned git archive URL/)).toBeNull();
    expect(screen.getByLabelText("Tracked branch (default master)")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: /^Save/ }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/sync_sources")).toHaveLength(1));
    const body = fetch.calls("POST", "/settings/sync_sources")[0].body as { config: Record<string, unknown> };
    expect(body.config).toMatchObject({ origin: "github", mode: "auto_definitions", schedule: "168h" });
    expect(body.config.archive_url).toBeUndefined();
  });

  it("shows a Recyclarr source's profile value changes, skipped keys and held values, and takes or keeps a held value", async () => {
    const review = {
      ...pending, id: 50, source: "household", added: [], spec_changed: [], retired: [], scores: [], profiles_changed: ["household-web"],
      fields: [{ profile: "household-web", field: "sizes/Remux-2160p", before: null, after: { min: 187.4, preferred: 0, max: 0 } }],
      notes: ["household.media_naming: skipped (Theatarr has no equivalent)"],
    };
    const held = {
      ...pending, id: 51, source: "household", kind: "scores", added: [], spec_changed: [], retired: [], profiles_changed: [], scores: [], held: [],
      impact: { files: 0, changed: 0, score_changed: 0, cutoff_changed: 0, upgrade_changed: 0, examples: [] },
      held_fields: [
        { profile: "household-web", field: "min_score", ours: 7, last: 5, upstream: 9 },
        { profile: "household-web", field: "qualities", ours: [{ group: "WEB 2160p", qualities: ["WEBDL-2160p", "WEBRip-2160p"] }], last: null, upstream: ["WEBDL-1080p"] },
      ],
    };
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview({ generation: 6 }) },
      { method: "GET", path: "/settings/sync_sources", reply: { generation: 6, records: [{ id: 3, key: "household", config: { kind: "recyclarr", flavour: "radarr", mode: "review", recyclarr: "radarr: {}" } }] } },
      { method: "GET", path: "/settings/formats/schema", reply: [] },
      { method: "GET", path: "/settings/sync/reviews", reply: [review, held] },
      { method: "GET", path: "/settings/sync/runs", reply: [] },
      { method: "POST", path: "/settings/sync/reviews/51/scores", reply: json({ generation: 7 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<ReviewsPage />, "/admin/reviews/quality");
    await user.click(await screen.findByRole("button", { name: "Review changes" }));
    expect(screen.getByText("household-web / sizes/Remux-2160p: not set → min 187.4 · preferred unlimited · max unlimited MB/min")).toBeTruthy();
    expect(screen.getByText("household.media_naming: skipped (Theatarr has no equivalent)")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Close review" }));

    expect(screen.getByText(/2 held profile value\(s\)/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Review held scores" }));
    expect(screen.getByText("household-web / min_score: ours 7 · source 5 → 9")).toBeTruthy();
    expect(screen.getByText("household-web / qualities: ours WEB 2160p (WEBDL-2160p, WEBRip-2160p) · source WEBDL-1080p (first run that sees it)")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Keep our value for household-web / min_score" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/sync/reviews/51/scores")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/sync/reviews/51/scores")[0].body).toEqual({ generation: 6, decisions: [{ profile: "household-web", field: "min_score", choice: "keep" }] });
    await user.click(screen.getByRole("button", { name: "Take the source's value for household-web / qualities" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/sync/reviews/51/scores")).toHaveLength(2));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Close review" })).toBeNull());
  });

  it("offers the quality sizes switch for a Recyclarr source, on by default", async () => {
    mockFetch([
      { method: "GET", path: "/settings", reply: overview() },
      { method: "GET", path: "/settings/sync_sources", reply: { generation: 4, records: [] } },
      { method: "GET", path: "/settings/formats/schema", reply: [] },
      { method: "GET", path: "/settings/sync/reviews", reply: [] },
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

it.each(["sources", "reviews"] as const)("does not show a false zero in %s while loading or failed", async part => {
 const { SyncReviews } = await import("./sync");
 mockFetch([
  { method: "GET", path: "/settings/sync/reviews", reply: () => new Promise(() => {}) },
  { method: "GET", path: "/settings/sync/runs", reply: [] },
 ]);
 const view = renderPage(<SyncReviews part={part} records={[]} generation={1} onChanged={async () => {}} />);
 expect(screen.getByText(part === "reviews" ? "Pending reviews (Loading)" : /Loading updates waiting/)).toBeTruthy();
 expect(screen.queryByText(/\b0\b/)).toBeNull();
 view.unmount();
 mockFetch([
  { method: "GET", path: "/settings/sync/reviews", reply: json({ error: "sync failed" }, 500) },
  { method: "GET", path: "/settings/sync/runs", reply: [] },
 ]);
 renderPage(<SyncReviews part={part} records={[]} generation={1} onChanged={async () => {}} />);
 expect(await screen.findByText(part === "reviews" ? "Pending reviews (unavailable)" : /unavailable updates waiting/)).toBeTruthy();
});
