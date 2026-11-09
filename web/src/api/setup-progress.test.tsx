// @vitest-environment jsdom
import { legacyTarget } from "@/lib/legacy-routes";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { setupSteps, useSetupProgress } from "./useSetupProgress";
import { usePreflight, type SetupState } from "./setup";
import type { VersionList } from "./settings-versions";
import { mockFetch, renderPage } from "@/test/page";
import { SetupReminder } from "@/components/setup-reminder";
import { SetupPage } from "@/pages/admin/setup";
const state: SetupState = { first_run: false, settings_source: "db", admin_password_generated: false, providers: 1, indexers: 1, versions: 1, versions_with_roots: 1, profiles: 1, api_keys: 1, missing: ["no TMDb source"] };
const versions: VersionList = { source: "db", generation: 1, versions: [{ id: 1, key: "movies", created_at: "", updated_at: "", roots: ["/media/movies"], config: { mode: "active", grab: true, kind: "movie", anime: false, profile: "hd", library: "/media/movies", storage: "debrid", indexers: ["feed"], providers: ["rd"], client: "", import: "symlink", upgrades: "auto", follows: "" } }], profiles: {}, format_sets: [], clients: [], indexers: ["feed"], providers: ["rd"], switches: { router_enabled: true, library_enabled: false, notify_enabled: false, versions: [{ key: "movies", mode: "active", grab_state: { mode: "active", grab: true, router_enabled: true, router_forced_off: false, will_grab: true } }] } };
it("counts five independent steps without requiring activation or treating missing data as done", () => {
 expect(setupSteps()).toEqual([false, false, false, false, false]);
 expect(setupSteps(state, versions)).toEqual([true, true, true, false, true]);
 expect(setupSteps({ ...state, providers: 0 }, versions)[1]).toBe(false);
 expect(setupSteps({ ...state, indexers: 0 }, versions)[1]).toBe(false);
 expect(setupSteps({ ...state, missing: ["no debrid mount: nothing can be imported"] }, versions)[2]).toBe(false);
 expect(setupSteps({ ...state, versions_with_roots: 0 }, versions)[2]).toBe(false);
 for (const config of [{ mode: "shadow" as const, grab: true }, { mode: "active" as const, grab: false }]) expect(setupSteps(state, { ...versions, switches: { ...versions.switches, versions: [{ ...versions.switches.versions[0], grab_state: { ...versions.switches.versions[0].grab_state!, ...config } }] } })[4]).toBe(false);
 expect(setupSteps(state, { ...versions, switches: { ...versions.switches, router_enabled: false } })[4]).toBe(false);
 expect(setupSteps(state, { ...versions, switches: { ...versions.switches, sources: { router: "forced off by environment" } } })[4]).toBe(false);
});
function Progress({ id }: { id: string }) {
 const p = useSetupProgress(); const run = usePreflight(p.versions.data?.generation);
 return <div><span data-testid={id}>{p.done}</span><button onClick={() => run.mutate(true)}>Full {id}</button><button onClick={() => run.mutate(false)}>Local {id}</button><button onClick={() => void p.versions.refetch()}>Refresh {id}</button></div>;
}
it("shares the latest full preflight across consumers and resets readiness on a new generation", async () => {
 const user = userEvent.setup(); let generation = 1; let fail = 0;
 mockFetch([{ method: "GET", path: "/setup", reply: state }, { method: "GET", path: "/settings/versions", reply: () => ({ ...versions, generation }) }, { method: "POST", path: "/preflight", reply: () => ({ at: "", items: [], ok: 1, warn: 1, fail }) }]);
 renderPage(<><Progress id="a" /><Progress id="b" /></>);
 await waitFor(() => expect(screen.getByTestId("a").textContent).toBe("4"));
 await user.click(screen.getByText("Full a"));
 await waitFor(() => expect(screen.getByTestId("b").textContent).toBe("5"));
 await user.click(screen.getByText("Local b"));
 await waitFor(() => expect(screen.getByTestId("a").textContent).toBe("4"));
 fail = 1; await user.click(screen.getByText("Full a"));
 expect(screen.getByTestId("b").textContent).toBe("4");
 fail = 0; await user.click(screen.getByText("Full a"));
 await waitFor(() => expect(screen.getByTestId("b").textContent).toBe("5"));
 generation = 2; await user.click(screen.getByText("Refresh a"));
 await waitFor(() => expect(screen.getByTestId("b").textContent).toBe("4"));
});
it("renders activation and reminders from the same count without writes", async () => {
 const fetch = mockFetch([{ method: "GET", path: "/auth/me", reply: { permissions: ["admin"] } }, { method: "GET", path: "/setup", reply: state }, { method: "GET", path: "/settings/versions", reply: versions }]);
 renderPage(<><SetupPage /><SetupReminder /></>);
 await screen.findByText("Finish setting up Theatarr: 4 of 5");
 expect(screen.getByText("Getting started: 4 of 5 steps complete")).toBeTruthy();
 expect(screen.getByRole("progressbar").getAttribute("value")).toBe("4");
 expect(screen.getByText(/Indexers: feed · Providers: rd/)).toBeTruthy();
 expect(screen.getByText(/Root: \/media\/movies/)).toBeTruthy();
 expect(screen.getByRole("link", { name: "Edit movies" }).getAttribute("href")).toBe("/admin/settings/libraries?edit=movies");
 for (const [name, href] of [["Change allow new downloads", "/admin/settings/downloads"], ["Change write library links", "/admin/settings/libraries"], ["Change send notifications", "/admin/settings/notifications"]]) expect(screen.getByRole("link", { name }).getAttribute("href")).toBe(href);
 for (const link of screen.getAllByRole("link")) {
  const url = new URL(link.getAttribute("href")!, "http://localhost");
  expect(legacyTarget(url.pathname, url.search, url.hash), url.pathname).toBeNull();
 }
 expect(fetch.requests.every(r => r.method === "GET")).toBe(true);
});
it.each([["browse"], ["users"]])("never requests setup or versions for a non-admin (%s)", async permission => {
 const fetch = mockFetch([{ method: "GET", path: "/auth/me", reply: { permissions: [permission] } }]);
 renderPage(<SetupReminder />);
 await waitFor(() => expect(fetch.calls("GET", "/auth/me")).toHaveLength(1));
 expect(screen.queryByText(/Finish setting up/)).toBeNull();
 expect(fetch.calls("GET", "/setup")).toHaveLength(0);
 expect(fetch.calls("GET", "/settings/versions")).toHaveLength(0);
});

it("shows running fallback gates, saved differences and a library display label", async () => {
 const fallback: VersionList = { ...versions, versions: [{ ...versions.versions[0], key: "movie-web", config: { ...versions.versions[0].config, profile: "web-2160p" } }], switches: { ...versions.switches, versions: [{ key: "movie-web", mode: "shadow", grab_state: { mode: "shadow", grab: false, router_enabled: true, router_forced_off: false, will_grab: false } }] } };
 expect(setupSteps(state, fallback)[4]).toBe(false);
 expect(setupSteps(state, { ...versions, switches: { ...versions.switches, versions: [] } })[4]).toBe(false);
 mockFetch([{ method: "GET", path: "/setup", reply: state }, { method: "GET", path: "/settings/versions", reply: fallback }]);
 renderPage(<SetupPage />);
 await screen.findByText("Mode saved: active, running: shadow");
 expect(screen.getByText("Allow downloads for this library saved: On, running: Off")).toBeTruthy();
 const card = screen.getByRole("heading", { name: "movie-web 4K WEB" }).parentElement!;
 expect(within(card).getByText("movie-web")).toBeTruthy();
 // The state comes from the running gates: shadow and downloads off, so nothing is downloaded.
 expect(within(card).getByText("Shadow")).toBeTruthy();
 expect(within(card).getByText("Preview only")).toBeTruthy();
 expect(within(card).queryByText("Downloads on")).toBeNull();
 await userEvent.click(within(card).getByRole("button", { name: "Why?" }));
 expect([...within(card).getByTestId("state-why").querySelectorAll("li")].map((li) => li.textContent?.split(" · ")[0])).toEqual(["Mode is Shadow: it previews and compares, it downloads nothing", "Allow downloads for this library is off"]);
 const step = screen.getByRole("heading", { name: "5. Review activation" }).closest("li")!;
 expect(within(step).getByText("Not yet")).toBeTruthy();
});

it("heads the activation card with its key, the display label beside it", async () => {
 const labelled = { ...versions, versions: [{ ...versions.versions[0], key: "movie-web", config: { ...versions.versions[0].config, profile: "web-2160p" } }] };
 mockFetch([{ method: "GET", path: "/setup", reply: state }, { method: "GET", path: "/settings/versions", reply: labelled }]);
 renderPage(<SetupPage />);
 expect(await screen.findByRole("heading", { name: "movie-web 4K WEB" })).toBeTruthy();
 expect(screen.getByText("movie-web")).toBeTruthy();
});

it("heads activation cards that share a display label with their distinct keys", async () => {
 const twins = { ...versions, versions: ["movie-web", "movie-web-theatarr"].map(key => ({ ...versions.versions[0], key, config: { ...versions.versions[0].config, profile: "web-2160p" } })) };
 mockFetch([{ method: "GET", path: "/setup", reply: state }, { method: "GET", path: "/settings/versions", reply: twins }]);
 renderPage(<SetupPage />);
 for (const key of ["movie-web", "movie-web-theatarr"]) {
  const heading = await screen.findByRole("heading", { name: `${key} 4K WEB` });
  expect(heading.firstElementChild?.textContent).toBe(key);
  expect(within(heading).getByText("4K WEB").className).toContain("text-muted-foreground");
 }
});
