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

/** The title page dialog's help line. */
export const TAGS_HELP = "Changes apply to every library that has this title. Connected apps see these tags, and a webhook limited to a tag gets this title's future events.";

/** What is in the box: the label it would add (trimmed, lower-case; empty when nothing is typed) and whether that
 *  breaks the rule. A dialog counts a good one as added on Save and refuses to confirm while it is bad. */
export function draftOf(text: string): { tag: string; bad: boolean } {
  const tag = tagOf(text);
  return { tag, bad: tag !== "" && !TAG_RULE.test(tag) };
}

/** The box that adds a label: Enter or Add puts what is typed (trimmed, lower-case) through onAdd; a label outside
 *  the rule is refused with the rule's words as it is typed. The dialog holds the text (draftOf). */
export function TagBox({ text, onText, onAdd, label = "Add a tag" }: { text: string; onText: (t: string) => void; onAdd: (tag: string) => void; label?: string }) {
  const list = useId();
  const labels = useTagLabels(true);
  const { tag, bad } = draftOf(text);
  const add = () => {
    if (tag === "" || bad) return;
    onAdd(tag);
    onText("");
  };
  return (
    <div>
      <div className="flex gap-2">
        <Input aria-label={label} placeholder={label} value={text} list={list} autoComplete="off" aria-invalid={bad || undefined}
          onChange={(e) => onText(e.target.value)}
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

/** The edit, by the stored (lower-case) label: what was added (typed, also a label already a chip) and which of the
 *  title's own labels were taken off. */
export type TagEdit = { add: string[]; remove: string[] };

export const addTag = (e: TagEdit, tag: string): TagEdit => ({ add: e.add.includes(tag) ? e.add : [...e.add, tag], remove: e.remove.filter((g) => g !== tag) });
/** × on a chip: cancels a pending add; on one of the title's own labels also records the removal. */
export const removeTag = (e: TagEdit, tag: string, own: boolean): TagEdit => ({ add: e.add.filter((g) => g !== tag), remove: own && !e.remove.includes(tag) ? [...e.remove, tag] : e.remove });

/** The title page's dialog. Labels are compared lower-case (a connected app may have stored "Kids"); a chip shows the
 *  title's own spelling. */
export function TitleTagsDialog({ titleId, name, tags, onClose }: { titleId: number; name: string; tags: string[]; onClose: () => void }) {
  const [edit, setEdit] = useState<TagEdit>({ add: [], remove: [] });
  const [text, setText] = useState("");
  const draft = draftOf(text);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const saved = useTagsSaved();
  const own = new Set(tags.map(tagOf));
  // The chips: the title's labels less the ones taken off, plus the new ones, in label order.
  const chips = [...tags.filter((g) => !edit.remove.includes(tagOf(g))), ...edit.add.filter((g) => !own.has(g))].sort((a, b) => tagOf(a).localeCompare(tagOf(b)));
  const save = async () => {
    setBusy(true);
    setError(undefined);
    try {
      // A good label left in the box counts as added.
      const all = draft.tag && !draft.bad ? addTag(edit, draft.tag) : edit;
      await editTags([titleId], all.add, all.remove);
      saved();
      onClose();
    } catch (e) {
      setError(e);
    }
    setBusy(false);
  };
  const changed = edit.add.length > 0 || edit.remove.length > 0 || draft.tag !== "";
  return (
    <ConfirmDialog
      scope={{ title: "Edit tags", does: [], keeps: [], confirm: "Save" }}
      subject={name}
      danger={false}
      busy={busy}
      disabled={!changed || draft.bad}
      error={error}
      onClose={onClose}
      onConfirm={() => void save()}
      testid="title-tags-dialog"
      top={
        <div className="space-y-3">
          <TagChips tags={chips} empty="No tags" onRemove={(g) => setEdit((e) => removeTag(e, tagOf(g), own.has(tagOf(g))))} />
          <TagBox text={text} onText={setText} onAdd={(g) => setEdit((e) => addTag(e, g))} />
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
  const [chosen, setTags] = useState<string[]>([]);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const saved = useTagsSaved();
  const add = mode === "add";
  // A good label left in the box counts as chosen.
  const draft = draftOf(text);
  const tags = draft.tag && !draft.bad && !chosen.includes(draft.tag) ? [...chosen, draft.tag] : chosen;
  const save = async () => {
    setBusy(true);
    setError(undefined);
    try {
      const r = await editTags(titleIds, add ? tags : [], add ? [] : tags);
      saved();
      // The count is of titles changed, not per label: with several labels only the total is true.
      const n = plural(r.changed, "title");
      toast({ tone: "ok", title: tags.length > 1 ? `Updated tags on ${n}` : `${add ? "Added" : "Removed"} ${tags[0]} ${add ? "to" : "from"} ${n}` });
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
      disabled={tags.length === 0 || draft.bad}
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
          <TagChips tags={chosen} empty="No tags chosen" onRemove={(g) => setTags((ts) => ts.filter((t) => t !== g))} />
          <TagBox text={text} onText={setText} onAdd={(g) => setTags((ts) => (ts.includes(g) ? ts : [...ts, g]))} />
          <p className="text-xs text-[var(--ink-3)]" data-testid="bulk-tags-help">{BULK_TAGS_HELP}</p>
        </div>
      }
    />
  );
}
