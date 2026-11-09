// The Requests page's "Upgrades waiting" tab (docs/design/ui.md, docs/design/manage.md "Upgrades waiting"): every held
// upgrade for managers, waiting ones first, with the decided ones (who, how, when) one filter away.
import { CheckCircle2 } from "lucide-react";
import { useState } from "react";
import { useUpgradeHolds } from "@/api/upgrades";
import { Empty, ErrorState, Loading } from "@/components/page";
import { UpgradeHoldBlock } from "@/components/upgrades/upgrade-hold";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";

const FILTERS = [
  { id: "pending", label: "Waiting" },
  { id: "approved,declined,expired,replaced", label: "Decided" },
];

export function UpgradesWaiting() {
  const [state, setState] = useState(FILTERS[0].id);
  const q = useUpgradeHolds({ state, limit: 200 });
  const items = q.data?.items ?? [];
  const counts = q.data?.counts ?? {};
  return (
    <div data-testid="upgrades-waiting">
      <p className="mb-3 text-sm text-[var(--ink-3)]">
        Libraries set to ask first keep their file until you approve a better release. Approving downloads it and swaps the
        file; declining keeps the release from being offered again.
      </p>
      <div className="mb-4 flex flex-wrap gap-1.5" role="group" aria-label="Upgrade state">
        {FILTERS.map((f) => (
          <button
            key={f.id}
            type="button"
            aria-pressed={state === f.id}
            onClick={() => setState(f.id)}
            className={cn(
              "rounded-full border border-[var(--line)] px-3 py-1 text-[13px] font-medium text-[var(--ink-3)] transition hover:border-[var(--line-2)]",
              state === f.id && "border-[var(--th-baccent)] bg-[color-mix(in_srgb,var(--accent)_14%,transparent)] text-[var(--accent-ink)]",
            )}
          >
            {f.label}
            {q.data ? <span className="ml-1.5 tabular-nums opacity-70">{f.id.split(",").reduce((a, s) => a + (counts[s] ?? 0), 0)}</span> : null}
          </button>
        ))}
      </div>
      {q.isPending ? (
        <Loading />
      ) : q.error ? (
        <ErrorState error={q.error} />
      ) : items.length === 0 ? (
        <Card>
          <Empty icon={CheckCircle2}>{state === "pending" ? "No upgrade waits for approval." : "No decided upgrades yet."}</Empty>
        </Card>
      ) : (
        <ul className="grid grid-cols-1 gap-3 lg:grid-cols-2" data-testid="upgrade-hold-list">
          {items.map((h) => (
            <li key={h.id} className="min-w-0">
              <Card className="p-0">
                <UpgradeHoldBlock h={h} showTitle className="border-0" />
              </Card>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
