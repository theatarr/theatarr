// Plain words for the anime review cards (Admin › Reviews › Anime mappings): episode ranges, mapping sources, the
// kind of each item and the question it asks.
import type { components } from "@/api/schema";

export type ReviewItem = components["schemas"]["AnimeReviewItem"];
export type ReviewOption = components["schemas"]["AnimeReviewOption"];

const pairs = (eps: unknown[] | undefined): [number, number][] =>
  (eps ?? []).filter((p): p is [number, number] => Array.isArray(p) && p.length === 2).map((p) => [Number(p[0]), Number(p[1])]);

/** "Season 2, episodes 13–24"; "Specials 5–6"; "Season 1, episode 3"; several seasons joined with "; ". */
export function rangeWords(eps: unknown[] | undefined): string {
  const bySeason = new Map<number, number[]>();
  for (const [s, e] of [...pairs(eps)].sort((a, b) => a[0] - b[0] || a[1] - b[1])) {
    const list = bySeason.get(s) ?? [];
    if (list[list.length - 1] !== e) list.push(e);
    bySeason.set(s, list);
  }
  const out: string[] = [];
  for (const [s, list] of bySeason) {
    const runs: string[] = [];
    for (let i = 0; i < list.length; ) {
      let j = i;
      while (j + 1 < list.length && list[j + 1] === list[j] + 1) j++;
      runs.push(i === j ? `${list[i]}` : `${list[i]}–${list[j]}`);
      i = j + 1;
    }
    const one = list.length === 1;
    out.push(s === 0 ? `${one ? "Special" : "Specials"} ${runs.join(", ")}` : `Season ${s}, ${one ? "episode" : "episodes"} ${runs.join(", ")}`);
  }
  return out.join("; ");
}

export const SOURCE_WORDS: Record<string, string> = {
  anibridge: "AniBridge list",
  scudlee: "ScudLee's anime-lists",
  heuristic: "Air date and title match",
  manual: "Set by hand",
};

export const sourceWords = (s: string | undefined) => (s ? SOURCE_WORDS[s] ?? s : "");

export const KIND_WORDS: Record<string, string> = {
  conflict: "Lists disagree",
  heuristic: "Matched by date and title",
  refresh_shrink: "Refresh would unmap episodes",
  refresh_moves_linked: "Refresh would move files",
};

export const isHeldRefresh = (it: ReviewItem) => it.kind === "refresh_shrink" || it.kind === "refresh_moves_linked";

/** A series by name, else "TVDB {id}". */
export const seriesName = (s: { tvdb?: number; name?: string } | undefined) => s?.name || `TVDB ${s?.tvdb ?? "?"}`;

/** The entry's title, else its English title, else its key. */
export const entryName = (it: ReviewItem) => it.entry?.title || it.entry?.title_en || it.title || it.entry_key || "";

/** The proposed option, else the first one. */
export const proposedOption = (it: ReviewItem) => it.options?.find((o) => o.proposed) ?? it.options?.[0];

/** "Mushoku Tensei Season 2, episodes 13–24". */
export const optionWords = (o: ReviewOption) => `${seriesName(o.series)} ${rangeWords(o.episodes)}`;

/** The one sentence a card asks. */
export function reviewQuestion(it: ReviewItem): string {
  const entry = entryName(it);
  const franchise = it.franchise_name || it.franchise || "";
  const p = proposedOption(it);
  const series = p ? seriesName(p.series) : seriesName({ tvdb: it.mapping?.tvdb });
  const r = it.refresh;
  switch (it.kind) {
    case "conflict":
      return `Which ${series} episodes are “${entry}”? The mapping lists disagree, so until you choose its episodes keep Sonarr's numbering.`;
    case "heuristic":
      return `Is “${entry}” ${series} ${rangeWords(p?.episodes ?? it.mapping?.episodes)}? No mapping list covers it; Theatarr matched it by air date and title and uses this match now.`;
    case "refresh_shrink":
      if (!r) return `Apply the refresh of ${franchise}?`;
      return `Apply the refresh of ${franchise}? It would map ${r.after ?? 0} of the ${r.before ?? 0} ${seriesName(r.series)} episodes mapped now. Nothing changes until you choose.`;
    case "refresh_moves_linked": {
      if (!r) return `Apply the refresh of ${franchise}?`;
      const from = r.from ? optionWords(r.from) : "Sonarr's numbering";
      const n = r.with_files ?? 0;
      if (!r.to) return `Apply the refresh of ${franchise}? It would stop mapping “${entry}” (${from}); its ${n} files would keep their place and new ones take Sonarr's numbering.`;
      return `Apply the refresh of ${franchise}? It would move “${entry}” from ${from} to ${optionWords(r.to)}; ${n} of these episodes have files, which would move with it.`;
    }
    default:
      return it.summary ?? "";
  }
}

const OUTCOME: Record<string, string> = { accepted: "Accepted", rejected: "Rejected", superseded: "Replaced by a later build" };

/** What became of a resolved item, with its note. */
export function outcomeWords(it: ReviewItem): string {
  const s = OUTCOME[it.state ?? ""] ?? "";
  return it.note ? `${s}. Note: ${it.note}` : s;
}
