// Activity's multi-select (Queue's Remove selected, Blocklist's Unblock selected): the selection state, the line above
// the table ("3 selected", the action, Clear), and the confirmation that runs the single-row route for each chosen
// row, one after another, and says what happened ("Removed 2 of 3. Not removed: Alpha (reason).").
import { useState, type ReactNode } from "react";
import { useRunEach } from "@/api/activity";
import { Button } from "@/components/ui/button";
import { ConfirmDialog, errText } from "@/components/ui/confirm-dialog";
import type { RowSelection } from "@/components/ui/row-select";
import type { Scope } from "@/lib/ops";

/** The chosen keys among the rows shown; anything chosen that is no longer shown is not acted on. */
export function useChosen<K extends string | number>(shown: readonly K[]): RowSelection<K> & { clear: () => void; count: number } {
  const [chosen, setChosen] = useState<ReadonlySet<K>>(new Set());
  const visible = new Set(shown.filter((k) => chosen.has(k)));
  return {
    chosen: visible,
    count: visible.size,
    toggle: (k, on) =>
      setChosen((s) => {
        const n = new Set(s);
        if (on) n.add(k);
        else n.delete(k);
        return n;
      }),
    all: (on) => setChosen(new Set(on ? shown : [])),
    clear: () => setChosen(new Set()),
  };
}

/** The line above the table while rows are chosen; on a phone it stays in view under the top bar. */
export function SelectionLine({ count, action, onAct, onClear }: { count: number; action: string; onAct: () => void; onClear: () => void }) {
  if (count === 0) return null;
  return (
    <div className="sticky top-[var(--top)] z-20 mb-2 sm:static flex flex-wrap items-center gap-2 rounded-[5px] border border-[var(--line)] bg-[var(--surface)] px-3 py-1.5 shadow-[var(--shadow-1)]" data-testid="selection-line">
      <strong className="text-sm">{count} selected</strong>
      <Button size="sm" variant="outline" onClick={onAct}>{action}</Button>
      <Button size="sm" variant="ghost" onClick={onClear}>Clear</Button>
    </div>
  );
}

/** The confirmation of a bulk action: the rows chosen when it opened, run one after another. */
export function BulkDialog<T extends { title: { id?: number; name: string } }>({
  items,
  scope,
  run,
  words,
  onDone,
  onClose,
  danger = true,
  testid,
  children,
}: {
  items: readonly T[];
  scope: (n: number) => Scope;
  run: (item: T) => Promise<unknown>;
  /** The outcome in words, from the count and the rows that failed with their reasons. */
  words: (n: number, failed: { name: string; reason: string }[]) => string;
  onDone: () => void;
  onClose: () => void;
  danger?: boolean;
  testid?: string;
  children?: ReactNode;
}) {
  const [batch] = useState(items);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<{ text: string; partial: boolean }>();
  const runEach = useRunEach();
  const go = async () => {
    setBusy(true);
    const failed = await runEach(batch, run);
    setBusy(false);
    setDone({ text: words(batch.length, failed.map((f) => ({ name: f.item.title.name, reason: errText(f.error) }))), partial: failed.length > 0 });
    onDone();
  };
  return (
    <ConfirmDialog scope={scope(batch.length)} busy={busy} done={done?.text} doneWarning={done?.partial} danger={danger} onConfirm={() => void go()} onClose={onClose} testid={testid}>
      {done ? null : children}
    </ConfirmDialog>
  );
}
