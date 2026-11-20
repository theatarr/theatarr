// Change episodes (docs/design/library.md "Which file is which episode"), for admins, in the episode overview's File: the
// episodes one of Theatarr's library links holds, corrected by hand (anime absolute numbering, packs, specials). The
// server renames the link for them in its library and keeps the choice for later imports of its download; with
// Replace, another file of that library holding a chosen episode moves to the trash instead of stopping the change.
import { ListOrdered } from "lucide-react";
import { useMemo, useState } from "react";
import { useEpisodes } from "@/api/library";
import { useReassignEpisodes } from "@/api/ops";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Choice, ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { reassignScope } from "@/lib/ops";

const code = (season: number, number: number) => `S${String(season).padStart(2, "0")}E${String(number).padStart(2, "0")}`;

function ChangeEpisodesDialog({ titleId, linkId, path, copy, held, onClose }: { titleId: number; linkId: number; path: string; copy: string; held: string[]; onClose: () => void }) {
  const list = useEpisodes(String(titleId));
  const reassign = useReassignEpisodes(titleId);
  const [chosen, setChosen] = useState<ReadonlySet<number> | null>(null);
  const [filter, setFilter] = useState("");
  const [replace, setReplace] = useState(false);
  const data = list.data?.episodes;
  const eps = useMemo(() => data ?? [], [data]);
  const label = (e: (typeof eps)[number]) => `${code(e.season, e.number)}${e.absolute ? ` · absolute ${e.absolute}` : ""}${e.name ? ` ${e.name}` : ""}`;
  // The file's episodes, worked out once per list, not on every keystroke in the filter.
  const stored = useMemo(() => new Set(eps.filter((e) => held.includes(code(e.season, e.number))).map((e) => e.id)), [eps, held]);
  const ticked = chosen ?? stored;
  // In the list's order; an episode hidden by the filter keeps its tick.
  const picked = eps.filter((e) => ticked.has(e.id)).map((e) => e.id);
  const unchanged = picked.length === stored.size && picked.every((id) => stored.has(id));
  const words = filter.trim().toLowerCase();
  const shown = words ? eps.filter((e) => label(e).toLowerCase().includes(words)) : eps;
  const toggle = (id: number, on: boolean) => {
    const next = new Set(ticked);
    if (on) next.add(id);
    else next.delete(id);
    setChosen(next);
  };
  return (
    <ConfirmDialog
      scope={reassignScope(path, copy, replace, picked.length)}
      subject={copy}
      busy={reassign.isPending || list.isPending}
      // The same episodes as now: nothing to change.
      disabled={unchanged}
      error={reassign.error ?? list.error}
      done={reassign.data?.message}
      danger={replace}
      onClose={onClose}
      onConfirm={() => reassign.mutate({ linkId, episode_ids: picked, replace })}
      testid="change-episodes-dialog"
    >
      {reassign.data ? null : (
        <>
          <div className="text-sm">
            <p>Episodes in this file ({picked.length} chosen)</p>
            <Input className="mt-1" aria-label="Filter episodes" placeholder="Filter episodes" value={filter} onChange={(e) => setFilter(e.target.value)} data-testid="change-episodes-filter" />
            <ul className="mt-1 max-h-60 overflow-y-auto rounded-md border border-[var(--line)] p-1" data-testid="change-episodes-list">
              {shown.map((e) => (
                <li key={e.id}>
                  <label className="flex min-h-10 cursor-pointer items-center gap-2 rounded px-1.5 hover:bg-[var(--hover)]">
                    <Checkbox checked={ticked.has(e.id)} onChange={(ev) => toggle(e.id, ev.target.checked)} />
                    <span className="min-w-0 [overflow-wrap:anywhere]">{label(e)}</span>
                  </label>
                </li>
              ))}
              {shown.length === 0 && eps.length > 0 ? <li className="px-1.5 py-2 text-[var(--ink-3)]">No episode matches.</li> : null}
            </ul>
          </div>
          <Choice checked={replace} onChange={setReplace} testid="change-episodes-replace">
            Replace: when a chosen episode already has a file in this library, move that file to Theatarr's trash
          </Choice>
        </>
      )}
    </ConfirmDialog>
  );
}

/** Why Change episodes is off for a link without a download (taken over from an arr): the choice has nowhere to be
 *  kept, so the server would refuse it. */
export const CHANGE_EPISODES_NO_DOWNLOAD = "Taken over from Sonarr or Radarr: use Manual import to change its episodes";

/** The File section's "Change episodes" for one of Theatarr's links (admins only; the caller checks). A link with no
 *  download gets the button disabled, with the reason. */
export function ChangeEpisodes({ titleId, linkId, path, copy, held, noDownload = false }: { titleId: number; linkId: number; path: string; copy: string; held: string[]; noDownload?: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button size="xs" variant="outline" onClick={() => setOpen(true)} disabled={noDownload} data-testid="change-episodes">
        <ListOrdered /> Change episodes
      </Button>
      {noDownload ? <p className="mt-1 text-xs text-[var(--ink-3)]" data-testid="change-episodes-why">{CHANGE_EPISODES_NO_DOWNLOAD}</p> : null}
      {open ? <ChangeEpisodesDialog titleId={titleId} linkId={linkId} path={path} copy={copy} held={held} onClose={() => setOpen(false)} /> : null}
    </>
  );
}
