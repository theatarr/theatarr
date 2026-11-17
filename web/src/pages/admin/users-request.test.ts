import { expect, it } from "vitest";
import { requestChoices } from "./users";

const versions = [
  { key: "remux", label: "4K Remux", kind: "movie" },
  { key: "web", label: "4K WEB", kind: "movie" },
  { key: "hd", label: "HD", kind: "movie" },
];

it("offers a group kept in sync as one choice, and as its members when the stored keys hold part of it", () => {
  const groups = [["remux", "web"]];
  const words = (set: string[] | null) => requestChoices(versions, groups, set).map((c) => `${c.label}=${c.keys.join(",")}`);
  expect(words(null)).toEqual(["4K Remux + 4K WEB=remux,web", "HD=hd"]);
  expect(words(["remux", "web"])).toEqual(["4K Remux + 4K WEB=remux,web", "HD=hd"]);
  expect(words(["web", "hd"])).toEqual(["4K Remux=remux", "4K WEB=web", "HD=hd"]);
  expect(requestChoices(versions, undefined, null)).toHaveLength(3);
});
