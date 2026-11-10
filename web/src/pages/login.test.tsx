// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { expect, it } from "vitest";
import { Login } from "./login";
import { json, mockFetch, renderPage } from "@/test/page";

const signedOut = { method: "GET", path: "/auth/me", reply: json({ error: "authentication required" }, 401) };
const noPlex = { method: "GET", path: "/auth/plex", reply: { enabled: false } };
const status = (needs: boolean, pending = false) => ({ method: "GET", path: "/auth/first-account", reply: { needs_account: needs, password_pending: pending } });

function renderLogin() {
  return renderPage(<Routes><Route path="/login" element={<Login />} /><Route path="/" element={<p>Home page</p>} /></Routes>, "/login");
}

it("shows Create your admin account on a new install, checks the form and lands signed in", async () => {
  const user = userEvent.setup();
  let created = false;
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: () => created ? { username: "admin", kind: "session", permissions: ["admin"], csrf_token: "c" } : json({ error: "authentication required" }, 401) },
    { method: "GET", path: "/auth/first-account", reply: () => ({ needs_account: !created, password_pending: false }) },
    noPlex,
    { method: "POST", path: "/auth/first-account", reply: () => { created = true; return { username: "admin", csrf_token: "new-csrf" }; } },
  ]);
  renderLogin();
  expect(await screen.findByRole("heading", { name: "Create your admin account" })).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "Sign in" })).toBeNull();
  expect((screen.getByLabelText("Username") as HTMLInputElement).value).toBe("admin");

  await user.click(screen.getByRole("button", { name: "Create account" }));
  expect((await screen.findByRole("alert")).textContent).toBe("The password must be at least 12 characters.");
  await user.type(screen.getByLabelText("Password"), "a long enough password");
  await user.type(screen.getByLabelText("Confirm password"), "a different password");
  await user.click(screen.getByRole("button", { name: "Create account" }));
  expect((await screen.findByRole("alert")).textContent).toBe("The passwords do not match.");
  await user.clear(screen.getByLabelText("Username"));
  await user.click(screen.getByRole("button", { name: "Create account" }));
  expect((await screen.findByRole("alert")).textContent).toBe("Enter a username.");
  expect(fetch.calls("POST", "/auth/first-account")).toHaveLength(0);

  await user.type(screen.getByLabelText("Username"), "admin");
  await user.clear(screen.getByLabelText("Confirm password"));
  await user.type(screen.getByLabelText("Confirm password"), "a long enough password");
  await user.click(screen.getByRole("button", { name: "Create account" }));
  expect(await screen.findByText("Home page")).toBeTruthy();
  expect(fetch.calls("POST", "/auth/first-account")[0].body).toEqual({ username: "admin", password: "a long enough password" });
});

it("says so when the install was claimed meanwhile", async () => {
  const user = userEvent.setup();
  mockFetch([signedOut, status(true), noPlex, { method: "POST", path: "/auth/first-account", reply: json({ error: "this install already has an account; sign in instead" }, 409) }]);
  renderLogin();
  await user.type(await screen.findByLabelText("Password"), "a long enough password");
  await user.type(screen.getByLabelText("Confirm password"), "a long enough password");
  await user.click(screen.getByRole("button", { name: "Create account" }));
  expect((await screen.findByRole("alert")).textContent).toContain("already has an account");
});

it("shows the normal sign-in page once an account exists, without the password hint", async () => {
  mockFetch([signedOut, status(false), noPlex]);
  renderLogin();
  expect(await screen.findByRole("heading", { name: "Sign in" })).toBeTruthy();
  expect(screen.queryByText("Create your admin account")).toBeNull();
  expect(screen.queryByTestId("initial-password-hint")).toBeNull();
});

it("points to the generated password file only while it is pending", async () => {
  mockFetch([signedOut, status(false, true), noPlex]);
  renderLogin();
  expect(await screen.findByRole("heading", { name: "Sign in" })).toBeTruthy();
  await waitFor(() => expect(screen.getByTestId("initial-password-hint").textContent).toContain("initial-admin-password"));
  expect(screen.queryByRole("button", { name: "Create account" })).toBeNull();
});

it("falls back to the sign-in form when the status cannot be read", async () => {
  mockFetch([signedOut, { method: "GET", path: "/auth/first-account", reply: json({ error: "not found" }, 404) }, noPlex]);
  renderLogin();
  expect(await screen.findByRole("heading", { name: "Sign in" })).toBeTruthy();
});
