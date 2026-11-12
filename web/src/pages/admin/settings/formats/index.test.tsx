// @vitest-environment jsdom
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { QualityOverview } from "@/api/settings-profiles";
import { overview } from "@/components/settings/fixtures";
import { mockFetch, renderPage } from "@/test/page";
import { FormatsPage } from ".";

const qualityOverview = (o: Partial<QualityOverview> = {}): QualityOverview => ({
  source: "db", generation: 4, trash: ["radarr"], categories: [], defaults: {}, profiles: [],
  formats: [
    { name: "DD+", names: ["DD+"], flavour: "radarr", trash_id: "abc", definition: "trash", group: "[Audio] Audio Formats", scopes: [], scored_by: [{ profile: "radarr-4k-web", score: 2500, state: "pinned" }, { profile: "radarr-4k-zurg", score: 1750, state: "follows" }] },
    { name: "x265 (HD)", names: ["x265 (HD)", "x265"], flavour: "radarr", definition: "frozen", group: "[Unwanted] Unwanted Formats", scopes: [], scored_by: [{ profile: "radarr-4k-web", score: -10000, state: "unlinked" }] },
    { name: "Our Group", names: ["Our Group"], flavour: "radarr", definition: "ours", group: "Our formats", scopes: [], id: 12, key: "our-group", scored_by: [] },
  ],
  ...o,
});
const routes = (ov = qualityOverview()) => [
  { method: "GET", path: "/settings", reply: overview() },
  { method: "GET", path: "/settings/quality/overview", reply: ov },
  { method: "GET", path: "/settings/formats", reply: { generation: 4, records: [{ id: 12, key: "our-group", config: { name: "Our Group", flavour: "radarr", specifications: [] } }] } },
  { method: "GET", path: "/settings/formats/schema", reply: [] },
];

describe("Custom formats page", () => {
  it("groups formats as TRaSH does, with each definition's origin and the profiles scoring it", async () => {
    mockFetch(routes());
    renderPage(<FormatsPage />, "/admin/settings/formats");
    const audio = (await screen.findByText("[Audio] Audio Formats", { exact: false })).closest("details")!;
    const dd = audio.querySelector('[data-format="DD+"]') as HTMLElement;
    expect(within(dd).getByText("TRaSH")).toBeTruthy();
    const scoring = within(dd).getByRole("list", { name: "Profiles scoring DD+" });
    expect(within(scoring).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["radarr-4k-web+2500yours", "radarr-4k-zurg+1750follows TRaSH"]);
    const frozen = screen.getByText("x265 (HD)").closest("li")!;
    expect(within(frozen).getAllByText("frozen copy").map((b) => b.getAttribute("data-state"))).toEqual([null, "unlinked"]);
    expect(within(frozen).getByText("also named x265")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Link formats to TRaSH" }).getAttribute("href")).toBe("/admin/settings/quality-sync");
    const ours = screen.getByText("Our Group").closest("li")!;
    expect(within(ours).getByText("ours")).toBeTruthy();
    expect(within(ours).getByText("No profile scores it.")).toBeTruthy();
    // One list of definitions: a standalone format is edited and exported from its row; no second list below.
    expect(within(ours).getByRole("button", { name: "Edit definition our-group" })).toBeTruthy();
    expect(within(ours).getByRole("link", { name: "Export arr JSON" }).getAttribute("href")).toBe("/api/theatarr/v1/settings/formats/12/export");
    expect(await screen.findByTestId("add")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Edit our-group" })).toBeNull();
    // Each score links to the profile that holds it.
    expect(within(scoring).getByRole("link", { name: "radarr-4k-web" }).getAttribute("href")).toBe("/admin/settings/profiles?edit=radarr-4k-web");
  });

  it("filters by format or profile name and opens a standalone format in its editor", async () => {
    mockFetch(routes());
    const user = userEvent.setup();
    renderPage(<FormatsPage />);
    await user.type(await screen.findByRole("textbox", { name: "Filter formats" }), "zurg");
    expect(screen.getAllByText("DD+").length).toBeGreaterThan(0);
    expect(screen.queryByText("x265 (HD)")).toBeNull();
    await user.clear(screen.getByRole("textbox", { name: "Filter formats" }));
    await user.click(screen.getByRole("button", { name: "Edit definition our-group" }));
    expect(await screen.findByRole("dialog")).toBeTruthy();
  });

  it("opens filtered to a format named in the link", async () => {
    mockFetch(routes());
    renderPage(<FormatsPage />, "/admin/settings/formats?filter=DD%2B");
    expect(((await screen.findByRole("textbox", { name: "Filter formats" })) as HTMLInputElement).value).toBe("DD+");
    expect(screen.getAllByText("DD+").length).toBeGreaterThan(0);
    expect(screen.queryByText("x265 (HD)")).toBeNull();
  });

  it("opens filtered to a format scored by its TRaSH ID", async () => {
    mockFetch(routes());
    renderPage(<FormatsPage />, "/admin/settings/formats?filter=abc");
    expect((await screen.findAllByText("DD+")).length).toBeGreaterThan(0);
    expect(screen.queryByText("No format matches.")).toBeNull();
    expect(screen.queryByText("x265 (HD)")).toBeNull();
  });

  it("keeps Edit and Export for a stored definition the merged list leaves out", async () => {
    // Two stored definitions share TRaSH ID abc: the overview has one row, keyed by the last.
    const ov = qualityOverview();
    ov.formats[0] = { ...ov.formats[0], id: 14, key: "dd-plus-b" };
    const records = { generation: 4, records: [
      { id: 12, key: "our-group", config: { name: "Our Group", flavour: "radarr", specifications: [] } },
      { id: 13, key: "dd-plus-a", config: { name: "DD+", flavour: "radarr", trash_id: "abc" } },
      { id: 14, key: "dd-plus-b", config: { name: "DD+", flavour: "radarr", trash_id: "abc" } },
    ] };
    mockFetch(routes(ov).map((r) => r.path === "/settings/formats" ? { ...r, reply: records } : r));
    const user = userEvent.setup();
    renderPage(<FormatsPage />);
    expect(await screen.findByRole("button", { name: "Edit definition dd-plus-b" })).toBeTruthy();
    const standalone = screen.getByRole("region", { name: "Standalone formats" });
    expect(await within(standalone).findByRole("button", { name: "Edit dd-plus-a" })).toBeTruthy();
    expect(within(standalone).getByRole("link", { name: "Export arr JSON" }).getAttribute("href")).toBe("/api/theatarr/v1/settings/formats/13/export");
    expect(within(standalone).queryByRole("button", { name: "Edit dd-plus-b" })).toBeNull();
    expect(within(standalone).queryByRole("button", { name: "Edit our-group" })).toBeNull();
    await user.click(within(standalone).getByRole("button", { name: "Edit dd-plus-a" }));
    expect(await screen.findByRole("dialog")).toBeTruthy();
  });

  it("filters the definitions under Standalone formats too", async () => {
    const ov = qualityOverview();
    ov.formats[0] = { ...ov.formats[0], id: 14, key: "dd-plus-b" };
    const records = { generation: 4, records: [
      { id: 13, key: "dd-plus-a", config: { name: "DD+", flavour: "radarr", trash_id: "abc" } },
      { id: 14, key: "dd-plus-b", config: { name: "DD+", flavour: "radarr", trash_id: "abc" } },
    ] };
    mockFetch(routes(ov).map((r) => r.path === "/settings/formats" ? { ...r, reply: records } : r));
    const user = userEvent.setup();
    renderPage(<FormatsPage />);
    const standalone = screen.getByRole("region", { name: "Standalone formats" });
    expect(await within(standalone).findByRole("button", { name: "Edit dd-plus-a" })).toBeTruthy();
    await user.type(screen.getByRole("textbox", { name: "Filter formats" }), "nothing-like-it");
    expect(screen.getByText("No format matches.")).toBeTruthy();
    expect(within(standalone).queryByRole("button", { name: "Edit dd-plus-a" })).toBeNull();
    await user.clear(screen.getByRole("textbox", { name: "Filter formats" }));
    await user.type(screen.getByRole("textbox", { name: "Filter formats" }), "abc");
    expect(within(standalone).getByRole("button", { name: "Edit dd-plus-a" })).toBeTruthy();
  });
});
