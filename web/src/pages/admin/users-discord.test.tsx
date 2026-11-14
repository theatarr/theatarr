// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { DiscordID, permissionWords } from "./users";
import type { User } from "@/api/users";
afterEach(cleanup);
const user = (discord_id?: string) => ({ id: 3, username: "ann", display_name: "Ann", role: "user", status: "active", local: false,
 extra_permissions: [], permissions: [], auto_approve: false, request_versions: null, created_at: "2026-11-01T00:00:00Z", sessions: 0, keys: 0, discord_id }) as User;
it("saves a Discord user id, refuses other text and clears it", async () => {
 const onSave = vi.fn();
 const ue = userEvent.setup();
 const r = render(<DiscordID u={user("")} busy={false} onSave={onSave} />);
 const input = screen.getByLabelText("Discord user id");
 const save = screen.getByRole("button", { name: "Save" }) as HTMLButtonElement;
 expect(save.disabled).toBe(true);
 await ue.type(input, "@ann");
 expect(screen.getByRole("alert").textContent).toBe("A Discord user id is 17 to 20 digits.");
 expect(save.disabled).toBe(true);
 await ue.clear(input);
 await ue.type(input, " 123456789012345678 ");
 await ue.click(save);
 expect(onSave).toHaveBeenLastCalledWith("123456789012345678");
 r.unmount();
 render(<DiscordID u={user("123456789012345678")} busy={false} onSave={onSave} />);
 await ue.clear(screen.getByLabelText("Discord user id"));
 await ue.click(screen.getByRole("button", { name: "Save" }));
 expect(onSave).toHaveBeenLastCalledWith("");
});

it("says an account's permissions in words, not their stored names", () => {
 expect(permissionWords(["browse", "request", "auto_approve", "manage_requests", "see_files", "own_keys"]))
  .toBe("browse the library, request titles, have requests apply at once, approve and decline requests, see files, sizes and releases, make their own API keys");
 expect(permissionWords(["future_perm"])).toBe("future_perm");
 expect(permissionWords([])).toBe("nothing yet");
});


it("offers a copyable sign-in link that says what approval means and what each role does, and locks your own role", async () => {
 const { mockFetch, renderPage } = await import("@/test/page");
 const { UsersAdmin } = await import("./users");
 const me = { ...user(), id: 1, username: "admin", display_name: "Admin", role: "admin", local: true };
 mockFetch([
  { method: "GET", path: "/auth/me", reply: { kind: "session", id: 1, username: "admin", permissions: ["admin", "users"], csrf_token: "t" } },
  { method: "GET", path: "/users", reply: { items: [me, user()], settings: { plex_new_users: "pending", plex_enabled: true }, versions: [] } },
  { method: "GET", path: "/keys", reply: { items: [] } },
 ]);
 renderPage(<UsersAdmin />, "/admin/users");
 const card = await screen.findByTestId("sign-in-link");
 expect((screen.getByLabelText("Sign-in link") as HTMLInputElement).value).toBe(`${window.location.origin}/login`);
 expect(card.textContent).toContain("Copy link");
 expect(screen.getByTestId("sign-in-approval").textContent).toContain("waits for approval");
 expect(screen.getByTestId("sign-in-roles").textContent).toBe("User: browses and requests; sees availability only. Manager: runs the library: adds, approves requests, sees files. Admin: everything, with the admin panel and users.");
 const roles = await screen.findAllByTestId("user-role") as HTMLSelectElement[];
 // Your own row cannot change its role; another account's can.
 const rows = screen.getAllByTestId("user-row");
 const mine = rows.find(r => r.getAttribute("data-username") === "admin")!.querySelector("select") as HTMLSelectElement;
 const ann = rows.find(r => r.getAttribute("data-username") === "ann")!.querySelector("select") as HTMLSelectElement;
 expect(roles).toHaveLength(2);
 expect(mine.disabled).toBe(true);
 expect(ann.disabled).toBe(false);
});
