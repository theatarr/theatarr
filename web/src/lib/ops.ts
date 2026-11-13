// What each recovery action does and leaves alone, in words, for its confirmation (docs/design/ui.md "Recovery
// actions"). Every dialog names its scope explicitly: the copy, the release, the counts, and the two things that never
// happen (a debrid torrent deleted: they are shared by every library; a real file deleted: only Theatarr's own symlinks
// move to the trash).

export type Scope = {
  /** The dialog's heading. */
  title: string;
  /** What the action will do, one line each. */
  does: string[];
  /** What stays as it is, one line each. */
  keeps: string[];
  /** The confirm button's words. */
  confirm: string;
  /** Why it cannot run (the confirm button is disabled), when it cannot. */
  blocked?: string;
  /** Where what stays is cleaned up: a line under "Stays as it is" that ends in a link (plain words for accounts
   * that cannot open an admin page). */
  cleanup?: { text: string; label: string; to: string; admin?: boolean };
};

/** Where torrents nothing uses are deleted: Admin › Torrents. The page has no address for its Unused filter, so the
 * words name it. */
export const UNUSED_TORRENTS = {
  text: "To free space at the provider, delete torrents nothing uses any more under",
  label: "Admin › Torrents (the Unused filter)",
  to: "/admin/torrents",
  admin: true,
};

export const SHARED_TORRENT = "The torrent at the debrid provider stays: torrents there are shared by every library";

/** A completed action's server message should stay visible after its row leaves a filtered list. */
export function operationOutcomeToast(title: string, message: string, search?: { started: boolean }): {
  title: string;
  body: string;
  tone: "info" | "warn";
} {
  return { title, body: message, tone: search && !search.started ? "warn" : "info" };
}

function plural(n: number, one: string, many = one + "s"): string {
  return `${n.toLocaleString("en-US")} ${n === 1 ? one : many}`;
}

// ---- Downloads ----

export type DownloadLike = { id?: number; state?: string; version?: string; release?: string; provider_name?: string };

/** The removal modes Activity offers. */
export type RemoveMode = "remove" | "block";

export function removeDownloadScope(d: DownloadLike, mode: RemoveMode, copy: string): Scope {
  const where = d.provider_name ? `The torrent at ${d.provider_name} stays: torrents there are shared by every library` : SHARED_TORRENT;
  const dismissed = d.state === "failed" && mode === "remove";
  const does = [dismissed ? "The failed download is dismissed from Activity; its failure stays in history" : "Theatarr stops following this download: its record ends as failed"];
  if (mode === "block") {
    does.push(`The release is blocklisted for the ${copy} library: Theatarr never grabs it again for that library`);
    does.push(`A search for the ${copy} library starts now (when the library is monitored and active)`);
  }
  return {
    title: mode === "block" ? "Remove, blocklist and search again" : dismissed ? "Dismiss this failed download" : "Remove this download",
    does,
    keeps: [where, "Files already linked into the library stay", ...(mode === "remove" ? ["The release is not blocklisted: a later search may pick it again"] : [])],
    confirm: mode === "block" ? "Remove, blocklist and search" : dismissed ? "Dismiss download" : "Remove download",
  };
}

/** Whether a download can be retried, and what a retry does. */
export function retryKind(state: string | undefined): "route" | "import" | null {
  switch (state) {
    case "failed":
      return "route";
    case "ready":
    case "importing":
      return "import";
  }
  return null;
}

export function retryScope(state: string | undefined, copy: string): Scope {
  const k = retryKind(state);
  if (k === "import") {
    return {
      title: "Import now",
      does: ["The library tries to link the finished download now, without waiting for its back-off"],
      keeps: [SHARED_TORRENT, "Links already made stay"],
      confirm: "Import now",
    };
  }
  return {
    title: "Retry this download",
    does: [`The same release goes to the router again for the ${copy} library: a new download starts when a provider takes it`],
    keeps: [
      "The failed download stays in the history",
      "A blocklisted release, or one from an indexer the library no longer lists, is refused, and nothing is added while the library is Watch only or Pause is on",
    ],
    confirm: "Retry",
    blocked: k === null ? `Nothing to retry: the download is ${state ?? "unknown"} and still on its way` : undefined,
  };
}

/** The download id of a history row about a failed download (journal id dl:<id>:fail), else 0. */
export function failedDownloadId(eventId: string | undefined): number {
  const m = /^dl:(\d+):fail$/.exec(eventId ?? "");
  return m ? Number(m[1]) : 0;
}

// ---- Blocklist ----

export function unblockScope(release: string, copy: string, search: boolean): Scope {
  return {
    title: "Unblock this release",
    does: [
      `${release} may be grabbed again for the ${copy} library`,
      ...(search ? [`A search for the ${copy} library starts now`] : []),
    ],
    keeps: ["Its other libraries' blocklists stay", "Nothing is grabbed until a search or a grab picks it"],
    confirm: search ? "Unblock and search" : "Unblock",
  };
}

// ---- Copies, titles and files ----

export type RemovalCopyLike = { key: string; label: string; links: number; downloads: number };
/** leftover: copies removed before whose library links are still active (an earlier removal's failed moves). */
export type RemovalScopeLike = { name: string; title: boolean; copies: RemovalCopyLike[]; leftover?: RemovalCopyLike[] };

/** The library links a removal with unlink moves: its copies' and the leftover copies'. */
export function removalLinks(s: RemovalScopeLike): number {
  return [...s.copies, ...(s.leftover ?? [])].reduce((n, c) => n + c.links, 0);
}

export function removalScope(s: RemovalScopeLike, unlink: boolean): Scope {
  const leftover = (s.leftover ?? []).reduce((n, c) => n + c.links, 0);
  if (s.copies.length === 0 && leftover > 0) {
    // Only the links of copies removed before: removing again with unlink moves them.
    return {
      title: `Unlink the files left by removed copies of ${s.name}`,
      does: [`${plural(leftover, "library link")} of removed copies ${leftover === 1 ? "moves" : "move"} to Theatarr's trash`],
      keeps: ["Debrid torrents and the files on the mount are never deleted", "The copies stay removed"],
      confirm: "Unlink files",
    };
  }
  const links = s.copies.reduce((n, c) => n + c.links, 0) + (unlink ? leftover : 0);
  const downloads = s.copies.reduce((n, c) => n + c.downloads, 0);
  const names = s.copies.map((c) => c.label).join(", ");
  const does: string[] = [];
  if (s.title) does.push(`${s.name} is removed from every library it is in (${names}): it stops being wanted, searched or upgraded there`);
  else does.push(`${s.name} is removed from ${names}: ${s.copies.length === 1 ? "this copy stops" : "these copies stop"} being wanted, searched or upgraded`);
  does.push(`${s.title ? "It is" : s.copies.length === 1 ? "It is" : "They are"} hidden from Seerr and the other arr clients`);
  if (downloads > 0) does.push(`Theatarr stops following ${plural(downloads, "download")} in progress`);
  if (unlink && links > 0) does.push(`${plural(links, "library link")} ${links === 1 ? "moves" : "move"} to Theatarr's trash`);
  const keeps = [
    "Debrid torrents and the files on the mount are never deleted",
    ...(unlink || links === 0 ? [] : [`${plural(links, "library file")} ${links === 1 ? "stays" : "stay"} linked and playable`]),
    s.title ? "Its history stays; adding the title again restores it" : `Its history stays; adding ${s.name} to the library again restores it`,
  ];
  return {
    title: s.title ? `Remove ${s.name}` : `Remove from ${names}`,
    does,
    keeps,
    confirm: s.title ? "Remove title" : s.copies.length === 1 ? "Remove this copy" : "Remove these copies",
    cleanup: UNUSED_TORRENTS,
    blocked: s.copies.length === 0 ? "Nothing to remove" : undefined,
  };
}

export function unlinkScope(path: string, copy: string): Scope {
  return {
    title: "Unlink this file",
    does: [
      `The library link ${path} moves to Theatarr's trash`,
      `The ${copy} copy is wanted again when it has no other file (a search may then find a replacement)`,
    ],
    keeps: ["The file it points at on the debrid mount is untouched", SHARED_TORRENT],
    confirm: "Unlink file",
  };
}

/** Change the episodes one library link holds (admins): replace also moves the other files of chosen episodes aside. */
export function reassignScope(path: string, copy: string, replace: boolean, chosen: number): Scope {
  return {
    title: "Change episodes",
    does: [
      `The library link ${path} is renamed for the episodes chosen in the ${copy} library`,
      "Episodes the file no longer holds are wanted again when no other file holds them",
      replace
        ? "A file of this library that holds a chosen episode moves to Theatarr's trash, with every episode it holds"
        : "A chosen episode that already has a file in this library stops the change",
      "Later imports of this download keep the choice",
    ],
    keeps: ["The file it points at on the debrid mount is untouched", "The other libraries' files", SHARED_TORRENT],
    confirm: "Change episodes",
    blocked: chosen === 0 ? "Choose at least one episode." : undefined,
  };
}

/** The links of a copy an operator may unlink: Theatarr's own active symlinks. */
export function unlinkable<T extends { kind?: string; state?: string; id?: number; path?: string }>(links: T[] | undefined): T[] {
  return (links ?? []).filter((l) => l.kind === "symlink" && l.state === "active" && !!l.id && !!l.path);
}

// ---- Grab ----

export type CandidateLike = { release?: string; accepted?: boolean; info_hash?: string; rejections?: { reason?: string; message?: string }[] };

export function grabScope(c: CandidateLike, copy: string, rejections?: string[]): Scope {
  const why = rejections ?? (c.rejections ?? []).map((r) => r.message || r.reason || "").filter(Boolean);
  const rejected = !c.accepted || (rejections?.length ?? 0) > 0;
  return {
    title: rejected ? "Grab a rejected release" : "Grab this release",
    does: [
      `${c.release ?? "This release"} goes to the router for the ${copy} library, which adds it at the first provider that has it`,
      ...(rejected ? [`Theatarr rejects it: ${why.join("; ") || "no reason given"}. Grabbing overrides that`] : []),
    ],
    keeps: [
      "The router still refuses a blocklisted release, and paces the add like any other",
      "The current file stays until the new one is imported",
    ],
    confirm: rejected ? "Grab anyway" : "Grab",
    blocked: c.info_hash ? undefined : "This release has no info hash: only magnets by hash can be grabbed",
  };
}
