// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { DiscordID } from "./users";
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
