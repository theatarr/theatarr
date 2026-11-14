// @vitest-environment jsdom
import { useState } from "react";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { sectionSecrets, emptySecrets } from "./section-draft";
import { SectionFields, objectValue } from "./field-input";
import type { SchemaField, SettingsSection } from "@/api/settings";
afterEach(cleanup);
const secret: SchemaField = { name: "password", label: "Password", type: "secret", help: "" };
const text: SchemaField = { name: "url", label: "URL", type: "string", help: "" };
function Form({ section, fields, errors = {} }: { section: SettingsSection; fields: SchemaField[]; errors?: Record<string, string> }) {
 const [config, setConfig] = useState(section.config);
 const [draft, setDraft] = useState(emptySecrets);
 return <><SectionFields fields={fields} config={objectValue(config)} onChange={setConfig} path={section.key} advanced={false} secrets={section.secrets.map(s => ({ ...s, path: `${section.key}.${s.path}` }))} secretDraft={draft} onSecrets={setDraft} errors={errors} /><output data-testid="body">{JSON.stringify({ config, ...sectionSecrets(section.key, draft) })}</output></>;
}
const body = () => JSON.parse(screen.getByTestId("body").textContent!);
function clients(stored = false) {
 render(<Form section={{ key: "clients", live: true, yaml: "", config: { old: { url: "http://client" } }, secrets: stored ? [{ path: "old.password", set: true }] : [] }} fields={[{ name: "", label: "Clients", type: "keyed", help: "", fields: [text, secret] }]} />);
 fireEvent.click(screen.getByText("old", { selector: "summary" }));
 fireEvent.change(screen.getByLabelText("Rename old"), { target: { value: "new" } });
}
it("rename stored client requires credentials and sends only the new secret path", () => {
 clients(true);
 expect(screen.getByText("Renaming needs its credentials again")).toBeTruthy();
 const rename = screen.getByRole("button", { name: "Rename old" });
 expect((rename as HTMLButtonElement).disabled).toBe(true);
 fireEvent.change(screen.getByLabelText("Rename Password"), { target: { value: "replacement" } });
 fireEvent.click(rename);
 expect(body()).toEqual({ config: { new: { url: "http://client" } }, secrets: { "new.password": "replacement" }, clear_secrets: [] });
});
it("rename after Replace moves the draft", () => {
 clients(true);
 fireEvent.click(screen.getByTestId("secret-replace"));
 fireEvent.change(screen.getByTestId("secret-input"), { target: { value: "draft" } });
 fireEvent.click(screen.getByRole("button", { name: "Rename old" }));
 expect(body().secrets).toEqual({ "new.password": "draft" });
});
it("plain rename and inline invalid names", () => {
 clients();
 fireEvent.change(screen.getByLabelText("Rename old"), { target: { value: " " } });
 expect(screen.getByText("Enter a name.")).toBeTruthy();
 fireEvent.change(screen.getByLabelText("Rename old"), { target: { value: "new" } });
 fireEvent.click(screen.getByRole("button", { name: "Rename old" }));
 expect(body().config).toEqual({ new: { url: "http://client" } });
});
it("notify rename requires and sends a full URL", () => {
 render(<Form section={{ key: "notify", live: true, yaml: "", config: { targets: { old: { url: "https://example/[redacted]" } } }, secrets: [] }} fields={[{ name: "targets", type: "keyed", help: "", fields: [text] }]} />);
 fireEvent.click(screen.getByText("old", { selector: "summary" }));
 fireEvent.change(screen.getByLabelText("Rename old"), { target: { value: "new" } });
 expect((screen.getByRole("button", { name: "Rename old" }) as HTMLButtonElement).disabled).toBe(true);
 fireEvent.change(screen.getByLabelText("Rename URL"), { target: { value: "https://example/full-token" } });
 fireEvent.click(screen.getByRole("button", { name: "Rename old" }));
 expect(body().config.targets.new.url).toBe("https://example/full-token");
});
it("object and keyed have no reset; leaf reset removes only its key", () => {
 render(<Form section={{ key: "library", live: true, yaml: "", config: { mount: { url: "a", unknown: 7 }, versions: { main: { url: "b" } } }, secrets: [] }} fields={[{ name: "mount", label: "Mount", type: "object", help: "", fields: [text] }, { name: "versions", label: "Versions", type: "keyed", help: "", fields: [text] }]} />);
 expect(screen.queryByRole("button", { name: "Reset Mount" })).toBeNull();
 expect(screen.queryByRole("button", { name: "Reset Versions" })).toBeNull();
 expect(screen.queryByRole("button", { name: "Add Versions" })).toBeNull();
 fireEvent.click(screen.getAllByRole("button", { name: "Reset URL" })[0]);
 expect(body().config.mount).toEqual({ unknown: 7 });
 fireEvent.click(screen.getByText("main", { selector: "summary span" }));
 expect(screen.queryByLabelText("Rename main")).toBeNull();
 expect(screen.queryByRole("button", { name: "Remove main" })).toBeNull();
 fireEvent.change(screen.getAllByLabelText("URL")[1], { target: { value: "updated" } });
 expect(body().config.versions.main.url).toBe("updated");
});
it("unset default true is checked and clicking writes false", () => {
 render(<Form section={{ key: "shadow", live: true, yaml: "", config: {}, secrets: [] }} fields={[{ name: "search", type: "boolean", help: "", default: "true" }]} />);
 expect(screen.getByRole("switch").getAttribute("aria-checked")).toBe("true");
 fireEvent.click(screen.getByRole("switch")); expect(body().config.search).toBe(false);
});
it.each([["6h0m0s", "6h"], ["24h0m0s", "24h"], ["0s", "0s"], ["168h0m0s", "168h"]])("duration %s selects equivalent preset", (duration, selected) => {
 render(<Form section={{ key: "torrents", live: true, yaml: "", config: { every: duration }, secrets: [] }} fields={[{ name: "every", type: "duration", help: "", zero_allowed: true }]} />);
 expect((screen.getByRole("combobox") as HTMLSelectElement).value).toBe(selected);
 fireEvent.change(screen.getByRole("combobox"), { target: { value: "0s" } }); expect(body().config).toEqual({});
});
it("indexed and keyed errors appear beside the offending input", () => {
 const { container } = render(<Form section={{ key: "egress", live: true, yaml: "", config: { vpn_ips: ["bad"], clients: { qbit: { url: "bad" } } }, secrets: [] }} fields={[{ name: "vpn_ips", type: "list", help: "" }, { name: "clients", type: "keyed", help: "", fields: [text] }]} errors={{ "egress.vpn_ips.0": "Bad IP", "egress.clients.qbit.url": "Bad URL" }} />);
 expect(within(screen.getByLabelText("vpn_ips 1").parentElement!).getByText("Bad IP")).toBeTruthy();
 expect(within(container.querySelector('[data-field="egress.clients.qbit.url"]')!).getByText("Bad URL")).toBeTruthy();
});
it.each([[1000000000, "GB", "1"], [2000000, "MB", "2"], [3000, "KB", "3"]])("bytes %s uses largest exact unit", (size, unit, count) => {
 render(<Form section={{ key: "ui", live: true, yaml: "", config: { size }, secrets: [] }} fields={[{ name: "size", type: "bytes", help: "" }]} />);
 expect((screen.getByLabelText("size unit") as HTMLSelectElement).value).toBe(unit);
 expect((screen.getByLabelText("size") as HTMLInputElement).value).toBe(count);
});
it("moves draft clears to the renamed record and refuses duplicate names", () => {
 render(<Form section={{ key: "clients", live: true, yaml: "", config: { old: {}, taken: {} }, secrets: [{ path: "old.password", set: true }] }} fields={[{ name: "", type: "keyed", help: "", fields: [secret] }]} />);
 fireEvent.click(screen.getByText("old", { selector: "summary" }));
 fireEvent.click(screen.getAllByTestId("secret-clear")[0]);
 fireEvent.change(screen.getByLabelText("Rename old"), { target: { value: "taken" } });
 expect(screen.getByText("That name already exists.")).toBeTruthy();
 expect((screen.getByRole("button", { name: "Rename old" }) as HTMLButtonElement).disabled).toBe(true);
 fireEvent.change(screen.getByLabelText("Rename old"), { target: { value: "new" } });
 fireEvent.click(screen.getByRole("button", { name: "Rename old" }));
 expect(body().clear_secrets).toEqual(["new.password"]);
});
