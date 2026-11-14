// @vitest-environment jsdom
import { cleanup, screen } from "@testing-library/react";
import { Route, Routes } from "react-router";
import { afterEach, expect, it } from "vitest";
import { json, mockFetch, renderPage } from "@/test/page";
import { RegistryContent } from "./registry";

afterEach(cleanup);

it("says a torrent the saved inventory does not know yet will appear after the next sync, and leads back to Torrents", async () => {
  const hash = "ab".repeat(20);
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin", "see_files"] } },
    { method: "GET", path: `/registry/${hash}`, reply: json({ error: "not found" }, 404) },
  ]);
  renderPage(<Routes><Route path="/admin/torrents/inventory/:hash" element={<RegistryContent />} /></Routes>, `/admin/torrents/inventory/${hash}`);
  const box = await screen.findByTestId("inventory-unknown");
  expect(box.textContent).toContain("Not in the saved inventory yet. It appears after the next provider sync.");
  expect(screen.getByRole("link", { name: "See it on Torrents" }).getAttribute("href")).toBe(`/admin/torrents?q=${hash.slice(0, 12)}`);
});
