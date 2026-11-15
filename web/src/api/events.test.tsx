// @vitest-environment jsdom
import { act, render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import { LiveProvider } from "./events";

class FakeSource {
  static all: FakeSource[] = [];
  closed = false;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(public url: string) {
    FakeSource.all.push(this);
  }
  addEventListener() {}
  close() {
    this.closed = true;
  }
}

afterEach(() => {
  vi.unstubAllGlobals();
  FakeSource.all = [];
});

// Over HTTP/1.1 a browser allows six connections to the server: streams of pages already left (another address
// opened, or kept in the back/forward cache) held them, and the next title page's requests waited for a minute.
it("closes the event stream when the page is left, and reopens it and refetches when the page is shown again", () => {
  vi.stubGlobal("EventSource", FakeSource);
  const qc = new QueryClient();
  const refetch = vi.spyOn(qc, "invalidateQueries");
  render(
    <QueryClientProvider client={qc}>
      <LiveProvider enabled>
        <p>page</p>
      </LiveProvider>
    </QueryClientProvider>,
  );
  expect(FakeSource.all).toHaveLength(1);
  act(() => {
    window.dispatchEvent(new PageTransitionEvent("pagehide", { persisted: true }));
  });
  expect(FakeSource.all[0].closed).toBe(true);
  expect(refetch).not.toHaveBeenCalled();
  act(() => {
    window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }));
  });
  expect(FakeSource.all).toHaveLength(2);
  expect(FakeSource.all[1].closed).toBe(false);
  // It missed the events meanwhile: what it shows is read again.
  expect(refetch).toHaveBeenCalledTimes(1);
  // A first show (not from the cache) opens nothing more.
  act(() => {
    window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: false }));
  });
  expect(FakeSource.all).toHaveLength(2);
});

it("refetches once when the stream reconnects after a drop, not on its first open", () => {
  vi.stubGlobal("EventSource", FakeSource);
  const qc = new QueryClient();
  const refetch = vi.spyOn(qc, "invalidateQueries");
  render(
    <QueryClientProvider client={qc}>
      <LiveProvider enabled>
        <p>page</p>
      </LiveProvider>
    </QueryClientProvider>,
  );
  const es = FakeSource.all[0];
  act(() => es.onopen?.());
  expect(refetch).not.toHaveBeenCalled();
  // A settings save closes the stream; the browser reconnects the same source.
  act(() => es.onerror?.());
  act(() => es.onerror?.());
  act(() => es.onopen?.());
  expect(refetch).toHaveBeenCalledTimes(1);
  expect(refetch).toHaveBeenCalledWith();
  act(() => es.onopen?.());
  expect(refetch).toHaveBeenCalledTimes(1);
});
