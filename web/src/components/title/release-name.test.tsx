// @vitest-environment jsdom
import { render } from "@testing-library/react";
import { expect, it } from "vitest";
import { ReleaseName } from "./release-name";

it("renders the name unchanged, its facts in coloured spans", () => {
  const name = "Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX";
  const { container } = render(<div><ReleaseName name={name} /></div>);
  expect(container.textContent).toBe(name);
  expect([...container.querySelectorAll("[data-fact]")].map((e) => `${e.getAttribute("data-fact")}:${e.textContent}`)).toEqual([
    "res:2160p", "source:WEB-DL", "audio:DDP5.1", "audio:Atmos", "hdr:DV", "hdr:HDR",
  ]);
});
