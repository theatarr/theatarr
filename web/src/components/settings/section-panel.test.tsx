// @vitest-environment jsdom
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import schema from "./testdata/schema.json";
import { SectionPanel } from "./section-panel";
import { ShowAdvancedToggle } from "@/lib/settings-map";
function setup(source = "db", config: unknown = { cached_only: false, poll_interval: "1m" }, status = 200) {
 let reads = 0;
 const fetch = mockFetch([
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/settings/sections", reply: () => ({ source, generation: ++reads > 1 && config !== null ? 5 : 4, sections: [{ key: "router", config: reads > 1 && config !== null ? { ...config as object, poll_interval: "2m" } : config, secrets: [] }] }) },
  { method: "PUT", path: "/settings/sections/router", reply: { status, body: status === 409 ? { error: "stale_generation" } : {} } },
 ]);
 renderPage(<SectionPanel id="grabbing" title="What to grab" section="router" keys={["cached_only"]} />);
 return fetch;
}
it("patches the latest config, preserving unrelated keys and saved notice", async () => {
 const fetch = setup();
 const input = await screen.findByRole("switch");
 fireEvent.click(input);
 fireEvent.click(screen.getByRole("button", { name: "Save" }));
 await screen.findByText("Saved.");
 expect(fetch.calls("PUT", "/settings/sections/router")[0].body).toEqual({ generation: 5, config: { cached_only: true, poll_interval: "2m" } });
});
it("keeps the database section editable without a read-only note", async () => {
 setup("db");
 expect((await screen.findByRole("switch") as HTMLInputElement).disabled).toBe(false);
 expect(screen.queryByText(/Read-only:/)).toBeNull();
 expect(screen.queryByText(/policy files/)).toBeNull();
});
it("creates an unconfigured section with only changed keys", async () => {
 const fetch = setup("db", null);
 fireEvent.click(await screen.findByRole("switch"));
 fireEvent.click(screen.getByRole("button", { name: "Save" }));
 await waitFor(() => expect(fetch.calls("PUT", "/settings/sections/router")).toHaveLength(1));
 expect(fetch.calls("PUT", "/settings/sections/router")[0].body).toEqual({ generation: 4, config: { cached_only: false } });
});
it("hides advanced panels until the shared toggle is on", async () => {
 mockFetch([{ method: "GET", path: "/settings/schema", reply: schema }, { method: "GET", path: "/settings/sections", reply: { source: "db", sections: [] } }]);
 renderPage(<><ShowAdvancedToggle /><SectionPanel id="inspection" title="Media inspection" section="library" keys={["probe"]} advanced /></>);
 expect(screen.queryByText("Media inspection")).toBeNull();
 fireEvent.click(screen.getByRole("button", { name: "Show advanced settings" }));
 await screen.findByRole("heading", { name: "Media inspection" });
 fireEvent.click(screen.getByRole("button", { name: "Show advanced settings" }));
});
it("shows the reload notice after a conflict without resubmitting", async () => {
 const fetch = setup("db", { cached_only: false }, 409);
 fireEvent.click(await screen.findByRole("switch"));
 fireEvent.click(screen.getByRole("button", { name: "Save" }));
 await screen.findByText(/Settings changed since this edit began/);
 expect(fetch.calls("PUT", "/settings/sections/router")).toHaveLength(1);
 expect(screen.getByRole("button", { name: /Reload/ })).toBeTruthy();
});
it("confirms 422 and resends exactly the reviewed patch with its token", async () => {
 const effects = [{ version: "", effect: "Allow downloads" }];
 const fetch = mockFetch([
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 4, sections: [{ key: "router", config: { cached_only: false }, secrets: [] }] } },
  { method: "PUT", path: "/settings/sections/router", reply: { status: 422, body: { error: "explicit confirmation required", effects, confirmation: { token: "proof", generation: 4, effects } } } },
  { method: "PUT", path: "/settings/sections/router", reply: {} },
 ]);
 renderPage(<SectionPanel id="grabbing" title="What to grab" section="router" keys={["cached_only"]} />);
 fireEvent.click(await screen.findByRole("switch")); fireEvent.click(screen.getByRole("button", { name: "Save" }));
 await screen.findByRole("dialog", { name: "Confirm what this change enables" });
 fireEvent.click(screen.getByRole("button", { name: "Confirm and save" }));
 await screen.findByText("Saved.");
 const [first, second] = fetch.calls("PUT", "/settings/sections/router");
 expect(second.body).toEqual(first.body); expect(second.headers["x-confirmation-token"]).toBe("proof");
});
it("writes the Plex token separately and preserves the mount", async () => {
 const fetch = mockFetch([
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 4, sections: [{ key: "library", config: { mount: { path: "/mnt" }, plex: { url: "http://plex" } }, secrets: [{ path: "plex.token", set: true }] }] } },
  { method: "PUT", path: "/settings/sections/library", reply: {} },
 ]);
 renderPage(<SectionPanel id="plex" title="Plex server" section="library" keys={["plex"]} />);
 fireEvent.click(await screen.findByRole("button", { name: "Replace" }));
 fireEvent.change(screen.getByLabelText("New credential"), { target: { value: "new-token" } });
 fireEvent.click(screen.getByRole("button", { name: "Save" }));
 await screen.findByText("Saved.");
 expect(fetch.calls("PUT", "/settings/sections/library")[0].body).toEqual({ generation: 4, config: { mount: { path: "/mnt" }, plex: { url: "http://plex" } }, secrets: { "plex.token": "new-token" } });
});
it("opens advanced settings when following an advanced panel anchor", async () => {
 mockFetch([{ method: "GET", path: "/settings/schema", reply: schema }, { method: "GET", path: "/settings/sections", reply: { source: "db", sections: [] } }]);
 renderPage(<><ShowAdvancedToggle /><SectionPanel id="inspection" title="Media inspection" section="library" keys={["probe"]} advanced /></>, "/admin/settings/libraries/files#inspection");
 await screen.findByRole("heading", { name: "Media inspection" });
 expect(screen.getByRole("button", { name: "Show advanced settings" }).getAttribute("aria-pressed")).toBe("true");
 fireEvent.click(screen.getByRole("button", { name: "Show advanced settings" }));
});
it("scrolls to its anchor once both the sections and the schema are in, whichever comes last", async () => {
 let release = () => {};
 const late = new Promise<void>((r) => { release = r; });
 mockFetch([
  { method: "GET", path: "/settings/schema", reply: async () => { await late; return schema; } },
  { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 4, sections: [{ key: "router", config: { cached_only: false }, secrets: [] }] } },
 ]);
 const scroll = vi.fn();
 const original = Element.prototype.scrollIntoView;
 Element.prototype.scrollIntoView = scroll;
 renderPage(<SectionPanel id="grabbing" title="What to grab" section="router" keys={["cached_only"]} />, "/admin/settings/downloads#grabbing");
 await new Promise((r) => setTimeout(r, 50));
 expect(scroll).not.toHaveBeenCalled();
 release();
 await screen.findByRole("switch");
 await waitFor(() => expect(scroll).toHaveBeenCalled());
 expect((scroll.mock.contexts[0] as HTMLElement).querySelector("#grabbing")).toBeTruthy();
 Element.prototype.scrollIntoView = original;
});
function catalog(config: unknown) {
 const fetch = mockFetch([
  { method: "GET", path: "/settings/schema", reply: schema },
  { method: "GET", path: "/settings/sections", reply: { source: "db", generation: 4, sections: [{ key: "cardigann", config, secrets: [] }] } },
  { method: "PUT", path: "/settings/sections/cardigann", reply: {} },
 ]);
 renderPage(<SectionPanel id="catalog" title="Indexer catalog and Prowlarr" section="cardigann" keys={["definitions", "flaresolverr_url", "prowlarr"]} />);
 return fetch;
}
it("says the catalog is off and turns it on by saving the usual repository", async () => {
 const fetch = catalog({ flaresolverr_url: "http://trawl:8191" });
 expect((await screen.findByTestId("catalog-state")).textContent).toContain("The catalog is off");
 fireEvent.click(screen.getByRole("button", { name: "Turn the catalog on" }));
 expect(screen.getByTestId("catalog-state").textContent).toBe("The catalog is off. Save to turn it on.");
 fireEvent.click(screen.getByRole("button", { name: "Save" }));
 await waitFor(() => expect(fetch.calls("PUT", "/settings/sections/cardigann")).toHaveLength(1));
 expect(fetch.calls("PUT", "/settings/sections/cardigann")[0].body).toEqual({ generation: 4, config: { flaresolverr_url: "http://trawl:8191", definitions: { repo: "prowlarr/indexers" } } });
});
it("says the catalog is on, with its defaults, when definitions is set", async () => {
 catalog({ definitions: {} });
 expect((await screen.findByTestId("catalog-state")).textContent).toBe("The catalog is on: it follows prowlarr/indexers, branch master.");
 expect(screen.queryByRole("button", { name: "Turn the catalog on" })).toBeNull();
});
