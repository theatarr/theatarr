// A torrent's or hash's usage as the Torrents page works it out (docs/design/torrents.md "Used, one answer"): the
// badge, the titles its references belong to and the references themselves (the first five, then "and N more").
// Used by Admin › Torrents and Saved inventory.
import { Link } from "react-router";
import { Badge } from "@/components/ui/badge";
import { fmtNumber } from "@/lib/format";
import { REF_LABEL, refSummary, usageTone, usageWord } from "@/lib/torrents";

type Ref = { kind: string; detail: string; title_id?: number; title?: string };
type Usage = { usage?: string; reasons?: string[]; refs?: Record<string, number>; ref_list?: Ref[]; titles?: { id: number; name: string }[] };

/** References listed before "and N more". */
export const REFS_SHOWN = 5;

export function UsageBadge({ u }: { u: Usage }) {
  return <Badge tone={usageTone(u.usage)}>{usageWord(u.usage, u.reasons)}</Badge>;
}

/** The titles a used torrent belongs to, as links to their title pages; max cuts the list ("and 2 more"). */
export function TitleLinks({ titles, max, testid, className }: { titles: readonly { id: number; name: string }[]; max?: number; testid?: string; className?: string }) {
  const shown = max ? titles.slice(0, max) : titles;
  return (
    <span className={className} data-testid={testid}>
      {shown.map((x, i) => (
        <span key={x.id}>{i > 0 ? ", " : ""}<Link to={`/title/${x.id}`} className="text-primary hover:underline">{x.name}</Link></span>
      ))}
      {titles.length > shown.length ? ` and ${titles.length - shown.length} more` : null}
    </span>
  );
}

/** A used torrent's or hash's references: the count, the titles they belong to (all of them, not only the listed
 *  references) as links, and the first five listed references, each with its title, then "and N more" counted from
 *  all references. Anything else: its reasons (unknown, not worked out) or that nothing references it. */
export function UsageDetail({ u }: { u: Usage }) {
  if (u.usage !== "used") {
    const reasons = u.reasons ?? [];
    return reasons.length > 0 ? (
      <ul className="space-y-0.5" data-testid="usage-reasons">{reasons.map((r, i) => <li key={i}>{r}</li>)}</ul>
    ) : <div>No file of it is referenced.</div>;
  }
  const titles = u.titles ?? [];
  const list = u.ref_list ?? [];
  const total = Object.values(u.refs ?? {}).reduce((n, x) => n + x, 0);
  const shown = list.slice(0, REFS_SHOWN);
  const more = Math.max(total, list.length) - shown.length;
  return (
    <div className="space-y-1">
      <div>{refSummary(u.refs)}</div>
      {titles.length > 0 ? (
        <div data-testid="torrent-titles">
          {titles.length === 1 ? "Title: " : "Titles: "}
          <TitleLinks titles={titles} />
        </div>
      ) : null}
      <ul className="space-y-0.5" data-testid="usage-refs">
        {shown.map((x, i) => (
          <li key={i} className="[overflow-wrap:anywhere]">
            {REF_LABEL[x.kind] ?? x.kind}: {x.detail}
            {x.title_id ? <> · <Link to={`/title/${x.title_id}`} className="text-primary hover:underline">{x.title || `title ${x.title_id}`}</Link></> : null}
          </li>
        ))}
      </ul>
      {more > 0 ? <div data-testid="usage-refs-more">and {fmtNumber(more)} more</div> : null}
    </div>
  );
}
