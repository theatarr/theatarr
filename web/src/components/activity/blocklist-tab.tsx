// Activity › Blocklist (pages/activity.tsx): the releases a version never routes again (accounts that see files).
import { CircleSlash } from "lucide-react";
import { useActivityBlocked } from "@/api/activity";
import { UnblockAction } from "@/components/activity/download-actions";
import { Poster } from "@/components/activity/row-bits";
import { Empty, ErrorState, Loading, VersionTag } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { fmtTime } from "@/lib/format";

export function BlocklistTab() {
  const q = useActivityBlocked(true);
  if (q.isPending) return <Loading />;
  if (q.error) return <ErrorState error={q.error} />;
  const items = q.data.blocked ?? [];
  if (items.length === 0) {
    return (
      <Card>
        <Empty icon={CircleSlash}>
          <span>No release is blocked.</span>
          <span className="text-xs">A release lands here when it fails the library check, stalls, holds dangerous files, is removed with blocklist, or is judged wrong in the journal.</span>
        </Empty>
      </Card>
    );
  }
  return (
    <Card className="p-0">
      <ul className="divide-y divide-border">
        {items.map((b) => (
          <li key={b.version + b.info_hash} className="flex gap-3 px-3 py-3 sm:px-4" data-testid="blocked-row">
            <Poster src={b.title.poster} className="w-9" />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-baseline justify-between gap-x-3">
                <span className="text-sm font-medium">
                  {b.title.name}
                  {b.title.year ? ` (${b.title.year})` : ""}
                </span>
                <span className="text-xs text-muted-foreground">{fmtTime(b.created_at)}</span>
              </div>
              <div className="mt-1 flex flex-wrap items-center gap-2">
                <VersionTag k={b.version} className="text-[11px]" />
                <Badge tone="warning">{b.words}</Badge>
                {b.researched_at ? <Badge tone="neutral">searched again</Badge> : null}
              </div>
              <p className="mt-1 break-all font-mono text-[12px] text-muted-foreground">{b.release}</p>
              {b.reason ? <p className="mt-0.5 break-words text-xs text-muted-foreground">{b.reason}</p> : null}
              <UnblockAction b={b} />
            </div>
          </li>
        ))}
      </ul>
    </Card>
  );
}
