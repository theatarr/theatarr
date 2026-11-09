// @vitest-environment jsdom
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { ImportPanel } from "./import";
import { overview } from "./fixtures";
import { expectNamedControls, json, mockFetch, renderPage } from "@/test/page";

const noneFilled = "Settings sections are kept as stored; none is filled from the files.";
const preview = (changes: unknown[], sections_note = noneFilled, sections_from_files: string[] = []) => ({
  generation: 4, changes, before: { indexers: [], providers: [], budgets: [] }, after: { indexers: [], providers: [], budgets: [] }, yaml: "indexers: []", files: {},
  sections_note, sections_from_files,
});
const effects = [
  { version: "movie-web", effect: "Enable library links under /library/movies" },
  { version: "movie-web", effect: "Allow provider rd to receive new torrents" },
];

describe("Import & export", () => {
  it("explains that an import fills the sections the database lacks", () => {
    mockFetch([]);
    renderPage(<ImportPanel overview={overview()} />);
    const intro = screen.getByTestId("import-authority").textContent;
    expect(intro).toContain("Any import fills the sections the database lacks from the files; the preview names them.");
    expect(intro).not.toContain("fresh database");
  });

  it("shows the sections note and never claims no differences when the import fills sections", async () => {
    mockFetch([{ method: "POST", path: "/settings/import/preview", reply: json(preview([], "Settings sections are kept as stored; filled from the files: provider_sync.", ["provider_sync"])) }]);
    const user = userEvent.setup();
    renderPage(<ImportPanel overview={overview()} />);
    await user.click(screen.getByRole("button", { name: /Preview import of generation 4/ }));
    expect((await screen.findByTestId("import-sections-note")).textContent).toBe("Settings sections are kept as stored; filled from the files: provider_sync.");
    expect(screen.getByTestId("import-nochange").textContent).toBe("No record changes: the import only fills the settings sections named above.");
    expect(screen.queryByText(/No differences/)).toBeNull();
  });

  it("shows the sections note above the record changes when nothing is filled", async () => {
    mockFetch([{ method: "POST", path: "/settings/import/preview", reply: json(preview([{ entity: "indexers", key: "public", operation: "create" }])) }]);
    const user = userEvent.setup();
    renderPage(<ImportPanel overview={overview()} />);
    await user.click(screen.getByRole("button", { name: /Preview import of generation 4/ }));
    const note = await screen.findByTestId("import-sections-note");
    expect(note.textContent).toBe("Settings sections are kept as stored; none is filled from the files.");
    expect(note.compareDocumentPosition(screen.getByRole("table")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.queryByTestId("import-nochange")).toBeNull();
  });

  it("says only that no record changes when nothing is filled: a kept section may still differ from the files", async () => {
    mockFetch([{ method: "POST", path: "/settings/import/preview", reply: json(preview([])) }]);
    const user = userEvent.setup();
    renderPage(<ImportPanel overview={overview()} />);
    await user.click(screen.getByRole("button", { name: /Preview import of generation 4/ }));
    expect((await screen.findByTestId("import-nochange")).textContent).toBe("No record changes. Settings sections stay as stored.");
    expect(screen.queryByText(/No differences/)).toBeNull();
  });

  it("points the settings backup at System › Backup", () => {
    mockFetch([]);
    renderPage(<ImportPanel overview={overview()} />);
    expect(screen.getByText(/nightly database backup/).textContent).toContain("the nightly database backup (System › Backup) holds them");
  });

  it("previews, requires review, shows all effects, then applies with its confirmation token", async () => {
    const fetch = mockFetch([
      { method: "POST", path: "/settings/import/preview", reply: json(preview([{ entity: "versions", key: "movie-web", operation: "update", fields: ["config"] }])) },
      { method: "POST", path: "/settings/import/confirm", reply: json({ generation: 4, token: "import-confirmation", effects }) },
      { method: "POST", path: "/settings/import", reply: json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<ImportPanel overview={overview()} />);
    await user.click(screen.getByRole("button", { name: /Preview import of generation 4/ }));
    await screen.findByRole("table");
    expectNamedControls();
    const reviewed = screen.getByRole("checkbox", { name: /I have reviewed the changes above/ });
    await user.click(reviewed);
    await user.click(screen.getByRole("button", { name: "Import now" }));

    const dialog = await screen.findByRole("dialog", { name: "Confirm library changes in this import" });
    for (const effect of effects) expect(within(dialog).getByText(effect.effect)).toBeTruthy();
    expect(fetch.calls("POST", "/settings/import")).toHaveLength(0);
    await waitFor(() => expect(document.activeElement).toBe(within(dialog).getByRole("button", { name: "Cancel" })));
    await user.click(within(dialog).getByRole("button", { name: "Confirm and import" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/import")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/import/preview")[0].body).toEqual({ generation: 4 });
    expect(fetch.calls("POST", "/settings/import")[0].body).toEqual({ generation: 4, orphan_active_downloads: false, confirmation_token: "import-confirmation" });
    expect(fetch.calls("POST", "/settings/import")[0].headers["x-csrf-token"]).toBe("ui-test-csrf");
    await waitFor(() => expect(screen.getByText(/generation 5 is stored and running/)).toBeTruthy());
  });

  it("applies an import without write effects and omits a confirmation token", async () => {
    const fetch = mockFetch([
      { method: "POST", path: "/settings/import/preview", reply: json(preview([{ entity: "indexers", key: "public", operation: "create" }])) },
      { method: "POST", path: "/settings/import/confirm", reply: json({ generation: 4, effects: [] }) },
      { method: "POST", path: "/settings/import", reply: json({ generation: 5 }) },
    ]);
    const user = userEvent.setup();
    renderPage(<ImportPanel overview={overview()} />);
    await user.click(screen.getByRole("button", { name: /Preview import of generation 4/ }));
    await screen.findByRole("table");
    await user.click(screen.getByRole("checkbox", { name: /I have reviewed the changes above/ }));
    await user.click(screen.getByRole("button", { name: "Import now" }));
    await waitFor(() => expect(fetch.calls("POST", "/settings/import")).toHaveLength(1));
    expect(fetch.calls("POST", "/settings/import")[0].body).toEqual({ generation: 4, orphan_active_downloads: false });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("shows a dash in the credential column for entities without credentials", async () => {
    const fetch = mockFetch([
      { method: "POST", path: "/settings/import/preview", reply: json(preview([
        { entity: "budgets", key: "shared", operation: "update" },
        { entity: "profiles", key: "web", operation: "create" },
      ])) },
    ]);
    const user = userEvent.setup();
    renderPage(<ImportPanel overview={overview()} />);
    await user.click(screen.getByRole("button", { name: /Preview import of generation 4/ }));
    const table = await screen.findByRole("table");
    const budget = within(table).getByRole("row", { name: /budget shared/ });
    const profile = within(table).getByRole("row", { name: /profile web/ });
    expect(within(budget).getByText("—")).toBeTruthy();
    expect(within(profile).getByText("—")).toBeTruthy();
    expect(fetch.calls("POST", "/settings/import/preview")).toHaveLength(1);
  });
});
