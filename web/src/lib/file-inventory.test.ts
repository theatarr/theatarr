import { describe, expect, it } from "vitest";
import type { Version } from "@/api/library";
import { fileInventoryRows } from "./file-inventory";

describe("file inventory", () => {
  it("lists active links once per path, with every library that links it", () => {
    const versions = [
      {
        id: 1,
        key: "anime-1080p",
        label: "Anime",
        links: [
          { id: 20, path: "/library/flower.mkv", state: "active", quality: "Bluray-1080p", info_hash: "abc", size_bytes: 100 },
          { id: 21, path: "/library/native.mkv", state: "active", quality: "WEBDL-1080p", info_hash: "def", size_bytes: 300 },
          { id: 22, path: "/library/old.mkv", state: "unlinked" },
        ],
      },
      { id: 2, key: "anime-remux", label: "Remux", links: [{ id: 20, path: "/library/flower.mkv", state: "active" }] },
    ] as Version[];

    const rows = fileInventoryRows(versions);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({ path: "/library/flower.mkv", size_bytes: 100, versions: ["anime-1080p", "anime-remux"] });
    expect(rows[1]).toMatchObject({ path: "/library/native.mkv", info_hash: "def", size_bytes: 300 });
  });
});
