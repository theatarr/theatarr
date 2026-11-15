// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { TitleRow } from "@/api/library";
import type { CopyColumn } from "@/lib/copies";
import { json, mockFetch, renderPage, type RequestRecord, type Route } from "@/test/page";
import { BulkBar } from "./bulk-monitor";

const managed = { can_edit: true, can_search: true, live_releases: true };
const rows = (n: number) => Array.from({ length: n }, (_, i) => ({
  id: i + 1, kind: "movie", name: `Film ${i + 1}`, versions: [{ id: 100 + i, key: "movies-hd", label: "HD", monitored: true, control: managed }],
})) as unknown as TitleRow[];
const columns = [{ key: "movies-hd", label: "HD" }, { key: "movies-4k", label: "4K" }] as unknown as CopyColumn[];

const me = (permissions: string[]) => ({ method: "GET", path: "/auth/me", reply: { id: 7, username: "owner", permissions, csrf_token: "t" } });
const wanted = { method: "GET", path: "/wanted", reply: { per_hour: 20, searched_last_hour: 0, items: [], total: 0 } };
const answer = (over: Record<string, unknown> = {}) => ({ queued: 0, started: 0, pending: 0, skipped: [], ...over });

function setup(n: number, routes: Route[]) {
  const api = mockFetch(routes);
  renderPage(<BulkBar titles={rows(n)} columns={columns} onClear={() => {}} />);
  return api;
}

/** Press Search now and confirm. */
async function searchNow(n: number, searches = n) {
  const user = userEvent.setup();
  const bar = await screen.findByRole("region", { name: "Bulk edit" });
  await user.click(await within(bar).findByRole("button", { name: "Search now" }));
  const dialog = await screen.findByRole("dialog", { name: `Search ${n} titles` });
  await waitFor(() => expect(within(dialog).getByTestId("bulk-search-estimate").textContent).toBe("Searches start as the hourly limit allows: up to 20 an hour."));
  await user.click(within(dialog).getByRole("button", { name: `Queue ${searches} searches` }));
  return bar;
}

describe("Bulk Search now", () => {
  it("is offered to managers", async () => {
    setup(3, [me(["see_files", "manage"])]);
    expect(await screen.findByRole("button", { name: "Search now" })).toBeTruthy();
  });

  it("has no bar without manage", async () => {
    const api = setup(3, [me(["see_files"])]);
    await waitFor(() => expect(api.calls("GET", "/auth/me")).toHaveLength(1));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByRole("region", { name: "Bulk edit" })).toBeNull();
  });

  it("searches every library each title is monitored in, without picking one; a title monitored nowhere is left out", async () => {
    const titles = [
      { id: 1, kind: "movie", name: "Film 1", versions: [{ key: "movies-hd", label: "HD", monitored: true }, { key: "movies-4k", label: "4K", monitored: true }] },
      { id: 2, kind: "movie", name: "Film 2", versions: [{ key: "movies-hd", label: "HD", monitored: false }] },
      { id: 3, kind: "movie", name: "Film 3", versions: [{ key: "movies-4k", label: "4K", monitored: true }] },
    ] as unknown as TitleRow[];
    const api = mockFetch([me(["manage"]), wanted, { method: "POST", path: "/wanted/search", reply: answer({ queued: 3, started: 3 }) }]);
    renderPage(<BulkBar titles={titles} columns={columns} onClear={() => {}} />);
    expect(await screen.findByText("3 titles selected")).toBeTruthy();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Search now" }));
    const dialog = await screen.findByRole("dialog", { name: "Search 3 titles" });
    expect(within(dialog).getByTestId("confirm-does").textContent).toContain("for 2 titles in every library each is monitored in (3 searches)");
    expect(within(dialog).getByTestId("confirm-keeps").textContent).toContain("1 title monitored in no library: not searched");
    await user.click(within(dialog).getByRole("button", { name: "Queue 3 searches" }));
    await screen.findByTestId("bulk-search-result");
    expect(api.calls("POST", "/wanted/search")[0].body).toEqual({ only: "missing", items: [
      { title_id: 1, version: "movies-hd" }, { title_id: 1, version: "movies-4k" }, { title_id: 3, version: "movies-4k" },
    ] });
  });

  it("sends 3 titles in one request and shows the answer", async () => {
    const api = setup(3, [me(["manage", "admin"]), wanted,
      { method: "POST", path: "/wanted/search", reply: answer({ queued: 3, started: 2, pending: 1 }) }]);
    const bar = await searchNow(3);
    const result = await within(bar).findByTestId("bulk-search-result");
    const posts = api.calls("POST", "/wanted/search");
    expect(posts).toHaveLength(1);
    expect(posts[0].body).toEqual({ only: "missing", items: [1, 2, 3].map((id) => ({ title_id: id, version: "movies-hd" })) });
    expect(result.textContent).toMatch(/^Queued 3 searches\. 2 started now, 1 waiting in the queue\. Wanted$/);
    expect(within(result).getByRole("link", { name: "Wanted" }).getAttribute("href")).toBe("/wanted");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("sends 450 titles in 3 requests and adds the answers up", async () => {
    const reply = (r: RequestRecord) => {
      const n = (r.body as { items: unknown[] }).items.length;
      // The queue as it stands after each request: only the last answer's pending is shown.
      return answer({ queued: n, started: n === 50 ? 0 : 5, pending: n === 50 ? 440 : 195 });
    };
    const api = setup(450, [me(["manage", "admin"]), wanted, { method: "POST", path: "/wanted/search", reply }]);
    const bar = await searchNow(450);
    const result = await within(bar).findByTestId("bulk-search-result");
    const posts = api.calls("POST", "/wanted/search");
    expect(posts.map((p) => (p.body as { items: unknown[] }).items.length)).toEqual([200, 200, 50]);
    expect((posts[2].body as { items: { title_id: number }[] }).items[0].title_id).toBe(401);
    expect(result.textContent).toBe("Queued 450 searches. 10 started now, 440 waiting in the queue. Wanted");
  });

  it("groups what the server skipped by its reason", async () => {
    setup(3, [me(["manage", "admin"]), wanted, {
      method: "POST", path: "/wanted/search", reply: answer({ queued: 1, started: 1, skipped: [
        { title_id: 2, version: "movies-hd", reason: "nothing of it is wanted now" },
        { title_id: 3, version: "movies-hd", reason: "nothing of it is wanted now" },
      ] }),
    }]);
    const bar = await searchNow(3);
    const result = await within(bar).findByTestId("bulk-search-result");
    expect(result.textContent).toBe("Queued 1 search. 1 started now, 0 waiting in the queue. Skipped 2 titles: nothing of it is wanted now. Wanted");
  });

  it("shows the server's message when downloads are off", async () => {
    const api = setup(3, [me(["manage", "admin"]), wanted,
      { method: "POST", path: "/wanted/search", reply: json({ error: "Theatarr is paused, so nothing can be searched." }, 409) }]);
    const bar = await searchNow(3);
    const result = await within(bar).findByTestId("bulk-search-result");
    expect(within(result).getByRole("alert").textContent?.trim()).toBe("Theatarr is paused, so nothing can be searched.");
    expect(result.textContent).not.toContain("Queued");
    expect(within(result).queryByRole("link")).toBeNull();
    expect(api.calls("POST", "/wanted/search")).toHaveLength(1);
  });

  it("says before the button is pressed that nothing is searched while paused", async () => {
    const api = setup(3, [me(["manage", "admin"]), { ...wanted, reply: { ...wanted.reply, downloads_enabled: false } }]);
    const user = userEvent.setup();
    const bar = await screen.findByRole("region", { name: "Bulk edit" });
    await user.click(await within(bar).findByRole("button", { name: "Search now" }));
    const dialog = await screen.findByRole("dialog", { name: "Search 3 titles" });
    expect((await within(dialog).findByTestId("confirm-blocked")).textContent).toBe("Theatarr is paused, so nothing can be searched.");
    expect(within(dialog).getByRole("button", { name: "Queue 3 searches" })).toHaveProperty("disabled", true);
    expect(api.calls("POST", "/wanted/search")).toHaveLength(0);
  });

  it("stops at a failed request and says what the earlier ones queued", async () => {
    let n = 0;
    const reply = () => (++n === 1 ? answer({ queued: 200, started: 4, pending: 196 }) : json({ error: "Theatarr is paused, so nothing can be searched." }, 409));
    const api = setup(450, [me(["manage", "admin"]), wanted, { method: "POST", path: "/wanted/search", reply }]);
    const bar = await searchNow(450);
    const result = await within(bar).findByTestId("bulk-search-result");
    expect(api.calls("POST", "/wanted/search")).toHaveLength(2);
    expect(result.textContent).toBe("Queued 200 searches. 4 started now, 196 waiting in the queue. Stopped after 200 of 450 searches: Theatarr is paused, so nothing can be searched. Wanted");
  });

  it("finishing leaves a dialog opened since alone", async () => {
    let release: () => void = () => {};
    const held = new Promise<void>((r) => { release = r; });
    setup(3, [me(["manage"]), wanted,
      { method: "POST", path: "/wanted/search", reply: async () => { await held; return answer({ queued: 3 }); } },
      { method: "POST", path: "/titles/monitor", reply: { dry_run: true, changed: 0, unchanged: 0, missing: 0, stale: 0, message: "Nothing to change", items: [] } }]);
    const bar = await searchNow(3);
    const user = userEvent.setup();
    await user.click(within(screen.getByRole("dialog", { name: "Search 3 titles" })).getByRole("button", { name: "Cancel" }));
    await user.click(within(bar).getByRole("button", { name: "Monitor" }));
    const review = await screen.findByRole("dialog", { name: "Review bulk edit" });
    release();
    await within(bar).findByTestId("bulk-search-result");
    expect(review.isConnected).toBe(true);
  });
});

describe("Bulk Monitor of a large selection", () => {
  it("previews and applies in groups of 500, joined, and Apply waits for the preview", async () => {
    let release: () => void = () => {};
    const held = new Promise<void>((res) => { release = res; });
    const reply = async (r: RequestRecord) => {
      const b = r.body as { title_ids: number[]; dry_run?: boolean; versions?: string[] };
      if (b.dry_run && b.versions) await held;
      const items = b.title_ids.map((id) => ({ title_id: id, title: `Film ${id}`, version: "movies-hd", label: "HD", outcome: "changed", from: true, to: false }));
      return { dry_run: !!b.dry_run, items, changed: items.length, unchanged: 0, missing: 0, stale: 0, message: "one group" };
    };
    const api = setup(1100, [me(["manage"]), { method: "POST", path: "/titles/monitor", reply }]);
    const user = userEvent.setup();
    const bar = await screen.findByRole("region", { name: "Bulk edit" });
    await user.click(within(bar).getByRole("button", { name: "Unmonitor" }));
    const dialog = await screen.findByRole("dialog", { name: "Review bulk edit" });
    await waitFor(() => expect(within(dialog).getByTestId("bulk-message").textContent).toBe("Would unmonitor 1100 libraries"));
    const sizes = () => api.calls("POST", "/titles/monitor").map((c) => (c.body as { title_ids: number[] }).title_ids.length);
    expect(sizes()).toEqual([500, 500, 100]);
    // Another library: Apply is off until its preview is in.
    await user.selectOptions(within(dialog).getByRole("combobox", { name: "Libraries" }), "movies-hd");
    await waitFor(() => expect(sizes()).toHaveLength(4));
    expect((within(dialog).getByRole("button", { name: /Apply to|Nothing to change/ }) as HTMLButtonElement).disabled).toBe(true);
    release();
    const applyButton = await within(dialog).findByRole("button", { name: "Apply to 1100 libraries" });
    await waitFor(() => expect((applyButton as HTMLButtonElement).disabled).toBe(false));
    await user.click(applyButton);
    await waitFor(() => expect(within(dialog).getByTestId("bulk-message").textContent).toBe("Unmonitored 1100 libraries"));
    const applies = api.calls("POST", "/titles/monitor").slice(6) as RequestRecord[];
    expect(applies.map((c) => (c.body as { title_ids: number[] }).title_ids.length)).toEqual([500, 500, 100]);
    expect(applies.map((c) => (c.body as { expect: unknown[] }).expect.length)).toEqual([500, 500, 100]);
    expect((applies[2].body as { title_ids: number[] }).title_ids[0]).toBe(1001);
  });
});
