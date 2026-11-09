// The API client: same-origin fetch to /api/theatarr/v1 with the session cookie. Every non-GET carries the session's CSRF
// token in X-CSRF-Token (docs/design/web.md). A 401 sends the user to /login?next=<here>.
import type { components } from "./schema";

export type Schemas = components["schemas"];
export const API_BASE = "/api/theatarr/v1";

let csrfToken = "";
export function setCSRF(t: string | undefined) {
  csrfToken = t ?? "";
}

export class ApiError extends Error {
  status: number;
  /** The error body's detail lines, when the server sent some (a grab's rejections). */
  detail?: string[];
  /** The parsed error body, when it carries more than the message (a 409's current generation, a 429's test result). */
  body?: unknown;
  /** Retry-After in seconds, when the server sent one (a 429). */
  retryAfter?: number;
  constructor(status: number, message: string, detail?: string[], body?: unknown, retryAfter?: number) {
    super(message);
    this.status = status;
    this.detail = detail;
    if (body !== undefined) this.body = body;
    if (retryAfter !== undefined) this.retryAfter = retryAfter;
  }
}

export function loginURL(): string {
  const here = window.location.pathname + window.location.search;
  return here.startsWith("/login") ? "/login" : `/login?next=${encodeURIComponent(here)}`;
}

type Opts = { method?: string; body?: unknown; redirectOn401?: boolean; signal?: AbortSignal; headers?: Record<string, string> };

export async function api<T>(path: string, opts: Opts = {}): Promise<T> {
  const method = opts.method ?? "GET";
  const headers: Record<string, string> = { ...opts.headers, Accept: "application/json" };
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET" && method !== "HEAD" && csrfToken) headers["X-CSRF-Token"] = csrfToken;
  const res = await fetch(API_BASE + path, {
    method,
    headers,
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
    credentials: "same-origin",
    signal: opts.signal,
  });
  if (res.status === 401 && opts.redirectOn401 !== false) {
    window.location.assign(loginURL());
    throw new ApiError(401, "authentication required");
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: unknown = undefined;
  try {
    data = text ? JSON.parse(text) : undefined;
  } catch {
    // not JSON
  }
  if (!res.ok) {
    const body = data as { error?: string; detail?: string[] } | undefined;
    const ra = res.headers.get("Retry-After");
    const retryAfter = ra && /^\d+$/.test(ra.trim()) ? Number(ra.trim()) : undefined;
    const more = data && typeof data === "object" && Object.keys(data).some((k) => k !== "error" && k !== "detail");
    throw new ApiError(res.status, body?.error ?? `${res.status} ${res.statusText}`, body?.detail, more ? data : undefined, retryAfter);
  }
  return data as T;
}

/** A query string from the defined values. */
export function qs(params: Record<string, string | number | boolean | undefined | null>): string {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== "") u.set(k, String(v));
  }
  const s = u.toString();
  return s ? `?${s}` : "";
}
