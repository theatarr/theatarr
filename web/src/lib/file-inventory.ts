// The About tab's file inventory includes old-manager files and Theatarr's active library links. Paths are unique in the
// inventory; when both sources describe one path, keep the file row's fuller metadata.
import type { Version } from "@/api/library";

export type FileInventoryRow = {
  key: string;
  versions: string[];
  path: string;
  instance?: string;
  current?: boolean;
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
    for (const f of v.files ?? []) {
      add(version, {
        path: f.path,
        instance: f.instance,
        current: f.current,
        quality: f.quality,
        size_bytes: f.size_bytes,
        custom_format_score: f.custom_format_score,
        info_hash: f.info_hash,
      });
    }
    for (const l of v.links ?? []) {
      if (l.state !== "active") continue;
      // Theatarr's own link: no instance word ("theatarr" says nothing here).
      add(version, {
        path: l.path,
        current: true,
        quality: l.quality ?? "",
        size_bytes: l.size_bytes,
        custom_format_score: l.score,
        info_hash: l.info_hash,
      });
    }
  }
  return rows;
}
