// @vitest-environment jsdom
import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { PROPERS, PROPERS_LABELS, Select } from "./fields";

it("names the Propers and repacks choices in words, keeping the stored values", () => {
  render(<Select label="Propers and repacks" value="do_not_upgrade" values={PROPERS} labels={PROPERS_LABELS} onChange={() => {}} />);
  const select = screen.getByRole("combobox") as HTMLSelectElement;
  expect([...select.options].map((o) => `${o.value}=${o.text}`)).toEqual(["=Default (prefer and upgrade)", "prefer_and_upgrade=Prefer and upgrade", "do_not_upgrade=Do not upgrade automatically", "do_not_prefer=Do not prefer"]);
  expect(select.value).toBe("do_not_upgrade");
  expect(screen.queryByText("do_not_upgrade")).toBeNull();
});
