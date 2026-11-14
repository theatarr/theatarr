// @vitest-environment jsdom
import { useState } from "react";
import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { describe, expect, it, afterEach } from "vitest";
import { FieldInput, SectionFields, type SecretDraft } from "./field-input";
import type { SchemaField } from "@/api/settings";
afterEach(cleanup);
function Harness({ field, initial }: { field: SchemaField; initial?: unknown }) {
 const [value, setValue] = useState(initial);
 const [secrets, setSecrets] = useState<SecretDraft>({ actions: {}, values: {} });
 return <><FieldInput field={field} value={value} path={`library.${field.name}`} onChange={setValue} secretDraft={secrets} onSecrets={setSecrets} secrets={[{ path: `library.${field.name}`, set: true, env: "FALLBACK" }]} /><output data-testid="value">{JSON.stringify({ value, secrets })}</output></>;
}
const field = (type: string, extra: Partial<SchemaField> = {}): SchemaField => ({ name: "option", label: "Option", type, help: "Help", ...extra });
const value = () => JSON.parse(screen.getByTestId("value").textContent || "{}");
describe("section field inputs", () => {
 it.each(["string", "integer", "number"])("edits %s and clears it without materializing defaults", (type) => {
  render(<Harness field={field(type, { default: "42" })} />);
  expect(value()).not.toHaveProperty("value");
  fireEvent.change(screen.getByLabelText("Option"), { target: { value: "12" } });
  expect(value().value).toBe(type === "string" ? "12" : 12);
  fireEvent.change(screen.getByLabelText("Option"), { target: { value: "" } });
  expect(value()).not.toHaveProperty("value");
 });
 it("edits boolean, with an explicit default reset", () => {
  render(<Harness field={field("boolean")} />);
  fireEvent.click(screen.getByRole("switch")); expect(value().value).toBe(true);
  // Short visible text (fits at 400 px); the accessible name still says what it resets.
  expect(screen.getByRole("button", { name: "Reset Option" }).textContent).toBe("Reset");
  fireEvent.click(screen.getByRole("button", { name: "Reset Option" })); expect(value()).not.toHaveProperty("value");
 });
 it("offers allowed duration presets and custom text", () => {
  render(<Harness field={field("duration", { min: 3600, max: 86400, zero_allowed: true })} />);
  expect(screen.queryByRole("option", { name: "7 days" })).toBeNull();
  fireEvent.change(screen.getByRole("combobox", { name: "Option" }), { target: { value: "0s" } }); expect(value()).not.toHaveProperty("value");
  fireEvent.change(screen.getByRole("combobox", { name: "Option" }), { target: { value: "custom" } });
  fireEvent.change(screen.getByLabelText("Option unit"), { target: { value: "hours" } });
  fireEvent.change(screen.getByLabelText("Option amount"), { target: { value: "36" } }); expect(value().value).toBe("36h");
 });
 it("offers duration presets on the scale of the field's default", () => {
  render(<Harness field={field("duration", { min: 60, default: "6m" })} />);
  const options = screen.getAllByRole("option").map((o) => o.textContent);
  expect(options).toEqual(["Default (6 minutes)", "1 minute", "5 minutes", "10 minutes", "30 minutes", "1 hour", "Custom"]);
 });
 it("keeps a saved preset of another scale selected, not Custom", () => {
  render(<Harness field={field("duration", { min: 60, default: "6m" })} initial="6h0m0s" />);
  expect((screen.getByRole("combobox") as HTMLSelectElement).value).toBe("6h");
 });
 it("edits enum and byte units", () => {
  const r = render(<Harness field={field("enum", { enum: ["a", "b"] })} />);
  fireEvent.change(screen.getByRole("combobox"), { target: { value: "b" } }); expect(value().value).toBe("b");
  r.unmount(); render(<Harness field={field("bytes")} initial="2GiB" />);
  expect(value().value).toBe("2GiB");
  fireEvent.change(screen.getByLabelText("Option unit"), { target: { value: "GB" } }); expect(value().value).toBe("2GiB");
  fireEvent.change(screen.getByLabelText("Option"), { target: { value: "3" } }); expect(value().value).toBe(3e9);
 });
 it.each(["list", "map", "string_map", "object_list", "keyed"])("adds and removes %s records", (type) => {
  render(<Harness field={field(type, { fields: [field("string")] })} />);
  if (type === "keyed") fireEvent.change(screen.getByLabelText("New Option name"), { target: { value: "main" } });
  fireEvent.click(screen.getByRole("button", { name: "Add Option" }));
  expect(Object.keys(value().value)).toHaveLength(1);
  if (type === "keyed") fireEvent.click(screen.getByText("main", { selector: "summary" }));
  fireEvent.click(screen.getByRole("button", { name: /^Remove/ })); expect(Object.keys(value().value)).toHaveLength(0);
 });
 it("keeps a typed lone minus in a number map value, so a negative weight can be typed", () => {
  render(<Harness field={field("map")} initial={{ best: 5 }} />);
  const input = screen.getByLabelText("Option value best") as HTMLInputElement;
  // A browser reports "" while a number input holds only "-": the input is not rewritten and nothing is saved yet.
  fireEvent.change(input, { target: { value: "" } });
  expect(input.value).toBe("");
  expect(value().value).toEqual({ best: 5 });
  fireEvent.change(input, { target: { value: "-100" } });
  expect(value().value).toEqual({ best: -100 });
  fireEvent.change(input, { target: { value: "2.5" } });
  expect(value().value).toEqual({ best: 2.5 });
  // Left blank, it is 0, as before.
  fireEvent.change(input, { target: { value: "" } });
  fireEvent.blur(input);
  expect(value().value).toEqual({ best: 0 });
 });
 it("edits nested objects preserving unknown children", () => {
  render(<Harness field={field("object", { fields: [{ name: "name", type: "string", help: "Name" }] })} initial={{ unknown: 7 }} />);
  fireEvent.change(screen.getByLabelText("name"), { target: { value: "new" } }); expect(value().value).toEqual({ unknown: 7, name: "new" });
 });
 it("never prefills a secret and tracks clear separately", () => {
  render(<Harness field={field("secret")} />);
  expect(screen.getByText("FALLBACK", { exact: false })).toBeTruthy();
  fireEvent.click(screen.getByTestId("secret-replace")); expect((screen.getByTestId("secret-input") as HTMLInputElement).value).toBe("");
  fireEvent.change(screen.getByTestId("secret-input"), { target: { value: "new-secret" } });
  expect(value().secrets.values["library.option"]).toBe("new-secret");
  fireEvent.click(screen.getByRole("button", { name: "Keep the stored one" })); fireEvent.click(screen.getByTestId("secret-clear")); expect(value().secrets.actions["library.option"]).toBe("clear"); expect(value()).not.toHaveProperty("value");
 });
});
it.each([
 ["string", "seed"], ["integer", 12], ["number", 1.5], ["boolean", true], ["enum", "a"],
 ["duration", "6h0m0s"], ["bytes", "2GB"], ["list", ["seed"]], ["map", { seed: 4 }],
 ["string_map", { seed: "text" }], ["object", { option: "seed" }], ["object_list", [{ option: "seed" }]],
 ["keyed", { seed: { option: "seed" } }], ["secret", undefined],
])("renders seeded %s without rewriting the config", (type, initial) => {
 render(<Harness field={field(type as string, { enum: type === "enum" ? ["a", "b"] : undefined, fields: [field("string")] })} initial={initial} />);
 expect(value().value).toEqual(initial);
});
it.each(["10m", "24h"])("keeps explicit Off for a duration whose unset default is %s", (defaultValue) => {
 render(<Harness field={field("duration", { nullable: true, zero_allowed: true, default: defaultValue })} />);
 expect((screen.getByRole("combobox") as HTMLSelectElement).value).toBe("");
 fireEvent.change(screen.getByRole("combobox"), { target: { value: "0s" } });
 expect(value().value).toBe("0s");
 fireEvent.click(screen.getByRole("button", { name: "Reset Option" }));
 expect(value()).not.toHaveProperty("value");
});

it("shows a record field's key beside its label only when the key says something the label does not", async () => {
 const { ConfigFields } = await import("./field-input");
 render(<ConfigFields fields={[{ name: "requests", label: "Requests", type: "integer", help: "Requests per period." }, { name: "key", label: "Name", type: "string", help: "Stable name." }]} values={{}} errors={{}} onChange={() => {}} />);
 expect(document.body.textContent).not.toMatch(/Requests\s*requests/);
 expect(document.body.textContent).toMatch(/Name\s*key/);
});

it("unfolds the named record a Find a setting link points at, and only that one", () => {
 window.history.pushState({}, "", "/admin/settings/metadata?open=library.option.tmdb#sources");
 try {
  render(<Harness field={field("keyed", { fields: [{ name: "url", label: "URL", type: "string", help: "" }] })} initial={{ tmdb: { url: "a" }, tvdb: { url: "b" } }} />);
  expect((screen.getByText("tmdb", { selector: "summary" }).parentElement as HTMLDetailsElement).open).toBe(true);
  expect((screen.getByText("tvdb", { selector: "summary" }).parentElement as HTMLDetailsElement).open).toBe(false);
 } finally {
  window.history.pushState({}, "", "/");
 }
});

describe("plain units, stored values kept until edited", () => {
 it("shows a stored custom duration in words and keeps it; an edit writes a Go duration", () => {
  render(<Harness field={field("duration", { min: 3600, default: "24h" })} initial="6m0s" />);
  expect((screen.getByRole("combobox", { name: "Option" }) as HTMLSelectElement).value).toBe("custom");
  expect((screen.getByLabelText("Option amount") as HTMLInputElement).value).toBe("6");
  expect((screen.getByLabelText("Option unit") as HTMLSelectElement).value).toBe("minutes");
  expect(value().value).toBe("6m0s");
  fireEvent.change(screen.getByLabelText("Option unit"), { target: { value: "days" } });
  expect(value().value).toBe("6m0s");
  fireEvent.change(screen.getByLabelText("Option amount"), { target: { value: "2" } });
  expect(value().value).toBe("48h");
 });
 it("shows a stored 0 on a field without Off as the default, without rewriting it", () => {
  render(<Harness field={field("duration", { min: 3600, default: "24h" })} initial="0s" />);
  const select = screen.getByRole("combobox", { name: "Option" }) as HTMLSelectElement;
  expect(select.value).toBe("");
  expect(select.selectedOptions[0].textContent).toBe("Default (24 hours)");
  expect(value().value).toBe("0s");
  fireEvent.change(select, { target: { value: "6h" } });
  expect(value().value).toBe("6h");
 });
 it("names presets in words and writes the preset's Go form", () => {
  render(<Harness field={field("duration", { min: 3600, default: "168h" })} />);
  expect(screen.getByRole("option", { name: "Default (7 days)" })).toBeTruthy();
  fireEvent.change(screen.getByRole("combobox", { name: "Option" }), { target: { value: "12h" } });
  expect(value().value).toBe("12h");
 });
 it("shows an unset switch's default in words and leaves it unset until clicked", () => {
  render(<Harness field={field("boolean", { default: "true" })} />);
  expect(screen.getByTestId("switch-state").textContent).toBe("On (default)");
  expect(screen.getByRole("switch").getAttribute("aria-checked")).toBe("true");
  expect(value()).not.toHaveProperty("value");
  fireEvent.click(screen.getByRole("switch"));
  expect(value().value).toBe(false);
  expect(screen.getByTestId("switch-state").textContent).toBe("Off");
 });
 it("keeps a stored false switch as stored", () => {
  render(<Harness field={field("boolean", { default: "true" })} initial={false} />);
  expect(screen.getByTestId("switch-state").textContent).toBe("Off");
  expect(value().value).toBe(false);
 });
 it("shows stored bytes in the unit that fits, keeps them, and writes bytes when edited", () => {
  render(<Harness field={field("bytes", { default: "2GiB" })} initial={2147483648} />);
  expect((screen.getByLabelText("Option") as HTMLInputElement).value).toBe("2");
  expect((screen.getByLabelText("Option unit") as HTMLSelectElement).value).toBe("GiB");
  expect(value().value).toBe(2147483648);
  fireEvent.change(screen.getByLabelText("Option"), { target: { value: "3" } });
  expect(value().value).toBe(3 * 1073741824);
 });
 it("shows a stored 0 size as the default's placeholder without rewriting it", () => {
  render(<Harness field={field("bytes", { default: "25MB", unit: "bytes/s" })} initial={0} />);
  const input = screen.getByLabelText("Option") as HTMLInputElement;
  expect(input.value).toBe("");
  expect(input.placeholder).toBe("25 MB/s");
  expect(value().value).toBe(0);
 });
 it("puts the rate on a speed's unit and no separate bytes/s line", () => {
  render(<Harness field={field("bytes", { default: "25MB", unit: "bytes/s" })} initial={25000000} />);
  expect((screen.getByLabelText("Option unit") as HTMLSelectElement).selectedOptions[0].textContent).toBe("MB/s");
  expect((screen.getByLabelText("Option") as HTMLInputElement).value).toBe("25");
  expect(document.body.textContent).not.toContain("bytes/s");
 });
 it("names one item on an Add button", () => {
  render(<Harness field={{ name: "scan_roots", label: "Folders to scan", type: "list", help: "" }} />);
  fireEvent.click(screen.getByRole("button", { name: "Add folder" }));
  expect(value().value).toEqual([""]);
 });
});

it("marks a per-library row whose library is gone and removes it", () => {
 function Rows() {
  const [config, setConfig] = useState<Record<string, unknown>>({ versions: { "movie-mm": { plex_section: 1 }, "old-tv": { plex_section: 2 } } });
  return <><SectionFields fields={[{ name: "versions", label: "Libraries", type: "keyed", help: "", fields: [{ name: "plex_section", label: "Plex library ID", type: "integer", help: "" }] }]} config={config} path="library" onChange={setConfig} libraryKeys={["movie-mm"]} /><output data-testid="config">{JSON.stringify(config)}</output></>;
 }
 render(<Rows />);
 expect(document.querySelectorAll("[data-removed]")).toHaveLength(1);
 expect(screen.getAllByText("library removed")).toHaveLength(1);
 fireEvent.click(screen.getByRole("button", { name: "Remove old-tv" }));
 expect(JSON.parse(screen.getByTestId("config").textContent!)).toEqual({ versions: { "movie-mm": { plex_section: 1 } } });
});
