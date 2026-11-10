// @vitest-environment jsdom
import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { TitleJournal } from "./details";
import { mockFetch, renderPage } from "@/test/page";

describe("Title journal", () => {
  it("links to the journal under Activity", async () => {
    mockFetch([
      { method: "GET", path: "/journal", reply: { items: [{ id: "dl:4", at: "2026-11-01T10:00:00Z", kind: "grab", title: { id: 70, name: "Juniper Signal" }, summary: "Grabbed" }] } },
    ]);
    renderPage(<TitleJournal id={70} />);
    expect((await screen.findByRole("link", { name: "Grabbed" })).getAttribute("href")).toBe("/activity/journal/dl%3A4");
    expect(screen.getByRole("link", { name: /Open in the journal/ }).getAttribute("href")).toBe("/activity/journal?title=70");
  });
});
