// @vitest-environment jsdom
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { mockFetch, renderPage } from "@/test/page";
import { SeriesTypeControl } from "./title-choices";

it("lets a manager change a series' type", async () => {
  const fetch = mockFetch([
    { method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } },
    { method: "POST", path: "/titles/series-type", reply: { changed: 1 } },
  ]);
  const user = userEvent.setup();
  renderPage(<SeriesTypeControl titleId={10} value="standard" />);
  const select = (await screen.findByRole("combobox", { name: "Series type" })) as HTMLSelectElement;
  expect(select.value).toBe("standard");
  expect([...select.options].map((o) => o.textContent)).toEqual(["Standard", "Daily", "Anime"]);
  await user.selectOptions(select, "daily");
  await waitFor(() => expect(fetch.calls("POST", "/titles/series-type")).toHaveLength(1));
  expect(fetch.calls("POST", "/titles/series-type")[0].body).toEqual({ title_ids: [10], series_type: "daily" });
});

it("shows a series' type as text without manage", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files"] } }]);
  const page = renderPage(<SeriesTypeControl titleId={10} value="daily" />);
  await waitFor(() => expect(page.container.textContent).toBe("Daily"));
  expect(screen.queryByRole("combobox")).toBeNull();
});

it("names the libraries the series is not searched in", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } }]);
  const versions = [{ id: 1, key: "tv-2160p", label: "TV 2160p", anime_skip: true }, { id: 2, key: "anime-tv", label: "Anime TV" }];
  const page = renderPage(<SeriesTypeControl titleId={10} value="anime" versions={versions} />);
  expect((await screen.findByTestId("series-type-skipped")).textContent).toBe("Not searched in TV 2160p: the series counts as anime, and that library does not take anime.");
  page.unmount();
  renderPage(<SeriesTypeControl titleId={10} value="standard" versions={[{ id: 2, key: "anime-tv", label: "Anime TV", anime_skip: true }]} />);
  expect((await screen.findByTestId("series-type-skipped")).textContent).toBe("Not searched in Anime TV: that library takes anime only, and the series does not count as anime.");
  page.unmount();
});

it("says nothing when every library is searched", async () => {
  mockFetch([{ method: "GET", path: "/auth/me", reply: { permissions: ["browse", "see_files", "manage"] } }]);
  renderPage(<SeriesTypeControl titleId={10} value="standard" versions={[{ id: 1, key: "tv-2160p" }]} />);
  await screen.findByRole("combobox", { name: "Series type" });
  expect(screen.queryByTestId("series-type-skipped")).toBeNull();
});
