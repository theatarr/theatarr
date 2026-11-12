import { DefinitionHolds } from "./add-indexer";
// @vitest-environment jsdom
// The add-indexer sheet: the catalog with its filters, turning it on, and a set-up form from a definition, the move
// from Prowlarr, and the held definition updates. Everything it adds is on (a moved tracker that needs a login is the
// exception), has a budget of its own and joins the ticked libraries.
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { CardigannDefinition, CardigannList, ProwlarrProposal } from "@/api/cardigann";
import type { VersionRecord } from "@/api/settings-versions";
import { SettingsTabView } from "@/pages/admin/settings";
import { dmm, overview, schema } from "./fixtures";
import { expectNamedControls, json, mockFetch, renderPage, type RequestRecord } from "@/test/page";

// Invented definitions and credentials.
const def = (o: Partial<CardigannDefinition>): CardigannDefinition => ({
  id: "site", name: "Site", description: "", type: "public", privacy: "public", language: "en-US", protocol: "torrent",
  links: ["https://site.example/"], settings: [], categories: [2000], supported: true, status: "runs", ...o,
});
const catalog: CardigannList = {
  status: { commit: "0123456789abcdef", definitions: 4, holds: 0 },
  total: 4,
  definitions: [
    def({ id: "thepiratebay", name: "The Pirate Bay", categories: [2000, 5000], hosts: ["apibay.org"] }),
    def({ id: "privatesite", name: "Private Site", type: "private", privacy: "private", status: "needs_account", links: ["https://private.example/", "https://mirror.private.example/"],
      settings: [
        { name: "username", label: "Username", type: "text", required: true },
        { name: "password", label: "Password", type: "password", secret: true, required: true },
        { name: "info_cookie", label: "How to get the cookie", type: "info_cookie", default: "Sign in with a browser." },
      ] }),
    def({ id: "cfsite", name: "Cloudflare Site", status: "needs_flaresolverr" }),
    def({ id: "broken", name: "Broken Site", status: "unsupported", supported: false }),
  ],
};
const indexers = (extra = {}) => <SettingsTabView tab="indexers" overview={overview({ indexers: [dmm], ...extra })} schema={schema} versions={[]} />;
const writes = (requests: RequestRecord[]) => requests.filter((r) => r.method !== "GET");
const lib = (key: string, kind: string, anime: boolean | null) => ({ id: 1, key, created_at: "", updated_at: "", roots: [], config: { kind, anime, mode: "active" } }) as unknown as VersionRecord;
// A movie library, a series library marked non-anime, and an anime series library.
const versionsRoute = { method: "GET", path: "/settings/versions", reply: json({ source: "db", generation: 4, versions: [lib("movies", "movie", null), lib("tv", "series", false), lib("anime", "series", true)] }) };

describe("add-indexer sheet", () => {
  it("lists what runs here, filters, and adds a private definition on, in the libraries that suit it, with its own budget and the account in the credential", async () => {
    const fetch = mockFetch([
      versionsRoute,
      { method: "GET", path: "/cardigann/definitions", reply: json(catalog) },
      { method: "POST", path: "/settings/budgets", reply: json({ generation: 5 }) },
      { method: "POST", path: "/settings/indexers/test", reply: json({ key: "privatesite", entity: "indexers", status: "ok", results: 12, latency_ms: 300, tested_at: "2026-10-29T10:00:00Z" }) },
      { method: "POST", path: "/settings/indexers", reply: json({ generation: 6 }) },
    ]);
    const user = userEvent.setup();
    renderPage(indexers(), "/admin/settings/indexers");
    const add = screen.getByRole("button", { name: "Add indexer" });
    await user.click(add);
    const sheet = screen.getByRole("dialog", { name: "Add indexer" });
    await waitFor(() => expect(document.activeElement).toBe(within(sheet).getByTestId("add-indexer-heading")));

    await within(sheet).findByTestId("catalog");
    const listed = () => Array.from(sheet.querySelectorAll("[data-definition]")).map((e) => e.getAttribute("data-definition"));
    expect(listed()).toEqual(["thepiratebay", "privatesite"]);
    await user.selectOptions(within(sheet).getByRole("combobox", { name: "Support" }), "all");
    expect(listed()).toEqual(["thepiratebay", "privatesite", "cfsite", "broken"]);
    expect(within(sheet).getAllByTestId("set-up")).toHaveLength(3);
    await user.selectOptions(within(sheet).getByRole("combobox", { name: "Privacy" }), "private");
    expect(listed()).toEqual(["privatesite"]);
    await user.clear(within(sheet).getByRole("searchbox", { name: "Search the catalog" }));
    await user.type(within(sheet).getByRole("searchbox", { name: "Search the catalog" }), "pirate");
    expect(listed()).toEqual([]);
    await user.clear(within(sheet).getByRole("searchbox", { name: "Search the catalog" }));

    await user.click(within(sheet).getByRole("button", { name: "Set up Private Site" }));
    const setup = await screen.findByTestId("setup");
    await waitFor(() => expect(document.activeElement).toBe(within(setup).getByTestId("setup-heading")));
    expectNamedControls();
    expect(within(setup).queryByTestId("private-rule")).toBeNull();
    expect((within(setup).getByTestId("setup-privacy") as HTMLInputElement).readOnly).toBe(true);
    expect(within(setup).getByTestId("setup-note").textContent).toContain("Sign in with a browser.");
    expect(within(setup).getAllByRole("option").map((o) => o.textContent)).toEqual(["https://private.example/", "https://mirror.private.example/"]);
    // A movie site: only the movie library is ticked.
    await within(setup).findByRole("group", { name: "Libraries" });
    expect(within(setup).getAllByRole("checkbox").filter((c) => (c as HTMLInputElement).checked).map((c) => c.closest("label")?.textContent)).toEqual(["Enabled", "movies Movies"]);

    // Required account fields are checked before anything is sent.
    await user.click(within(setup).getByTestId("setup-add"));
    expect(within(setup).getAllByText("required")).toHaveLength(2);
    expect(writes(fetch.requests)).toHaveLength(0);

    await user.type(within(setup).getByTestId("setting-username"), "invented-user");
    await user.type(within(setup).getByTestId("setting-password"), "invented-pass");
    await user.selectOptions(within(setup).getByTestId("setup-url"), "https://mirror.private.example/");
    await user.click(within(setup).getByTestId("setup-test"));
    await within(setup).findByTestId("setup-test-result");
    await user.click(within(setup).getByTestId("setup-add"));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.activeElement).toBe(add);

    const config = { kind: "cardigann", definition: "privatesite", url: "https://mirror.private.example/", privacy: "private", budget: "privatesite" };
    const secret = JSON.stringify({ username: "invented-user", password: "invented-pass" });
    // The test wrote the budget once; Add reused it and saved the indexer on, in its library, in one write.
    expect(writes(fetch.requests).map((r) => `${r.method} ${r.path}`)).toEqual(["POST /settings/budgets", "POST /settings/indexers/test", "POST /settings/indexers"]);
    expect(fetch.calls("POST", "/settings/budgets")[0].body).toEqual({ generation: 4, key: "privatesite", config: { requests: 10, per: "1m" } });
    expect(fetch.calls("POST", "/settings/indexers/test")[0].body).toEqual({ key: "privatesite", config, enabled: true, priority: 10, secret });
    expect(fetch.calls("POST", "/settings/indexers")[0].body).toEqual({ generation: 4, key: "privatesite", config, enabled: true, priority: 10, secret, versions: ["movies"] });
  });

  it("sends what is left ticked: unticking a library and Enabled", async () => {
    const fetch = mockFetch([
      versionsRoute,
      { method: "GET", path: "/cardigann/definitions", reply: json(catalog) },
      { method: "POST", path: "/settings/budgets", reply: json({ generation: 5 }) },
      { method: "POST", path: "/settings/indexers", reply: json({ generation: 6 }) },
    ]);
    const user = userEvent.setup();
    renderPage(indexers(), "/admin/settings/indexers");
    await user.click(screen.getByRole("button", { name: "Add indexer" }));
    await user.click(await screen.findByRole("button", { name: "Set up The Pirate Bay" }));
    const setup = await screen.findByTestId("setup");
    // Movies and TV: every library suits it. No test is needed before Add.
    const libraries = await within(setup).findByRole("group", { name: "Libraries" });
    expect(within(libraries).getAllByRole("checkbox").map((c) => (c as HTMLInputElement).checked)).toEqual([true, true, true]);
    await user.click(within(libraries).getByRole("checkbox", { name: /^tv/ }));
    await user.click(within(setup).getByRole("checkbox", { name: "Enabled" }));
    await user.click(within(setup).getByTestId("setup-add"));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(fetch.calls("POST", "/settings/indexers")[0].body).toMatchObject({ generation: 5, key: "thepiratebay", enabled: false, versions: ["movies", "anime"] });
  });

  // The catalog off answers an empty list with the reason (not an error), so nothing retries or logs it.
  const catalogOff = { status: {}, total: 0, definitions: [], unavailable: "the definition catalog is off: turn it on under Settings › Indexers › Catalog (cardigann.definitions)" };

  it("turns the catalog on from the sheet", async () => {
    let on = false;
    const fetch = mockFetch([
      { method: "GET", path: "/cardigann/definitions", reply: () => json(on ? catalog : catalogOff) },
      { method: "GET", path: "/settings/sections", reply: json({ source: "db", generation: 4, sections: [{ key: "cardigann", live: true, config: null, yaml: "", secrets: [] }] }) },
      { method: "PUT", path: "/settings/sections/cardigann", reply: () => { on = true; return json({ generation: 5 }); } },
    ]);
    const user = userEvent.setup();
    renderPage(indexers(), "/admin/settings/indexers");
    await user.click(screen.getByRole("button", { name: "Add indexer" }));
    // The sheet opens on the catalog, which says what turning it on does.
    expect(screen.getByRole("tab", { name: "Catalog" }).getAttribute("aria-selected")).toBe("true");
    const off = await screen.findByTestId("catalog-off");
    expect(off.textContent).toContain("no tracker is contacted until you add one");
    await user.click(within(off).getByRole("button", { name: "Turn on the catalog" }));
    await screen.findByTestId("catalog");
    expect(fetch.calls("PUT", "/settings/sections/cardigann")).toHaveLength(1);
    expect(fetch.calls("PUT", "/settings/sections/cardigann")[0].body).toEqual({ generation: 4, config: { definitions: { repo: "Prowlarr/Indexers" } } });
  });

  it("adds a Torznab source from the Torznab & built-in sources tab, confirms its effects and saves again with the token", async () => {
    const effects = [{ version: "movie-web", effect: "Indexer new-indexer becomes searchable: active version movie-web may query it." }];
    const fetch = mockFetch([
      versionsRoute,
      { method: "GET", path: "/cardigann/definitions", reply: json(catalogOff) },
      { method: "POST", path: "/settings/indexers", reply: (r: RequestRecord) => r.headers["x-confirmation-token"]
        ? json({ generation: 5 })
        : json({ error: "explicit confirmation required", effects, confirmation: { token: "indexer-confirmation", generation: 4, effects } }, 422) },
    ]);
    const user = userEvent.setup();
    renderPage(indexers(), "/admin/settings/indexers");
    await user.click(screen.getByRole("button", { name: "Add indexer" }));
    await user.click(screen.getByRole("tab", { name: "Torznab & built-in sources" }));
    expect(Array.from(screen.getByTestId("builtin-list").querySelectorAll("button")).map((b) => b.getAttribute("data-testid"))).toEqual(
      ["builtin-torznab", "builtin-dmm", "builtin-torrentio", "builtin-zilean", "builtin-seadex"],
    );
    // DMM's key comes only with a paid sponsorship.
    expect(within(screen.getByTestId("builtin-dmm")).getByText("Paid key")).toBeTruthy();
    expect(screen.getByTestId("builtin-dmm").textContent).toContain("paying GitHub sponsors");
    expect(screen.queryAllByText("Paid key")).toHaveLength(1);
    await user.click(await screen.findByRole("button", { name: /Torznab \/ Newznab/ }));
    const form = within(await screen.findByTestId("record-form"));
    expectNamedControls();
    // Paste whole values: typing per character re-renders the whole editor each time.
    await user.click(form.getByText("Show advanced"));
    await user.clear(form.getByRole("textbox", { name: /^Name/ }));
    await user.click(form.getByRole("textbox", { name: /^Name/ }));
    await user.paste("new-indexer");
    await user.click(form.getByRole("textbox", { name: /^Service URL/i }));
    await user.paste("https://indexer.example/api");
    await user.clear(form.getByRole("textbox", { name: /^Search priority/i }));
    await user.paste("2");
    const posts = () => fetch.calls("POST", "/settings/indexers");

    // Cancelling the confirmation saves nothing and keeps the draft open.
    await user.click(form.getByRole("button", { name: "Save" }));
    let dialog = await screen.findByRole("dialog", { name: "Confirm what this change enables" });
    expect(within(dialog).getByText(effects[0].effect)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Confirm what this change enables" })).toBeNull());
    expect(posts()).toHaveLength(1);
    expect(screen.getByTestId("record-form")).toBeTruthy();

    await user.click(form.getByRole("button", { name: "Save" }));
    dialog = await screen.findByRole("dialog", { name: "Confirm what this change enables" });
    expect(within(dialog).getByText(effects[0].effect)).toBeTruthy();
    expect(posts()).toHaveLength(2);
    await user.click(within(dialog).getByRole("button", { name: "Confirm and save" }));
    await waitFor(() => expect(posts()).toHaveLength(3));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    const [first, again, confirmed] = posts();
    const body = { generation: 4, key: "new-indexer", enabled: true, priority: 2, config: { kind: "torznab", url: "https://indexer.example/api" }, versions: ["movies", "tv", "anime"] };
    // The token travels in a header on the retry only; every body is the same draft.
    expect(first.body).toEqual(body);
    expect(first.headers["x-confirmation-token"]).toBeUndefined();
    expect(again.headers["x-confirmation-token"]).toBeUndefined();
    expect(again.body).toEqual(first.body);
    expect(confirmed.body).toEqual(first.body);
    expect(confirmed.headers["x-confirmation-token"]).toBe("indexer-confirmation");
    expect(confirmed.headers["x-csrf-token"]).toBe("ui-test-csrf");
  });

  it("says which catalog definitions the RSS sync cannot read, in the set-up form and the indexer list", async () => {
    const withRSS: CardigannList = {
      ...catalog,
      definitions: [
        def({ id: "thepiratebay", name: "The Pirate Bay", rss: true }),
        def({ id: "musicsite", name: "Music Site", rss: false, rss_problem: "the site maps no movie or TV category" }),
      ],
    };
    mockFetch([{ method: "GET", path: "/cardigann/definitions", reply: json(withRSS) }]);
    const native = (key: string) => ({ id: 9, key, kind: "cardigann", enabled: false, priority: 1, secret_set: false,
      config: { kind: "cardigann", definition: key, url: "https://site.example/", privacy: "public", budget: key } });
    const user = userEvent.setup();
    renderPage(indexers({ indexers: [dmm, native("thepiratebay"), { ...native("musicsite"), id: 10 }] }), "/admin/settings/indexers");
    const row = (key: string) => document.querySelector(`[data-record="${key}"]`) as HTMLElement;
    await waitFor(() => expect(row("musicsite").textContent).toContain("no RSS: the site maps no movie or TV category"));
    expect(row("thepiratebay").textContent).not.toContain("no RSS");

    await user.click(screen.getByRole("button", { name: "Add indexer" }));
    const sheet = screen.getByRole("dialog", { name: "Add indexer" });
    await user.click(await within(sheet).findByRole("button", { name: "Set up Music Site" }));
    expect((await screen.findByTestId("setup-no-rss")).textContent).toContain("the site maps no movie or TV category");
  });

  it("moves only the ticked proposals from Prowlarr, each on with its own budget and in the ticked libraries, a login one off", async () => {
    const proposals: ProwlarrProposal[] = [
      { prowlarr: { id: 9, name: "The Pirate Bay", definition_name: "thepiratebay", privacy: "public", enabled: true }, status: "proposed", key: "thepiratebay",
        indexer: { kind: "cardigann", definition: "thepiratebay", url: "https://thepiratebay.org/", privacy: "public", budget: "thepiratebay", hosts: ["apibay.org"] }, budget: { requests: "10", per: "1m" } },
      { prowlarr: { id: 5, name: "Nyaa", definition_name: "nyaasi", privacy: "public", enabled: true }, status: "proposed", key: "nyaasi",
        indexer: { kind: "cardigann", definition: "nyaasi", url: "https://nyaa.si/", privacy: "public", budget: "nyaasi", settings: { sort: "seeders" } }, budget: { requests: "10", per: "1m" } },
      { prowlarr: { id: 3, name: "Private One", definition_name: "privateone", privacy: "private", enabled: true }, status: "proposed", key: "privateone",
        indexer: { kind: "cardigann", definition: "privateone", url: "https://private.example/", privacy: "private", budget: "privateone" }, budget: { requests: "10", per: "1m" }, secrets: ["password"] },
      { prowlarr: { id: 2, name: "Custom feed", definition_name: "Torznab", privacy: "public", enabled: true }, status: "no_definition" },
      { prowlarr: { id: 1, name: "DMM again", definition_name: "dmm", privacy: "public", enabled: true }, status: "proposed", key: "dmm",
        indexer: { kind: "cardigann", definition: "dmm", url: "https://dmm.example/", privacy: "public", budget: "dmm" }, budget: { requests: "10", per: "1m" } },
    ];
    const fetch = mockFetch([
      versionsRoute,
      { method: "GET", path: "/cardigann/definitions", reply: json(catalog) },
      { method: "GET", path: "/settings/prowlarr/feeds", reply: json({ configured: true, url: "http://10.0.0.5:9696", feeds: [] }) },
      { method: "GET", path: "/cardigann/prowlarr", reply: proposals },
      { method: "POST", path: "/settings/budgets", reply: (r: RequestRecord) => json({ generation: (r.body as { generation: number }).generation + 1 }) },
      { method: "POST", path: "/settings/indexers", reply: (r: RequestRecord) => json({ generation: (r.body as { generation: number }).generation + 1 }) },
    ]);
    const user = userEvent.setup();
    renderPage(indexers(), "/admin/settings/indexers");
    await user.click(screen.getByRole("button", { name: "Add indexer" }));
    await user.click(screen.getByRole("tab", { name: "Move from Prowlarr (once)" }));
    // Prowlarr is read only when asked.
    expect(fetch.calls("GET", "/cardigann/prowlarr")).toHaveLength(0);
    await user.click(screen.getByTestId("read-prowlarr"));
    const tab = await screen.findByTestId("migrate-tab");
    await within(tab).findByRole("table");
    expectNamedControls();
    // Three can be added (ticked); the custom feed has no definition and dmm exists already.
    expect(within(tab).getAllByRole("checkbox", { name: /^Add / }).map((c) => (c as HTMLInputElement).checked)).toEqual([true, true, true]);
    expect(within(tab).getByText("Needs its login afterwards: password")).toBeTruthy();
    expect(within(tab).getByText("no catalog definition: link it on Link Prowlarr instead")).toBeTruthy();
    await user.click(within(tab).getByRole("checkbox", { name: "Add Nyaa as nyaasi" }));
    // The Pirate Bay has movies and TV: every library is ticked; untick one.
    const libraries = within(tab).getByRole("group", { name: "Libraries" });
    expect(within(libraries).getAllByRole("checkbox").map((c) => (c as HTMLInputElement).checked)).toEqual([true, true, true]);
    await user.click(within(libraries).getByRole("checkbox", { name: /^anime/ }));
    await user.click(within(tab).getByTestId("migrate"));
    await waitFor(() => expect(within(tab).getByText("added off: enter its login, then turn it on")).toBeTruthy());
    expect(within(tab).getAllByText("added")).toHaveLength(1);

    expect(writes(fetch.requests).map((r) => `${r.method} ${r.path} ${(r.body as { key: string }).key}`)).toEqual([
      "POST /settings/budgets thepiratebay", "POST /settings/indexers thepiratebay", "POST /settings/budgets privateone", "POST /settings/indexers privateone",
    ]);
    const [tpb, priv] = fetch.calls("POST", "/settings/indexers").map((r) => r.body as Record<string, unknown>);
    expect(fetch.calls("POST", "/settings/budgets")[0].body).toEqual({ generation: 4, key: "thepiratebay", config: { requests: 10, per: "1m" } });
    expect(tpb).toEqual({ generation: 5, key: "thepiratebay", enabled: true, priority: 1, versions: ["movies", "tv"], config: { kind: "cardigann", definition: "thepiratebay", url: "https://thepiratebay.org/", privacy: "public", budget: "thepiratebay", hosts: ["apibay.org"] } });
    expect(priv).toMatchObject({ generation: 7, key: "privateone", enabled: false, priority: 2, versions: ["movies", "tv"], config: { privacy: "private" } });
    expect(priv.secret).toBeUndefined();
  });

  it("lists held definition updates and approves one only after confirming", async () => {
    const fetch = mockFetch([
      { method: "GET", path: "/cardigann/holds", reply: [{ id: "thepiratebay", reasons: ["new hosts"], detail: ["adds host: api2.example"], commit: "abcdef0123", since: "2026-10-28T10:00:00Z" }] },
      { method: "POST", path: "/cardigann/holds/thepiratebay/approve", reply: { ok: true } },
    ]);
    const user = userEvent.setup();
    renderPage(<DefinitionHolds />, "/admin/reviews/indexers");
    const holds = await screen.findByTestId("holds");
    expect(holds.textContent).toContain("adds host: api2.example");
    await user.click(within(holds).getByRole("button", { name: "Review thepiratebay" }));
    const confirm = await screen.findByTestId("hold-confirm");
    expect(confirm.textContent).not.toContain("searches are refused");
    expect(fetch.calls("POST", "/cardigann/holds/thepiratebay/approve")).toHaveLength(0);
    await user.click(within(confirm).getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(fetch.calls("POST", "/cardigann/holds/thepiratebay/approve")).toHaveLength(1));
  });
});
