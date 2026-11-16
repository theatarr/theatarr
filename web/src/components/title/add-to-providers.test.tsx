// @vitest-environment jsdom
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { addToTooltip, copyLine, copyPart, copyResults, type CopyResult } from "@/lib/add-to-providers";
import { AddToProviders } from "./add-to-providers";

const H = "a".repeat(40);
const at = "2026-11-16T12:00:01Z";
const r = (o: Partial<CopyResult>): CopyResult => ({ provider: "tb", code: "tb", outcome: "added", at, ...o });

describe("the result words", () => {
  it("says each provider's outcome in the provider tags", () => {
    expect(copyPart(r({}))).toBe("Added to TB");
    expect(copyPart(r({ outcome: "present", code: "rd" }))).toBe("Already on RD");
    expect(copyPart(r({ outcome: "present", detail: "still downloading (12%)" }))).toBe("Already on TB, still downloading (12%)");
    expect(copyPart(r({ outcome: "present", detail: "failed there", code: "ad" }))).toBe("Already on AD, but failed there");
    expect(copyPart(r({ outcome: "not_cached", code: "rd" }))).toBe("Not cached on RD");
    expect(copyPart(r({ outcome: "refused", code: "rd" }))).toBe("RD refuses this release name");
    expect(copyPart(r({ outcome: "skipped", until: "2026-11-16T14:32:00Z" }))).toMatch(/^TB is busy until .+, try again then$/);
    expect(copyPart(r({ outcome: "failed", detail: "boom" }))).toBe("Could not add to TB: boom");
    expect(copyPart(r({ outcome: "failed", detail: "could not read the account: tb: gateway: dial tcp" }))).toBe("Could not read TB's account just now");
    expect(copyPart(r({ outcome: "failed", detail: "the add failed: tb: 503" }))).toBe("Could not add to TB just now");
    expect(copyLine([r({}), r({ outcome: "not_cached", code: "rd" })])).toBe("Added to TB · Not cached on RD");
  });
  it("keeps this click's results only", () => {
    const st = { running: false, results: [r({ at: "2026-11-16T11:00:00Z", code: "rd" }), r({})] };
    expect(copyResults(st, "2026-11-16T12:00:00.5Z").map((x) => x.code)).toEqual(["tb"]);
    expect(copyResults(st, null)).toEqual([]);
  });
  it("names the whole pack when the file came in one", () => {
    expect(addToTooltip()).toBe("Adds this file's torrent to the other providers that have it cached. An add that is not cached is removed again.");
    expect(addToTooltip(true)).toContain("this file's torrent (the whole pack, when it came in one) to");
  });
});

describe("the button", () => {
  it("shows only while a provider with a key lacks the torrent", () => {
    mockFetch([]);
    renderPage(<AddToProviders hash={H} addTo={[]} />);
    expect(screen.queryByTestId("add-to-providers")).toBeNull();
    renderPage(<AddToProviders hash={null} addTo={["rd"]} />);
    expect(screen.queryByTestId("add-to-providers")).toBeNull();
  });

  it("spins while the copy runs", async () => {
    mockFetch([
      { method: "POST", path: "/torrents/copy", reply: { job: 3, since: "2026-11-16T12:00:00Z" } },
      { method: "GET", path: `/torrents/copy/${H}`, reply: { running: true, results: [] } },
    ]);
    renderPage(<AddToProviders hash={H} addTo={["tb"]} />);
    fireEvent.click(screen.getByTestId("add-to-providers"));
    await waitFor(() => expect((screen.getByTestId("add-to-providers") as HTMLButtonElement).disabled).toBe(true));
    expect(screen.getByTestId("add-to-providers").querySelector(".animate-spin")).toBeTruthy();
  });

  it("says how each provider ended once it is done", async () => {
    const api = mockFetch([
      { method: "POST", path: "/torrents/copy", reply: { job: 3, since: "2026-11-16T12:00:00Z" } },
      { method: "GET", path: `/torrents/copy/${H}`, reply: { running: false, results: [r({ provider: "rd", code: "rd", outcome: "not_cached" }), r({})] } },
    ]);
    renderPage(<AddToProviders hash={H} addTo={["tb", "rd"]} />);
    fireEvent.click(screen.getByTestId("add-to-providers"));
    expect((await screen.findByTestId("add-to-providers-result")).textContent).toBe("Added to TB · Not cached on RD");
    expect(api.calls("POST", "/torrents/copy")[0].body).toEqual({ hash: H });
  });

  it("goes once every provider holds the torrent, and keeps this click's line", async () => {
    mockFetch([
      { method: "POST", path: "/torrents/copy", reply: { job: 3, since: "2026-11-16T12:00:00Z" } },
      { method: "GET", path: `/torrents/copy/${H}`, reply: { running: false, results: [r({})] } },
    ]);
    // The page's refreshed add_to, empty once TB holds it.
    function Page() {
      const [addTo, setAddTo] = useState(["tb"]);
      return <><button type="button" onClick={() => setAddTo([])}>refresh</button><AddToProviders hash={H} addTo={addTo} /></>;
    }
    renderPage(<Page />);
    fireEvent.click(screen.getByTestId("add-to-providers"));
    expect((await screen.findByTestId("add-to-providers-result")).textContent).toBe("Added to TB");
    fireEvent.click(screen.getByRole("button", { name: "refresh" }));
    expect(screen.queryByTestId("add-to-providers")).toBeNull();
    expect(screen.getByTestId("add-to-providers-result").textContent).toBe("Added to TB");
  });
});
