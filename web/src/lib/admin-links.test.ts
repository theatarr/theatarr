import { describe, expect, it } from "vitest";
import { adminLink, inventoryLink, journalLink } from "./admin-links";

describe("admin links", () => {
  it("point at the current admin pages", () => {
    expect(inventoryLink("abc123")).toBe("/admin/torrents/inventory/abc123");
    expect(journalLink("dl:4/1")).toBe("/admin/advanced/journal/dl%3A4%2F1");
  });
  it("are for admins only", () => {
    expect(adminLink(true, inventoryLink("abc"))).toBe("/admin/torrents/inventory/abc");
    expect(adminLink(false, inventoryLink("abc"))).toBeUndefined();
  });
});
