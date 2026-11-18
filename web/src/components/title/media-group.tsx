// A file's media in plain text (docs/design/ui.md, Title page): a library row's Details and the episode window show
// it under "Media", one row per fact the file has, every value in full (no tooltip, no "+N"), so it reads on a phone.
import type { MediaInfo } from "@/api/manage";
import { KV } from "@/components/page";
import { mediaRows } from "@/lib/media";

export function MediaGroup({ m }: { m: MediaInfo | undefined | null }) {
  const rows = mediaRows(m);
  if (rows.length === 0) return null;
  return (
    <div className="space-y-1.5" data-testid="media-group">
      <div className="text-[11px] font-bold uppercase tracking-[0.05em] text-[var(--ink-3)]">Media</div>
      <KV
        items={rows.map(([label, lines]) => [
          label,
          lines.length > 1 ? (
            <span key={label} className="flex flex-col">
              {lines.map((l, i) => <span key={i}>{l}</span>)}
            </span>
          ) : (
            lines[0]
          ),
        ])}
      />
    </div>
  );
}
