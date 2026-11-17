// Libraries kept in sync (Settings › Libraries, "Keep in sync"; docs/design/ui.md): what the dialogs say about the
// libraries an add also gives a copy. Which partners gain one is the server's answer (each add option's `synced`);
// nothing here works the rule out.

/** The partners an add brings because a ticked library is in sync with them: partner key → the ticked library's key.
 *  A partner ticked itself is not listed (it is an ordinary ticked box). */
export function syncedBy(options: { key: string; synced?: string[] }[], ticked: (key: string) => boolean): Record<string, string> {
  const out: Record<string, string> = {};
  for (const o of options) {
    if (!ticked(o.key)) continue;
    for (const p of o.synced ?? []) {
      if (!ticked(p) && out[p] === undefined) out[p] = o.key;
    }
  }
  return out;
}

/** The group of a library, or undefined. */
export function groupOf(groups: string[][] | undefined, key: string): string[] | undefined {
  return groups?.find((g) => g.includes(key));
}

/** "4K WEB" / "4K WEB and HD" / "4K WEB, HD and SD". */
export function namesList(names: string[]): string {
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

/** The one-line note where an add to a library is configured elsewhere (collections, import lists, Connected apps):
 *  "Titles added to 4K Remux are also added to 4K WEB (in sync)."; empty when the library is in no group, or when
 *  every partner is among chosen (the libraries chosen there) already. */
export function syncNote(groups: string[][] | undefined, key: string, nameOf: (key: string) => string, chosen: string[] = []): string {
  const partners = (groupOf(groups, key) ?? []).filter((k) => k !== key && !chosen.includes(k));
  if (partners.length === 0) return "";
  return `Titles added to ${nameOf(key)} are also added to ${namesList(partners.map(nameOf))} (in sync).`;
}
