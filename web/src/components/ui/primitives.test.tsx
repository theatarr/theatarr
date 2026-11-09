// @vitest-environment jsdom
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { expect, it, vi } from "vitest";
import { Button } from "./button";
import { Checkbox } from "./checkbox";
import { EmptyState } from "./empty-state";
import { Select } from "./select";
import { Switch } from "./switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "./tabs";

it("toggles a switch by click and keyboard and reports its state", async () => {
  const user = userEvent.setup();
  function Harness() {
    const [on, setOn] = useState(false);
    return <Switch aria-label="Allow new downloads" checked={on} onCheckedChange={setOn} />;
  }
  render(<Harness />);
  const sw = screen.getByRole("switch", { name: "Allow new downloads" });
  expect(sw.getAttribute("aria-checked")).toBe("false");
  await user.click(sw);
  expect(sw.getAttribute("aria-checked")).toBe("true");
  sw.focus();
  await user.keyboard(" ");
  expect(sw.getAttribute("aria-checked")).toBe("false");
});

it("does not toggle a disabled switch", async () => {
  const onChange = vi.fn();
  render(<Switch aria-label="Grab" checked={false} onCheckedChange={onChange} disabled />);
  await userEvent.setup().click(screen.getByRole("switch", { name: "Grab" }));
  expect(onChange).not.toHaveBeenCalled();
});

it("keeps a native checkbox and sets indeterminate", async () => {
  const onChange = vi.fn();
  const { rerender } = render(<Checkbox aria-label="Select all" indeterminate onChange={onChange} />);
  const box = screen.getByRole("checkbox", { name: "Select all" }) as HTMLInputElement;
  expect(box.indeterminate).toBe(true);
  rerender(<Checkbox aria-label="Select all" onChange={onChange} />);
  expect(box.indeterminate).toBe(false);
  await userEvent.setup().click(box);
  expect(onChange).toHaveBeenCalledTimes(1);
});

it("renders a native select with the caller's options", async () => {
  const onChange = vi.fn();
  render(<Select aria-label="Library" defaultValue="a" onChange={onChange}><option value="a">A</option><option value="b">B</option></Select>);
  await userEvent.setup().selectOptions(screen.getByRole("combobox", { name: "Library" }), "b");
  expect(onChange).toHaveBeenCalledTimes(1);
});

it("shows an empty state's title, text and next step", () => {
  render(<EmptyState title="No libraries yet" action={<button type="button">Add a library</button>}>Add one to start.</EmptyState>);
  expect(screen.getByText("No libraries yet")).toBeTruthy();
  expect(screen.getByText("Add one to start.")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Add a library" })).toBeTruthy();
});

// Tailwind v4's outline-none sets --tw-outline-style: none, which a bare focus-visible:outline-2 then reads, so the
// focus outline only shows when the focus state also sets its style.
it("gives the outlined primitives a visible keyboard focus style", () => {
  const { container } = render(
    <div>
      <Button>Save</Button><Switch aria-label="Focus check" checked={false} /><Checkbox aria-label="Enabled" />
      <Tabs defaultValue="one"><TabsList><TabsTrigger value="one">One</TabsTrigger></TabsList><TabsContent value="one">Panel</TabsContent></Tabs>
    </div>,
  );
  const box = within(container);
  // The tab panel takes focus from the keyboard (Tab past the tab list), so it needs the outline too.
  const panel = box.getByRole("tabpanel");
  expect(panel.getAttribute("tabindex")).toBe("0");
  for (const el of [box.getByRole("button", { name: "Save" }), box.getByRole("switch"), box.getByRole("checkbox"), box.getByRole("tab"), panel]) {
    const cls = el.className.split(/\s+/);
    expect(cls).toContain("focus-visible:outline-2");
    if (cls.includes("outline-none")) expect(cls).toContain("focus-visible:outline-solid");
  }
});
