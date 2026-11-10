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
