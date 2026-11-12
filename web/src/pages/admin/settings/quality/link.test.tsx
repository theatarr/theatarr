// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { overview } from "@/components/settings/fixtures";
import { json, mockFetch, renderPage } from "@/test/page";
import { QualityPanel } from ".";

const preview = {
  generation: 4, source: "trash-radarr", flavour: "radarr", commit: "c".repeat(40),
  counts: { linked: 1, matched: 2, diverged: 1, ambiguous: 1, unmatched: 1 },
  items: [
    { key: "3D", status: "linked", trash_id: "t3d" },
    { set: "radarr-anime", key: "10bit - Notifiarr", status: "matched", trash_id: "t10", candidates: [{ trash_id: "t10", name: "10bit", same_spec: true, same_name: true }] },
    { set: "radarr-anime", key: "BR-DISK", status: "matched", trash_id: "tbr", candidates: [{ trash_id: "tbr", name: "BR-DISK", same_spec: true, same_name: true }] },
    { set: "radarr-anime", key: "LQ", status: "diverged", reason: "the TRaSH format of this name has another specification", candidates: [{ trash_id: "tlq", name: "LQ", same_spec: false, same_name: true }] },
    { set: "radarr-anime", key: "Disc", status: "ambiguous", candidates: [{ trash_id: "ta", name: "A", same_spec: true, same_name: false }, { trash_id: "tb", name: "B", same_spec: true, same_name: false }] },
    { set: "radarr-anime", key: "Tier Fallback", status: "unmatched" },
  ],
};

describe("Link formats to TRaSH", () => {
  it("previews the match, preselects matched formats only, and links the explicit selection at the previewed generation and commit", async () => {
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview() },
      { method: "GET", path: "/settings/sync_sources", reply: { generation: 4, records: [{ id: 9, key: "trash-radarr", config: { kind: "trash", flavour: "radarr", origin: "github", mode: "auto_definitions" } }] } },
      { method: "GET", path: "/settings/formats/schema", reply: [] },
      { method: "GET", path: "/settings/sync/changes", reply: [] },
      { method: "GET", path: "/settings/sync/runs", reply: [] },
      { method: "POST", path: "/settings/sync_sources/9/link/preview", reply: preview },
      { method: "POST", path: "/settings/sync_sources/9/link/apply", reply: json({ generation: 5, revision: "r", linked: 3 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<QualityPanel entity="sync_sources" />);
    await user.click(await screen.findByRole("button", { name: "Link formats to TRaSH" }));
    expect((await screen.findByTestId("trash-link-counts")).textContent).toBe("Matched 2 · diverged 1 · ambiguous 1 · unmatched 1 · already linked 1");
    expect((screen.getByRole("checkbox", { name: /10bit - Notifiarr/ }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText("TRaSH format for radarr-anime / LQ") as HTMLSelectElement).value).toBe("");
    expect(screen.getByText("radarr-anime / Tier Fallback")).toBeTruthy();
    await user.click(screen.getByRole("checkbox", { name: /BR-DISK/ }));
    await user.selectOptions(screen.getByLabelText("TRaSH format for radarr-anime / LQ"), "tlq");
    await user.selectOptions(screen.getByLabelText("TRaSH format for radarr-anime / Disc"), "tb");
    await user.click(screen.getByRole("button", { name: "Link 3 formats" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/sync_sources/9/link/apply")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/sync_sources/9/link/apply")[0].body).toEqual({
      generation: 4, commit: "c".repeat(40),
      links: [
        { set: "radarr-anime", key: "10bit - Notifiarr", trash_id: "t10" },
        { set: "radarr-anime", key: "LQ", trash_id: "tlq" },
        { set: "radarr-anime", key: "Disc", trash_id: "tb" },
      ],
    });
    expect(await screen.findByText("Linked 3 formats to TRaSH.")).toBeTruthy();
    expect(fetch.calls("POST", "/settings/sync_sources/9/link/preview")).toHaveLength(1);
  });
});
