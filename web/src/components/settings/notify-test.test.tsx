// @vitest-environment jsdom
import { useState } from "react";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it } from "vitest";
import { SectionFields, notifySaveConfig, objectValue, type SecretDraft } from "./field-input";
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
it("shows each destination the fields of its format and keeps the URL and views through a format round trip", async () => {
 mockFetch([]);
 const user = userEvent.setup();
 const tgFields: SchemaField[] = [{ name: "targets", label: "Notification destinations", type: "keyed", help: "", fields: [
  { name: "url", label: "Destination URL", type: "string", help: "" },
  { name: "views", label: "Sonarr/Radarr views", type: "list", help: "" },
  { name: "native", label: "Send Theatarr events", type: "boolean", help: "" },
  { name: "format", label: "Message format", type: "enum", enum: ["discord", "ntfy", "telegram"], help: "" },
  { name: "chat_id", label: "Telegram chat ID", type: "string", help: "" },
  { name: "warnings", label: "Also send warnings", type: "boolean", help: "" },
 ] }];
 let last: Record<string, unknown> = {};
 const start = { targets: { hook: { url: "http://hooks.example/[redacted]", views: ["radarr"], native: true } } };
 function TgForm() {
  const [c, setC] = useState<Record<string, unknown>>(start);
  return <SectionFields fields={tgFields} config={objectValue(c)} onChange={(v) => { last = v; setC(v); }} path="notify" advanced={false} errors={{}} />;
 }
 render(<TgForm />);
 await user.click(screen.getByText("hook", { selector: "summary" }));
 const shown = () => ["Destination URL", "Sonarr/Radarr views", "Send Theatarr events", "Telegram chat ID", "Also send warnings"].filter((l) => screen.queryAllByText(l).length > 0);
 expect(shown()).toEqual(["Destination URL", "Sonarr/Radarr views", "Send Theatarr events"]);
 const format = () => screen.getByRole("combobox", { name: /Message format/ });
 await user.selectOptions(format(), "telegram");
 expect(shown()).toEqual(["Telegram chat ID", "Also send warnings"]);
 await user.selectOptions(format(), "discord");
 expect(shown()).toEqual(["Destination URL", "Also send warnings"]);
 await user.selectOptions(format(), "");
 expect(last).toEqual(start);
});
it("saves a Telegram destination without a URL or views, and each format without what it refuses", () => {
 const section: Record<string, unknown> = { targets: {
  tg: { url: "http://hooks.example/[redacted]", views: ["radarr"], format: "telegram", chat_id: "-100", native: true },
  chan: { url: "https://discord.com/[redacted]", views: ["radarr"], format: "discord", warnings: true },
  hook: { url: "http://hooks.example/x", views: ["radarr"], native: true, warnings: true },
 } };
 const before = structuredClone(section);
 expect(notifySaveConfig(section)).toEqual({ targets: {
  tg: { format: "telegram", chat_id: "-100", native: true },
  chan: { url: "https://discord.com/[redacted]", format: "discord", warnings: true },
  hook: { url: "http://hooks.example/x", views: ["radarr"], native: true },
 } });
 expect(section).toEqual(before);
});
it("drops an empty credential box that a format switch hides", async () => {
 mockFetch([]);
 const user = userEvent.setup();
 const secFields: SchemaField[] = [{ name: "targets", label: "Notification destinations", type: "keyed", help: "", fields: [
  { name: "format", label: "Message format", type: "enum", enum: ["discord", "ntfy", "telegram"], help: "" },
  { name: "auth_header", label: "Authorization header", type: "secret", help: "" },
 ] }];
 let draft: SecretDraft = { actions: { "notify.targets.hook.auth_header": "replace" }, values: {} };
 function SecForm() {
  const [c, setC] = useState<Record<string, unknown>>({ targets: { hook: { url: "http://hooks.example/x", native: true } } });
  const [d, setD] = useState<SecretDraft>(draft);
  return <SectionFields fields={secFields} config={objectValue(c)} onChange={setC} path="notify" advanced={false} errors={{}}
   secrets={[{ path: "notify.targets.hook.auth_header", set: false }]} secretDraft={d} onSecrets={(n) => { draft = n; setD(n); }} />;
 }
 render(<SecForm />);
 await user.click(screen.getByText("hook", { selector: "summary" }));
 await user.selectOptions(screen.getByRole("combobox", { name: /Message format/ }), "telegram");
 expect(draft.actions).toEqual({});
});
