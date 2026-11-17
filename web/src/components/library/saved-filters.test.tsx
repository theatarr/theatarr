// @vitest-environment jsdom
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { SavedFilters } from "./saved-filters";

afterEach(() => window.localStorage.clear());

describe("Saved filters", () => {
  it("shows the user's saved filters once the user is known, though mounted before", () => {
    window.localStorage.setItem("theatarr.library.saved-filters.series.7", JSON.stringify([{ name: "Finished", query: "series_status=ended" }]));
    const view = render(<SavedFilters list="series" uid={undefined} current="" apply={() => {}} />);
    expect(screen.queryByRole("combobox", { name: "Saved filters" })).toBeNull();
    view.rerender(<SavedFilters list="series" uid={7} current="series_status=ended" apply={() => {}} />);
    expect((screen.getByRole("combobox", { name: "Saved filters" }) as HTMLSelectElement).value).toBe("Finished");
  });
});
