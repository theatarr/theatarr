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
it("closes the event stream when the page is left and reopens it when the page is shown again", () => {
  vi.stubGlobal("EventSource", FakeSource);
  render(
    <QueryClientProvider client={new QueryClient()}>
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
  act(() => {
    window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }));
  });
  expect(FakeSource.all).toHaveLength(2);
  expect(FakeSource.all[1].closed).toBe(false);
});
