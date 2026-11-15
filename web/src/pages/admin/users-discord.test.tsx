// @vitest-environment jsdom
import { cleanup, render, screen, within } from "@testing-library/react";
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


it("leads with an invite link, shown once after it is made, keeps the sign-in page as one line, lists open invites and locks your own role", async () => {
 const { mockFetch, renderPage } = await import("@/test/page");
 const { UsersAdmin } = await import("./users");
 const me = { ...user(), id: 1, username: "admin", display_name: "Admin", role: "admin", local: true };
 const invited = { ...user(), id: 9, username: "invite-ab12", display_name: "Bea", status: "invited", invite_expires_at: "2099-01-08T00:00:00Z" };
 const api = mockFetch([
  { method: "GET", path: "/auth/me", reply: { kind: "session", id: 1, username: "admin", permissions: ["admin", "users"], csrf_token: "t" } },
  { method: "GET", path: "/users", reply: { items: [me, user(), invited], settings: { plex_new_users: "pending", plex_enabled: true }, versions: [] } },
  { method: "GET", path: "/keys", reply: { items: [] } },
  { method: "POST", path: "/users/invites", reply: { status: 201, body: { token: "tok123", user: { ...invited, id: 10, display_name: "Cy" } } } },
 ]);
 const ue = userEvent.setup();
 renderPage(<UsersAdmin />, "/admin/users");
 const card = await screen.findByTestId("sign-in-link");
 expect(card.textContent).toContain("Make an invite link for one person");
 expect(screen.getByTestId("sign-in-approval").textContent).toBe(`Without an invite: send the sign-in page, ${window.location.origin}/login. A first Plex sign-in there waits under Waiting for approval.`);
 expect(screen.getByTestId("sign-in-roles").textContent).toBe("User: browses and requests; sees availability only. Manager: runs the library: adds, approves requests, sees files. Admin: everything, with the admin panel and users.");
 // An open invite is listed with its expiry, not among the accounts.
 const rows = screen.getAllByTestId("invite-row");
 expect(rows).toHaveLength(1);
 expect(rows[0].textContent).toContain("Bea");
 expect(rows[0].textContent).toContain("Link works until");
 await ue.click(within(card).getByRole("radio", { name: "Manager" }));
 await ue.type(screen.getByTestId("invite-name"), "Cy");
 await ue.click(screen.getByTestId("invite-create"));
 expect((await screen.findByLabelText("Invite link") as HTMLInputElement).value).toBe(`${window.location.origin}/invite#tok123`);
 expect(api.calls("POST", "/users/invites")[0].body).toEqual({ role: "manager", auto_approve: true, name: "Cy", days: 7 });
 const roles = await screen.findAllByTestId("user-role") as HTMLSelectElement[];
 // Your own row cannot change its role; another account's can.
 const accounts = screen.getAllByTestId("user-row");
 const mine = accounts.find(r => r.getAttribute("data-username") === "admin")!.querySelector("select") as HTMLSelectElement;
 const ann = accounts.find(r => r.getAttribute("data-username") === "ann")!.querySelector("select") as HTMLSelectElement;
 expect(roles).toHaveLength(2);
 expect(mine.disabled).toBe(true);
 expect(ann.disabled).toBe(false);
});
