// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { afterEach, expect, it } from "vitest";
import { InvitePage } from "./invite";
import { json, mockFetch, renderPage } from "@/test/page";

afterEach(() => window.history.replaceState(null, "", "/"));

function renderInvite(token: string) {
  window.history.replaceState(null, "", `/invite${token ? `#${token}` : ""}`);
  return renderPage(<Routes><Route path="/invite" element={<InvitePage />} /><Route path="/" element={<p>Home page</p>} /></Routes>, "/invite");
}

it("lets an invited person choose a username and password, sends the token in the body only, and lands on Home", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([
    { method: "POST", path: "/auth/invite", reply: { name: "Bea", expires_at: "2099-01-01T00:00:00Z", plex: true } },
    { method: "POST", path: "/auth/invite/accept", reply: { username: "bea", csrf_token: "c" } },
    { method: "GET", path: "/auth/me", reply: { kind: "session", id: 5, username: "bea", permissions: ["browse"], csrf_token: "c" } },
  ]);
  renderInvite("tok123");
  expect(await screen.findByRole("heading", { name: /^Join / })).toBeTruthy();
  expect(screen.getByTestId("invite-line").textContent).toContain("Bea, you have been invited");
  expect(screen.getByTestId("invite-plex")).toBeTruthy();
  expect((screen.getByLabelText("Username") as HTMLInputElement).value).toBe("bea");
  await user.type(screen.getByLabelText("Password"), "a long enough password");
  await user.type(screen.getByLabelText("Confirm password"), "a long enough password");
  await user.click(screen.getByRole("button", { name: "Create account" }));
  expect(await screen.findByText("Home page")).toBeTruthy();
  expect(fetch.calls("POST", "/auth/invite/accept")[0].body).toEqual({ token: "tok123", username: "bea", password: "a long enough password" });
  expect(fetch.requests.every((r) => !r.search.includes("tok123"))).toBe(true);
});

it("says plainly when a link is expired, used or missing, and offers nothing else", async () => {
  mockFetch([{ method: "POST", path: "/auth/invite", reply: json({ error: "This invite link has expired. Ask for a new one." }, 410) }]);
  const r = renderInvite("old");
  expect((await screen.findByTestId("invite-line")).textContent).toBe("This invite link has expired. Ask for a new one.");
  expect(screen.queryByRole("button")).toBeNull();
  expect(screen.queryByRole("textbox")).toBeNull();
  r.unmount();
  renderInvite("");
  expect((await screen.findByTestId("invite-line")).textContent).toBe("This invite link is not valid or has already been used.");
});
