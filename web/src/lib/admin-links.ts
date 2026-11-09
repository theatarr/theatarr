// Links into admin-only pages (Provider inventory, Decision journal) from screens managers see too. A manager has
// "see files" but no admin pages, so such a link would open "Not for your account": callers render the text plain
// (an info hash copyable) when adminLink answers undefined.

export function inventoryLink(hash: string): string {
  return `/admin/torrents/inventory/${encodeURIComponent(hash)}`;
}

export function journalLink(id: string): string {
  return `/admin/advanced/journal/${encodeURIComponent(id)}`;
}

/** The link for an admin; undefined for everyone else. */
export function adminLink(admin: boolean, to: string): string | undefined {
  return admin ? to : undefined;
}
