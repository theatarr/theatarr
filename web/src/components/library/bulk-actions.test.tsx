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

const copy = (key: string, label: string, links: number, downloads = 0) => ({ key, label, links, downloads });
const scope = (title_id: number, name: string, copies: ReturnType<typeof copy>[], title = true) => ({ title_id, name, copies, title, leftover: [] });
type ScopeT = ReturnType<typeof scope>;
/** The per-title scope read again just before each removal: as previewed unless changed. */
const reread = (list: ScopeT[]) => list.map((sc) => ({ method: "GET", path: `/titles/${sc.title_id}/removal`, reply: sc }));
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
      scope(1, "Alpha", [copy("hd", "HD", 3, 1), copy("uhd", "4K", 2)]),
      scope(2, "Beta", [copy("hd", "HD", 1)]),
      scope(3, "Gamma", [copy("uhd", "4K", 0)]),
      scope(4, "Delta", [copy("tv", "TV", 10)]),
    ],
    skipped: [{ title_id: 5, reason: "in no library" }],
  };

  it("sums the scopes, lists the titles and removes each through the per-title removal", async () => {
    const { api, cleared } = setup([
      { method: "POST", path: "/titles/removal", reply: scopes },
      ...reread(scopes.scopes),
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
    expect(within(dialog).getByTestId("bulk-remove-skipped").textContent).toBe("Skipped 1 title: in no library.");
    expect(within(dialog).getByText("Delete them from the debrid providers")).toBeTruthy();
    // The single dialog's two choices, both on; the torrents need the files.
    await user.click(within(dialog).getByTestId("remove-files"));
    expect((within(dialog).getByTestId("remove-debrid") as HTMLInputElement).disabled).toBe(true);
    expect(within(dialog).getByTestId("confirm-does").textContent).not.toContain("deleted");
    await user.click(within(dialog).getByTestId("remove-files"));
    await user.click(within(dialog).getByRole("button", { name: "Remove 4 titles" }));
    await screen.findByText("Removed 4 titles");
    // Each title with exactly the libraries its preview showed.
    expect([1, 2, 3, 4].map((id) => api.calls("POST", `/titles/${id}/remove`)[0]?.body)).toEqual([["hd", "uhd"], ["hd"], ["uhd"], ["tv"]].map(
      (versions) => ({ versions, unlink: true, debrid: true })));
    expect(api.calls("GET", "/titles/1/removal")[0].search).toBe("?versions=hd%2Cuhd");
    expect(screen.getByText("Their debrid torrents are being cleaned up; each title's History shows the outcome.")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(cleared()).toBe(1);
  });

  it("goes on after a refused title and says which failed and why", async () => {
    setup([
      { method: "POST", path: "/titles/removal", reply: { scopes: scopes.scopes.slice(0, 3), skipped: [] } },
      ...reread(scopes.scopes.slice(0, 3)),
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
      ...reread([scope(1, "Alpha", [copy("uhd", "4K", 2)], false), scope(3, "Gamma", [copy("uhd", "4K", 0)])]),
      ...[1, 3].map((id) => ({ method: "POST", path: `/titles/${id}/remove`, reply: removed({ title: id === 3 }) })),
    ]);
    const user = userEvent.setup();
    await user.click(await within(await bar()).findByRole("button", { name: "Remove from a library" }));
    let dialog = await screen.findByRole("dialog", { name: "Remove 3 titles from HD" });
    expect(within(dialog).getByTestId("confirm-does").textContent).toContain("1 title keeps a copy in another library and stays visible to apps that include it; 2 are in no other library and are hidden from Seerr and the other arr clients");
    await user.selectOptions(within(dialog).getByRole("combobox", { name: "Library" }), "uhd");
    dialog = await screen.findByRole("dialog", { name: "Remove 2 titles from 4K" });
    expect(within(dialog).getByTestId("confirm-does").textContent).toContain("1 title keeps a copy in another library and stays visible to apps that include it; 1 is in no other library and is hidden from Seerr and the other arr clients");
    expect(within(dialog).getByTestId("bulk-remove-skipped").textContent).toBe("Skipped 3 titles: not in this library.");
    expect(within(dialog).getByTestId("confirm-keeps").textContent).toContain("A file another library of the title uses stays");
    await user.click(within(dialog).getByRole("button", { name: "Remove 2 titles from 4K" }));
    await screen.findByText("Removed 2 titles from 4K");
    expect(api.calls("POST", "/titles/1/remove")[0].body).toEqual({ versions: ["uhd"], unlink: true, debrid: true });
    expect(api.calls("POST", "/titles/2/remove")).toHaveLength(0);
  });
});

describe("Bulk Remove after the preview", () => {
  it("removes only what the preview showed: a title that changed since is skipped and said so", async () => {
    // Previewed: Alpha in HD with no files, Beta in HD. Since then Alpha got a 4K library and a file.
    const preview = { scopes: [scope(1, "Alpha", [copy("hd", "HD", 0)]), scope(2, "Beta", [copy("hd", "HD", 1)])], skipped: [] };
    const { api } = setup([
      { method: "POST", path: "/titles/removal", reply: preview },
      { method: "GET", path: "/titles/1/removal", reply: scope(1, "Alpha", [copy("hd", "HD", 1)], false) },
      { method: "GET", path: "/titles/2/removal", reply: preview.scopes[1] },
      { method: "POST", path: "/titles/2/remove", reply: removed() },
    ]);
    const user = userEvent.setup();
    await user.click(await within(await bar()).findByRole("button", { name: "Remove" }));
    const dialog = await screen.findByRole("dialog", { name: "Remove 2 titles" });
    await user.click(within(dialog).getByRole("button", { name: "Remove 2 titles" }));
    await screen.findByText("Removed 1 of 2 titles");
    expect(screen.getByText(/^1 title not removed \(Alpha\): changed since the preview\./)).toBeTruthy();
    // Alpha's check asked for the previewed library only, and Alpha was not removed.
    expect(api.calls("GET", "/titles/1/removal")[0].search).toBe("?versions=hd");
    expect(api.calls("POST", "/titles/1/remove")).toHaveLength(0);
    expect(api.calls("POST", "/titles/2/remove")[0].body).toEqual({ versions: ["hd"], unlink: true, debrid: true });
  });

  it("stops after the title in hand when the dialog is closed, and leaves the bar and a newer dialog alone", async () => {
    let release: () => void = () => {};
    const held = new Promise<void>((r) => { release = r; });
    const preview = { scopes: [scope(1, "Alpha", [copy("hd", "HD", 0)]), scope(2, "Beta", [copy("hd", "HD", 1)])], skipped: [] };
    const { api, cleared } = setup([
      { method: "POST", path: "/titles/removal", reply: preview },
      ...reread(preview.scopes),
      { method: "POST", path: "/titles/1/remove", reply: async () => { await held; return removed(); } },
      { method: "POST", path: "/titles/monitor", reply: { dry_run: true, changed: 0, unchanged: 0, missing: 0, stale: 0, message: "Nothing to change", items: [] } },
    ]);
    const user = userEvent.setup();
    await user.click(await within(await bar()).findByRole("button", { name: "Remove" }));
    const dialog = await screen.findByRole("dialog", { name: "Remove 2 titles" });
    await user.click(within(dialog).getByRole("button", { name: "Remove 2 titles" }));
    await waitFor(() => expect(api.calls("POST", "/titles/1/remove")).toHaveLength(1));
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await user.click(await within(await bar()).findByRole("button", { name: "Monitor" }));
    const review = await screen.findByRole("dialog", { name: "Review bulk edit" });
    release();
    await screen.findByText("Removed 1 of 2 titles");
    expect(api.calls("POST", "/titles/2/remove")).toHaveLength(0);
    expect(cleared()).toBe(0);
    expect(review.isConnected).toBe(true);
  });
});

describe("Bulk Remove of a large selection", () => {
  it("reads the preview in groups of 500 and joins them in the selection's order", async () => {
    const many = Array.from({ length: 1100 }, (_, i) => ({ id: i + 1, kind: "movie", name: `Film ${i + 1}`, versions: [{ key: "hd", label: "HD", monitored: true }] })) as unknown as TitleRow[];
    const reply = (r: RequestRecord) => {
      const ids = (r.body as { title_ids: number[] }).title_ids;
      return { scopes: ids.filter((id) => id !== 7).map((id) => scope(id, `Film ${id}`, [copy("hd", "HD", 1)])), skipped: ids.includes(7) ? [{ title_id: 7, reason: "in no library" }] : [] };
    };
    const api = mockFetch([me(["manage"]), { method: "POST", path: "/titles/removal", reply }]);
    renderPage(<BulkBar titles={many} columns={columns} onClear={() => {}} />);
    const user = userEvent.setup();
    await user.click(await within(await bar()).findByRole("button", { name: "Remove" }));
    const dialog = await screen.findByRole("dialog", { name: "Remove 1,099 titles" });
    expect(api.calls("POST", "/titles/removal").map((c) => (c.body as { title_ids: number[] }).title_ids.length)).toEqual([500, 500, 100]);
    expect(within(dialog).getByText("Film 1, Film 2, Film 3 and 1096 more")).toBeTruthy();
    expect(within(dialog).getByTestId("bulk-remove-skipped").textContent).toBe("Skipped 1 title: in no library.");
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

  it("preselects the first library that adds a title, not one every selected title is in", async () => {
    mockFetch([me(["manage", "add"]), ...routes()]);
    renderPage(<BulkBar titles={titles.slice(0, 2)} columns={columns} kinds={["movie"]} onClear={() => {}} />);
    const user = userEvent.setup();
    await user.click(await within(await bar()).findByRole("button", { name: "Add to a library" }));
    expect(await screen.findByRole("dialog", { name: "Add 1 title to 4K" })).toBeTruthy();
  });

  // HD and 4K fit the list; Anime movies does not.
  const mixed = { method: "GET", path: "/add/options", reply: { kind: "movie", anime: false, router_enabled: true, seasons: [], versions: [
    { key: "am", label: "Anime movies", kind: "movie", fits: false },
    { key: "hd", label: "HD", kind: "movie", fits: true },
    { key: "uhd", label: "4K", kind: "movie", fits: true },
  ] } };

  it("preselects a fitting library that adds a title, never one that does not fit", async () => {
    mockFetch([me(["manage", "add"]), mixed]);
    renderPage(<BulkBar titles={titles.slice(0, 2)} columns={columns} kinds={["movie"]} onClear={() => {}} />);
    const user = userEvent.setup();
    await user.click(await within(await bar()).findByRole("button", { name: "Add to a library" }));
    const dialog = await screen.findByRole("dialog", { name: "Add 1 title to 4K" });
    expect((within(dialog).getByRole("combobox", { name: "Library" }) as HTMLSelectElement).value).toBe("uhd");
  });

  it("preselects nothing when the selection is in every fitting library; a library that does not fit can still be picked", async () => {
    mockFetch([me(["manage", "add"]), mixed, { method: "PUT", path: "/titles/1/versions/am", reply: {} }]);
    renderPage(<BulkBar titles={titles.slice(0, 1)} columns={columns} kinds={["movie"]} onClear={() => {}} />);
    const user = userEvent.setup();
    await user.click(await within(await bar()).findByRole("button", { name: "Add to a library" }));
    let dialog = await screen.findByRole("dialog", { name: "Add 1 title to a library" });
    const select = within(dialog).getByRole("combobox", { name: "Library" }) as HTMLSelectElement;
    expect(select.value).toBe("");
    expect(within(dialog).getByRole("option", { name: "Choose a library" })).toBeTruthy();
    expect(within(dialog).queryByText("This will")).toBeNull();
    expect(within(dialog).queryByTestId("confirm-keeps")).toBeNull();
    expect((within(dialog).getByTestId("confirm-ok") as HTMLButtonElement).disabled).toBe(true);
    await user.selectOptions(select, "am");
    dialog = await screen.findByRole("dialog", { name: "Add 1 title to Anime movies" });
    expect((within(dialog).getByRole("button", { name: "Add 1 title" }) as HTMLButtonElement).disabled).toBe(false);
  });
});

describe("Bulk Add to a library kept in sync", () => {
  it("says what else is added and counts the copies added in sync", async () => {
    mockFetch([me(["manage", "add"]), // The line follows the saved group, not each option's synced answer (empty for a title type that fits no partner).
    { method: "GET", path: "/add/options", reply: { kind: "movie", anime: false, router_enabled: true, seasons: [], sync_groups: [["uhd", "hd"]], versions: [
      { key: "hd", label: "HD", kind: "movie", fits: true },
      { key: "uhd", label: "4K", kind: "movie", fits: true },
    ] } },
    { method: "PUT", path: "/titles/2/versions/uhd", reply: { synced: [] } },
    { method: "PUT", path: "/titles/3/versions/uhd", reply: { synced: ["hd"] } },
    { method: "PUT", path: "/titles/5/versions/uhd", reply: {} }]);
    renderPage(<><BulkBar titles={titles} columns={columns} kinds={["movie"]} onClear={() => {}} /><Toaster /></>);
    const user = userEvent.setup();
    await user.click(await within(await bar()).findByRole("button", { name: "Add to a library" }));
    let dialog = await screen.findByRole("dialog", { name: "Add 1 title to HD" });
    expect(within(dialog).getByTestId("bulk-add-synced").textContent).toBe("Also added to 4K where a title has no copy there or was removed from it.");
    await user.selectOptions(within(dialog).getByRole("combobox", { name: "Library" }), "uhd");
    dialog = await screen.findByRole("dialog", { name: "Add 3 titles to 4K" });
    expect(within(dialog).getByTestId("bulk-add-synced").textContent).toBe("Also added to HD where a title has no copy there or was removed from it.");
    const keeps = within(dialog).getByTestId("confirm-keeps").textContent;
    expect(keeps).toContain("Their files");
    expect(keeps).not.toContain("other libraries");
    await user.click(within(dialog).getByRole("button", { name: "Add 3 titles" }));
    expect(await screen.findByText("Added 3 titles to 4K and 1 also added to HD")).toBeTruthy();
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
