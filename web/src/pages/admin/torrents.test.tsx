// @vitest-environment jsdom
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { expectNamedControls, mockFetch, renderPage } from "@/test/page";
import { TorrentBackups } from "@/components/backups/torrent-backups";
import schema from "@/components/settings/testdata/schema.json";
import { TorrentsPage } from "./torrents";

const h1 = "1".repeat(40);
const h2 = "2".repeat(40);
const torrent = (id: string, hash: string, name: string, usage: string, extra = {}) => ({
  provider: "rd", id, hash, name, bytes: 1024, added: "2026-10-01T00:00:00Z", status: "downloaded", state: "downloaded",
  progress: 100, files: 1, linked_files: 0, usage, added_by_theatarr: false, ...extra,
});
const report = {
  generated_at: "2026-10-29T12:00:00Z",
  providers: [{ provider: "rd", kind: "realdebrid", ok: true, as_of: "2026-10-29T11:00:00Z", account_torrents: 2, unique_hashes: 2, bytes: 2048, usage: { used: 1, unused: 1, unknown: 0, unused_bytes: 1024, unknown_bytes: 0 },
    torrents: [torrent("1", h1, "Unused.Movie", "unused"), torrent("2", h2, "Used.Movie", "used", { refs: { link: 1 }, ref_list: [{ kind: "link", detail: "/lib/Used.mkv" }] })] }],
  unused: [{ provider: "rd", id: "1" }], duplicate_hash: [], duplicate_title: [], failed: [], stalled: [], largest: [],
};
const plan = (override: boolean) => ({
  items: [
    { provider: "rd", id: "1", hash: h1, name: "Unused.Movie", bytes: 1024, usage: "unused", action: "delete" },
    override
      ? { provider: "rd", id: "2", hash: h2, name: "Used.Movie", bytes: 1024, usage: "used", action: "delete", override: true, override_token: "o1-x" }
      : { provider: "rd", id: "2", hash: h2, name: "Used.Movie", bytes: 1024, usage: "used", action: "refuse", refusal: "used: referenced by 1 link", needs_override: true, expected_override_token: "o1-x", refs: { link: 1 }, ref_list: [{ kind: "link", detail: "/lib/Used.mkv" }] },
  ],
  count: override ? 2 : 1, bytes: override ? 2048 : 1024, refused: override ? 0 : 1, digest: "d", confirmation_token: override ? "d1-2" : "d1-1",
  warning: "A full backup of every provider is taken first", deleted: 0, failed: 0,
});

describe("torrents page", () => {
  it("shows every copy result on a row, and a newer report's rows replace the earlier ones", async () => {
    const at = (t: string, copies: object[]) => ({ ...report, generated_at: t, providers: [{ ...report.providers[0], torrents: [torrent("1", h1, "Unused.Movie", "unused", { copies })] }] });
    const reports = [at("2026-11-13T12:00:00Z", [{ provider: "tb", state: "added", updated_at: "2026-11-13T12:00:00Z" }]),
      at("2026-11-13T12:05:00Z", [{ provider: "tb", state: "failed", message: "x", updated_at: "2026-11-13T12:04:00Z" }])];
    let n = 0;
    const fetch = mockFetch([
      { method: "GET", path: "/torrents/report", reply: () => reports[Math.min(n++, 1)] },
      { method: "POST", path: "/torrents/refresh", reply: () => reports[1] },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "GET", path: "/status", reply: { versions: [] } },
    ]);
    renderPage(<TorrentsPage />);
    await screen.findByText("Unused.Movie");
    fireEvent.click(screen.getAllByRole("button", { expanded: false })[0]);
    expect((await screen.findByTestId("other-providers")).textContent).toMatch(/^TB added, /);
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(screen.getByTestId("other-providers").textContent).toMatch(/^TB failed, /));
    expect(fetch.calls("POST", "/torrents/refresh")).toHaveLength(1);
  });

  it("says when the record was read, and an account never read is not an empty one", async () => {
    const tb = { provider: "tb", kind: "torbox", ok: false, never_read: true, account_torrents: 0, unique_hashes: 0, bytes: 0,
      usage: { used: 0, unused: 0, unknown: 0, unused_bytes: 0, unknown_bytes: 0 }, torrents: [] };
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: { ...report, providers: [...report.providers, tb] } },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "GET", path: "/status", reply: { versions: [] } },
    ]);
    renderPage(<TorrentsPage />);
    expect((await screen.findByTestId("provider-count-tb")).textContent).toBe("Not read yet — Refresh reads it");
    expect(screen.queryByTestId("provider-as-of-tb")).toBeNull();
    expect(screen.getByTestId("provider-count-rd").textContent).toBe("2 account torrents · 2 unique hashes · 2.0 KiB · 1 unused (1.0 KiB)");
    expect(screen.getByTestId("provider-as-of-rd").textContent).toMatch(/^as of /);
    // Said once, near the times: usage is as of the record.
    expect(screen.getAllByTestId("usage-as-of")).toHaveLength(1);
  });

  it("keeps showing the earlier record of an account whose read failed, and says so", async () => {
    const failed = { ...report.providers[0], error: "rd: HTTP 500", error_at: "2026-10-29T11:30:00Z" };
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: { ...report, providers: [failed] } },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "GET", path: "/status", reply: { versions: [] } },
    ]);
    renderPage(<TorrentsPage />);
    const problem = await screen.findByTestId("provider-problem-rd");
    expect(problem.textContent).toMatch(/^Could not read RD just now: HTTP 500\. Showing the record as of .+\./);
    expect(problem.textContent).not.toContain("could not be read");
    expect(screen.getByTestId("provider-count-rd").textContent).toContain("2 account torrents");
    expect(screen.getAllByTestId("torrent-row")).toHaveLength(2);
  });

  it("Refresh: busy while it reads, then the result per account and the list from the new record; a failure says so", async () => {
    let release: () => void = () => {};
    const after = { ...report, generated_at: "2026-10-29T13:00:00Z", providers: [{ ...report.providers[0], as_of: "2026-10-29T13:00:00Z", account_torrents: 1, unique_hashes: 1,
      torrents: [torrent("3", "3".repeat(40), "New.Movie", "unused")] }, { provider: "tb", kind: "torbox", ok: false, never_read: true, error: "tb: HTTP 401", account_torrents: 0, unique_hashes: 0, bytes: 0,
      usage: { used: 0, unused: 0, unknown: 0, unused_bytes: 0, unknown_bytes: 0 }, torrents: [] }], refresh_error: "tb: HTTP 401" };
    let fail = false;
    let n = 0;
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: () => (n++ === 0 ? report : after) },
      { method: "POST", path: "/torrents/refresh", reply: () => (fail ? { status: 502, body: { error: "sync failed" } } : new Promise((r) => { release = () => r(after); })) },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "GET", path: "/status", reply: { versions: [] } },
    ]);
    renderPage(<TorrentsPage />);
    await screen.findByText("Used.Movie");
    fireEvent.click(screen.getByTestId("refresh-record"));
    const busy = await screen.findByRole("button", { name: "Reading the accounts…" });
    expect((busy as HTMLButtonElement).disabled).toBe(true);
    release();
    const result = await screen.findByTestId("refresh-result");
    expect(result.textContent).toBe("Read the accounts: RD 1 account torrent · TB could not be read. Not every account was read: tb: HTTP 401.");
    expect(await screen.findByText("New.Movie")).toBeTruthy();
    expect(screen.queryByText("Used.Movie")).toBeNull();
    expect(screen.getByTestId("provider-problem-tb").textContent).toMatch(/^Could not read TB just now: HTTP 401\./);
    fail = true;
    await waitFor(() => expect((screen.getByTestId("refresh-record") as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByTestId("refresh-record"));
    await waitFor(() => expect(screen.getByTestId("refresh-result").textContent).toMatch(/^Refresh failed: .*The page shows the record as it was\./));
  });

  it("shows a torrent whose usage is not worked out with its reason, outside Unused; decoded names with the provider's own as tooltip", async () => {
    const why = "its usage is worked out after the next provider sync; Refresh works it out now";
    const rep = { ...report, providers: [{ ...report.providers[0], torrents: [report.providers[0].torrents[0],
      torrent("5", "5".repeat(40), "[SoM] Dragon Ball Z", "unknown", { reasons: [why], raw_name: "[SoM]%20Dragon%20Ball%20Z" })] }] };
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: rep },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "GET", path: "/status", reply: { versions: [] } },
    ]);
    renderPage(<TorrentsPage />);
    const name = await screen.findByText("[SoM] Dragon Ball Z");
    expect(name.getAttribute("title")).toBe("The provider's name: [SoM]%20Dragon%20Ball%20Z");
    expect(screen.queryByText(/%20/)).toBeNull();
    expect(screen.getByText("not worked out")).toBeTruthy();
    expect(screen.getByTestId("torrent-row-reason").textContent).toBe(why);
    fireEvent.click(screen.getByTestId("group-unused"));
    expect(screen.getAllByTestId("torrent-row")).toHaveLength(1);
    fireEvent.click(screen.getByLabelText("Select all shown"));
    expect(screen.queryByLabelText("Select [SoM] Dragon Ball Z")).toBeNull();
    expect((screen.getByLabelText("Select Unused.Movie") as HTMLInputElement).checked).toBe(true);
  });

  it("lists the first five references of a used torrent and how many more", async () => {
    const list = Array.from({ length: 20 }, (_, i) => ({ kind: "link", detail: `/lib/E${i + 1}.mkv` }));
    const rep = { ...report, providers: [{ ...report.providers[0], torrents: [torrent("2", h2, "Used.Movie", "used", { refs: { link: 291 }, ref_list: list })] }] };
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: rep },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "GET", path: "/status", reply: { versions: [] } },
    ]);
    renderPage(<TorrentsPage />);
    await screen.findByText("Used.Movie");
    fireEvent.click(screen.getByRole("button", { expanded: false }));
    expect(screen.getByTestId("usage-refs").querySelectorAll("li")).toHaveLength(5);
    expect(screen.getByTestId("usage-refs-more").textContent).toBe("and 286 more");
  });

  it("adds a row's torrent to the other providers and polls only that torrent's copies, not the report", async () => {
    const fetch = mockFetch([
      { method: "GET", path: "/torrents/report", reply: report },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "GET", path: "/status", reply: { versions: [] } },
      { method: "POST", path: "/torrents/copy", reply: { copies: [{ provider: "ad", state: "queued", updated_at: "2026-11-13T12:00:00Z" }] } },
      { method: "GET", path: "/torrents/copies", reply: { copies: [{ provider: "ad", state: "not_cached", updated_at: "2026-11-13T12:00:00Z" }] } },
    ]);
    renderPage(<TorrentsPage />);
    await screen.findByText("Unused.Movie");
    fireEvent.click(screen.getAllByRole("button", { expanded: false })[0]);
    fireEvent.click(await screen.findByTestId("add-to-providers"));
    await waitFor(() => expect(fetch.calls("POST", "/torrents/copy")).toHaveLength(1));
    expect(fetch.calls("POST", "/torrents/copy")[0].body).toEqual({ provider: "rd", id: "1" });
    await waitFor(() => expect(fetch.calls("GET", "/torrents/copies").length).toBeGreaterThan(0), { timeout: 8000 });
    await waitFor(() => expect(screen.getByTestId("other-providers").textContent).toMatch(/^AD not cached/));
    // The action's words follow the current result: nothing left being added.
    expect(screen.queryByTestId("add-to-providers-result")).toBeNull();
    expect(fetch.calls("GET", "/torrents/report")).toHaveLength(1);
  }, 15000);

  it("edits other libraries' folders (moved from Settings › General) below the list", async () => {
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: report },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 4, sections: [{ key: "torrents", config: { scan_roots: ["/other/movies"] }, secrets: [] }] } },
      { method: "GET", path: "/settings/schema", reply: schema },
    ]);
    renderPage(<TorrentsPage />, "/admin/torrents#scan-roots");
    const heading = await screen.findByRole("heading", { name: "Other libraries’ folders" });
    expect(heading.closest("[id]")?.id).toBe("scan-roots");
    expect(await screen.findByDisplayValue("/other/movies")).toBeTruthy();
    // Below the list, and only once the list is there, so a #scan-roots link lands where the panel stays.
    const list = await screen.findByRole("table");
    expect(heading.compareDocumentPosition(list) & Node.DOCUMENT_POSITION_PRECEDING).toBeTruthy();
  });
  it("filters by group, previews a delete, needs an override naming the references, and sends the confirmed token", async () => {
    const api = mockFetch([
      { method: "GET", path: "/torrents/report", reply: report },
      { method: "GET", path: "/torrents/backups", reply: { backups: [], every_s: 0, keep: 30 } },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "POST", path: "/torrents/delete/preview", reply: (r: { body?: { items: { override?: boolean }[] } }) => plan(!!r.body?.items.some((i) => i.override)) },
      { method: "POST", path: "/torrents/delete", reply: { ...plan(true), backup_id: "20261029T120000.000Z-pre-delete", deleted: 2 } },
    ]);
    renderPage(<TorrentsPage />);
    expect(await screen.findByText("Used.Movie")).toBeTruthy();
    expect(screen.queryByText(/Scheduled backups are off/)).toBeNull();
    expectNamedControls();
    fireEvent.click(screen.getByTestId("group-unused"));
    expect(screen.getAllByTestId("torrent-row")).toHaveLength(1);
    fireEvent.click(screen.getByTestId("group-all"));
    fireEvent.click(screen.getByLabelText("Select all shown"));
    fireEvent.click(screen.getByTestId("delete-selected"));
    expect(await screen.findByRole("heading", { name: /Delete 1 torrent/ })).toBeTruthy();
    expect(screen.getByText("A full backup of every provider is taken first")).toBeTruthy();
    expect(screen.getByTestId("refused-item").textContent).toContain("/lib/Used.mkv");
    fireEvent.click(screen.getByTestId("override"));
    await waitFor(() => expect(screen.getByRole("heading", { name: /Delete 2 torrents/ })).toBeTruthy());
    const preview = api.calls("POST", "/torrents/delete/preview");
    expect(preview[preview.length - 1].body).toEqual({ items: [{ provider: "rd", id: "1", hash: h1 }, { provider: "rd", id: "2", hash: h2, override: true, override_token: "o1-x" }] });
    fireEvent.click(screen.getByTestId("confirm-ok"));
    expect(await screen.findByTestId("confirm-done")).toBeTruthy();
    const sent = api.calls("POST", "/torrents/delete");
    expect(sent).toHaveLength(1);
    expect((sent[0].body as { confirmation_token: string }).confirmation_token).toBe("d1-2");
  });

  it("shows each account's Provider tag under All providers, takes ?q= into the filter, and links a used torrent's titles", async () => {
    const tb = { provider: "tb", kind: "torbox", ok: true, as_of: "2026-10-29T11:00:00Z", account_torrents: 1, unique_hashes: 1, bytes: 1024, usage: { used: 0, unused: 0, unknown: 1, unused_bytes: 0, unknown_bytes: 1024 },
      torrents: [{ ...torrent("9", "9".repeat(40), "Other.Movie", "unknown"), provider: "tb" }] };
    const used = torrent("2", h2, "Used.Movie", "used", { refs: { link: 1 }, ref_list: [{ kind: "link", detail: "/lib/Used.mkv", title_id: 7, title: "Used" }], titles: [{ id: 7, name: "Used" }, { id: 8, name: "Also" }] });
    const rep = { ...report, providers: [{ ...report.providers[0], torrents: [report.providers[0].torrents[0], used] }, tb] };
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: rep },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
    ]);
    const page = renderPage(<TorrentsPage />);
    await screen.findByText("Used.Movie");
    expect(screen.getByRole("columnheader", { name: "Provider" })).toBeTruthy();
    expect(screen.getAllByTestId("torrent-provider").map((c) => c.textContent).sort()).toEqual(["RD", "RD", "TB"]);
    fireEvent.click(screen.getByRole("tab", { name: "TB" }));
    expect(screen.queryByRole("columnheader", { name: "Provider" })).toBeNull();
    page.unmount();

    renderPage(<TorrentsPage />, `/admin/torrents?q=${h2.slice(0, 12)}`);
    await screen.findByText("Used.Movie");
    expect((screen.getByLabelText("Filter torrents") as HTMLInputElement).value).toBe(h2.slice(0, 12));
    expect(screen.getAllByTestId("torrent-row")).toHaveLength(1);
    // The titles sit beside the usage without opening it; the hash leads to the Saved inventory entry.
    expect([...screen.getByTestId("torrent-row-titles").querySelectorAll("a")].map((l) => [l.textContent, l.getAttribute("href")])).toEqual([["Used", "/title/7"], ["Also", "/title/8"]]);
    expect(screen.getByTestId("torrent-hash").getAttribute("href")).toBe(`/admin/torrents/inventory/${h2}`);
    fireEvent.click(screen.getByRole("button", { expanded: false }));
    const refs = screen.getByTestId("torrent-refs");
    expect([...refs.querySelectorAll("a")].map((l) => [l.textContent, l.getAttribute("href")])).toEqual([["Used", "/title/7"], ["Also", "/title/8"], ["Used", "/title/7"]]);
  });

  it("shows an AllDebrid account's count against its limit and warns near it", async () => {
    const ad = { provider: "ad", kind: "alldebrid", ok: true, as_of: "2026-10-29T11:00:00Z", account_torrents: 4600, unique_hashes: 4600, limit: 5000, near_limit: true, bytes: 0,
      usage: { used: 0, unused: 0, unknown: 0, unused_bytes: 0, unknown_bytes: 0 }, torrents: [] };
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: { ...report, providers: [...report.providers, ad] } },
      { method: "GET", path: "/torrents/backups", reply: { backups: [], every_s: 0, keep: 30 } },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
    ]);
    renderPage(<TorrentsPage />);
    expect((await screen.findByTestId("provider-count-ad")).textContent).toContain("4,600 of 5,000");
    expect(screen.getByTestId("provider-count-rd").textContent).not.toContain(" of ");
    expect(screen.getByTestId("near-limit").textContent).toContain("AD holds 4,600 of its 5,000");
  });
});

it("keeps Saved after the backup query refetches changed values", async () => {
 let every = 21600;
 const api = mockFetch([
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/torrents/report", reply: report },
  { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
  { method: "GET", path: "/torrents/backups", reply: () => ({ backups: [], every_s: every, keep: 30 }) },
  { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 1, sections: [{ key: "torrents", config: { backup: { every: "6h0m0s", keep: 30 } }, secrets: [] }] } },
  { method: "PUT", path: "/settings/sections/torrents", reply: () => { every = 86400; return {}; } },
 ]);
 renderPage(<TorrentBackups />);
 await waitFor(() => expect((screen.getByLabelText("Backup interval") as HTMLSelectElement).disabled).toBe(false));
 fireEvent.change(screen.getByLabelText("Backup interval"), { target: { value: "24h" } });
 fireEvent.click(screen.getByRole("button", { name: "Save" }));
 await screen.findByText(/Scheduled every 24 h/);
 expect(api.calls("GET", "/torrents/backups").length).toBeGreaterThan(1);
 expect(screen.getByText("Saved.")).toBeTruthy();
});
