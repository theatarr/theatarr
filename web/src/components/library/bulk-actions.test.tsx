// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { TitleRow } from "@/api/library";
import { Toaster } from "@/components/toaster";
import type { CopyColumn } from "@/lib/copies";
import { json, mockFetch, renderPage, type RequestRecord, type Route } from "@/test/page";
import { BulkBar } from "./bulk-monitor";

const me = (permissions: string[]) => ({ method: "GET", path: "/auth/me", reply: { id: 7, username: "owner", permissions, csrf_token: "t" } });
const titles = [
  { id: 1, kind: "movie", name: "Alpha", versions: [{ key: "hd", label: "HD", monitored: true }, { key: "uhd", label: "4K", monitored: true }] },
  { id: 2, kind: "movie", name: "Beta", versions: [{ key: "hd", label: "HD", monitored: true }] },
  { id: 3, kind: "movie", name: "Gamma", versions: [{ key: "uhd", label: "4K", monitored: false }] },
  { id: 4, kind: "series", name: "Delta", versions: [{ key: "tv", label: "TV", monitored: true }] },
  { id: 5, kind: "movie", name: "Epsilon", versions: [{ key: "hd", label: "HD", monitored: true }] },
] as unknown as TitleRow[];
const columns = [{ key: "hd", label: "HD" }, { key: "uhd", label: "4K" }, { key: "tv", label: "TV" }] as CopyColumn[];

const copy = (key: string, label: string, links: number, downloads = 0, arr_files = 0) => ({ key, label, links, downloads, arr_files });
const scope = (title_id: number, name: string, copies: ReturnType<typeof copy>[], title = true) => ({ title_id, name, copies, title, leftover: [] });
const removed = (over: Record<string, unknown> = {}) => ({ removed: ["hd"], title: true, downloads_ended: 0, unlinked: [], kept: [], failed: [], cleanup: true, message: "Removed.", ...over });

function setup(routes: Route[], perms = ["manage", "add", "see_files"]) {
  const api = mockFetch([me(perms), ...routes]);
  let cleared = 0;
  renderPage(<><BulkBar titles={titles} columns={columns} kinds={["movie", "series"]} anime onClear={() => cleared++} /><Toaster /></>);
  return { api, cleared: () => cleared };
}

const bar = () => screen.findByRole("region", { name: "Bulk edit" });

describe("Bulk bar", () => {
  it("counts titles and offers each action to a manager with add", async () => {
    setup([]);
    expect(within(await bar()).getByText("5 titles selected")).toBeTruthy();
    for (const name of ["Monitor", "Unmonitor", "Search now", "Add to a library", "Remove from a library", "Remove"]) {
      expect(await within(await bar()).findByRole("button", { name })).toBeTruthy();
    }
  });

  it("offers no Add to a library without the add permission", async () => {
    setup([], ["manage"]);
    await within(await bar()).findByRole("button", { name: "Remove" });
    expect(within(await bar()).queryByRole("button", { name: "Add to a library" })).toBeNull();
  });
});

describe("Bulk Remove", () => {
  const scopes = {
    scopes: [
      scope(1, "Alpha", [copy("hd", "HD", 3, 1), copy("uhd", "4K", 2, 0, 4)]),
      scope(2, "Beta", [copy("hd", "HD", 1)]),
      scope(3, "Gamma", [copy("uhd", "4K", 0)]),
      scope(4, "Delta", [copy("tv", "TV", 10)]),
    ],
    skipped: [{ title_id: 5, reason: "in no library" }],
  };

  it("sums the scopes, lists the titles and removes each through the per-title removal", async () => {
    const { api, cleared } = setup([
      { method: "POST", path: "/titles/removal", reply: scopes },
      ...[1, 2, 3, 4].map((id) => ({ method: "POST", path: `/titles/${id}/remove`, reply: removed() })),
    ]);
    const user = userEvent.setup();
    await user.click(await within(await bar()).findByRole("button", { name: "Remove" }));
    const dialog = await screen.findByRole("dialog", { name: "Remove 4 titles" });
    expect(api.calls("POST", "/titles/removal")[0].body).toEqual({ title_ids: [1, 2, 3, 4, 5], versions: [] });
    expect(within(dialog).getByText("Alpha, Beta, Gamma and Delta")).toBeTruthy();
    const does = within(dialog).getByTestId("confirm-does").textContent;
    expect(does).toContain("4 titles are removed from every library they are in");
    expect(does).toContain("Theatarr stops following 1 download in progress");
    expect(does).toContain("16 library files are deleted from the library");
    expect(does).toContain("Their debrid torrents are deleted from the providers");
    expect(within(dialog).getByTestId("confirm-keeps").textContent).toContain("4 files that Sonarr or Radarr manages stay");
    expect(within(dialog).getByTestId("bulk-remove-skipped").textContent).toBe("Skipped 1 title: in no library.");
    // The single dialog's two choices, both on; the torrents need the files.
    await user.click(within(dialog).getByTestId("remove-files"));
    expect((within(dialog).getByTestId("remove-debrid") as HTMLInputElement).disabled).toBe(true);
    expect(within(dialog).getByTestId("confirm-does").textContent).not.toContain("deleted");
    await user.click(within(dialog).getByTestId("remove-files"));
    await user.click(within(dialog).getByRole("button", { name: "Remove 4 titles" }));
    await screen.findByText("Removed 4 titles");
    expect([1, 2, 3, 4].map((id) => api.calls("POST", `/titles/${id}/remove`)[0]?.body)).toEqual(
      Array(4).fill({ versions: [], unlink: true, debrid: true }));
    expect(screen.getByText("Their debrid torrents are being cleaned up; each title's History shows the outcome.")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(cleared()).toBe(1);
  });

  it("goes on after a refused title and says which failed and why", async () => {
    setup([
      { method: "POST", path: "/titles/removal", reply: { scopes: scopes.scopes.slice(0, 3), skipped: [] } },
      { method: "POST", path: "/titles/1/remove", reply: removed({ kept: [{ path: "/a", reason: "shared" }] }) },
      { method: "POST", path: "/titles/2/remove", reply: json({ error: "a copy Radarr still manages cannot be removed" }, 409) },
      { method: "POST", path: "/titles/3/remove", reply: removed({ cleanup: false }) },
    ]);
    const user = userEvent.setup();
    await user.click(await within(await bar()).findByRole("button", { name: "Remove" }));
    const dialog = await screen.findByRole("dialog", { name: "Remove 3 titles" });
    await user.click(within(dialog).getByRole("button", { name: "Remove 3 titles" }));
    await screen.findByText("Removed 2 of 3 titles");
    expect(screen.getByText(/1 title not removed \(Beta\): a copy Radarr still manages cannot be removed\. 1 library file left in place; each title's History says why\./)).toBeTruthy();
  });

  it("removes from one library chosen in the dialog, skipping titles not in it", async () => {
    const reply = (r: RequestRecord) => {
      const v = (r.body as { versions: string[] }).versions[0];
      return v === "uhd"
        ? { scopes: [scope(1, "Alpha", [copy("uhd", "4K", 2)], false), scope(3, "Gamma", [copy("uhd", "4K", 0)])], skipped: [2, 4, 5].map((id) => ({ title_id: id, reason: "not in this library" })) }
        : { scopes: [scope(1, "Alpha", [copy("hd", "HD", 3)], false), scope(2, "Beta", [copy("hd", "HD", 1)]), scope(5, "Epsilon", [copy("hd", "HD", 1)])], skipped: [3, 4].map((id) => ({ title_id: id, reason: "not in this library" })) };
    };
    const { api } = setup([
      { method: "POST", path: "/titles/removal", reply },
      ...[1, 3].map((id) => ({ method: "POST", path: `/titles/${id}/remove`, reply: removed({ title: id === 3 }) })),
    ]);
    const user = userEvent.setup();
    await user.click(await within(await bar()).findByRole("button", { name: "Remove from a library" }));
    let dialog = await screen.findByRole("dialog", { name: "Remove 3 titles from HD" });
    expect(within(dialog).getByTestId("confirm-does").textContent).toContain("2 titles are in no other library and leave the library");
    await user.selectOptions(within(dialog).getByRole("combobox", { name: "Library" }), "uhd");
    dialog = await screen.findByRole("dialog", { name: "Remove 2 titles from 4K" });
    expect(within(dialog).getByTestId("confirm-does").textContent).toContain("1 title is in no other library and leaves the library");
    expect(within(dialog).getByTestId("bulk-remove-skipped").textContent).toBe("Skipped 3 titles: not in this library.");
    expect(within(dialog).getByTestId("confirm-keeps").textContent).toContain("A file another library of the title uses stays");
    await user.click(within(dialog).getByRole("button", { name: "Remove 2 titles from 4K" }));
    await screen.findByText("Removed 2 titles from 4K");
    expect(api.calls("POST", "/titles/1/remove")[0].body).toEqual({ versions: ["uhd"], unlink: true, debrid: true });
    expect(api.calls("POST", "/titles/2/remove")).toHaveLength(0);
  });
});

describe("Bulk Add to a library", () => {
  const options = (kind: string, versions: { key: string; label: string }[]) => ({ kind, anime: true, router_enabled: true, versions: versions.map((v) => ({ ...v, kind, fits: true })), seasons: [] });
  const routes = (put: unknown = {}) => [
    { method: "GET", path: "/add/options", reply: (r: RequestRecord) => (r.search.includes("kind=series") ? options("series", [{ key: "tv", label: "TV" }]) : options("movie", [{ key: "hd", label: "HD" }, { key: "uhd", label: "4K" }])) },
    { method: "PUT", path: "/titles/3/versions/uhd", reply: put },
    { method: "PUT", path: "/titles/2/versions/uhd", reply: {} },
    { method: "PUT", path: "/titles/5/versions/uhd", reply: {} },
  ];

  it("adds each title of the library's kind, skipping those already in it and of the other kind", async () => {
    const { api } = setup(routes(json({ error: "unknown library" }, 400)));
    const user = userEvent.setup();
    await user.click(await within(await bar()).findByRole("button", { name: "Add to a library" }));
    let dialog = await screen.findByRole("dialog", { name: "Add 1 title to HD" });
    await user.selectOptions(within(dialog).getByRole("combobox", { name: "Library" }), "uhd");
    dialog = await screen.findByRole("dialog", { name: "Add 3 titles to 4K" });
    expect(within(dialog).getByText("Beta, Gamma and Epsilon")).toBeTruthy();
    const keeps = within(dialog).getByTestId("confirm-keeps").textContent;
    expect(keeps).toContain("Nothing is searched now");
    expect(keeps).toContain("1 title already in 4K");
    expect(keeps).toContain("1 series skipped: 4K is a library for movies");
    await user.click(within(dialog).getByRole("button", { name: "Add 3 titles" }));
    await screen.findByText("Added 2 of 3 titles to 4K");
    expect(screen.getByText("1 title not added (Gamma): unknown library.")).toBeTruthy();
    expect(api.calls("PUT", "/titles/2/versions/uhd")[0].body).toEqual({ monitored: true });
    expect(api.requests.filter((r) => r.method !== "GET").map((r) => r.path)).toEqual(["/titles/2/versions/uhd", "/titles/3/versions/uhd", "/titles/5/versions/uhd"]);
  });
});

describe("Bulk Monitor", () => {
  it("previews every library, then the library picked in the review", async () => {
    const preview = { dry_run: true, changed: 1, unchanged: 0, missing: 0, stale: 0, message: "Would unmonitor 1 library", items: [] };
    const { api } = setup([{ method: "POST", path: "/titles/monitor", reply: preview }]);
    const user = userEvent.setup();
    await user.click(await within(await bar()).findByRole("button", { name: "Unmonitor" }));
    const dialog = await screen.findByRole("dialog", { name: "Review bulk edit" });
    await within(dialog).findByText("Would unmonitor 1 library");
    await user.selectOptions(within(dialog).getByRole("combobox", { name: "Libraries" }), "uhd");
    await waitFor(() => expect(api.calls("POST", "/titles/monitor")).toHaveLength(2));
    expect(api.calls("POST", "/titles/monitor").map((c) => c.body)).toEqual([
      { title_ids: [1, 2, 3, 4, 5], monitored: false, dry_run: true },
      { title_ids: [1, 2, 3, 4, 5], versions: ["uhd"], monitored: false, dry_run: true },
    ]);
  });
});
