import { describe, expect, it } from "vitest";
import { adminLink, journalHome, journalLink, torrentsLink } from "./admin-links";

describe("admin links", () => {
  it("point at the current admin pages", () => {
    expect(torrentsLink("ABC123def4567890")).toBe("/admin/torrents?q=abc123def456");
    expect(journalLink("dl:4/1")).toBe("/admin/system/journal/dl%3A4%2F1");
    expect(journalHome()).toBe("/admin/system/journal");
    expect(journalHome("?title=70")).toBe("/admin/system/journal?title=70");
  });
  it("are for admins only", () => {
    expect(adminLink(true, torrentsLink("abc"))).toBe("/admin/torrents?q=abc");
    expect(adminLink(false, torrentsLink("abc"))).toBeUndefined();
  });
});
