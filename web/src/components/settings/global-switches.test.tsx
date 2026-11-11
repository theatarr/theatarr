// @vitest-environment jsdom
import { cleanup, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { Link } from "react-router";
import { AdvancedSwitch, GlobalSwitchesCard, PauseSwitch } from "./global-switches";
import { useSettings, type SettingsOverview } from "@/api/settings";
function GeneralSettingsPage() { const q = useSettings(); return q.data ? <GlobalSwitchesCard overview={q.data} /> : null; }
import { json, mockFetch, renderPage } from "@/test/page";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { qk } from "@/api/query-keys";

/** Reads what Wanted and the status read, under their keys. */
function StatusAndWanted() {
  useQuery({ queryKey: qk.status, queryFn: () => api("/status") });
  useQuery({ queryKey: ["wanted", {}], queryFn: () => api("/wanted") });
  return null;
}

const overview = (source = "db", router = false, forced = false) => ({
  source, writable: source === "db", generation: 7, active_generation: 7, provider_epoch: 0, policy_dir: "/policy", yaml_owned: [], indexers: [], providers: [], budgets: [],
  switches: { router_enabled: router, library_enabled: false, notify_enabled: false, versions: [], sources: { router: forced ? "forced off by environment" : source, library: source, notify: source } },
});

describe("Global switches", () => {
  it("shows DB state and retries the exact enable with its confirmation token", async () => {
    const effects = [{ version: "", effect: "Turn on Allow new downloads (router.enabled): movie-web (downloads on): providers rd." }];
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview() },
      { method: "PUT", path: "/settings/switches", reply: json({ error: "explicit confirmation required", effects, confirmation: { token: "proof", generation: 8, effects } }, 422) },
      { method: "PUT", path: "/settings/switches", reply: { generation: 8 } },
    ]);
    const user = userEvent.setup();
    renderPage(<GeneralSettingsPage />);
    await user.click(await screen.findByRole("switch", { name: "Allow new downloads" }));
    await screen.findByRole("dialog", { name: "Confirm what this change enables" });
    expect(screen.getByText(effects[0].effect)).toBeTruthy();
    expect(fetch.calls("PUT", "/settings/switches")).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Confirm and save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/switches")).toHaveLength(2));
    const [first, second] = fetch.calls("PUT", "/settings/switches");
    expect(first.body).toEqual({ generation: 7, router_enabled: true });
    expect(second.body).toEqual(first.body);
    expect(second.headers["x-confirmation-token"]).toBe("proof");
  });

  it("turns off immediately without a confirmation dialog", async () => {
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: overview("db", true) },
      { method: "PUT", path: "/settings/switches", reply: { generation: 8 } },
    ]);
    renderPage(<GeneralSettingsPage />);
    await userEvent.setup().click(await screen.findByRole("switch", { name: "Allow new downloads" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/switches")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/switches")[0].body).toEqual({ generation: 7, router_enabled: false });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("disables forced-off and YAML controls and names their sources", async () => {
    mockFetch([{ method: "GET", path: "/settings", reply: overview("db", false, true) }]);
    renderPage(<GeneralSettingsPage />);
    const router = await screen.findByRole("switch", { name: "Allow new downloads" });
    expect((router as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByText("Forced off by environment")).toBeTruthy();
    expect((screen.getByRole("switch", { name: "Write library links" }) as HTMLInputElement).disabled).toBe(false);
  });

  it("keeps the switches read-only while settings cannot be written", async () => {
    mockFetch([{ method: "GET", path: "/settings", reply: { ...overview(), writable: false } }]);
    renderPage(<GeneralSettingsPage />);
    await screen.findByRole("switch", { name: "Allow new downloads" });
    for (const input of screen.getAllByRole("switch")) expect((input as HTMLInputElement).disabled).toBe(true);
    expect(screen.queryByText(/policy files/)).toBeNull();
  });
});

it("explains that disabling adds still allows indexer reads", async () => {
 mockFetch([{ method: "GET", path: "/settings", reply: overview() }]);
 renderPage(<GeneralSettingsPage />);
 expect(await screen.findByText("Off: Theatarr adds no torrents to your debrid accounts. RSS checks and comparison searches still read indexers. Each library is also Live or Watch only on its own.")).toBeTruthy();
});

describe("Pause in the admin header", () => {
  const both = (router: boolean, library: boolean, extra: Record<string, unknown> = {}) => {
    const o = overview("db", router);
    return { ...o, ...extra, switches: { ...o.switches, library_enabled: library, notify_enabled: true } } as unknown as SettingsOverview;
  };

  it("on: turns both switches off in one save, without a dialog", async () => {
    const fetch = mockFetch([{ method: "GET", path: "/settings", reply: both(true, true) }, { method: "PUT", path: "/settings/switches", reply: { generation: 8 } }]);
    renderPage(<PauseSwitch />);
    const pause = await screen.findByRole("switch", { name: "Pause" });
    expect((pause as HTMLInputElement).checked).toBe(false);
    expect(screen.getByTestId("pause").getAttribute("title")).toMatch(/Queued searches wait and start when you resume/);
    await userEvent.setup().click(pause);
    await waitFor(() => expect(fetch.calls("PUT", "/settings/switches")).toHaveLength(1));
    expect(fetch.calls("PUT", "/settings/switches")[0].body).toEqual({ generation: 7, router_enabled: false, library_enabled: false });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("a save reads the status and Wanted again, so due rows and search buttons follow the switches", async () => {
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: both(true, true) },
      { method: "GET", path: "/status", reply: {} },
      { method: "GET", path: "/wanted", reply: { items: [] } },
      { method: "PUT", path: "/settings/switches", reply: { generation: 8 } },
    ]);
    renderPage(<><PauseSwitch /><StatusAndWanted /></>);
    const pause = await screen.findByRole("switch", { name: "Pause" });
    await waitFor(() => expect(fetch.calls("GET", "/wanted")).toHaveLength(1));
    expect(fetch.calls("GET", "/status")).toHaveLength(1);
    await userEvent.setup().click(pause);
    await waitFor(() => expect(fetch.calls("GET", "/wanted")).toHaveLength(2));
    expect(fetch.calls("GET", "/status")).toHaveLength(2);
  });

  it("off: turns both on, through the confirmation listing both effects", async () => {
    const effects = [{ effect: "Turn on Allow new downloads (router.enabled): new torrent adds to debrid are allowed for Live libraries none." }, { effect: "Turn on Write library links (library.enabled): links may be written by Live libraries none." }];
    const fetch = mockFetch([
      { method: "GET", path: "/settings", reply: both(false, false) },
      { method: "PUT", path: "/settings/switches", reply: json({ error: "explicit confirmation required", effects, confirmation: { token: "proof", generation: 8, effects } }, 422) },
      { method: "PUT", path: "/settings/switches", reply: { generation: 8 } },
    ]);
    const user = userEvent.setup();
    renderPage(<PauseSwitch />);
    const pause = await screen.findByRole("switch", { name: "Pause" });
    expect((pause as HTMLInputElement).checked).toBe(true);
    expect(screen.getByText("Paused")).toBeTruthy();
    await user.click(pause);
    await screen.findByRole("dialog", { name: "Confirm what this change enables" });
    for (const e of effects) expect(screen.getByText(e.effect)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Confirm and save" }));
    await waitFor(() => expect(fetch.calls("PUT", "/settings/switches")).toHaveLength(2));
    const [first, second] = fetch.calls("PUT", "/settings/switches");
    expect(first.body).toEqual({ generation: 7, router_enabled: true, library_enabled: true });
    expect(second.body).toEqual(first.body);
    expect(second.headers["x-confirmation-token"]).toBe("proof");
  });

  it.each([[true, false, "/admin/settings/libraries#switches"], [false, true, "/admin/settings/downloads#switches"]])("router %s, links %s: shows Custom linking to the switch that is off", async (router, library, href) => {
    const fetch = mockFetch([{ method: "GET", path: "/settings", reply: both(router, library) }]);
    renderPage(<PauseSwitch fallback={<span>signal</span>} />);
    const custom = await screen.findByTestId("pause-custom");
    expect(custom.textContent).toBe("Pause: Custom");
    expect(custom.getAttribute("href")).toBe(href);
    expect(screen.queryByRole("switch")).toBeNull();
    expect(fetch.calls("PUT", "/settings/switches")).toHaveLength(0);
    // New downloads off keeps the header's signal beside Custom.
    expect(!!screen.queryByText("signal")).toBe(!router);
  });

  it("shows the fallback instead when settings are read-only or a switch is forced off", async () => {
    mockFetch([{ method: "GET", path: "/settings", reply: both(true, true, { writable: false }) }]);
    const first = renderPage(<PauseSwitch fallback={<span>signal</span>} />);
    expect(await screen.findByText("signal")).toBeTruthy();
    expect(screen.queryByRole("switch", { name: "Pause" })).toBeNull();
    first.unmount();
    const forced = overview("db", false, true);
    mockFetch([{ method: "GET", path: "/settings", reply: { ...forced, switches: { ...forced.switches, library_enabled: false } } }]);
    renderPage(<PauseSwitch fallback={<span>signal</span>} />);
    expect(await screen.findByText("signal")).toBeTruthy();
    expect(screen.queryByTestId("pause-custom")).toBeNull();
  });

  it("puts each switch under an Advanced fold, open when it is off or linked to", async () => {
    const o = both(true, false);
    renderPage(<><AdvancedSwitch overview={o} field="router_enabled" /><AdvancedSwitch overview={o} field="library_enabled" /></>);
    expect(screen.queryByRole("switch", { name: "Allow new downloads" })).toBeNull();
    expect(screen.getByRole("switch", { name: "Write library links" })).toBeTruthy();
    expect(screen.getByText(/Pause, at the top of every admin page/)).toBeTruthy();
    cleanup();
    renderPage(<AdvancedSwitch overview={o} field="router_enabled" />, "/admin/settings/downloads#switches");
    expect(screen.getByRole("switch", { name: "Allow new downloads" })).toBeTruthy();
  });

  it("opens the fold when a #switches link is followed on its own page, again after it was closed", async () => {
    const user = userEvent.setup();
    const o = both(true, true);
    renderPage(<><Link to="/admin/settings/downloads#switches">Pause: Custom</Link><AdvancedSwitch overview={o} field="router_enabled" /></>, "/admin/settings/downloads");
    expect(screen.queryByRole("switch", { name: "Allow new downloads" })).toBeNull();
    await user.click(screen.getByText("Pause: Custom"));
    expect(await screen.findByRole("switch", { name: "Allow new downloads" })).toBeTruthy();
    await user.click(screen.getByText("Advanced"));
    expect(screen.queryByRole("switch", { name: "Allow new downloads" })).toBeNull();
    await user.click(screen.getByText("Pause: Custom"));
    expect(await screen.findByRole("switch", { name: "Allow new downloads" })).toBeTruthy();
  });
});
