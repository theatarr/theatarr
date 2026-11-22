// @vitest-environment jsdom
import { useState } from "react";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it } from "vitest";
import { SectionFields, objectValue } from "./field-input";
import type { SchemaField } from "@/api/settings";
import { mockFetch } from "@/test/page";
afterEach(cleanup);
const fields: SchemaField[] = [{ name: "targets", label: "Notification destinations", type: "keyed", help: "", fields: [
 { name: "url", label: "Destination URL", type: "string", help: "" },
 { name: "format", label: "Message format", type: "enum", enum: ["discord", "ntfy"], help: "" },
] }];
function Form({ config }: { config: Record<string, unknown> }) {
 const [c, setC] = useState(config);
 return <SectionFields fields={fields} config={objectValue(c)} onChange={setC} path="notify" advanced={false} errors={{}} />;
}
it("sends a Discord target's test and shows the answer", async () => {
 const fetch = mockFetch([
  { method: "POST", path: "/settings/notify/targets/chan/test", reply: { ok: true } },
  { method: "POST", path: "/settings/notify/targets/chan/test", reply: { ok: false, error: "Not sent: notifications are off (notify.enabled is false)" } },
 ]);
 const user = userEvent.setup();
 render(<Form config={{ targets: { chan: { url: "https://discord.com/[redacted]", format: "discord" } } }} />);
 await user.click(screen.getByText("chan", { selector: "summary" }));
 expect((screen.getByRole("combobox", { name: /Message format/ }) as HTMLSelectElement).value).toBe("discord");
 await user.click(screen.getByRole("button", { name: "Send test" }));
 expect(await screen.findByText("Test sent.")).toBeTruthy();
 await user.click(screen.getByRole("button", { name: "Send test" }));
 expect(await screen.findByRole("alert")).toHaveProperty("textContent", "Not sent: notifications are off (notify.enabled is false)");
 await waitFor(() => expect(fetch.calls("POST", "/settings/notify/targets/chan/test")).toHaveLength(2));
});
it("has no Send test outside notification targets or while renaming", async () => {
 mockFetch([]);
 const user = userEvent.setup();
 render(<Form config={{ targets: { chan: { url: "https://discord.com/[redacted]" } } }} />);
 await user.click(screen.getByText("chan", { selector: "summary" }));
 await user.type(screen.getByLabelText("Rename chan"), "2");
 expect(screen.queryByRole("button", { name: "Send test" })).toBeNull();
});
it("shows each destination the fields of its format and drops the URL when it becomes a Telegram one", async () => {
 mockFetch([]);
 const user = userEvent.setup();
 const tgFields: SchemaField[] = [{ name: "targets", label: "Notification destinations", type: "keyed", help: "", fields: [
  { name: "url", label: "Destination URL", type: "string", help: "" },
  { name: "format", label: "Message format", type: "enum", enum: ["discord", "ntfy", "telegram"], help: "" },
  { name: "chat_id", label: "Telegram chat ID", type: "string", help: "" },
 ] }];
 let last: Record<string, unknown> = {};
 function TgForm() {
  const [c, setC] = useState<Record<string, unknown>>({ targets: { chan: { url: "https://discord.com/[redacted]", format: "discord" } } });
  return <SectionFields fields={tgFields} config={objectValue(c)} onChange={(v) => { last = v; setC(v); }} path="notify" advanced={false} errors={{}} />;
 }
 render(<TgForm />);
 await user.click(screen.getByText("chan", { selector: "summary" }));
 expect(screen.getByRole("textbox", { name: /Destination URL/ })).toBeTruthy();
 expect(screen.queryByRole("textbox", { name: /Telegram chat ID/ })).toBeNull();
 await user.selectOptions(screen.getByRole("combobox", { name: /Message format/ }), "telegram");
 expect(screen.queryByRole("textbox", { name: /Destination URL/ })).toBeNull();
 expect(screen.getByRole("textbox", { name: /Telegram chat ID/ })).toBeTruthy();
 expect(last).toEqual({ targets: { chan: { format: "telegram" } } });
});
