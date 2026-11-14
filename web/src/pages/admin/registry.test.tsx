// @vitest-environment jsdom
import { cleanup, screen, waitFor } from "@testing-library/react";
import { Route, Routes } from "react-router";
import { afterEach, expect, it } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import { Registry, RegistryContent } from "./registry";

afterEach(cleanup);

it("says a torrent the saved inventory does not know yet will appear after the next sync, and leads back to Torrents", async () => {
  const hash = "ab".repeat(20);
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "see_files"] } },
    { method: "GET", path: `/registry/${hash}`, reply: json({ error: "not found" }, 404) },
  ]);
  renderPage(<Routes><Route path="/admin/torrents/inventory/:hash" element={<RegistryContent />} /></Routes>, `/admin/torrents/inventory/${hash}`);
  const box = await screen.findByTestId("inventory-unknown");
  expect(box.textContent).toContain("Not in the saved inventory. It appears after a provider sync if one of your provider accounts holds it.");
  expect(screen.getByRole("link", { name: "See it on Torrents" }).getAttribute("href")).toBe(`/admin/torrents?q=${hash.slice(0, 12)}`);
});

it("lists each hash with the Torrents page's usage and links, the state in words that do not claim usage, and a count in torrents", async () => {
  const a = "a".repeat(40);
  const b = "b".repeat(40);
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "see_files"] } },
    { method: "GET", path: "/registry", reply: { total: 3, items: [
      { info_hash: a, name: "[SoM] Dragon Ball Z", state: "unreferenced", last_seen_at: "2026-11-13T10:00:00Z", usage: "used", linked_files: 291 },
      { info_hash: b, name: "Other", state: "absent", last_seen_at: "2026-11-01T10:00:00Z" },
      { info_hash: "e".repeat(40), name: "Pending", state: "unreferenced", last_seen_at: "2026-11-01T10:00:00Z", usage: "unknown",
        usage_reason: "its usage is worked out after the next provider sync; Refresh works it out now" },
    ] } },
  ]);
  renderPage(<Registry />, "/admin/torrents/inventory");
  await screen.findByText("[SoM] Dragon Ball Z");
  const used = screen.getAllByTestId("inventory-used").map((c) => c.textContent);
  // A hash not worked out yet reads as its entry does, with the reason as tooltip.
  expect(used).toEqual(["used291 links", "—", "not worked out"]);
  expect(screen.getAllByTestId("inventory-used")[2].firstElementChild?.getAttribute("title")).toMatch(/^its usage is worked out/);
  expect(screen.getAllByText("Held, not tied to an import").map((e) => e.tagName)).toContain("SPAN");
  expect(screen.queryByText("unreferenced")).toBeNull();
  expect(screen.queryByRole("columnheader", { name: "Libraries" })).toBeNull();
  expect(screen.getByTestId("state-unreferenced").textContent).toBe("Held, not tied to an import");
  expect(screen.getByTestId("inventory-total").textContent).toBe("3 torrents (unique hashes)");
});

it("shows a hash's usage with five references and how many more, the providers with a header and a gone badge, its copies, and no empty sections", async () => {
  const hash = "c".repeat(40);
  const refs = Array.from({ length: 20 }, (_, i) => ({ kind: "link", detail: `/lib/E${i + 1}.mkv`, title_id: 7, title: "Dragon Ball Z" }));
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "see_files"] } },
    { method: "GET", path: "/status", reply: { versions: [] } },
    { method: "GET", path: "/torrents/copies", reply: { copies: [{ provider: "rd", state: "not_cached", updated_at: "2026-11-13T12:00:00Z" }] } },
    { method: "GET", path: `/registry/${hash}`, reply: {
      info_hash: hash, name: "[SoM] Dragon Ball Z", state: "unreferenced", size_bytes: 1024, first_seen_at: "2026-10-01T00:00:00Z", last_seen_at: "2026-11-13T00:00:00Z",
      providers: [
        { provider: "tb", torrent_id: "77", folder_name: "[SoM] Dragon Ball Z", seen_at: "2026-11-01T00:00:00Z", state: "present" },
        { provider: "ad", torrent_id: "848864501", folder_name: "[SoM] Dragon Ball Z", seen_at: "2026-10-01T00:00:00Z", state: "absent", absent_at: "2026-10-26T00:00:00Z" },
      ],
      files: [], versions: [], arr_files: [], links: [],
      usage: { usage: "used", linked_files: 291, refs: { link: 291 }, ref_list: refs, titles: [{ id: 7, name: "Dragon Ball Z" }] },
    } },
  ]);
  renderPage(<Routes><Route path="/admin/torrents/inventory/:hash" element={<RegistryContent />} /></Routes>, `/admin/torrents/inventory/${hash}`);
  const usage = await screen.findByTestId("inventory-usage");
  expect(usage.textContent).toContain("291 links into it");
  expect(usage.querySelectorAll("[data-testid=usage-refs] li")).toHaveLength(5);
  expect(screen.getByTestId("usage-refs-more").textContent).toBe("and 286 more");
  expect(screen.getByTestId("torrent-titles").querySelector("a")?.getAttribute("href")).toBe("/title/7");
  expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Provider", "Name", "Torrent id", "Held since"]);
  const rows = screen.getAllByTestId("inventory-provider");
  expect(rows[0].textContent).toContain("TB");
  expect(rows[0].querySelector("[data-testid=provider-gone]")).toBeNull();
  expect(rows[1].querySelector("[data-testid=provider-gone]")?.textContent).toMatch(/^gone since /);
  expect(screen.queryByRole("heading", { name: "Arr files" })).toBeNull();
  expect(screen.queryByText("No library points at this content.")).toBeNull();
  expect(screen.getByTestId("add-to-providers")).toBeTruthy();
  // The hash's copies are read when the entry opens.
  await waitFor(() => expect(screen.getByTestId("other-providers").textContent).toMatch(/^RD not cached/));
});

it("says a hash no account holds has no usage worked out, not unused", async () => {
  const hash = "d".repeat(40);
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "see_files"] } },
    { method: "GET", path: "/status", reply: { versions: [] } },
    { method: "GET", path: `/registry/${hash}`, reply: { info_hash: hash, name: "Gone", state: "gone", providers: [], files: [], versions: [], arr_files: [], links: [] } },
  ]);
  renderPage(<Routes><Route path="/admin/torrents/inventory/:hash" element={<RegistryContent />} /></Routes>, `/admin/torrents/inventory/${hash}`);
  expect((await screen.findByTestId("inventory-usage")).textContent).toBe("No account holds it in the record, so its usage is not worked out.");
  expect(screen.queryByTestId("inventory-copies")).toBeNull();
  expect(screen.getByText("Removed").className).toMatch(/bad-ink/);
});
