// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, it } from "vitest";
import { ProfileFields, type Config } from "./fields";

afterEach(cleanup);

function Harness({ initial, seen }: { initial: Config; seen: Config[] }) {
  const [config, setConfig] = useState(initial);
  return <ProfileFields config={config} onChange={(next) => { seen.push(next); setConfig(next); }} />;
}

it("takes a typed negative number in the score, minimum and cutoff fields", async () => {
  const user = userEvent.setup();
  const seen: Config[] = [];
  render(<Harness initial={{ qualities: [], formats: { "web-id": 5 }, min_score: 0, cutoff_score: 0 }} seen={seen} />);
  for (const [label, read] of [
    ["Minimum format score", (c: Config) => c.min_score],
    ["Cutoff format score (upgrade until)", (c: Config) => c.cutoff_score],
    ["web-id", (c: Config) => (c.formats as Record<string, number>)["web-id"]],
  ] as const) {
    const input = screen.getByLabelText(label);
    await user.clear(input);
    await user.type(input, "-25");
    expect(read(seen.at(-1)!)).toBe(-25);
    expect((input as HTMLInputElement).value).toBe("-25");
    await user.tab();
    expect((input as HTMLInputElement).value).toBe("-25");
  }
});

it("turns a field left blank, or a lone minus, into 0 on leaving it", async () => {
  const user = userEvent.setup();
  const seen: Config[] = [];
  render(<Harness initial={{ qualities: [], formats: {}, min_score: 40, cutoff_score: 30 }} seen={seen} />);
  const min = screen.getByLabelText("Minimum format score") as HTMLInputElement;
  await user.clear(min);
  expect(min.value).toBe("");
  await user.tab();
  expect(seen.at(-1)!.min_score).toBe(0);
  expect(min.value).toBe("0");
  const cutoff = screen.getByLabelText("Cutoff format score (upgrade until)") as HTMLInputElement;
  await user.clear(cutoff);
  await user.type(cutoff, "-");
  await user.tab();
  expect(seen.at(-1)!.cutoff_score).toBe(0);
  expect(cutoff.value).toBe("0");
});
