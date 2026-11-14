import { describe, expect, it } from "vitest";
import type { Version } from "@/api/library";
import { fileInventoryRows } from "./file-inventory";

describe("file inventory", () => {
  it("includes active native links and deduplicates paths while keeping file metadata", () => {
    const versions = [
      {
        id: 1,
        key: "anime-1080p",
        label: "Anime",
        files: [{ id: 10, path: "/library/flower.mkv", instance: "sonarr", current: true, quality: "Bluray-1080p", size_bytes: 100 }],
        links: [
          { id: 20, path: "/library/flower.mkv", state: "active", quality: "Bluray-1080p", info_hash: "abc" },
          { id: 21, path: "/library/native.mkv", state: "active", quality: "WEBDL-1080p", info_hash: "def", size_bytes: 300 },
          { id: 22, path: "/library/old.mkv", state: "unlinked" },
        ],
      },
      { id: 2, key: "anime-remux", label: "Remux", files: [], links: [{ id: 20, path: "/library/flower.mkv", state: "active" }] },
    ] as Version[];

    const rows = fileInventoryRows(versions);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({ path: "/library/flower.mkv", instance: "sonarr", size_bytes: 100, versions: ["anime-1080p", "anime-remux"] });
    // Theatarr's own link: its size, and no "theatarr" instance word.
    expect(rows[1]).toMatchObject({ path: "/library/native.mkv", current: true, info_hash: "def", size_bytes: 300 });
    expect(rows[1].instance).toBeUndefined();
  });
});
