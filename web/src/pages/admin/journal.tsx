// System › Decision journal (admins; /admin/system/journal, an entry at /admin/system/journal/:id): the grab journal
// (GET /journal, /journal/{id}) with the flags stored when each entry's History row was written; Flagged lists the
// entries with a warn or error flag, each on its own. A line is one thing that happened (the server folds a download's
// import and link actions into its grab, an adoption's files into one); an entry shows what was decided, the full paths
// of the files its summary names, and what followed, each step linked to its own entry.
import { useEffect, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { useJournal, useJournalEntry, useStatus, type JournalEntry } from "@/api/hooks";
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
import { Select } from "@/components/ui/select";
import { toTop } from "@/components/ui/pager";
import { attemptCodeWords, attemptOutcomeWords } from "@/lib/router-words";
import { JOURNAL_KINDS, journalFlagWords, journalKindWords, journalLabel, journalPathsTip, journalSourceWords } from "@/lib/journal-words";

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
  return (
    <span className="flex flex-wrap gap-1">
      {(e.flags ?? []).map((f) => <Badge key={f.code} tone={sevTone(f.severity)} title={f.code}>{journalFlagWords(f.code)}</Badge>)}
    </span>
  );
}

/** An entry's kind as a badge (its label, else the kind in words), the code as its tooltip. */
function KindBadge({ kind, label, className }: { kind?: string; label?: string; className?: string }) {
  return <Badge tone="outline" className={className} title={kind}>{journalLabel({ kind, label })}</Badge>;
}

function TitleCell({ e }: { e: JournalEntry }) {
  return (
    <span className="flex items-center gap-3">
      <TitleThumb id={e.title?.id} />
      <span>
        <Link className="font-medium hover:text-primary" to={journalLink(e.id ?? "")}>{e.title?.name ? nameWithoutYear(e.title.name, e.title.year) : "—"}</Link>
        {e.title?.year ? <span className="text-muted-foreground"> ({e.title.year})</span> : null}
        <div className="max-w-[30rem] text-xs text-muted-foreground [overflow-wrap:anywhere]" title={journalPathsTip(e)} data-testid="journal-row-summary">{e.summary}</div>
        {/* Phones and tablets: the columns that hide (when, kind, library, flags) stay inline under the title. */}
        <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground md:hidden" data-testid="journal-inline">
          <span className="sm:hidden">{fmtTime(e.at)}</span>
          <KindBadge kind={e.kind} label={e.label} className="sm:hidden" />
          {e.version ? <VersionTag k={e.version} /> : null}
          <Flags e={e} />
        </div>
      </span>
    </span>
  );
}

/** System › Decision journal: the list under its header. */
export function JournalPage() {
  return (
    <div>
      <PageHeader crumb={[{ label: "System", to: "/admin" }]} title="Decision journal" />
      <JournalList />
    </div>
  );
}

/** The journal list. */
export function JournalList() {
  const [params, setParams] = useSearchParams();
  const suspect = params.get("suspect") === "1";
  const kind = params.get("kind") ?? "";
  const urlTitle = params.get("title") ?? "";
  const [title, setTitle] = useState(urlTitle);
  // The box follows the URL: a link or Back that changes ?title= (the tab link drops it) shows what is applied.
  useEffect(() => setTitle(urlTitle), [urlTitle]);
  // Cursor pagination: the cursors of the pages before this one, so Newer can go back. They belong to one filter: any
  // change of the URL's filters, by the controls or a link, starts at the newest page.
  const filterKey = `${suspect}|${kind}|${urlTitle}`;
  const [paging, setPaging] = useState<{ key: string; cursors: string[] }>({ key: filterKey, cursors: [] });
  // A new filter key drops the stored cursors, so going back to an earlier filter starts at its newest page too.
  if (paging.key !== filterKey) setPaging({ key: filterKey, cursors: [] });
  const cursors = paging.key === filterKey ? paging.cursors : [];
  // Newer and Older return to the top, as the shared pager does, so the new page is read from its first row.
  const setCursors = (c: string[]) => { setPaging({ key: filterKey, cursors: c }); toTop(); };
  const cursor = cursors[cursors.length - 1];
  const q = useJournal({ suspect, kind: kind || undefined, title: urlTitle || undefined, cursor });
  const set = (k: string, v?: string) => {
    const n = new URLSearchParams(params);
    if (v) n.set(k, v);
    else n.delete(k);
    setParams(n);
  };
  const items = q.data?.items ?? [];
  // An empty list under a filter or on a later page is not an empty journal.
  const filtered = !!kind || !!urlTitle || cursors.length > 0;
  return (
    <div>
      <p className="mb-4 text-sm text-muted-foreground">Every grab decision and what followed it, newest first: one line per thing that happened (a download's import and file changes are on its grab's line; open it for each step). Flagged shows the entries a check found suspect.</p>
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <div className="flex gap-1">
          <Button size="xs" variant={suspect ? "ghost" : "secondary"} onClick={() => set("suspect")}>All</Button>
          <Button size="xs" variant={suspect ? "secondary" : "ghost"} onClick={() => set("suspect", "1")} data-testid="journal-suspect">Flagged</Button>
        </div>
        <Select aria-label="Kind" value={kind} onChange={(e) => set("kind", e.target.value)} className="w-auto">
          <option value="">All kinds</option>
          {JOURNAL_KINDS.map((k) => <option key={k} value={k}>{journalKindWords(k)}</option>)}
        </Select>
        <form className="flex items-center gap-2" onSubmit={(ev) => { ev.preventDefault(); set("title", title.trim()); }}>
          <Input aria-label="Title" placeholder="Title or id" value={title} onChange={(e) => setTitle(e.target.value)} className="h-8 w-48" />
          <Button size="sm" variant="outline" type="submit" className="h-8">Filter</Button>
        </form>
      </div>
      {q.isPending ? <Loading /> : q.error ? <ErrorState error={q.error} /> : items.length === 0 && !q.data?.next_cursor ? (
        <Card><Empty>{suspect ? "Nothing flagged." : filtered ? "No entries." : "No entries yet. One appears each time a library judges a release, when it searches or grabs. A title added a moment ago may show nothing yet."}</Empty></Card>
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
                  <TableCell className="hidden sm:table-cell"><KindBadge kind={e.kind} label={e.label} /><div className="mt-1 text-xs text-muted-foreground" title={e.source}>{journalSourceWords(e.source)}</div></TableCell>
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
  const crumb = [{ label: "System", to: "/admin" }, { label: "Decision journal", to: journalHome() }];
  if (q.isPending) return <Loading />;
  if (q.error) return (<div><PageHeader crumb={crumb} title={id} docTitle="Decision journal" /><ErrorState error={q.error} /></div>);
  const e = q.data;
  const r = e.reasoning;
  return (
    <div>
      <PageHeader
        crumb={crumb}
        docTitle="Decision journal"
        title={<span className="flex items-center gap-4"><TitleThumb id={e.title?.id} className="w-12" /><span>{e.title?.name ? nameWithoutYear(e.title.name, e.title.year) : id}{e.title?.year ? <span className="text-muted-foreground"> ({e.title.year})</span> : null}</span></span>}
        description={<span className="flex flex-wrap items-center gap-2"><KindBadge kind={e.kind} label={e.label} />{e.version ? <VersionTag k={e.version} /> : null}<span title={e.source}>{journalSourceWords(e.source)}</span><span>{fmtTime(e.at)}</span><PolicyRev e={e} current={status.data?.policy_revision} /></span>}
        actions={e.title?.id ? <Button asChild variant="outline" size="sm"><a href={`/title/${e.title.id}`}>Title page</a></Button> : undefined}
      />
      {e.summary ? (
        <Card className="text-sm leading-relaxed [overflow-wrap:anywhere]" data-testid="journal-summary">
          {e.summary}
          {(e.paths ?? []).length > 0 ? (
            <ul className="mt-2 space-y-0.5 text-xs text-muted-foreground" data-testid="journal-paths">
              {(e.paths ?? []).map((p) => <li key={p}><Mono className="break-all">{p}</Mono></li>)}
            </ul>
          ) : null}
        </Card>
      ) : null}
      {e.id?.startsWith("run:") ? (e.related ?? []).filter(id => id.startsWith("dl:")).map(id => <p key={id} className="mt-2 text-sm"><Link className="text-primary hover:underline" to={journalLink(id)}>View download</Link></p>) : null}

      {(e.flags ?? []).length > 0 ? (
        <Section title="Flags">
          <Card>
            <ul className="space-y-2 text-sm">
              {(e.flags ?? []).map((f) => (
                <li key={f.code} className="flex flex-wrap items-baseline gap-2">
                  <Badge tone={sevTone(f.severity)} title={f.code}>{journalFlagWords(f.code)}</Badge>
                  <span>{f.reason}</span>
                </li>
              ))}
            </ul>
          </Card>
        </Section>
      ) : null}

      {e.release ? (
        <Section title="Release">
          <Card>
            <KV items={[
              ["Release", <span key="r" className="break-words">{e.release.title || "—"}</span>],
              ["Quality", e.release.quality || "—"],
              ["Score", e.release.score !== undefined && e.release.score !== null ? fmtNumber(e.release.score) : "—"],
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
                    <TableCell>
                      <Badge tone={a.outcome === "accepted" ? "positive" : "warning"} title={[a.outcome, a.code].filter(Boolean).join(" · ")}>{a.code === "reused" ? "Reused" : attemptOutcomeWords(a.outcome)}</Badge>
                      {/* On a phone the reason is a second line under the outcome; wider screens give it its own column. */}
                      <div className="mt-1 break-words text-[12.5px] text-muted-foreground sm:hidden" data-testid="attempt-reason-phone">{a.message || attemptCodeWords(a.code)}</div>
                    </TableCell>
                    <TableCell className="hidden break-words text-[13px] text-muted-foreground sm:table-cell">{a.message || attemptCodeWords(a.code)}</TableCell>
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
                  <KindBadge kind={a.kind} />
                  {a.ref ? <Link className="min-w-0 [overflow-wrap:anywhere] hover:text-primary" to={journalLink(a.ref)}>{a.text}</Link> : <span className="min-w-0 [overflow-wrap:anywhere]">{a.text}</span>}
                </li>
              ))}
            </ul>
          </Card>
        </Section>
      ) : null}

    </div>
  );
}
