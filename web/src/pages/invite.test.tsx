// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Link, Route, Routes } from "react-router";
import { expect, it } from "vitest";
import { InvitePage } from "./invite";
import { json, mockFetch, renderPage } from "@/test/page";

function renderInvite(token: string) {
  return renderPage(<><Link to="/invite#second">another link</Link><Routes><Route path="/invite" element={<InvitePage />} /><Route path="/" element={<p>Home page</p>} /></Routes></>, `/invite${token ? `#${token}` : ""}`);
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

it("reads a second invite link opened in the same tab afresh", async () => {
  const user = userEvent.setup();
  const fetch = mockFetch([
    { method: "POST", path: "/auth/invite", reply: (r: { body?: unknown }) => (r.body as { token: string }).token === "second"
      ? { name: "Cy", expires_at: "2099-01-01T00:00:00Z", plex: false }
      : json({ error: "This invite link has expired. Ask for a new one." }, 410) },
  ]);
  renderInvite("old");
  expect((await screen.findByTestId("invite-line")).textContent).toBe("This invite link has expired. Ask for a new one.");
  await user.click(screen.getByRole("link", { name: "another link" }));
  expect((await screen.findByTestId("invite-line")).textContent).toContain("Cy, you have been invited");
  expect((screen.getByLabelText("Username") as HTMLInputElement).value).toBe("cy");
  expect(fetch.calls("POST", "/auth/invite").map((r) => (r.body as { token: string }).token)).toEqual(["old", "second"]);
});

it("ends on the dead-link message when the invite is gone at a Plex sign-in", async () => {
  const opened = { location: { href: "" }, close: () => {} };
  const realOpen = window.open;
  window.open = (() => opened) as unknown as typeof window.open;
  try {
    const user = userEvent.setup();
    mockFetch([
      { method: "POST", path: "/auth/invite", reply: { name: "", expires_at: "2099-01-01T00:00:00Z", plex: true } },
      { method: "POST", path: "/auth/plex/start", reply: { id: "pin", auth_url: "https://app.plex.tv/auth", expires_at: "2099-01-01T00:00:00Z" } },
      { method: "POST", path: "/auth/plex/poll", reply: json({ status: "invite_gone", error: "This invite link is not valid or has already been used." }, 404) },
    ]);
    renderInvite("tok");
    await user.click(await screen.findByTestId("invite-plex"));
    expect(await screen.findByText("This invite link is not valid or has already been used.", {}, { timeout: 4000 })).toBeTruthy();
    expect(screen.queryByRole("textbox")).toBeNull();
  } finally {
    window.open = realOpen;
  }
});
