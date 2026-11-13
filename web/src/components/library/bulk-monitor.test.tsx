// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { TitleRow } from "@/api/library";
import type { CopyColumn } from "@/lib/copies";
import { json, mockFetch, renderPage, type RequestRecord, type Route } from "@/test/page";
import { BulkBar } from "./bulk-monitor";

const managed = { mode: "active", can_edit: true, can_search: true, live_releases: true };
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

/** Pick the library, press Search now and confirm. */
async function searchNow(n: number) {
  const user = userEvent.setup();
  const bar = screen.getByRole("region", { name: "Bulk edit" });
  await user.selectOptions(within(bar).getByLabelText("Target libraries"), "movies-hd");
  await user.click(await within(bar).findByRole("button", { name: "Search now" }));
  const dialog = await screen.findByRole("dialog", { name: `Search ${n} titles in HD` });
  await waitFor(() => expect(within(dialog).getByTestId("bulk-search-estimate").textContent).toBe("Searches start as the hourly limit allows: up to 20 an hour."));
  await user.click(within(dialog).getByRole("button", { name: `Queue ${n} titles` }));
  return bar;
}

describe("Bulk Search now", () => {
  it("is hidden without admin", async () => {
    const api = setup(3, [me(["see_files", "manage"])]);
    await waitFor(() => expect(api.calls("GET", "/auth/me")).toHaveLength(1));
    await new Promise((r) => setTimeout(r, 20));
    const bar = screen.getByRole("region", { name: "Bulk edit" });
    expect(within(bar).getByRole("button", { name: "Review changes" })).toBeTruthy();
    expect(within(bar).queryByRole("button", { name: "Search now" })).toBeNull();
    expect(api.calls("GET", "/wanted")).toHaveLength(0);
  });

  it("needs one library, and asks nothing of the server until confirmed", async () => {
    const api = setup(3, [me(["manage", "admin"])]);
    const button = await screen.findByRole("button", { name: "Search now" }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(screen.getByText("Search now needs one library: pick it above.")).toBeTruthy();
    expect(screen.getByTestId("bulk-local").textContent).toContain("No search is started.");
    expect(api.calls("GET", "/wanted")).toHaveLength(0);
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
    expect(within(result).getByRole("link", { name: "Wanted" }).getAttribute("href")).toBe("/wanted?version=movies-hd");
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

  it("stops at a failed request and says what the earlier ones queued", async () => {
    let n = 0;
    const reply = () => (++n === 1 ? answer({ queued: 200, started: 4, pending: 196 }) : json({ error: "Theatarr is paused, so nothing can be searched." }, 409));
    const api = setup(450, [me(["manage", "admin"]), wanted, { method: "POST", path: "/wanted/search", reply }]);
    const bar = await searchNow(450);
    const result = await within(bar).findByTestId("bulk-search-result");
    expect(api.calls("POST", "/wanted/search")).toHaveLength(2);
    expect(result.textContent).toBe("Queued 200 searches. 4 started now, 196 waiting in the queue. Stopped after 200 of 450 titles: Theatarr is paused, so nothing can be searched. Wanted");
  });
});
