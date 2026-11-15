// The About tab's file inventory: Theatarr's active library links, one row per path with every library that links it.
import type { Version } from "@/api/library";

export type FileInventoryRow = {
  key: string;
  versions: string[];
  path: string;
  quality?: string;
  size_bytes?: number;
  custom_format_score?: number;
  info_hash?: string;
};

export function fileInventoryRows(versions: Version[]): FileInventoryRow[] {
  const rows: FileInventoryRow[] = [];
  const byPath = new Map<string, FileInventoryRow>();
  const add = (version: string, row: Omit<FileInventoryRow, "key" | "versions" | "path"> & { path?: string }) => {
    const path = row.path;
    if (!path) return;
    const found = byPath.get(path);
    if (found) {
      if (!found.versions.includes(version)) found.versions.push(version);
      if (!found.quality && row.quality) found.quality = row.quality;
      if (!found.info_hash && row.info_hash) found.info_hash = row.info_hash;
      if (found.size_bytes == null && row.size_bytes != null) found.size_bytes = row.size_bytes;
      return;
    }
    const next: FileInventoryRow = { ...row, path, key: path, versions: [version] };
    byPath.set(path, next);
    rows.push(next);
  };

  for (const v of versions) {
    const version = v.key ?? v.label ?? "";
    for (const l of v.links ?? []) {
      if (l.state !== "active") continue;
      add(version, {
        path: l.path,
        quality: l.quality ?? "",
        size_bytes: l.size_bytes,
        custom_format_score: l.score,
        info_hash: l.info_hash,
      });
    }
  }
  return rows;
}
