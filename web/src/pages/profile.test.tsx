// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { Profile } from "./profile";
import { mockFetch, renderPage } from "@/test/page";

it("keeps Change password disabled until the three fields are filled", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, username: "kid", role: "user", local: true, permissions: ["browse", "request"], csrf_token: "t" } }]);
  const user = userEvent.setup();
  renderPage(<Profile />, "/me");
  const button = (await screen.findByRole("button", { name: "Change password" })) as HTMLButtonElement;
  expect(button.disabled).toBe(true);
  await user.type(screen.getByLabelText("Current password"), "old-password");
  await user.type(screen.getByLabelText("New password"), "a-new-password-1");
  expect(button.disabled).toBe(true);
  await user.type(screen.getByLabelText("New password again"), "a-new-password-1");
  expect(button.disabled).toBe(false);
});
