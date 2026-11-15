// @vitest-environment jsdom
import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Details, TitleJournal } from "./details";
import { mockFetch, renderPage } from "@/test/page";

describe("Title journal", () => {
  it("links to the journal under Activity", async () => {
    mockFetch([
      { method: "GET", path: "/journal", reply: { items: [{ id: "dl:4", at: "2026-11-01T10:00:00Z", kind: "grab", title: { id: 70, name: "Juniper Signal" }, summary: "Grabbed" }] } },
    ]);
    renderPage(<TitleJournal id={70} />);
    expect((await screen.findByRole("link", { name: "Grabbed" })).getAttribute("href")).toBe("/admin/system/journal/dl%3A4");
    expect(screen.getByRole("link", { name: /Open in the journal/ }).getAttribute("href")).toBe("/admin/system/journal?title=70");
  });
});

describe("Release dates", () => {
  it("lists the rows behind the chosen dates, the country named once", async () => {
    mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin"] } }]);
    const release_dates = [
      { source: "tmdb", country: "US", type: "theatrical", date: "2024-10-18" },
      { source: "tmdb", country: "US", type: "digital", date: "2024-12-17" },
    ];
    renderPage(<Details d={{ id: 70, kind: "movie", name: "Anora", release_dates, in_cinemas: "2024-10-18", digital_release: "2024-12-17", versions: [] } as never} />);
    const table = await screen.findByTestId("release-dates");
    expect(within(table).getAllByRole("row")).toHaveLength(3);
    expect(screen.getByTestId("release-dates-country").textContent).toBe("US dates");
    expect(within(table).queryByText("US")).toBeNull();
  });

  it("marks the first cinema date anywhere, shown beside the primary country's other dates", async () => {
    mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin"] } }]);
    const release_dates = [
      { source: "tmdb-art", country: "", type: "theatrical", date: "1985-06-20" },
      { source: "tmdb", country: "US", type: "digital", date: "2001-03-01" },
    ];
    renderPage(<Details d={{ id: 71, kind: "movie", name: "Old", release_dates, in_cinemas: "1985-06-20", digital_release: "2001-03-01", versions: [] } as never} />);
    const table = await screen.findByTestId("release-dates");
    expect(within(table).getAllByRole("row")[1].textContent).toContain("In cinemas · first release, not US");
    expect(within(table).getAllByRole("row")[2].textContent).not.toContain("first release");
    expect(screen.getByTestId("release-dates-country").textContent).toBe("US dates, except the first release");
  });
});

describe("Technical details", () => {
  it("folds the identifiers in words, links the collection, and leaves out empty files and inventory", async () => {
    mockFetch([{ method: "GET", path: "/auth/me", reply: { id: 1, permissions: ["admin"] } }]);
    renderPage(<Details d={{ id: 70, kind: "movie", name: "Alien", monitored: true, versions: [], release_dates: [{ source: "tmdb-art", country: "", type: "theatrical", date: "1979-05-25" }],
      external_ids: [{ source: "tmdb_collection", id: "8091" }, { source: "wikidata", id: "Q103569" }], collection: { tmdb_id: 8091, name: "Alien Collection" } } as never} />);
    const tech = await screen.findByTestId("technical-details");
    expect(tech.hasAttribute("open")).toBe(false);
    expect(within(tech).getByText("TMDb collection")).toBeTruthy();
    expect(within(tech).getByText("Wikidata")).toBeTruthy();
    expect(within(tech).getByText("Movie")).toBeTruthy();
    expect(within(tech).getByRole("link", { name: /Alien Collection/ }).getAttribute("href")).toBe("/collections/8091");
    expect(within(tech).queryByText(/Files|Saved inventory|Registry/)).toBeNull();
    expect(within(screen.getByTestId("release-dates")).getByText("TMDb")).toBeTruthy();
  });
});
