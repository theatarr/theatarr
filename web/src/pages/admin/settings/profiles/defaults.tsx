// Where the TRaSH presets come from, said in the New profile picker: the TRaSH-Guides checkout in THEATARR_TRASH when
// one loads, otherwise the snapshot bundled with this version.
import { Link } from "react-router";
import { Mono } from "@/components/page";

/** Where the defaults come from, in words. */
export function TrashSourceLine({ source, commit, date }: { source?: string; commit?: string; date?: string }) {
  const at = commit ? <> (commit <Mono>{commit.slice(0, 7)}</Mono>{date ? `, ${date}` : ""})</> : null;
  if (source === "bundled") return <p className="text-[13px] text-muted-foreground" data-testid="trash-source">TRaSH defaults bundled with this version{at}. This list changes with a new version of Theatarr, or follows a TRaSH-Guides checkout when one is mounted (<Mono>THEATARR_TRASH</Mono>). <Link className="underline" to="/admin/settings/quality-sync">Quality sync</Link> updates the profiles bound to a source, not this list.</p>;
  if (source === "checkout") return <p className="text-[13px] text-muted-foreground" data-testid="trash-source">TRaSH defaults from the TRaSH-Guides checkout in <Mono>THEATARR_TRASH</Mono>{at}.</p>;
  return null;
}
