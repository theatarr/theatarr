import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError, qs, setCSRF } from "./client";

describe("api client", () => {
  afterEach(() => vi.unstubAllGlobals());
  it("sends X-CSRF-Token on writes only", async () => {
    const calls: { url: string; init: RequestInit }[] = [];
    vi.stubGlobal("fetch", async (url: string, init: RequestInit) => {
      calls.push({ url, init });
      return new Response('{"ok":true}', { status: 200 });
    });
    setCSRF("tok");
    await api("/status");
    await api("/keys", { method: "POST", body: { name: "x" } });
    const h0 = calls[0].init.headers as Record<string, string>;
    const h1 = calls[1].init.headers as Record<string, string>;
    expect(calls[0].url).toBe("/api/theatarr/v1/status");
    expect(h0["X-CSRF-Token"]).toBeUndefined();
    expect(h1["X-CSRF-Token"]).toBe("tok");
    expect(h1["Content-Type"]).toBe("application/json");
    expect(calls[1].init.credentials).toBe("same-origin");
  });
  it("turns error bodies into ApiError", async () => {
    vi.stubGlobal("fetch", async () => new Response('{"error":"missing or invalid CSRF token"}', { status: 403 }));
    await expect(api("/keys", { method: "POST" })).rejects.toEqual(new ApiError(403, "missing or invalid CSRF token"));
  });
  it("builds query strings from set values", () => {
    expect(qs({ a: "1", b: undefined, c: "", d: 0 })).toBe("?a=1&d=0");
    expect(qs({})).toBe("");
  });
});
