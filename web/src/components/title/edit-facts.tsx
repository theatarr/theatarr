// Edit file facts… on the title page (docs/design/library.md "Correcting an installed file"), for managers: the file
// rows of a movie's library row and of the episode window. The quality (with its revision: 2 or more is a Proper or
// Repack, as manual import sets it), languages and release group the file was imported with, prefilled with what is
// stored. Saving stores them as the file's own and works out its score and cutoff again in every library that has
// it; nothing is renamed.
import { PencilLine } from "lucide-react";
import { useState } from "react";
import { useEditFileFacts, useFileFacts } from "@/api/ops";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";

type Form = { quality: string; revision: number; languages: string[]; release_group: string };

/** Revision 2 or more is a Proper or Repack, as manual import sets it. */
const REVISION_WORDS: Record<number, string> = { 1: "1 · first release", 2: "2 · Proper or Repack" };
const revisions = (stored: number) => [...new Set([1, 2, 3, 4, stored])].sort((a, b) => a - b);

const SCOPE = {
  title: "Edit file facts",
  does: ["The file's quality, languages and release group are stored as set here; its score and whether it meets the cutoff are worked out again in every library that has it."],
  keeps: ["The file is not renamed or moved; the rename preview shows a new name if the naming uses these facts."],
  confirm: "Save",
};

function EditFactsDialog({ titleId, linkId, name, onClose }: { titleId: number; linkId: number; name: string; onClose: () => void }) {
  const facts = useFileFacts(titleId, linkId);
  const edit = useEditFileFacts(titleId);
  const [form, setForm] = useState<Form | null>(null);
  const f = facts.data;
  const v: Form | null = form ?? (f ? { quality: f.quality, revision: f.revision, languages: f.languages, release_group: f.release_group } : null);
  const set = (patch: Partial<Form>) => v && setForm({ ...v, ...patch });
  return (
    <ConfirmDialog
      scope={SCOPE}
      subject={name}
      busy={edit.isPending || facts.isPending}
      error={edit.error ?? facts.error}
      done={edit.data?.message}
      onClose={onClose}
      danger={false}
      onConfirm={() => v && edit.mutate({ linkId, ...v })}
      testid="edit-facts-dialog"
    >
      {edit.data || !v || !f ? null : (
        <div className="grid gap-2 text-sm">
          <label className="grid gap-1">
            Quality
            <Select value={v.quality} onChange={(e) => set({ quality: e.target.value })} data-testid="edit-facts-quality">
              {f.quality_choices.map((q) => <option key={q} value={q}>{q}</option>)}
            </Select>
          </label>
          <label className="grid gap-1">
            Revision
            <Select value={v.revision} onChange={(e) => set({ revision: Number(e.target.value) })} data-testid="edit-facts-revision">
              {revisions(f.revision).map((r) => <option key={r} value={r}>{REVISION_WORDS[r] ?? String(r)}</option>)}
            </Select>
          </label>
          <label className="grid gap-1">
            Languages
            <select
              multiple
              className="block h-32 w-full rounded-[5px] border border-input bg-field px-1.5 py-1 text-[13.5px] text-foreground"
              value={v.languages}
              onChange={(e) => set({ languages: Array.from(e.target.selectedOptions, (o) => o.value) })}
              data-testid="edit-facts-languages"
            >
              {f.language_choices.map((l) => <option key={l} value={l}>{l}</option>)}
            </select>
          </label>
          <label className="grid gap-1">
            Release group
            <Input value={v.release_group} maxLength={100} onChange={(e) => set({ release_group: e.target.value })} data-testid="edit-facts-group" />
          </label>
        </div>
      )}
    </ConfirmDialog>
  );
}

/** A file row's "Edit file facts…" for one of the title's files (managers; the caller checks). */
export function EditFacts({ titleId, linkId, path }: { titleId: number; linkId: number; path: string }) {
  const [open, setOpen] = useState(false);
  const name = path.split("/").pop() ?? path;
  return (
    <>
      <Button size="xs" variant="ghost" onClick={() => setOpen(true)} data-testid="link-edit-facts">
        <PencilLine /> Edit file facts…
      </Button>
      {open ? <EditFactsDialog titleId={titleId} linkId={linkId} name={name} onClose={() => setOpen(false)} /> : null}
    </>
  );
}
