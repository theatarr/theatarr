import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, type RenderOptions } from "@testing-library/react";
import { afterEach, expect, vi } from "vitest";
import { createElement, type ReactElement } from "react";
import { MemoryRouter } from "react-router";
import { setCSRF } from "@/api/client";

export type RequestRecord = { method: string; path: string; search: string; headers: Record<string, string>; body?: unknown; signal?: AbortSignal };
export type Reply = { status?: number; body?: unknown; headers?: Record<string, string> };
export type Route = { method: string; path: string; reply: unknown };

/** Render a page with the providers its hooks expect and an in-memory URL; fetch stays inside this test. */
export function renderPage(ui: ReactElement, path = "/", options?: Omit<RenderOptions, "wrapper">) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } });
  return render(
    createElement(MemoryRouter, { initialEntries: [path] },
      createElement(QueryClientProvider, { client }, ui)),
    options,
  );
}

/** Routes the app's same-origin API calls by method/path, records headers and JSON bodies, and never reaches a server. */
export function mockFetch(routes: Route[]) {
  const requests: RequestRecord[] = [];
  const uses = new Map<Route, number>();
  setCSRF("ui-test-csrf");
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url, window.location.origin);
    const headers = Object.fromEntries(new Headers(init.headers).entries());
    const request: RequestRecord = {
      method: (init.method ?? "GET").toUpperCase(),
      path: url.pathname.replace(/^\/api\/theatarr\/v1/, ""),
      search: url.search,
      headers,
      body: typeof init.body === "string" ? JSON.parse(init.body) : undefined,
      signal: init.signal ?? undefined,
    };
    requests.push(request);
    const matching = routes.filter((candidate) => candidate.method.toUpperCase() === request.method && candidate.path === request.path);
    const route = matching.find((candidate) => !uses.has(candidate)) ?? matching[0];
    if (!route) throw new Error(`Unexpected API request: ${request.method} ${request.path}`);
    uses.set(route, (uses.get(route) ?? 0) + 1);
    const value = typeof route.reply === "function"
      ? await (route.reply as (request: RequestRecord) => unknown | Promise<unknown>)(request)
      : route.reply;
    const reply = value && typeof value === "object" && ("body" in value || "status" in value || "headers" in value)
      ? value as Reply
      : { body: value };
    return new Response(reply.body === undefined ? null : JSON.stringify(reply.body), {
      status: reply.status ?? 200,
      headers: { "Content-Type": "application/json", ...reply.headers },
    });
  });
  return {
    requests,
    calls: (method: string, path: string) => requests.filter((request) => request.method === method.toUpperCase() && request.path === path),
  };
}

export const json = (body: unknown, status = 200): Reply => ({ status, body });

/** Ensure every visible form control can be found through its accessible role and a non-empty accessible name. */
export function expectNamedControls() {
  for (const role of ["textbox", "combobox", "checkbox", "spinbutton"] as const) {
    const all = screen.queryAllByRole(role);
    expect(screen.queryAllByRole(role, { name: /\S/ })).toHaveLength(all.length);
  }
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setCSRF(undefined);
});
