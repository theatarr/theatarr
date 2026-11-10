// @vitest-environment jsdom
import { cleanup, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { Login } from "@/pages/login";
import { mockFetch, renderPage } from "@/test/page";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("says where a new install's first admin password is", async () => {
  mockFetch([
    { method: "GET", path: "/auth/me", reply: { status: 401, body: { error: "signed out" } } },
    { method: "GET", path: "/auth/plex", reply: { enabled: false } },
  ]);
  renderPage(<Login />, "/login");
  const hint = await screen.findByTestId("first-password-hint");
  expect(hint.textContent).toContain("container log");
  expect(hint.textContent).toContain("<data dir>/initial-admin-password");
});
