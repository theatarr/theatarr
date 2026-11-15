// Links into admin-only pages (Torrents, Decision journal) from screens managers see too. A manager has
// "see files" but no admin pages, so such a link would open "Not for your account": callers render the text plain
// (an info hash copyable) when adminLink answers undefined.

/** Admin › Torrents filtered to one torrent: the first 12 hex of its hash, which the filter matches as a prefix. */
export function torrentsLink(hash: string): string {
  return `/admin/torrents?q=${encodeURIComponent(hash.slice(0, 12).toLowerCase())}`;
}

export function journalLink(id: string): string {
  return `/admin/system/journal/${encodeURIComponent(id)}`;
}

/** The journal list (System › Decision journal), with an optional filter query such as "?suspect=1". */
export function journalHome(query = ""): string {
  return "/admin/system/journal" + query;
}

/** The link for an admin; undefined for everyone else. */
export function adminLink(admin: boolean, to: string): string | undefined {
  return admin ? to : undefined;
}
