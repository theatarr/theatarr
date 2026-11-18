// Tags edited in Theatarr (docs/design/arrview.md "Tags per version"): the labels for suggestions (GET /tags) and
// one call that adds and removes labels on titles (POST /titles/tags).
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Schemas } from "./client";
import { qk } from "./query-keys";

export type TitleTagsResult = Schemas["TitleTagsResult"];

/** POST /titles/tags takes at most this many titles (internal/web/copies.go maxBulkTitles). */
export const TAGS_CHUNK = 500;

/** Every label, for the editor's suggestions; read while an editor is open. */
export const useTagLabels = (enabled: boolean) =>
  useQuery({ queryKey: ["tags"], queryFn: () => api<Schemas["TagList"]>("/tags"), enabled, staleTime: 30_000 });

/** The rule the server keeps (the compatible tag create's): letters, digits and hyphens. */
export const TAG_RULE = /^[a-z0-9-]+$/;
export const TAG_RULE_WORDS = "Allowed characters a-z, 0-9 and -";

/** What is typed, as it is stored: trimmed and lower-case. */
export const tagOf = (typed: string) => typed.trim().toLowerCase();

/** Adds and removes labels on titles, in chunks of TAGS_CHUNK; the answer adds up the titles changed. */
export async function editTags(titleIds: number[], add: string[], remove: string[]): Promise<TitleTagsResult> {
  let changed = 0;
  let tags: string[] | undefined;
  for (let i = 0; i < titleIds.length; i += TAGS_CHUNK) {
    const r = await api<TitleTagsResult>("/titles/tags", { method: "POST", body: { title_ids: titleIds.slice(i, i + TAGS_CHUNK), add, remove } });
    changed += r.changed;
    tags = r.tags;
  }
  return { changed, tags };
}

/** After a save: the title pages, the lists (their Tag filter and facet) and the labels. */
export function useTagsSaved() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["title"] });
    void qc.invalidateQueries({ queryKey: qk.titles.all });
    void qc.invalidateQueries({ queryKey: ["tags"] });
  };
}
