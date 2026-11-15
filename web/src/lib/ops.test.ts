import { describe, expect, it } from "vitest";
import {
  bulkRemovalScope,
  someNames,
  endedByRemoval,
  failedDownloadId,
  leftoverOnly,
  grabScope,
  leftoverLinks,
  removalLinks,
  removalScope,
  removeDownloadScope,
  retryKind,
  retryScope,
  SHARED_TORRENT,
  unblockScope,
  unlinkable,
  unlinkScope,
  operationOutcomeToast,
} from "./ops";

describe("recovery scopes", () => {
  it("keeps the server's skipped-search result visible as a warning", () => {
    expect(operationOutcomeToast("Download removed", "Removed. Not searched: this copy is in shadow mode", { started: false })).toEqual({
      title: "Download removed",
      body: "Removed. Not searched: this copy is in shadow mode",
      tone: "warn",
    });
  });

  it("says a removal never touches the shared torrent, and what blocklist adds", () => {
    const r = removeDownloadScope({ id: 4, provider_name: "Real-Debrid" }, "remove", "4K WEB");
    expect(r.keeps[0]).toBe("The torrent at Real-Debrid stays: torrents there are shared by every library");
    expect(r.keeps).toContain("The release is not blocklisted: a later search may pick it again");
    expect(r.does).toHaveLength(1);
    const b = removeDownloadScope({ id: 4 }, "block", "4K WEB");
    expect(b.keeps[0]).toBe(SHARED_TORRENT);
    expect(b.does).toEqual([
      "Theatarr stops following this download: its record ends as failed",
      "The release is blocklisted for the 4K WEB library: Theatarr never grabs it again for that library",
      "A search for the 4K WEB library starts now (when the library is monitored and active)",
    ]);
    expect(b.confirm).toBe("Remove, blocklist and search");
  });

  it("retries a failed download by routing and a finished one by importing", () => {
    expect(retryKind("failed")).toBe("route");
    expect(retryKind("ready")).toBe("import");
    expect(retryKind("importing")).toBe("import");
    expect(retryKind("downloading")).toBeNull();
    expect(retryScope("failed", "4K").blocked).toBeUndefined();
    expect(retryScope("ready", "4K").title).toBe("Import now");
    expect(retryScope("adding", "4K").blocked).toBe("Nothing to retry: the download is adding and still on its way");
  });

  it("finds the download of a failure in the history", () => {
    expect(failedDownloadId("dl:12:fail")).toBe(12);
    expect(failedDownloadId("dl:12")).toBe(0);
    expect(failedDownloadId("dl:12:import")).toBe(0);
    expect(failedDownloadId("e3b0c442")).toBe(0);
    expect(failedDownloadId(undefined)).toBe(0);
  });

  it("knows a failure a removal ended", () => {
    expect(endedByRemoval("copy removed by admin")).toBe(true);
    expect(endedByRemoval("Copy removed by someone")).toBe(true);
    expect(endedByRemoval("deleted through sonarr-main by Sonarr")).toBe(true);
    expect(endedByRemoval("removed in Activity by admin")).toBe(false);
    expect(endedByRemoval("stalled: not ready")).toBe(false);
    expect(endedByRemoval(undefined)).toBe(false);
  });

  it("names the copy an unblock frees", () => {
    const u = unblockScope("Dune.2021.2160p.WEB-DL-GRP", "4K WEB", true);
    expect(u.does).toEqual(["Dune.2021.2160p.WEB-DL-GRP may be grabbed again for the 4K WEB library", "A search for the 4K WEB library starts now"]);
    expect(unblockScope("x", "4K", false).confirm).toBe("Unblock");
  });

  it("scopes a copy removal with its links and downloads, with and without the files and the torrents", () => {
    const one = { name: "Dune", title: false, copies: [{ key: "movie-web", label: "4K WEB", links: 2, downloads: 1 }] };
    const keep = removalScope(one, false);
    expect(keep.title).toBe("Remove from 4K WEB");
    expect(keep.does).toEqual([
      "Dune is removed from 4K WEB: this copy stops being wanted, searched or upgraded",
      "It is hidden from Seerr and the other arr clients",
      "Theatarr stops following 1 download in progress",
    ]);
    expect(keep.keeps).toEqual([
      "2 library files stay linked and playable",
      "Its debrid torrents stay",
      "Its history stays",
      "Import lists will not restore the removed libraries; add it yourself to bring it back",
    ]);
    expect(keep.cleanup).toEqual({ text: "To free space at the provider, delete torrents nothing uses any more under", label: "Admin › Torrents › Not used by Theatarr", to: "/admin/torrents?group=unused", admin: true });
    // The torrents cannot go while the files are kept.
    expect(removalScope(one, false, true).does).toEqual(keep.does);
    const both = removalScope(one, true, true);
    expect(both.does).toContain("2 library files are deleted from the library (moved to Theatarr's trash), and the folders they leave empty are removed");
    expect(both.does).toContain("Its debrid torrents are deleted from the providers, after a backup, unless another title or library in Theatarr uses them");
    expect(both.keeps).toEqual([
      "A file another library of the title uses stays",
      "Its history stays",
      "Import lists will not restore the removed libraries; add it yourself to bring it back",
    ]);
    expect(both.cleanup).toBeUndefined();
    expect(both.confirm).toBe("Remove from this library");
    // Files deleted, torrents kept: they stay, and the Not used by Theatarr filter is linked.
    const filesOnly = removalScope(one, true, false);
    expect(filesOnly.does).not.toContain(both.does.at(-1));
    expect(filesOnly.keeps).toContain("Its debrid torrents stay");
    expect(filesOnly.cleanup).toBeDefined();
  });

  it("scopes a title removal", () => {
    const all = {
      name: "Dune",
      title: true,
      copies: [
        { key: "movie-remux", label: "4K Remux", links: 1, downloads: 0 },
        { key: "movie-web", label: "4K WEB", links: 0, downloads: 0 },
      ],
    };
    const s = removalScope(all, true);
    expect(s.title).toBe("Remove Dune");
    expect(s.does[0]).toBe("Dune is removed from every library it is in (4K Remux, 4K WEB): it stops being wanted, searched or upgraded there");
    expect(s.confirm).toBe("Remove title");
    expect(s.blocked).toBeUndefined();
    // No library of the title stays, so no file is shared with one.
    expect(s.keeps).not.toContain("A file another library of the title uses stays");
    expect(removalScope({ name: "X", title: false, copies: [] }, false).blocked).toBe("Nothing to remove");
  });

  it("offers to delete the files removed libraries kept, with or without their torrents", () => {
    const left = { key: "movie-web", label: "4K WEB", links: 2, downloads: 0 };
    const s = removalScope({ name: "Dune", title: false, copies: [], leftover: [left] }, true);
    expect(s.blocked).toBeUndefined();
    expect(s.confirm).toBe("Delete files");
    expect(s.does).toEqual([
      "2 library files of removed libraries are deleted from the library (moved to Theatarr's trash)",
      "Its debrid torrents are deleted from the providers, after a backup, unless another title or library in Theatarr uses them",
    ]);
    expect(removalScope({ name: "Dune", title: false, copies: [], leftover: [left] }, true, false).keeps).toContain("Its debrid torrents stay");
    expect(removalScope({ name: "Dune", title: false, copies: [], leftover: [left] }, false).blocked).toBe("Nothing to do: the files are kept");
    expect(s.keeps).toContain("A file another library of the title uses stays");
    // The kept files of a title in no library: no library is left to share one.
    expect(removalScope({ name: "Dune", title: false, copies: [], leftover: [left] }, true, true, true).keeps).not.toContain("A file another library of the title uses stays");
    expect(leftoverOnly({ name: "Dune", title: false, copies: [], leftover: [left] })).toBe(true);
    const one = { name: "Dune", title: true, copies: [{ key: "movie-remux", label: "4K Remux", links: 1, downloads: 0 }], leftover: [left] };
    expect(removalLinks(one)).toBe(3);
    expect(removalScope(one, true).does).toContain("3 library files are deleted from the library (moved to Theatarr's trash), and the folders they leave empty are removed");
    expect(removalScope(one, false).keeps).toContain("1 library file stays linked and playable");
  });

  it("counts a removed library's dead symlinks among the files Delete files takes", () => {
    const ls = [
      { id: 1, path: "/a", kind: "symlink", state: "active" },
      { id: 2, path: "/b", kind: "symlink", state: "dead" },
      { id: 3, path: "/c", kind: "symlink", state: "unlinked" },
      { id: 4, path: "/d", kind: "hardlink", state: "active" },
    ];
    expect(leftoverLinks(ls).map((l) => l.id)).toEqual([1, 2]);
  });

  it("unlinks only active symlinks, and says the target stays", () => {
    const ls = [
      { id: 1, path: "/a", kind: "symlink", state: "active" },
      { id: 2, path: "/b", kind: "hardlink", state: "active" },
      { id: 3, path: "/c", kind: "symlink", state: "unlinked" },
      { id: 4, path: "/d", kind: "copy", state: "active" },
    ];
    expect(unlinkable(ls).map((l) => l.id)).toEqual([1]);
    expect(unlinkable(undefined)).toEqual([]);
    const u = unlinkScope("/lib/Dune (2021)/Dune.mkv", "4K WEB");
    expect(u.does[0]).toBe("The library link /lib/Dune (2021)/Dune.mkv moves to Theatarr's trash");
    expect(u.keeps[0]).toBe("The file it points at on the debrid mount is untouched");
  });

  it("warns before grabbing a rejected release", () => {
    const ok = grabScope({ release: "Dune.WEB", accepted: true, info_hash: "ab" }, "4K WEB");
    expect(ok.confirm).toBe("Grab");
    expect(ok.does).toHaveLength(1);
    const bad = grabScope({ release: "Dune.720p", accepted: false, info_hash: "ab", rejections: [{ reason: "q", message: "720p is not wanted" }] }, "4K WEB");
    expect(bad.title).toBe("Grab a rejected release");
    expect(bad.does[1]).toBe("Theatarr rejects it: 720p is not wanted. Grabbing overrides that");
    expect(bad.confirm).toBe("Grab anyway");
    // Rejected only on the server's second look: its reasons win.
    const late = grabScope({ release: "Dune.WEB", accepted: true, info_hash: "ab" }, "4K WEB", ["blocklisted"]);
    expect(late.confirm).toBe("Grab anyway");
    expect(grabScope({ release: "x", accepted: true }, "4K").blocked).toMatch(/no info hash/);
  });
});

describe("bulkRemovalScope", () => {
  const s = (name: string, links: number, title = true, leftover = 0) => ({ name, title, copies: [{ key: "hd", label: "HD", links, downloads: 0 }], leftover: leftover ? [{ key: "old", label: "Old", links: leftover, downloads: 0 }] : [] });

  it("sums the files, leftovers included only when the files are deleted", () => {
    const sc = bulkRemovalScope([s("A", 2), s("B", 3, true, 4)], true);
    expect(sc.title).toBe("Remove 2 titles");
    expect(sc.does).toContain("9 library files are deleted from the library (moved to Theatarr's trash), and the folders they leave empty are removed");
    const keep = bulkRemovalScope([s("A", 2), s("B", 3, true, 4)], false);
    expect(keep.keeps).toContain("5 library files stay linked and playable");
    expect(keep.keeps).toContain("Their debrid torrents stay");
    expect(keep.cleanup?.to).toBe("/admin/torrents?group=unused");
    expect(bulkRemovalScope([], true).blocked).toBe("Nothing to remove");
  });

  it("names a few titles and counts the rest", () => {
    expect(someNames(["A"])).toBe("A");
    expect(someNames(["A", "B", "C", "D"])).toBe("A, B, C and D");
    expect(someNames(["A", "B", "C", "D", "E"])).toBe("A, B, C and 2 more");
  });
});
