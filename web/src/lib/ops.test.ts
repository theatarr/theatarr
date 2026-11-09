import { describe, expect, it } from "vitest";
import {
  failedDownloadId,
  grabScope,
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

  it("names the copy an unblock frees", () => {
    const u = unblockScope("Dune.2021.2160p.WEB-DL-GRP", "4K WEB", true);
    expect(u.does).toEqual(["Dune.2021.2160p.WEB-DL-GRP may be grabbed again for the 4K WEB library", "A search for the 4K WEB library starts now"]);
    expect(unblockScope("x", "4K", false).confirm).toBe("Unblock");
  });

  it("scopes a copy removal with its links and downloads", () => {
    const one = { name: "Dune", title: false, copies: [{ key: "movie-web", label: "4K WEB", links: 2, downloads: 1 }] };
    const keep = removalScope(one, false);
    expect(keep.title).toBe("Remove the 4K WEB library");
    expect(keep.does).toEqual([
      "The 4K WEB library stops being wanted, searched or upgraded",
      "It is hidden from Seerr and the other arr clients",
      "Theatarr stops following 1 download in progress",
    ]);
    expect(keep.keeps).toContain("2 library files stay linked and playable");
    expect(keep.keeps[0]).toBe("Debrid torrents and the files on the mount are never deleted");
    const unlink = removalScope(one, true);
    expect(unlink.does).toContain("2 library links move to Theatarr's trash");
    expect(unlink.keeps).not.toContain("2 library files stay linked and playable");
    expect(unlink.confirm).toBe("Remove library");
  });

  it("scopes a title removal and refuses a copy an arr still owns", () => {
    const all = {
      name: "Dune",
      title: true,
      copies: [
        { key: "movie-remux", label: "4K Remux", links: 1, downloads: 0, managed_by: "radarr_4k_debrid" },
        { key: "movie-web", label: "4K WEB", links: 0, downloads: 0 },
      ],
    };
    const s = removalScope(all, true);
    expect(s.title).toBe("Remove Dune");
    expect(s.does[0]).toBe("Dune leaves the library: every library (4K Remux, 4K WEB) stops being wanted, searched or upgraded");
    expect(s.confirm).toBe("Remove title");
    expect(s.blocked).toBe("4K Remux is managed by radarr_4k_debrid until cutover: remove it there");
    expect(removalScope({ name: "X", title: false, copies: [] }, false).blocked).toBe("Nothing to remove");
  });

  it("offers to move the links removed libraries left behind", () => {
    const left = { key: "movie-web", label: "4K WEB", links: 2, downloads: 0 };
    const s = removalScope({ name: "Dune", title: false, copies: [], leftover: [left] }, true);
    expect(s.blocked).toBeUndefined();
    expect(s.confirm).toBe("Unlink files");
    expect(s.does).toEqual(["2 library links of removed libraries move to Theatarr's trash"]);
    const one = { name: "Dune", title: true, copies: [{ key: "movie-remux", label: "4K Remux", links: 1, downloads: 0 }], leftover: [left] };
    expect(removalLinks(one)).toBe(3);
    expect(removalScope(one, true).does).toContain("3 library links move to Theatarr's trash");
    expect(removalScope(one, false).keeps).toContain("1 library file stays linked and playable");
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
