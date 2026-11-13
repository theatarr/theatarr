// @vitest-environment jsdom
// The libraries window reads a library as the index pill and the title page row do (lib/library-status.ts): same words, same colour.
import { screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { copyTone, copyWords, TONE } from "@/lib/library-status";
import { libraryStatus } from "@/lib/library-rows";
import { CopyDialog } from "./copy-dialog";

it("says what the index says for a series library", () => {
  mockFetch([]);
  const v = { key: "tv-web", label: "WEB", monitored: true, has_file: true, coverage: { have: 3, total: 6, aired: 6, upcoming: 0, missing: 3 } };
  const empty = { key: "tv-remux", label: "Remux", monitored: true, has_file: false, coverage: { have: 0, total: 0, aired: 0, upcoming: 0 } };
  const t = { id: 1, kind: "series" as const, name: "Alpha", release_date: "2019-01-01", status: "Ended", versions: [v, empty] };
  renderPage(<CopyDialog t={t} seeFiles manage={false} onClose={() => {}} />);
  const web = screen.getByText("3 of 6 episodes linked");
  expect(copyWords(v, t)).toBe("3 of 6 episodes linked");
  expect(web.className).toContain("--bad-ink");
  expect(screen.getByText("No episode information yet")).toBeTruthy();
});

it("says Not monitored, not Missing, for an unmonitored movie without a file, as the pill and the title page do", () => {
  mockFetch([]);
  const v = { key: "movie-web", label: "WEB", state: "wanted" as const, monitored: false, has_file: false };
  const t = { id: 2, kind: "movie" as const, name: "Beta", release_date: "2020-01-01", versions: [v] };
  renderPage(<CopyDialog t={t} seeFiles manage={false} onClose={() => {}} />);
  const words = screen.getByText("Not monitored");
  expect(words.className).toContain("text-muted-foreground");
  expect(screen.queryByText("Missing")).toBeNull();
  // The index pill and the title page row: same words, same (idle, neutral) tone.
  expect(copyWords(v, t)).toBe("not monitored");
  expect(TONE[copyTone(v, t)]).toBe("neutral");
  expect(libraryStatus({ ...v, id: 1 }, t)).toMatchObject({ label: "Not monitored", tone: "neutral" });
});

