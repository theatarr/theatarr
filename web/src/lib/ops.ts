// What each recovery action does and leaves alone, in words, for its confirmation (docs/design/ui.md "Recovery
// actions"). Every dialog names its scope explicitly: the copy, the release, the counts, and what never happens: a real
// file deleted (only Theatarr's own symlinks move to the trash), or a debrid torrent deleted that anything else still
// uses (only a title removal with its files deletes torrents, and only those nothing else uses).
import { plural } from "./format";

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

/** Where torrents nothing uses are deleted: Admin › Torrents, opened on its Not used by Theatarr filter. */
export const UNUSED_TORRENTS = {
  text: "To free space at the provider, delete torrents nothing uses any more under",
  label: "Admin › Torrents › Not used by Theatarr",
  to: "/admin/torrents?group=unused",
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
      "A release from an indexer the library no longer lists is refused, a blocklisted one asks first, and nothing is added while Pause is on",
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

/** Whether a failure's reason says a removal ended the download (router.EndRemoved: the web UI's "copy removed by …",
 *  an arr view's "deleted through … by …"; internal/activity endedByRemovalSQL). The copy is gone: History offers no Retry. */
export function endedByRemoval(reason: string | undefined): boolean {
  return /^(copy removed by |deleted through .+ by )/i.test(reason ?? "");
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

/** sync_of: the named library a copy is kept in sync with, when it goes because of it (Keep in sync). */
export type RemovalCopyLike = { key: string; label: string; links: number; downloads: number; sync_of?: string };
/** leftover: copies removed before whose library links are still active (an earlier removal's failed moves).
 *  links / unlink_links: the server's distinct counts (a file two libraries share counts once) of the copies' links,
 *  and of those with the leftover copies'; without them, the per-library counts are summed. */
export type RemovalScopeLike = { name: string; title: boolean; copies: RemovalCopyLike[]; leftover?: RemovalCopyLike[]; links?: number; unlink_links?: number };

/** The library links a removal with the files deleted takes out: its copies' and the leftover copies'. */
export function removalLinks(s: RemovalScopeLike): number {
  return s.unlink_links ?? [...s.copies, ...(s.leftover ?? [])].reduce((n, c) => n + c.links, 0);
}

/** The library links of a removal's copies (files: with the leftover copies', which only a removal with the files
 *  takes), each file once. */
function scopeLinks(s: RemovalScopeLike, files: boolean): number {
  if (files) return removalLinks(s);
  return s.links ?? s.copies.reduce((n, c) => n + c.links, 0);
}

/** "<Partner> is kept in sync with <Library> and is removed too", per partner pair of the scopes (with the number of
 *  titles when there are several), naming its files and debrid torrents when the boxes take them. */
function syncLines(scopes: readonly RemovalScopeLike[], files: boolean, debrid: boolean): string[] {
  const pairs = new Map<string, { partner: string; named: string; n: number }>();
  for (const s of scopes) {
    for (const c of s.copies) {
      if (!c.sync_of) continue;
      const named = s.copies.find((x) => x.key === c.sync_of)?.label ?? c.sync_of;
      const k = `${c.label}\u0000${named}`;
      const p = pairs.get(k) ?? { partner: c.label, named, n: 0 };
      p.n++;
      pairs.set(k, p);
    }
  }
  const many = scopes.length > 1;
  return [...pairs.values()].map(({ partner, named, n }) => {
    const its = many && n > 1 ? "their" : "its";
    const tail = files ? (debrid ? `, ${its} files and debrid torrents too` : `, ${its} files too`) : "";
    return `${partner} is kept in sync with ${named} and is removed too${many ? ` for ${plural(n, "title")}` : ""}${tail}`;
  });
}

/** Whether a removal only deletes the files libraries removed before kept (the Delete files… dialog): no copy to
 *  remove, so deleting the files is the point and not a choice. */
export function leftoverOnly(s: RemovalScopeLike): boolean {
  return s.copies.length === 0 && (s.leftover ?? []).some((c) => c.links > 0);
}

/** The torrent clean-up a removal does with "Delete it from the debrid providers", and its guard. */
export const REMOVAL_CLEANUP = "Its debrid torrents are deleted from the providers, after a backup, unless another title or library in Theatarr uses them";
export const REMOVAL_LISTS = "Import lists will not restore the removed libraries; add it yourself to bring it back";
/** The whole title's optional list exclusion: it extends REMOVAL_LISTS (always true) to every library. */
export const REMOVAL_EXCLUDE = "Also keep lists from adding it to any other library";
/** Why "Delete it from the debrid providers" is off while the files are kept. */
export const REMOVAL_DEBRID_NEEDS_FILES = "Deleting the torrents needs the files deleted too: kept files would stop playing";
/** After a copy goes while others stay: what the apps show (followed by the title, or "it" / "them"). */
const REMAINING_COPY = "Apps whose libraries include a remaining copy still show";
const SHARED_FILE_STAYS = "A file another library of the title uses stays";

/** The removal dialog's scope. files: "Delete the files"; debrid: "Delete it from the debrid providers" (only with
 *  files). gone: the title is in no library (the kept-files line of a removed title), so no other library can share
 *  a file. */
export function removalScope(s: RemovalScopeLike, files: boolean, debrid = files, gone = false): Scope {
  debrid = files && debrid;
  const leftover = removalLinks(s);
  const torrents = debrid ? [] : ["Its debrid torrents stay"];
  if (leftoverOnly(s)) {
    // Only the files libraries removed before kept: deleting them takes their torrents along when asked, with the
    // same guard.
    return {
      title: `Delete the files kept from ${s.name}`,
      does: [
        `${plural(leftover, "library file")} of removed libraries ${leftover === 1 ? "is" : "are"} deleted from the library (moved to Theatarr's trash)`,
        ...(debrid ? [REMOVAL_CLEANUP] : []),
      ],
      keeps: [...(gone ? [] : [SHARED_FILE_STAYS]), ...torrents, "The libraries stay removed; its history stays"],
      confirm: "Delete files",
      cleanup: debrid ? undefined : UNUSED_TORRENTS,
      blocked: files ? undefined : "Nothing to do: the files are kept",
    };
  }
  const links = scopeLinks(s, files);
  const downloads = s.copies.reduce((n, c) => n + c.downloads, 0);
  const names = s.copies.map((c) => c.label).join(", ");
  const does: string[] = [];
  if (s.title) does.push(`${s.name} is removed from every library it is in (${names}): it stops being wanted, searched or upgraded there`);
  else does.push(`${s.name} is removed from ${names}: ${s.copies.length === 1 ? "this copy stops" : "these copies stop"} being wanted, searched or upgraded`);
  does.push(...syncLines([s], files, debrid));
  // The title leaves Seerr and the arr clients only with its last copy; an app whose view covers a library it stays in
  // (a view can cover several) still shows it.
  if (s.title) does.push("It is hidden from Seerr and the other arr clients");
  else does.push(`${names} ${s.copies.length === 1 ? "loses its copy" : "lose their copies"}. ${REMAINING_COPY} ${s.name}`);
  if (downloads > 0) does.push(`Theatarr stops following ${plural(downloads, "download")} in progress`);
  if (files && links > 0) does.push(`${plural(links, "library file")} ${links === 1 ? "is" : "are"} deleted from the library (moved to Theatarr's trash), and the folders they leave empty are removed`);
  if (debrid) does.push(REMOVAL_CLEANUP);
  const keeps = [
    // A file can be shared only with a library the title stays in.
    ...(files
      ? s.title ? [] : [SHARED_FILE_STAYS]
      : links === 0 ? [] : [`${plural(links, "library file")} ${links === 1 ? "stays" : "stay"} linked and playable`]),
    ...torrents,
    "Its history stays",
    REMOVAL_LISTS,
  ];
  return {
    title: s.title ? `Remove ${s.name}` : `Remove from ${names}`,
    does,
    keeps,
    confirm: s.title ? "Remove title" : s.copies.length === 1 ? "Remove from this library" : "Remove from these libraries",
    cleanup: debrid ? undefined : UNUSED_TORRENTS,
    blocked: s.copies.length === 0 ? "Nothing to remove" : undefined,
  };
}

/** A short list of names: "Alpha, Beta, Gamma and 9 more". */
export function someNames(names: readonly string[], shown = 3): string {
  if (names.length <= shown + 1) return names.length > 1 ? `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}` : (names[0] ?? "");
  return `${names.slice(0, shown).join(", ")} and ${names.length - shown} more`;
}

/** The selection bar's Remove: removalScope's lines with the titles' counts summed. library: the one library the
 *  titles are removed from (Remove from a library); none removes each whole title. */
export function bulkRemovalScope(scopes: readonly RemovalScopeLike[], files: boolean, debrid = files, library?: string): Scope {
  debrid = files && debrid;
  const n = scopes.length;
  const one = n === 1;
  const sum = (f: (s: RemovalScopeLike) => number) => scopes.reduce((t, s) => t + f(s), 0);
  const links = sum((s) => scopeLinks(s, files));
  const downloads = sum((s) => s.copies.reduce((t, c) => t + c.downloads, 0));
  // Titles the removal takes out of every library: all of them without a library, else those with no other.
  const whole = scopes.filter((s) => s.title).length;
  const they = one ? "It is" : "They are";
  const does = [
    library
      ? `${plural(n, "title")} ${one ? "is" : "are"} removed from ${library}: ${one ? "it stops" : "they stop"} being wanted, searched or upgraded there`
      : `${plural(n, "title")} ${one ? "is" : "are"} removed from every library ${one ? "it is" : "they are"} in: ${one ? "it stops" : "they stop"} being wanted, searched or upgraded`,
  ];
  // Titles that keep another copy stay in the apps whose libraries include it; only those in no other library leave
  // Seerr and the arr clients.
  const partly = library !== undefined && whole < n;
  if (partly && whole === 0) does.push(`${library} loses ${one ? "its copy" : "their copies"}. ${REMAINING_COPY} ${one ? "it" : "them"}`);
  else if (partly) {
    // A mixed selection: each group by its count.
    const kept = n - whole;
    does.push(
      `${plural(kept, "title")} ${kept === 1 ? "keeps a copy in another library and stays" : "keep a copy in another library and stay"} visible to apps that include it; ` +
        `${whole} ${whole === 1 ? "is in no other library and is" : "are in no other library and are"} hidden from Seerr and the other arr clients`,
    );
  }
  if (library && !partly && whole > 0) does.push(`${plural(whole, "title")} ${whole === 1 ? "is in no other library and leaves" : "are in no other library and leave"} the library`);
  if (!partly) does.push(`${they} hidden from Seerr and the other arr clients`);
  does.push(...syncLines(scopes, files, debrid));
  if (downloads > 0) does.push(`Theatarr stops following ${plural(downloads, "download")} in progress`);
  if (files && links > 0) does.push(`${plural(links, "library file")} ${links === 1 ? "is" : "are"} deleted from the library (moved to Theatarr's trash), and the folders they leave empty are removed`);
  if (debrid) does.push(one ? REMOVAL_CLEANUP : "Their debrid torrents are deleted from the providers, after a backup, unless another title or library in Theatarr uses them");
  const keeps = [
    ...(files
      ? whole < n ? [SHARED_FILE_STAYS] : []
      : links === 0 ? [] : [`${plural(links, "library file")} ${links === 1 ? "stays" : "stay"} linked and playable`]),
    ...(debrid ? [] : [one ? "Its debrid torrents stay" : "Their debrid torrents stay"]),
    one ? "Its history stays" : "Their history stays",
    one ? REMOVAL_LISTS : "Import lists will not restore the removed libraries; add them yourself to bring them back",
  ];
  const title = library ? `Remove ${plural(n, "title")} from ${library}` : `Remove ${plural(n, "title")}`;
  return { title, does, keeps, confirm: title, cleanup: debrid ? undefined : UNUSED_TORRENTS, blocked: n === 0 ? "Nothing to remove" : undefined };
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
/** The links a removed library left that "Delete files" takes out: Theatarr's symlinks, active or dead (a dead one's
 *  file is gone, but the link stays in the folder). */
export function leftoverLinks<T extends { kind?: string; state?: string; id?: number; path?: string }>(links: T[] | undefined): T[] {
  return (links ?? []).filter((l) => l.kind === "symlink" && (l.state === "active" || l.state === "dead") && !!l.id && !!l.path);
}

export function unlinkable<T extends { kind?: string; state?: string; id?: number; path?: string }>(links: T[] | undefined): T[] {
  return (links ?? []).filter((l) => l.kind === "symlink" && l.state === "active" && !!l.id && !!l.path);
}

// ---- Grab ----

export type CandidateLike = { release?: string; accepted?: boolean; info_hash?: string; rejections?: { reason?: string; message?: string }[] };

/** What a grab overrides when Theatarr rejects the release (rejections: the server's reasons at grab time, over the
 *  candidate's own); undefined for an accepted release. */
export function rejectionWarning(c: CandidateLike, rejections?: string[]): string | undefined {
  const why = rejections ?? (c.rejections ?? []).map((r) => r.message || r.reason || "").filter(Boolean);
  const rejected = !c.accepted || (rejections?.length ?? 0) > 0;
  return rejected ? `Theatarr rejects it: ${why.join("; ") || "no reason given"}. Grabbing overrides that` : undefined;
}

export function grabScope(c: CandidateLike, copy: string, rejections?: string[]): Scope {
  const rejection = rejectionWarning(c, rejections);
  const rejected = !!rejection;
  return {
    title: rejected ? "Grab a rejected release" : "Grab this release",
    does: [
      `${c.release ?? "This release"} goes to the router for the ${copy} library, which adds it at the first provider that has it`,
      ...(rejection ? [rejection] : []),
    ],
    keeps: [
      "A blocklisted release asks first, and the router paces the add like any other",
      "The current file stays until the new one is imported",
    ],
    confirm: rejected ? "Grab anyway" : "Grab",
    blocked: c.info_hash ? undefined : "This release has no info hash: only magnets by hash can be grabbed",
  };
}

/** What the server's 409 says when only the library's blocklist refuses a grab by hand (Error.blocked). */
export type BlockedRelease = { library: string; reason: string };

/** The block of a 409 that only the library's blocklist refused, if that is how it failed. */
export function blockedRelease(e: unknown): BlockedRelease | undefined {
  const b = (e as { status?: number; body?: { blocked?: BlockedRelease } } | undefined)?.body?.blocked;
  return (e as { status?: number } | undefined)?.status === 409 && b?.library ? b : undefined;
}

/** The confirmation of a grab (Grab, Add or Retry) the server refused only for the library's blocklist: confirming
 *  sends it again with unblock, which takes the release off that library's blocklist. */
export function unblockGrabScope(b: BlockedRelease, verb: "Grab" | "Add" | "Retry"): Scope {
  const reason = b.reason.trim().replace(/\.+$/, "");
  return {
    title: `${verb} a blocklisted release`,
    does: [`This release is on ${b.library}'s blocklist${reason ? `: ${reason}` : ""}. ${verb} it anyway? It is taken off the blocklist.`],
    keeps: ["Its other libraries' blocklists stay"],
    confirm: `${verb} anyway`,
  };
}
