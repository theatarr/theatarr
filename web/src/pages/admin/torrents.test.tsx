// @vitest-environment jsdom
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { useLocation } from "react-router";
import { describe, expect, it } from "vitest";
import { expectNamedControls, mockFetch, renderPage } from "@/test/page";
import { TorrentBackups } from "@/components/backups/torrent-backups";
import schema from "@/components/settings/testdata/schema.json";
import { TorrentsPage } from "./torrents";
import { SettingsPage } from "./settings/pages";
import { overview } from "@/components/settings/fixtures";

function Where() { return <output data-testid="where">{useLocation().search}</output>; }
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
      ? { provider: "rd", id: "2", hash: h2, name: "Used.Movie", bytes: 1024, usage: "used", action: "delete", override: true, override_token: "o1-x", expected_override_token: "o1-x", refs: { link: 1 }, ref_list: [{ kind: "link", detail: "/lib/Used.mkv" }] }
      : { provider: "rd", id: "2", hash: h2, name: "Used.Movie", bytes: 1024, usage: "used", action: "refuse", refusal: "used: referenced by 1 link", needs_override: true, expected_override_token: "o1-x", refs: { link: 1 }, ref_list: [{ kind: "link", detail: "/lib/Used.mkv" }] },
  ],
  count: override ? 2 : 1, bytes: override ? 2048 : 1024, refused: override ? 0 : 1, digest: "d", confirmation_token: override ? "d1-2" : "d1-1",
  warning: "A full backup of every provider is taken first", deleted: 0, failed: 0,
});

describe("torrents page", () => {
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
    expect(screen.getByTestId("usage-as-of").textContent).not.toMatch(/same time/);
  });

  it("a failed copy with a complete copy elsewhere says so under its status and first in the usage hover", async () => {
    const failed = torrent("2", h2, "Used.Movie", "used", { state: "error", status: "error", progress: 0, refs: { link: 1 }, served_by: ["tb"] });
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: { ...report, providers: [{ ...report.providers[0], torrents: [report.providers[0].torrents[0], failed] }] } },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "GET", path: "/status", reply: { versions: [] } },
    ]);
    renderPage(<TorrentsPage />);
    const line = await screen.findByTestId("torrent-served-by");
    expect(line.textContent).toBe("This copy failed. Another complete copy is on TB.");
    expect(screen.getAllByTestId("torrent-served-by")).toHaveLength(1);
    expect(line.closest("tr")?.querySelector("button[title]")?.getAttribute("title")).toMatch(/^This copy failed\. Another complete copy is on TB\.\n/);
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
    expect(result.textContent).toBe("Read the accounts: RD 1 account torrent · TB could not be read.");
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

  it("finds a pack by an episode number inside its range, shows the range, and Select all shown takes it", async () => {
    const rep = { ...report, providers: [{ ...report.providers[0], torrents: [report.providers[0].torrents[0],
      torrent("6", "6".repeat(40), "[SoM] Dragon Ball Z 001-291", "unused", { parsed: "Dragon Ball Z 1–291" })] }] };
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: rep },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "GET", path: "/status", reply: { versions: [] } },
    ]);
    renderPage(<TorrentsPage />);
    await screen.findByText("[SoM] Dragon Ball Z 001-291");
    fireEvent.change(screen.getByRole("textbox", { name: "Filter torrents" }), { target: { value: "dragon 155" } });
    await waitFor(() => expect(screen.getAllByTestId("torrent-row")).toHaveLength(1));
    expect(screen.queryByText(/1-2-3/)).toBeNull();
    fireEvent.click(screen.getByLabelText("Select all shown"));
    expect((screen.getByLabelText("Select [SoM] Dragon Ball Z 001-291") as HTMLInputElement).checked).toBe(true);
    fireEvent.click(screen.getByLabelText("Select all shown"));
    // The range as the row shows it finds the pack as well, and Select all shown takes it.
    fireEvent.change(screen.getByRole("textbox", { name: "Filter torrents" }), { target: { value: "1–291" } });
    await waitFor(() => expect(screen.getAllByTestId("torrent-row")).toHaveLength(1));
    fireEvent.click(screen.getByLabelText("Select all shown"));
    expect((screen.getByLabelText("Select [SoM] Dragon Ball Z 001-291") as HTMLInputElement).checked).toBe(true);
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

  it("says beside the filters what Not used checks and links the folders' setting; the form is in Settings now", async () => {
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: report },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 4, sections: [{ key: "torrents", config: { scan_roots: ["/other/movies", "/other/tv"] }, secrets: [] }] } },
    ]);
    renderPage(<TorrentsPage />, "/admin/torrents");
    const line = await screen.findByTestId("unused-summary");
    await waitFor(() => expect(line.textContent).toContain("any of the 2 folders of other libraries listed in settings"));
    expect(screen.getByTestId("scan-roots-link").getAttribute("href")).toBe("/admin/settings/downloads/torrents#scan-roots");
    expect(screen.getByTestId("group-unused").textContent).toBe("Not used by Theatarr 1");
    expect(screen.queryByRole("heading", { name: "Other libraries’ folders" })).toBeNull();
  });

  it("splits a hash held twice on one account from a hash on several accounts; each chip counts the chosen account", async () => {
    const h = "d".repeat(40);
    const dup = (p: string, id: string, bytes: number) => ({ ...torrent(id, h, `Dup.${p}.${id}`, "unused"), provider: p, bytes });
    const ad = { provider: "ad", kind: "alldebrid", ok: true, as_of: "2026-10-29T11:00:00Z", account_torrents: 1, unique_hashes: 1, bytes: 1, usage: { used: 0, unused: 1, unknown: 0, unused_bytes: 1, unknown_bytes: 0 },
      torrents: [dup("ad", "13", 1)] };
    const rep = { ...report, providers: [{ ...report.providers[0], torrents: [dup("rd", "12", 5000), report.providers[0].torrents[0], dup("rd", "11", 1)] }, ad],
      duplicate_hash: [{ key: h, label: "Dup", members: [{ provider: "rd", id: "12" }, { provider: "rd", id: "11" }, { provider: "ad", id: "13" }] }] };
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: rep },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
    ]);
    renderPage(<TorrentsPage />, "/admin/torrents?provider=rd&group=duplicate_hash");
    await screen.findByText("Dup.rd.12");
    expect(screen.getByTestId("group-duplicate_hash").textContent).toBe("Duplicate on account 2");
    expect(screen.getByTestId("group-several_accounts").textContent).toBe("On several accounts 2");
    // The copies sit together by id, not by size.
    expect(screen.getAllByTestId("torrent-row").map((r) => r.querySelector("[title]")?.textContent)).toEqual(["Dup.rd.11", "Dup.rd.12"]);
    fireEvent.click(screen.getByRole("tab", { name: "AD" }));
    expect(screen.getByTestId("group-count-duplicate_hash").textContent).toBe("0");
    expect(screen.getByTestId("group-count-several_accounts").textContent).toBe("1");
    expect(screen.getByText("Nothing matches.")).toBeTruthy();
    fireEvent.click(screen.getByTestId("group-several_accounts"));
    expect(screen.getAllByTestId("torrent-row")).toHaveLength(1);
    expect(screen.getByText("Dup.ad.13")).toBeTruthy();
  });

  it("names an API key in the journal as one, and counts torrents with their plural", async () => {
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: report },
      { method: "GET", path: "/torrents/journal", reply: { entries: [
        { id: 2, action: "backup", outcome: "ok", backup_id: "b2", detail: "before deleting 1 torrents", by: "key:cleaner", at: "2026-10-29T12:00:00Z" },
        { id: 1, action: "backup", outcome: "ok", backup_id: "b1", detail: "scheduled: 2946 torrents", by: "schedule", at: "2026-10-29T11:00:00Z" },
      ] } },
    ]);
    renderPage(<TorrentsPage />);
    expect(await screen.findByText("cleaner (API key)")).toBeTruthy();
    expect(screen.getByText("before deleting 1 torrent")).toBeTruthy();
    expect(screen.getByText("scheduled: 2,946 torrents")).toBeTruthy();
  });

  it("links the journal beside the filters", async () => {
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: report },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
    ]);
    renderPage(<TorrentsPage />);
    await screen.findByText("Used.Movie");
    const link = screen.getByRole("link", { name: "Journal" });
    expect(link.getAttribute("href")).toBe("#journal");
    expect(document.getElementById("journal")?.querySelector("h2")?.textContent).toBe("Journal");
    fireEvent.click(link);
  });

  it("keeps the provider, filter, text and sort in the URL", async () => {
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: report },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
    ]);
    renderPage(<><TorrentsPage /><Where /></>, "/admin/torrents?group=unused&sort=name");
    expect(await screen.findByText("Unused.Movie")).toBeTruthy();
    expect(screen.getAllByTestId("torrent-row")).toHaveLength(1);
    expect((screen.getByLabelText("Sort") as HTMLSelectElement).value).toBe("name");
    fireEvent.click(screen.getByTestId("group-all"));
    expect(screen.getAllByTestId("torrent-row")).toHaveLength(2);
    fireEvent.change(screen.getByLabelText("Filter torrents"), { target: { value: "used" } });
    fireEvent.change(screen.getByLabelText("Sort"), { target: { value: "size" } });
    expect(screen.getByTestId("where").textContent).toBe("?q=used");
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
    expect(screen.getByText("A backup is taken first. If a torrent due for deletion can't be backed up, nothing is deleted.")).toBeTruthy();
    expect(screen.getByTestId("refused-item").textContent).toContain("/lib/Used.mkv");
    fireEvent.click(screen.getByTestId("override"));
    await waitFor(() => expect(screen.getByRole("heading", { name: /Delete 2 torrents/ })).toBeTruthy());
    const preview = api.calls("POST", "/torrents/delete/preview");
    expect(preview[preview.length - 1].body).toEqual({ items: [{ provider: "rd", id: "1", hash: h1 }, { provider: "rd", id: "2", hash: h2, override: true, override_token: "o1-x" }] });
    fireEvent.click(screen.getByTestId("confirm-ok"));
    const done = await screen.findByTestId("confirm-done");
    // The backup is named by a link to System › Backup, not by its id.
    expect(done.textContent).toBe("Backup taken (System › Backup). Deleted 2, failed 0.");
    expect(within(done).getByRole("link", { name: "System › Backup" }).getAttribute("href")).toBe("/admin/system/backup");
    const sent = api.calls("POST", "/torrents/delete");
    expect(sent).toHaveLength(1);
    expect((sent[0].body as { confirmation_token: string }).confirmation_token).toBe("d1-2");
  });

  it("keeps an overridden row listed with its checkbox; untick, tick and Confirm send exactly the last preview's set", async () => {
    const api = mockFetch([
      { method: "GET", path: "/torrents/report", reply: report },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "POST", path: "/torrents/delete/preview", reply: (r: { body?: { items: { override?: boolean }[] } }) => plan(!!r.body?.items.some((i) => i.override)) },
      { method: "POST", path: "/torrents/delete", reply: { ...plan(false), backup_id: "b", deleted: 1 } },
    ]);
    renderPage(<TorrentsPage />);
    await screen.findByText("Used.Movie");
    fireEvent.click(screen.getByLabelText("Select all shown"));
    fireEvent.click(screen.getByTestId("delete-selected"));
    await screen.findByRole("heading", { name: /Delete 1 torrent/ });
    // Nothing chosen was added in the last day: the recent choice is not offered.
    expect(screen.queryByTestId("allow-recent")).toBeNull();
    fireEvent.click(screen.getByTestId("override"));
    await screen.findByRole("heading", { name: /Delete 2 torrents/ });
    // The overridden torrent is still listed, ticked, with its references.
    expect(screen.getByTestId("refused-item").textContent).toContain("Used.Movie");
    expect((screen.getByTestId("override") as HTMLInputElement).checked).toBe(true);
    fireEvent.click(screen.getByTestId("override"));
    await screen.findByRole("heading", { name: /Delete 1 torrent/ });
    expect((screen.getByTestId("override") as HTMLInputElement).checked).toBe(false);
    fireEvent.click(screen.getByTestId("confirm-ok"));
    await screen.findByTestId("confirm-done");
    const sent = api.calls("POST", "/torrents/delete")[0].body as { items: unknown[]; confirmation_token: string };
    expect(sent).toEqual({ items: [{ provider: "rd", id: "1", hash: h1 }, { provider: "rd", id: "2", hash: h2 }], confirmation_token: "d1-1" });
    // Done: the recent and override choices and the rows are gone, so the outcome is in view.
    expect(screen.queryByTestId("allow-recent")).toBeNull();
    expect(screen.queryByTestId("override")).toBeNull();
    expect(screen.queryByTestId("refused-item")).toBeNull();
  });

  it("takes only the newest preview: a slow earlier answer arriving late is ignored, and Confirm waits for a pending one", async () => {
    const waiting: (() => void)[] = [];
    let n = 0;
    const api = mockFetch([
      { method: "GET", path: "/torrents/report", reply: report },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "POST", path: "/torrents/delete/preview", reply: (r: { body?: { items: { override?: boolean }[] } }) => {
        const p = plan(!!r.body?.items.some((i) => i.override));
        // The second preview (the tick) answers only when released; the others at once.
        return ++n === 2 ? new Promise((res) => waiting.push(() => res(p))) : p;
      } },
      { method: "POST", path: "/torrents/delete", reply: { ...plan(false), backup_id: "b", deleted: 1 } },
    ]);
    renderPage(<TorrentsPage />);
    await screen.findByText("Used.Movie");
    fireEvent.click(screen.getByLabelText("Select all shown"));
    fireEvent.click(screen.getByTestId("delete-selected"));
    await screen.findByRole("heading", { name: /Delete 1 torrent/ });
    fireEvent.click(screen.getByTestId("override"));
    await waitFor(() => expect(waiting).toHaveLength(1));
    expect((screen.getByTestId("confirm-ok") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByTestId("override"));
    await waitFor(() => expect(api.calls("POST", "/torrents/delete/preview")).toHaveLength(3));
    await waitFor(() => expect((screen.getByTestId("confirm-ok") as HTMLButtonElement).disabled).toBe(false));
    waiting[0]();
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.getByRole("heading", { name: /Delete 1 torrent/ })).toBeTruthy();
    expect((screen.getByTestId("override") as HTMLInputElement).checked).toBe(false);
    fireEvent.click(screen.getByTestId("confirm-ok"));
    await screen.findByTestId("confirm-done");
    expect(api.calls("POST", "/torrents/delete")[0].body).toEqual({ items: [{ provider: "rd", id: "1", hash: h1 }, { provider: "rd", id: "2", hash: h2 }], confirmation_token: "d1-1" });
  });

  it("tick all that have another complete copy ticks only those; a used and recent torrent offers both choices", async () => {
    const h3 = "3".repeat(40), h4 = "4".repeat(40);
    const rep = { ...report, providers: [{ ...report.providers[0], torrents: [torrent("2", h2, "Used.Movie", "used"), torrent("3", h3, "Copy.Movie", "used"), torrent("4", h4, "Lone.Movie", "used")] }] };
    const refs = Array.from({ length: 18 }, (_, i) => ({ kind: "link", detail: `/lib/E${i + 1}.mkv` }));
    const item = (id: string, hash: string, name: string, tok: string, sent: Record<string, string>, extra = {}) =>
      sent[`rd:${id}`] === tok
        ? { provider: "rd", id, hash, name, bytes: 1, usage: "used", action: "delete", override: true, override_token: tok, expected_override_token: tok, refs: { link: 18 }, ref_list: refs, ...extra }
        : { provider: "rd", id, hash, name, bytes: 1, usage: "used", action: "refuse", refusal: "used: referenced by 18 links", needs_override: true, expected_override_token: tok, refs: { link: 18 }, ref_list: refs, ...extra };
    const api = mockFetch([
      { method: "GET", path: "/torrents/report", reply: rep },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "POST", path: "/torrents/delete/preview", reply: (r: { body?: { items: { id: string; override_token?: string }[] } }) => {
        const sent = Object.fromEntries((r.body?.items ?? []).filter((i) => i.override_token).map((i) => [`rd:${i.id}`, i.override_token as string]));
        const items = [
          item("2", h2, "Used.Movie", "o2", sent, { surviving_copy: "12" }),
          item("3", h3, "Copy.Movie", "o3", sent, { surviving_copy: "13" }),
          item("4", h4, "Lone.Movie", "o4", sent, { needs_recent: true }),
        ];
        const count = items.filter((i) => i.action === "delete").length;
        return { items, count, bytes: count, refused: 3 - count, digest: "d", confirmation_token: `d1-${count}`, warning: "", deleted: 0, failed: 0 };
      } },
    ]);
    renderPage(<TorrentsPage />);
    await screen.findByText("Lone.Movie");
    fireEvent.click(screen.getByLabelText("Select all shown"));
    fireEvent.click(screen.getByTestId("delete-selected"));
    await screen.findByRole("heading", { name: /Delete 0 torrents/ });
    expect(screen.getAllByTestId("surviving-copy").map((x) => x.textContent)).toEqual(["Another complete copy stays on RD.", "Another complete copy stays on RD."]);
    // The used and recent torrent: its override and the recent choice both show.
    expect(screen.getByTestId("allow-recent")).toBeTruthy();
    expect(screen.getAllByTestId("override")).toHaveLength(3);
    // Three references, the rest folded.
    expect(screen.getAllByTestId("override-refs")[0].querySelectorAll(":scope > ul > li")).toHaveLength(3);
    expect(screen.getAllByTestId("override-refs-more")[0].textContent).toBe("and 15 more");
    fireEvent.click(screen.getByTestId("override-all-copies"));
    await screen.findByRole("heading", { name: /Delete 2 torrents/ });
    const last = () => api.calls("POST", "/torrents/delete/preview").at(-1)?.body as { items: { id: string; override?: boolean }[] };
    expect(last().items.filter((i) => i.override).map((i) => i.id)).toEqual(["2", "3"]);
    expect((screen.getByTestId("override-all-copies") as HTMLInputElement).checked).toBe(true);
    expect(screen.getAllByTestId("override").map((x) => (x as HTMLInputElement).checked)).toEqual([true, true, false]);
    fireEvent.click(screen.getByTestId("override-all-copies"));
    await screen.findByRole("heading", { name: /Delete 0 torrents/ });
    expect(last().items.filter((i) => i.override)).toEqual([]);
    expect(screen.getByTestId("confirm-blocked").textContent).toBe("Nothing chosen can be deleted as it is.");
  });

  it("keeps Confirm off when the latest preview failed, though the choices match an older one", async () => {
    const waiting: (() => void)[] = [];
    let n = 0;
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: report },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "POST", path: "/torrents/delete/preview", reply: () => {
        n++;
        if (n === 2) return new Promise((res) => waiting.push(() => res(plan(true))));
        if (n === 3) return { status: 502, body: { error: "rd: gateway: backing off" } };
        return plan(false);
      } },
    ]);
    renderPage(<TorrentsPage />);
    await screen.findByText("Used.Movie");
    fireEvent.click(screen.getByLabelText("Select all shown"));
    fireEvent.click(screen.getByTestId("delete-selected"));
    await screen.findByRole("heading", { name: /Delete 1 torrent/ });
    fireEvent.click(screen.getByTestId("override"));
    await waitFor(() => expect(waiting).toHaveLength(1));
    fireEvent.click(screen.getByTestId("override"));
    await screen.findByTestId("confirm-error");
    waiting[0]();
    await new Promise((r) => setTimeout(r, 20));
    expect((screen.getByTestId("confirm-ok") as HTMLButtonElement).disabled).toBe(true);
  });

  it("drops a tick whose copy that stays is gone by the next preview, and previews again without it", async () => {
    let n = 0;
    const api = mockFetch([
      { method: "GET", path: "/torrents/report", reply: report },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "POST", path: "/torrents/delete/preview", reply: (r: { body?: { items: { override?: boolean }[] } }) => {
        n++;
        const p = plan(false);
        // The first preview names the copy that stays; after the tick the copy is gone: a new token, no survivor.
        p.items[1] = n === 1 ? { ...p.items[1], surviving_copy: "12" } : { ...p.items[1], expected_override_token: "o1-gone" };
        if (r.body?.items.some((i) => i.override)) p.items[1] = { ...p.items[1], refusal: "the override does not name this torrent's references as they are now: referenced by 1 link" };
        return p;
      } },
    ]);
    renderPage(<TorrentsPage />);
    await screen.findByText("Used.Movie");
    fireEvent.click(screen.getByLabelText("Select all shown"));
    fireEvent.click(screen.getByTestId("delete-selected"));
    expect((await screen.findByTestId("surviving-copy")).textContent).toBe("Another complete copy stays on RD.");
    fireEvent.click(screen.getByTestId("override"));
    await waitFor(() => expect(api.calls("POST", "/torrents/delete/preview")).toHaveLength(3));
    await waitFor(() => expect((screen.getByTestId("override") as HTMLInputElement).checked).toBe(false));
    expect(screen.queryByTestId("surviving-copy")).toBeNull();
    expect((api.calls("POST", "/torrents/delete/preview")[2].body as { items: { override?: boolean }[] }).items.some((i) => i.override)).toBe(false);
  });

  it("names the chosen torrents the filter hides", async () => {
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: { ...report, providers: [{ ...report.providers[0], torrents: [...report.providers[0].torrents,
        ...[1, 2, 3, 4, 5].map((i) => torrent(String(i + 2), String(i + 2).repeat(40), `Also.${i}`, "used"))] }] } },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "POST", path: "/torrents/delete/preview", reply: plan(false) },
    ]);
    renderPage(<TorrentsPage />);
    await screen.findByText("Used.Movie");
    fireEvent.click(screen.getByLabelText("Select all shown"));
    fireEvent.click(screen.getByTestId("group-unused"));
    fireEvent.click(screen.getByTestId("delete-selected"));
    // Named, with their account: the first three, then how many more.
    expect((await screen.findByTestId("chosen-outside")).textContent).toBe("6 chosen torrents are outside the current filter: RD Used.Movie, RD Also.1, RD Also.2 and 3 more.");
  });

  it("names a refused torrent by its selected name when the provider could not be listed", async () => {
    const refusal = { items: [{ provider: "rd", id: "2", hash: h2, name: "", bytes: 0, action: "refuse", refusal: "the provider could not be listed: rd: gateway: backing off" }],
      count: 0, bytes: 0, refused: 1, digest: "d", confirmation_token: "d0", warning: "", deleted: 0, failed: 0 };
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: report },
      { method: "GET", path: "/torrents/backups", reply: { backups: [], every_s: 0, keep: 30 } },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
      { method: "POST", path: "/torrents/delete/preview", reply: refusal },
    ]);
    renderPage(<TorrentsPage />);
    expect(await screen.findByText("Used.Movie")).toBeTruthy();
    fireEvent.click(screen.getByTestId("group-all"));
    fireEvent.click(screen.getByLabelText("Select all shown"));
    fireEvent.click(screen.getByTestId("delete-selected"));
    const item = await screen.findByTestId("refused-item");
    expect(item.textContent).toContain("Used.Movie: the provider could not be listed: rd: gateway: backing off");
    expect(item.textContent).not.toContain(" 2:");
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
    // The titles sit beside the usage without opening it; the hash is shown plain (this is its page).
    expect([...screen.getByTestId("torrent-row-titles").querySelectorAll("a")].map((l) => [l.textContent, l.getAttribute("href")])).toEqual([["Used", "/title/7"], ["Also", "/title/8"]]);
    expect(screen.getByTestId("torrent-hash").textContent).toBe(h2.slice(0, 12));
    fireEvent.click(screen.getByRole("button", { expanded: false }));
    const refs = screen.getByTestId("torrent-refs");
    expect([...refs.querySelectorAll("a")].map((l) => [l.textContent, l.getAttribute("href")])).toEqual([["Used", "/title/7"], ["Also", "/title/8"], ["Used", "/title/7"]]);
  });

  it("folds the files Theatarr recorded for a torrent under its usage", async () => {
    const files = [{ path: "Pack/E01.mkv", size: 1024 }, { path: "Pack/E02.mkv", size: 2048 }];
    const used = torrent("2", h2, "Used.Movie", "used", { refs: { link: 1 }, recorded_files: files });
    mockFetch([
      { method: "GET", path: "/torrents/report", reply: { ...report, providers: [{ ...report.providers[0], torrents: [used] }] } },
      { method: "GET", path: "/torrents/journal", reply: { entries: [] } },
    ]);
    renderPage(<TorrentsPage />);
    await screen.findByText("Used.Movie");
    expect(screen.queryByTestId("torrent-files")).toBeNull();
    fireEvent.click(screen.getByRole("button", { expanded: false }));
    const folded = screen.getByTestId("torrent-files");
    expect(folded.querySelector("summary")?.textContent).toBe("2 files");
    expect([...folded.querySelectorAll("li")].map((l) => l.textContent)).toEqual(["Pack/E01.mkv · 1.0 KiB", "Pack/E02.mkv · 2.0 KiB"]);
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

it("saving the schedule in Settings refreshes what System › Backup says, and keeps Saved", async () => {
 let every = 21600;
 const api = mockFetch([
  { method: "GET", path: "/settings", reply: overview() },
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/policy", reply: { versions: [] } },
  { method: "GET", path: "/torrents/report", reply: report },
  { method: "GET", path: "/torrents/backups", reply: () => ({ backups: [], every_s: every, keep: 30 }) },
  { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 1, sections: [{ key: "torrents", config: { scan_roots: ["/other/movies"], backup: { every: "6h0m0s", keep: 30 } }, secrets: [] }] } },
  { method: "PUT", path: "/settings/sections/torrents", reply: () => { every = 86400; return {}; } },
 ]);
 renderPage(<><SettingsPage /><TorrentBackups /></>, "/admin/settings/downloads/torrents");
 // Download clients › Torrents holds both panels of the torrents section.
 expect(await screen.findByRole("heading", { name: "Other libraries’ folders" })).toBeTruthy();
 expect(await screen.findByDisplayValue("/other/movies")).toBeTruthy();
 await waitFor(() => expect((screen.getByLabelText("Backup interval") as HTMLSelectElement).disabled).toBe(false));
 expect(await screen.findByText(/Scheduled every 6 h/)).toBeTruthy();
 fireEvent.change(screen.getByLabelText("Backup interval"), { target: { value: "24h" } });
 const panel = screen.getByLabelText("Backup interval").closest("[id]") as HTMLElement;
 expect(panel.id).toBe("torrent-backups");
 fireEvent.click(within(panel).getByRole("button", { name: "Save" }));
 await screen.findByText(/Scheduled every 24 h/);
 expect(api.calls("GET", "/torrents/backups").length).toBeGreaterThan(1);
 expect(screen.getByText("Saved.")).toBeTruthy();
 // The schedule form sends only its own field changed; the folders stay as stored.
 expect(api.calls("PUT", "/settings/sections/torrents")[0].body).toEqual({ generation: 1, config: { scan_roots: ["/other/movies"], backup: { every: "24h", keep: 30 } } });
 // The folders form beside it saves the folders and keeps the stored schedule.
 const folders = screen.getByRole("heading", { name: "Other libraries’ folders" }).closest("[id]") as HTMLElement;
 expect(folders.id).toBe("scan-roots");
 fireEvent.change(within(folders).getByDisplayValue("/other/movies"), { target: { value: "/other/films" } });
 fireEvent.click(within(folders).getByRole("button", { name: "Save" }));
 await waitFor(() => expect(api.calls("PUT", "/settings/sections/torrents")).toHaveLength(2));
 expect(api.calls("PUT", "/settings/sections/torrents")[1].body).toEqual({ generation: 1, config: { scan_roots: ["/other/films"], backup: { every: "6h0m0s", keep: 30 } } });
});
