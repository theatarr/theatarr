// Tags edited in Theatarr (docs/design/ui.md, Title page and "Library views"; docs/design/arrview.md "Tags per
// version"). About's Tags fact has Edit for managers: the title's labels as chips, × takes one off, the box adds one
// (suggesting the labels there are). Save sends only what the person changed (POST /titles/tags): a label added or
// typed goes on every library that has the title, a label taken off comes off every one, and any other label stays
// as each library has it. Saving sends no event. The selection bar's Tags… (BulkTagsDialog) adds labels to, or removes
// them from, the selected titles with the same box and the same call.
import { Pencil, X } from "lucide-react";
import { useId, useState } from "react";
import { editTags, TAG_RULE, TAG_RULE_WORDS, tagOf, useTagLabels, useTagsSaved } from "@/api/tags";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/toaster";
import { plural } from "@/lib/format";
import { namesList } from "@/lib/sync-groups";

/** The title page dialog's help line. */
export const TAGS_HELP = "Changes apply to every library that has this title. Connected apps see these tags, and a webhook limited to a tag gets this title's future events.";

/** The box that adds a label: Enter or Add puts what is typed (trimmed, lower-case) through onAdd; a label outside
 *  the rule is refused with the rule's words as it is typed. */
export function TagBox({ onAdd, label = "Add a tag" }: { onAdd: (tag: string) => void; label?: string }) {
  const [text, setText] = useState("");
  const list = useId();
  const labels = useTagLabels(true);
  const tag = tagOf(text);
  const bad = tag !== "" && !TAG_RULE.test(tag);
  const add = () => {
    if (tag === "" || bad) return;
    onAdd(tag);
    setText("");
  };
  return (
    <div>
      <div className="flex gap-2">
        <Input aria-label={label} placeholder={label} value={text} list={list} autoComplete="off" aria-invalid={bad || undefined}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              add();
            }
          }} data-testid="tag-input" />
        <Button variant="outline" size="sm" className="h-8" disabled={tag === "" || bad} onClick={add} data-testid="tag-add">Add</Button>
      </div>
      <datalist id={list}>
        {(labels.data?.tags ?? []).map((l) => <option key={l} value={l} />)}
      </datalist>
      {bad ? <p className="mt-1 text-xs text-[var(--bad-ink)]" role="alert" data-testid="tag-bad">{TAG_RULE_WORDS}</p> : null}
    </div>
  );
}

/** Labels as chips, each with × to take it off. */
export function TagChips({ tags, onRemove, empty }: { tags: string[]; onRemove: (tag: string) => void; empty: string }) {
  if (tags.length === 0) return <p className="text-muted-foreground" data-testid="tag-chips">{empty}</p>;
  return (
    <ul className="flex flex-wrap gap-1.5" data-testid="tag-chips">
      {tags.map((g) => (
        <li key={g} className="inline-flex items-center gap-0.5 rounded-full border border-[var(--line)] bg-[var(--bg)] py-0.5 pl-2.5 pr-0.5 text-[13px] text-[var(--ink)]">
          {g}
          <button type="button" aria-label={`Remove ${g}`} onClick={() => onRemove(g)} className="rounded-full p-1 text-[var(--ink-3)] hover:bg-[var(--line)] hover:text-[var(--ink)]">
            <X className="size-3" />
          </button>
        </li>
      ))}
    </ul>
  );
}

/** The edit: what was added (typed, also a label already a chip) and what was taken off. */
export type TagEdit = { add: string[]; remove: string[] };

export const addTag = (e: TagEdit, tag: string): TagEdit => ({ add: e.add.includes(tag) ? e.add : [...e.add, tag], remove: e.remove.filter((g) => g !== tag) });
export const removeTag = (e: TagEdit, tag: string): TagEdit => ({ add: e.add.filter((g) => g !== tag), remove: e.remove.includes(tag) ? e.remove : [...e.remove, tag] });

/** The title page's dialog. */
export function TitleTagsDialog({ titleId, name, tags, onClose }: { titleId: number; name: string; tags: string[]; onClose: () => void }) {
  const [edit, setEdit] = useState<TagEdit>({ add: [], remove: [] });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const saved = useTagsSaved();
  // The chips: the title's labels less the ones taken off, plus the new ones, in label order.
  const chips = [...new Set([...tags.filter((g) => !edit.remove.includes(g)), ...edit.add])].sort();
  const save = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await editTags([titleId], edit.add, edit.remove.filter((g) => tags.includes(g)));
      saved();
      onClose();
    } catch (e) {
      setError(e);
    }
    setBusy(false);
  };
  const changed = edit.add.length > 0 || edit.remove.some((g) => tags.includes(g));
  return (
    <ConfirmDialog
      scope={{ title: "Edit tags", does: [], keeps: [], confirm: "Save" }}
      subject={name}
      danger={false}
      busy={busy}
      disabled={!changed}
      error={error}
      onClose={onClose}
      onConfirm={() => void save()}
      testid="title-tags-dialog"
      top={
        <div className="space-y-3">
          <TagChips tags={chips} empty="No tags" onRemove={(g) => setEdit((e) => removeTag(e, g))} />
          <TagBox onAdd={(g) => setEdit((e) => addTag(e, g))} />
          <p className="text-xs text-[var(--ink-3)]" data-testid="title-tags-help">{TAGS_HELP}</p>
        </div>
      }
    />
  );
}

/** About's Edit beside the labels. */
export function EditTitleTags({ titleId, name, tags }: { titleId: number; name: string; tags: string[] }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" className="inline-flex items-center gap-1 text-muted-foreground hover:text-[var(--ink)] hover:underline" onClick={() => setOpen(true)} data-testid="title-tags-edit">
        <Pencil className="size-3" aria-hidden />
        Edit
      </button>
      {open ? <TitleTagsDialog titleId={titleId} name={name} tags={tags} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

/** The selection bar's dialog's help line. */
export const BULK_TAGS_HELP = "Changes apply to every library that has these titles. Connected apps see these tags, and a webhook limited to a tag gets those titles' future events.";

/** The selection bar's Tags…: add labels to, or remove labels from, the selected titles (no "replace all"). */
export function BulkTagsDialog({ titleIds, onClose, onDone }: { titleIds: number[]; onClose: () => void; onDone: () => void }) {
  const [mode, setMode] = useState<"add" | "remove">("add");
  const [tags, setTags] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const saved = useTagsSaved();
  const add = mode === "add";
  const save = async () => {
    setBusy(true);
    setError(undefined);
    try {
      const r = await editTags(titleIds, add ? tags : [], add ? [] : tags);
      saved();
      toast({ tone: "ok", title: `${add ? "Added" : "Removed"} ${namesList(tags)} ${add ? "to" : "from"} ${plural(r.changed, "title")}` });
      onDone();
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };
  const radio = (value: "add" | "remove", words: string) => (
    <label className="inline-flex cursor-pointer items-center gap-1.5">
      <input type="radio" name="bulk-tags-mode" value={value} checked={mode === value} onChange={() => setMode(value)} data-testid={`bulk-tags-${value}`} />
      {words}
    </label>
  );
  return (
    <ConfirmDialog
      scope={{ title: `Tags for ${plural(titleIds.length, "title")}`, does: [], keeps: [], confirm: add ? "Add tags" : "Remove tags" }}
      danger={false}
      busy={busy}
      disabled={tags.length === 0}
      error={error}
      onClose={onClose}
      onConfirm={() => void save()}
      testid="bulk-tags"
      top={
        <div className="space-y-3">
          <div className="flex gap-4" role="radiogroup" aria-label="Add or remove">
            {radio("add", "Add to these titles")}
            {radio("remove", "Remove from these titles")}
          </div>
          <TagChips tags={tags} empty="No tags chosen" onRemove={(g) => setTags((ts) => ts.filter((t) => t !== g))} />
          <TagBox onAdd={(g) => setTags((ts) => (ts.includes(g) ? ts : [...ts, g]))} />
          <p className="text-xs text-[var(--ink-3)]" data-testid="bulk-tags-help">{BULK_TAGS_HELP}</p>
        </div>
      }
    />
  );
}
