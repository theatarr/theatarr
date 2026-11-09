// Change episodes (docs/design/library.md "Which file is which episode"), for admins, in the episode overview's File: the
// episodes one of Theatarr's library links holds, corrected by hand (anime absolute numbering, packs, specials). The
// server renames the link for them in its library and keeps the choice for later imports of its download; with
// Replace, another file of that library holding a chosen episode moves to the trash instead of stopping the change.
import { ListOrdered } from "lucide-react";
import { useState } from "react";
import { useEpisodes } from "@/api/library";
import { useReassignEpisodes } from "@/api/ops";
import { Button } from "@/components/ui/button";
import { Choice, ConfirmDialog } from "@/components/ui/confirm-dialog";
import { reassignScope } from "@/lib/ops";

const code = (season: number, number: number) => `S${String(season).padStart(2, "0")}E${String(number).padStart(2, "0")}`;

function ChangeEpisodesDialog({ titleId, linkId, path, copy, held, onClose }: { titleId: number; linkId: number; path: string; copy: string; held: string[]; onClose: () => void }) {
  const list = useEpisodes(String(titleId));
  const reassign = useReassignEpisodes(titleId);
  const [chosen, setChosen] = useState<number[] | null>(null);
  const [replace, setReplace] = useState(false);
  const eps = list.data?.episodes ?? [];
  const picked = chosen ?? eps.filter((e) => held.includes(code(e.season, e.number))).map((e) => e.id);
  return (
    <ConfirmDialog
      scope={reassignScope(path, copy, replace, picked.length)}
      subject={copy}
      busy={reassign.isPending || list.isPending}
      error={reassign.error ?? list.error}
      done={reassign.data?.message}
      danger={replace}
      onClose={onClose}
      onConfirm={() => reassign.mutate({ linkId, episode_ids: picked, replace })}
      testid="change-episodes-dialog"
    >
      {reassign.data ? null : (
        <>
          <label className="block text-sm">
            Episodes in this file
            <select
              multiple
              className="mt-1 block h-40 w-full rounded-md border border-[var(--line)] bg-transparent p-1 text-sm"
              value={picked.map(String)}
              onChange={(e) => setChosen(Array.from(e.target.selectedOptions, (o) => Number(o.value)))}
              data-testid="change-episodes-select"
            >
              {eps.map((e) => (
                <option key={e.id} value={e.id}>
                  {code(e.season, e.number)}
                  {e.absolute ? ` · absolute ${e.absolute}` : ""} {e.name}
                </option>
              ))}
            </select>
          </label>
          <Choice checked={replace} onChange={setReplace} testid="change-episodes-replace">
            Replace: when a chosen episode already has a file in this library, move that file to Theatarr's trash
          </Choice>
        </>
      )}
    </ConfirmDialog>
  );
}

/** The File section's "Change episodes" for one of Theatarr's links (admins only; the caller checks). */
export function ChangeEpisodes({ titleId, linkId, path, copy, held }: { titleId: number; linkId: number; path: string; copy: string; held: string[] }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button size="xs" variant="outline" onClick={() => setOpen(true)} data-testid="change-episodes">
        <ListOrdered /> Change episodes
      </Button>
      {open ? <ChangeEpisodesDialog titleId={titleId} linkId={linkId} path={path} copy={copy} held={held} onClose={() => setOpen(false)} /> : null}
    </>
  );
}
