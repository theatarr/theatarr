// Activity › Decision journal (admins; the list is a tab of Activity, pages/activity.tsx): the grab journal (GET /journal, /journal/{id}) with ok/wrong verdicts (POST /journal/{id}/verdict).
import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { useJournal, useJournalEntry, useJournalVerdict, useStatus, type JournalEntry } from "@/api/hooks";
import { TitleThumb } from "@/components/poster-card";
import { Empty, ErrorState, KV, Loading, Mono, PageHeader, Section, VersionTag } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtBytes, fmtNumber, fmtTime, nameWithoutYear, plural } from "@/lib/format";
import { journalHome, journalLink } from "@/lib/admin-links";
import { ProviderTag } from "@/components/provider-pills";
import { InfoHash } from "@/components/title/info-hash";

const KINDS = ["grab", "upgrade", "plan", "search", "research", "failure", "stall", "blocklist", "import", "link", "verdict"];

type Tone = "neutral" | "positive" | "warning" | "negative" | "info";
const sevTone = (s?: string): Tone => (s === "error" ? "negative" : s === "warn" ? "warning" : s === "info" ? "info" : "neutral");

/** The policy revision that made the decision, marked against the running one (GET /status policy_revision). */
function PolicyRev({ e, current }: { e: JournalEntry; current?: string }) {
  if (!e.policy_revision) return null;
  const now = current !== undefined && e.policy_revision === current;
  return (
    <span className="flex items-center gap-1" data-testid="journal-policy" title="Revision of the policy files that made this decision">
      <span>policy</span>
      <Mono>{e.policy_revision}</Mono>
      {current !== undefined ? <span className="text-muted-foreground">{now ? "(current)" : "(earlier policy)"}</span> : null}
      {e.queued_policy_revision ? <span className="text-muted-foreground">· queued under <Mono>{e.queued_policy_revision}</Mono></span> : null}
    </span>
  );
}

function Flags({ e }: { e: JournalEntry }) {
  const open = (e.flags ?? []).filter((f) => !f.acked);
  return (
    <span className="flex flex-wrap gap-1">
      {open.map((f) => <Badge key={f.code} tone={sevTone(f.severity)}>{f.code}</Badge>)}
      {e.verdict ? <Badge tone={e.verdict.verdict === "ok" ? "positive" : "negative"}>{e.verdict.verdict}</Badge> : null}
    </span>
  );
}

function TitleCell({ e }: { e: JournalEntry }) {
  return (
    <span className="flex items-center gap-3">
      <TitleThumb id={e.title?.id} />
      <span>
        <Link className="font-medium hover:text-primary" to={journalLink(e.id ?? "")}>{e.title?.name ? nameWithoutYear(e.title.name, e.title.year) : "—"}</Link>
        {e.title?.year ? <span className="text-muted-foreground"> ({e.title.year})</span> : null}
        <div className="max-w-[30rem] text-xs text-muted-foreground [overflow-wrap:anywhere]">{e.summary}</div>
        {/* Phones and tablets: the columns that hide (when, kind, library, flags) stay inline under the title. */}
        <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground md:hidden" data-testid="journal-inline">
          <span className="sm:hidden">{fmtTime(e.at)}</span>
          <Badge tone="outline" className="sm:hidden">{e.kind}</Badge>
          {e.version ? <VersionTag k={e.version} /> : null}
          <Flags e={e} />
        </div>
      </span>
    </span>
  );
}

/** The journal list: the body of Activity › Decision journal, under Activity's header and tabs. */
export function JournalList() {
  const [params, setParams] = useSearchParams();
  const suspect = params.get("suspect") === "1";
  const kind = params.get("kind") ?? "";
  const [title, setTitle] = useState(params.get("title") ?? "");
  // Cursor pagination: the cursors of the pages before this one, so Newer can go back.
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors[cursors.length - 1];
  const q = useJournal({ suspect, kind: kind || undefined, title: params.get("title") || undefined, cursor });
  const set = (k: string, v?: string) => {
    const n = new URLSearchParams(params);
    if (v) n.set(k, v);
    else n.delete(k);
    setParams(n);
    setCursors([]);
  };
  const items = q.data?.items ?? [];
  // An empty list under a filter or on a later page is not an empty journal.
  const filtered = !!kind || !!params.get("title") || cursors.length > 0;
  return (
    <div>
      <p className="mb-4 text-sm text-muted-foreground">Every grab decision and what followed it, newest first. Judge suspect entries ok or wrong.</p>
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <div className="flex gap-1">
          <Button size="xs" variant={suspect ? "ghost" : "secondary"} onClick={() => set("suspect")}>All</Button>
          <Button size="xs" variant={suspect ? "secondary" : "ghost"} onClick={() => set("suspect", "1")} data-testid="journal-suspect">Suspect</Button>
        </div>
        <select
          aria-label="Kind"
          value={kind}
          onChange={(e) => set("kind", e.target.value)}
          className="h-8 rounded-md border border-input bg-field px-2 text-sm"
        >
          <option value="">all kinds</option>
          {KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
        </select>
        <form className="flex gap-2" onSubmit={(ev) => { ev.preventDefault(); set("title", title.trim()); }}>
          <Input aria-label="Title" placeholder="Title or id" value={title} onChange={(e) => setTitle(e.target.value)} className="h-8 w-48" />
          <Button size="xs" variant="outline" type="submit">Filter</Button>
        </form>
      </div>
      {q.isPending ? <Loading /> : q.error ? <ErrorState error={q.error} /> : items.length === 0 && !q.data?.next_cursor ? (
        <Card><Empty>{suspect ? "Nothing suspect." : filtered ? "No entries." : "No entries yet. One appears each time a Live library judges a release, when it searches or grabs.. A title added a moment ago may show nothing yet."}</Empty></Card>
      ) : (
        <Card className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="hidden sm:table-cell">When</TableHead>
                <TableHead>Title</TableHead>
                <TableHead className="hidden sm:table-cell">Kind</TableHead>
                <TableHead className="hidden md:table-cell">Library</TableHead>
                <TableHead className="hidden md:table-cell">Flags</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.length === 0 ? (
                <TableRow><TableCell colSpan={5} className="text-muted-foreground">No matches on this page; try Older.</TableCell></TableRow>
              ) : items.map((e) => (
                <TableRow key={e.id} data-testid="journal-row">
                  <TableCell className="hidden whitespace-nowrap text-[13px] sm:table-cell">{fmtTime(e.at)}</TableCell>
                  <TableCell><TitleCell e={e} /></TableCell>
                  <TableCell className="hidden sm:table-cell"><Badge tone="outline">{e.kind}</Badge><div className="mt-1 text-xs text-muted-foreground"><Mono>{e.source}</Mono></div></TableCell>
                  <TableCell className="hidden md:table-cell">{e.version ? <VersionTag k={e.version} /> : null}</TableCell>
                  <TableCell className="hidden md:table-cell"><Flags e={e} /></TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
      <div className="mt-3 flex gap-2">
        <Button size="sm" variant="outline" disabled={cursors.length === 0} onClick={() => setCursors(cursors.slice(0, -1))}>Newer</Button>
        <Button size="sm" variant="outline" disabled={!q.data?.next_cursor} onClick={() => setCursors([...cursors, q.data?.next_cursor ?? ""])}>Older</Button>
      </div>
    </div>
  );
}

export function JournalEntryPage() {
  const { id = "" } = useParams();
  const q = useJournalEntry(id);
  const status = useStatus();
  const verdict = useJournalVerdict();
  const [reason, setReason] = useState("");
  const crumb = [{ label: "Activity", to: "/activity" }, { label: "Decision journal", to: journalHome() }];
  if (q.isPending) return <Loading />;
  if (q.error) return (<div><PageHeader crumb={crumb} title={id} docTitle="Decision journal" /><ErrorState error={q.error} /></div>);
  const e = q.data;
  const r = e.reasoning;
  const judge = (v: "ok" | "wrong") => verdict.mutate({ id: e.id ?? id, verdict: v, reason: reason.trim() || undefined });
  return (
    <div>
      <PageHeader
        crumb={crumb}
        docTitle="Decision journal"
        title={<span className="flex items-center gap-4"><TitleThumb id={e.title?.id} className="w-12" /><span>{e.title?.name ? nameWithoutYear(e.title.name, e.title.year) : id}{e.title?.year ? <span className="text-muted-foreground"> ({e.title.year})</span> : null}</span></span>}
        description={<span className="flex flex-wrap items-center gap-2"><Badge tone="outline">{e.kind}</Badge>{e.version ? <VersionTag k={e.version} /> : null}<Mono>{e.source}</Mono><span>{fmtTime(e.at)}</span><PolicyRev e={e} current={status.data?.policy_revision} /></span>}
        actions={e.title?.id ? <Button asChild variant="outline" size="sm"><a href={`/title/${e.title.id}`}>Title page</a></Button> : undefined}
      />
      {e.summary ? <Card className="text-sm leading-relaxed [overflow-wrap:anywhere]" data-testid="journal-summary">{e.summary}</Card> : null}

      <Section title="Verdict" aside={e.verdict ? `${e.verdict.verdict} by ${e.verdict.by ?? "?"} · ${fmtTime(e.verdict.at)}` : undefined}>
        <Card>
          {(e.flags ?? []).length > 0 ? (
            <ul className="mb-4 space-y-2 text-sm">
              {(e.flags ?? []).map((f) => (
                <li key={f.code} className="flex flex-wrap items-baseline gap-2">
                  <Badge tone={f.acked ? "neutral" : sevTone(f.severity)}>{f.code}</Badge>
                  <span className={f.acked ? "text-muted-foreground line-through" : ""}>{f.reason}</span>
                </li>
              ))}
            </ul>
          ) : <p className="mb-4 text-sm text-muted-foreground">No flags.</p>}
          {e.verdict?.reason ? <p className="mb-3 text-sm">“{e.verdict.reason}”{e.verdict.action_note ? <span className="text-muted-foreground"> · {e.verdict.action_note}</span> : null}</p> : null}
          <div className="flex flex-wrap items-center gap-2">
            <Input aria-label="Reason" placeholder="Reason (optional)" value={reason} onChange={(ev) => setReason(ev.target.value)} className="h-8 max-w-sm" />
            <Button size="sm" variant="outline" disabled={verdict.isPending} onClick={() => judge("ok")} data-testid="verdict-ok">Ok</Button>
            <Button size="sm" variant="destructive" disabled={verdict.isPending} onClick={() => judge("wrong")} data-testid="verdict-wrong">Wrong</Button>
          </div>
          <p className="mt-2 text-xs text-muted-foreground">{e.id?.startsWith("run:") ? "Records your verdict only. To block the release, use its download in Activity." : "Both acknowledge the flags. Wrong on a Live library with the router on also blocklists the release and queues a re-search."}</p>
          {e.id?.startsWith("run:") ? (e.related ?? []).filter(id => id.startsWith("dl:")).map(id => <Link key={id} className="text-sm text-primary hover:underline" to={journalLink(id)}>View download</Link>) : null}
          {verdict.error ? <div className="mt-2"><ErrorState error={verdict.error} /></div> : null}
        </Card>
      </Section>

      {e.release ? (
        <Section title="Release">
          <Card>
            <KV items={[
              ["Release", <span key="r" className="break-words">{e.release.title || "—"}</span>],
              ["Quality", e.release.quality || "—"],
              ["Score", e.release.score ?? "—"],
              ["Size", fmtBytes(e.release.size)],
              ["Indexer", <Mono key="i">{e.release.indexer || "—"}</Mono>],
              ["Hash", e.release.info_hash ? <InfoHash key="h" hash={e.release.info_hash} admin /> : "—"],
            ]} />
          </Card>
        </Section>
      ) : null}

      {r ? (
        <Section title="Reasoning" aside={r.results !== undefined ? `${plural(r.results, "result")} · ${fmtNumber(r.accepted ?? 0)} accepted` : undefined}>
          <Card className="space-y-2 text-sm">
            {r.basis ? <div><span className="text-muted-foreground">Basis </span>{r.basis}</div> : null}
            {r.chosen ? <div><span className="text-muted-foreground">Chosen </span><span className="break-words">{r.chosen.release}</span> · {r.chosen.quality} · score {fmtNumber(r.chosen.score)}</div> : null}
            {(r.runners_up ?? []).slice(0, 5).map((c, i) => (
              <div key={i} className="break-words text-muted-foreground">#{c.rank || i + 2} {c.release} · score {fmtNumber(c.score)}{c.accepted ? "" : " · rejected"}</div>
            ))}
            {r.detail || r.notes ? <div>{r.detail || r.notes}</div> : null}
            {r.missing ? <div className="text-muted-foreground">Not recorded: {r.missing}</div> : null}
          </Card>
        </Section>
      ) : null}

      {(e.attempts ?? []).length > 0 ? (
        <Section title="Provider attempts">
          <Card className="p-0">
            <Table>
              <TableBody>
                {(e.attempts ?? []).map((a, i) => (
                  <TableRow key={i}>
                    <TableCell className="whitespace-nowrap text-[13px]">{fmtTime(a.at)}</TableCell>
                    <TableCell>{a.provider ? <ProviderTag account={a.provider} className="font-medium" /> : null}</TableCell>
                    <TableCell><Badge tone={a.outcome === "accepted" ? "positive" : "warning"}>{a.outcome}</Badge></TableCell>
                    <TableCell className="hidden break-words text-[13px] text-muted-foreground sm:table-cell">{a.message || a.code}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
        </Section>
      ) : null}

      {(e.after ?? []).length > 0 ? (
        <Section title="What followed">
          <Card className="p-0">
            <ul className="divide-y divide-border text-sm">
              {(e.after ?? []).map((a, i) => (
                <li key={i} className="flex flex-wrap items-baseline gap-2 px-4 py-2">
                  <span className="whitespace-nowrap text-[13px] text-muted-foreground">{fmtTime(a.at)}</span>
                  <Badge tone="outline">{a.kind}</Badge>
                  {a.ref ? <Link className="hover:text-primary" to={journalLink(a.ref)}>{a.text}</Link> : <span>{a.text}</span>}
                </li>
              ))}
            </ul>
          </Card>
        </Section>
      ) : null}
    </div>
  );
}
