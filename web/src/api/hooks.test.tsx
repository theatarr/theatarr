// @vitest-environment jsdom
import { QueryClient, QueryClientProvider, focusManager, notifyManager } from "@tanstack/react-query";
import { act, cleanup, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mockFetch } from "@/test/page";
import { useLogFiles, useLogLevel, useLogs, useSetLogLevel, type Logs } from "./hooks";
import { useRequest } from "./requests";

function provider() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
  return ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children);
}

async function tick(ms = 0) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms); });
}

const page = (items: Logs["items"], after: number, extra: Partial<Logs> = {}): Logs => ({ items, components: [], boot_id: "boot-a", after, more: false, skipped: 0, seen: after, ...extra });

const line = (seq: number, time = "2026-10-30T12:00:00.123456789Z") => ({ seq, time, level: "INFO", msg: `Line ${seq}` });

beforeEach(() => {
  vi.useFakeTimers();
  notifyManager.setScheduler(queueMicrotask);
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  vi.spyOn(document, "hidden", "get").mockReturnValue(false);
  focusManager.setFocused(undefined);
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  focusManager.setFocused(undefined);
  notifyManager.setScheduler((callback) => { setTimeout(callback, 0); });
});

describe("log polling", () => {
  it("fetches the level once, slows file polling, and invalidates the level after its mutation", async () => {
    let level = "INFO";
    const fetch = mockFetch([
      { method: "GET", path: "/logs/level", reply: () => ({ level }) },
      { method: "GET", path: "/logs/files", reply: { items: [] } },
      { method: "PUT", path: "/logs/level", reply: () => { level = "WARN"; return { level }; } },
    ]);
    const { result } = renderHook(() => ({ level: useLogLevel(), files: useLogFiles(), change: useSetLogLevel() }), { wrapper: provider() });
    await tick();
    await tick(29_999);
    expect(fetch.calls("GET", "/logs/level")).toHaveLength(1);
    expect(fetch.calls("GET", "/logs/files")).toHaveLength(1);
    await tick(1);
    expect(fetch.calls("GET", "/logs/files")).toHaveLength(2);
    await act(async () => { await result.current.change.mutateAsync("warn"); });
    await tick();
    expect(fetch.calls("GET", "/logs/level")).toHaveLength(2);
    expect(result.current.level.data?.level).toBe("WARN");
  });

  it("polls by sequence through equal timestamps, clock rollback and empty pages, and resets by boot id", async () => {
    let poll = 0;
    const pages: Logs[] = [
      page([line(100)], 100),
      page([line(101, "2026-10-30T11:00:00Z")], 101),
      page([], 110, { components: ["library"] }),
      page([], 0, { boot_id: "boot-b" }),
      page([line(120, "2026-10-30T10:00:00Z")], 120, { boot_id: "boot-b" }),
    ];
    const fetch = mockFetch([{ method: "GET", path: "/logs", reply: () => pages[poll++] }]);
    const { result } = renderHook(() => useLogs("info", ""), { wrapper: provider() });
    await tick();
    expect(new URLSearchParams(fetch.calls("GET", "/logs")[0].search).has("after")).toBe(false);
    await tick(5000);
    const second = new URLSearchParams(fetch.calls("GET", "/logs")[1].search);
    expect(second.get("after")).toBe("100");
 expect(second.get("seen")).toBe("100");
    expect(second.get("boot_id")).toBe("boot-a");
    expect(second.has("since")).toBe(false);
    expect(result.current.data?.items.map((entry) => entry.seq)).toEqual([100, 101]);
    await tick(5000);
    expect(result.current.data?.items.map((entry) => entry.seq)).toEqual([100, 101]);
    expect(result.current.data?.components).toEqual(["library"]);
    await tick(5000);
    expect(new URLSearchParams(fetch.calls("GET", "/logs")[3].search).get("after")).toBe("110");
    expect(result.current.data?.items).toEqual([]);
 expect(result.current.data?.restartTotal).toBe(1);
    await tick(5000);
    expect(new URLSearchParams(fetch.calls("GET", "/logs")[4].search).get("boot_id")).toBe("boot-b");
    expect(result.current.data?.items.map((entry) => entry.seq)).toEqual([120]);
  });

  it("drains bursts larger than one page and reports ring overflow", async () => {
    let poll = 0;
    const pages = [page([line(1)], 1),
      page(Array.from({ length: 500 }, (_, i) => line(i + 2)), 501, { more: true }),
      page(Array.from({ length: 250 }, (_, i) => line(i + 502)), 751),
      page([line(1000)], 1000, { skipped: 248 }),
      page([], 1001),
    ];
    const fetch = mockFetch([{ method: "GET", path: "/logs", reply: () => pages[poll++] }]);
    const { result } = renderHook(() => useLogs("info", ""), { wrapper: provider() });
    await tick();
    await tick(5000);
    expect(fetch.calls("GET", "/logs")).toHaveLength(3);
    expect(new URLSearchParams(fetch.calls("GET", "/logs")[2].search).get("after")).toBe("501");
    expect(result.current.data?.items.map((e) => e.seq)).toEqual(Array.from({ length: 751 }, (_, i) => i + 1));
    await tick(5000);
    expect(result.current.data?.skippedTotal).toBe(248);
    await tick(5000);
    expect(result.current.data?.skippedTotal).toBe(248);
  });

  it("keeps at most 2000 lines and starts a changed filter without a cursor", async () => {
    let poll = 0;
    const fetch = mockFetch([{ method: "GET", path: "/logs", reply: () => page(
      poll++ === 0 ? Array.from({ length: 2000 }, (_, i) => line(i + 1)) : [line(2001)], 2001,
    ) }]);
    const { result, rerender } = renderHook(({ level }) => useLogs(level, ""), { wrapper: provider(), initialProps: { level: "info" } });
    await tick();
    await tick(5000);
    expect(result.current.data?.items).toHaveLength(2000);
    expect(result.current.data?.items[0].seq).toBe(2);
    rerender({ level: "error" });
    await tick();
    expect(new URLSearchParams(fetch.calls("GET", "/logs").at(-1)!.search).has("after")).toBe(false);
  });

  it("pauses log, file and request polling while the document is hidden and resumes when visible", async () => {
    const fetch = mockFetch([
      { method: "GET", path: "/logs", reply: page([line(1)], 1) },
      { method: "GET", path: "/logs/files", reply: { items: [] } },
      { method: "GET", path: "/requests/1", reply: { id: 1 } },
    ]);
    renderHook(() => { useLogs("info", ""); useLogFiles(); useRequest(1); }, { wrapper: provider() });
    await tick();
    vi.mocked(Object.getOwnPropertyDescriptor(document, "visibilityState")!.get!).mockReturnValue("hidden");
    vi.mocked(Object.getOwnPropertyDescriptor(document, "hidden")!.get!).mockReturnValue(true);
    act(() => { window.dispatchEvent(new Event("visibilitychange")); });
    await tick(60_000);
    expect(fetch.requests).toHaveLength(3);
    vi.mocked(Object.getOwnPropertyDescriptor(document, "visibilityState")!.get!).mockReturnValue("visible");
    vi.mocked(Object.getOwnPropertyDescriptor(document, "hidden")!.get!).mockReturnValue(false);
    act(() => { window.dispatchEvent(new Event("visibilitychange")); });
    await tick(15_000);
    expect(fetch.calls("GET", "/logs").length).toBeGreaterThan(1);
    expect(fetch.calls("GET", "/logs/files").length).toBeGreaterThan(1);
    expect(fetch.calls("GET", "/requests/1").length).toBeGreaterThan(1);
  });
});
