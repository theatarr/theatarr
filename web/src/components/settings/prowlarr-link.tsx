// Link Prowlarr, a tab of the add-indexer sheet (docs/design/settings.md, "Adding an indexer"): Prowlarr's address and
// API key are entered once (cardigann.prowlarr, the key stored write-only like other keys); Theatarr lists Prowlarr's
// indexers and adds the ticked ones as Torznab feeds, on and in the ticked libraries, in one save. The trackers, their
// logins and their VPN stay in Prowlarr. ProwlarrConnection is shared with Move from Prowlarr (once).
import { Loader2 } from "lucide-react";
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { prowlarrFeedsKey, useProwlarrFeeds, type ProwlarrFeed } from "@/api/prowlarr";
import { qk } from "@/api/query-keys";
import { settingsOverview, useSettingsWrite } from "@/api/settings";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { explainError, type Explained } from "@/lib/settings";
import { IndexerLibraries, librariesReady, type LibraryList } from "./indexer-libraries";
import { Announcer, ExplainBox, Field, useAnnouncement } from "./parts";
import { useSectionPatch } from "./use-section-patch";

/** Prowlarr's address and key. Not configured: the fields and Connect. Configured: where it is, and Change. */
export function ProwlarrConnection({ configured, url }: { configured: boolean; url?: string }) {
  const [editing, setEditing] = useState(false);
  const [address, setAddress] = useState(url ?? "");
  const [key, setKey] = useState("");
  const [keyError, setKeyError] = useState("");
  const qc = useQueryClient();
  const patch = useSectionPatch("cardigann", () => {
    setEditing(false);
    setKey("");
    void qc.invalidateQueries({ queryKey: prowlarrFeedsKey });
  });
  if (configured && !editing) {
    return (
      <p className="flex flex-wrap items-center gap-2 text-sm" data-testid="prowlarr-connected">
        {url ? <span>Prowlarr at <code>{url}</code>.</span> : null}
        <Button type="button" size="sm" variant="outline" onClick={() => { setAddress(url ?? ""); setEditing(true); }} data-testid="prowlarr-change">Change</Button>
      </p>
    );
  }
  const connect = () => {
    const next = address.trim();
    // The stored key never goes to a new address (the server refuses it too).
    const same = (a: string) => a.trim().replace(/\/+$/, "").toLowerCase();
    if (configured && url && !key && same(next) !== same(url)) {
      setKeyError("Enter the API key again for the new address");
      return;
    }
    setKeyError("");
    void patch.save(
      (config) => {
        const prowlarr = config.prowlarr && typeof config.prowlarr === "object" ? (config.prowlarr as Record<string, unknown>) : {};
        config.prowlarr = { ...prowlarr, url: next };
      },
      key ? { "cardigann.prowlarr.api_key": key } : undefined,
    );
  };
  return (
    <form className="grid gap-3 sm:grid-cols-2" data-testid="prowlarr-connection" onSubmit={(e) => { e.preventDefault(); connect(); }}>
      <Field label="Prowlarr address">
        <Input value={address} placeholder="http://prowlarr:9696" onChange={(e) => setAddress(e.target.value)} disabled={patch.busy} data-testid="prowlarr-url" />
      </Field>
      <Field label="Prowlarr API key" error={keyError || undefined} help="In Prowlarr: Settings › General › API Key. Stored like Theatarr's other keys; Theatarr only reads Prowlarr's indexer list and changes nothing there.">
        <Input type="password" autoComplete="new-password" ignorePasswordManagers value={key} placeholder={configured ? "stored: leave empty to keep it for the same address" : undefined}
          onChange={(e) => { setKey(e.target.value); setKeyError(""); }} disabled={patch.busy} data-testid="prowlarr-key" />
      </Field>
      <p className="text-[12.5px] text-muted-foreground sm:col-span-2">Prowlarr must be on your local network.</p>
      <div className="flex gap-2 sm:col-span-2">
        <Button type="submit" size="sm" disabled={patch.busy || !address.trim() || (!configured && !key)} data-testid="prowlarr-connect">
          {patch.busy ? <Loader2 className="animate-spin" /> : null} Connect
        </Button>
        {configured ? <Button type="button" size="sm" variant="outline" onClick={() => setEditing(false)}>Cancel</Button> : null}
      </div>
      <div className="sm:col-span-2">{patch.feedback}</div>
    </form>
  );
}

const HERE: Record<ProwlarrFeed["status"], (f: ProwlarrFeed) => string> = {
  linkable: (f) => `added as ${f.key}`,
  linked: (f) => `linked as ${f.linked}`,
  disabled: () => "off in Prowlarr",
  usenet: () => "Usenet: Theatarr searches torrents only",
};

export function LinkTab({ libraries }: { libraries: LibraryList }) {
  const feeds = useProwlarrFeeds();
  const qc = useQueryClient();
  const [picked, setPicked] = useState<Record<number, boolean>>({});
  const [chosen, setChosen] = useState<string[]>();
  const [error, setError] = useState<Explained>();
  const [announcement, announce] = useAnnouncement();
  const write = useSettingsWrite();
  const libs = libraries.versions ?? [];
  const versions = chosen ?? libs.map((v) => v.key);
  const list = feeds.data?.feeds ?? [];
  const ticked = list.filter((f) => f.status === "linkable" && (picked[f.id] ?? true));

  const link = async () => {
    setError(undefined);
    try {
      // The current generation: connecting Prowlarr just now saved one past the page's overview.
      const { generation } = await qc.fetchQuery({ queryKey: qk.settings.overview, queryFn: settingsOverview, staleTime: 0 });
      await write.mutateAsync({ method: "POST", path: "/prowlarr/feeds", body: { generation, ids: ticked.map((f) => f.id), versions } });
      setPicked({});
      announce(`${ticked.length} feeds linked.`);
    } catch (e) {
      setError(explainError(e));
      announce("Nothing was linked.");
    }
  };
  return (
    <div className="space-y-3" data-testid="link-tab">
      <Announcer announcement={announcement} testid="link-announce" />
      <div className="rounded-xl border border-[var(--th-info-border)] bg-[var(--accent-soft)] p-3 text-[13px] text-[var(--accent-ink)]">
        <b>Link my Prowlarr feeds.</b> Theatarr searches through your Prowlarr: the trackers, their logins and their VPN stay
        in Prowlarr, and each indexer you tick becomes a feed here. To have Theatarr run a tracker itself instead, use <b>Move
        from Prowlarr</b>.
      </div>
      {feeds.isPending ? (
        <p className="text-sm text-muted-foreground"><Loader2 className="mr-1 inline size-4 animate-spin" aria-hidden />Reading Prowlarr…</p>
      ) : feeds.error ? (
        <>
          <ExplainBox error={explainError(feeds.error)} />
          <ProwlarrConnection configured />
        </>
      ) : !feeds.data.configured ? (
        <ProwlarrConnection configured={false} />
      ) : (
        <>
          <ProwlarrConnection configured url={feeds.data.url} />
          {list.length === 0 ? (
            <p className="text-sm text-muted-foreground">Prowlarr has no indexers.</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead><span className="sr-only">Link</span></TableHead>
                  <TableHead>Prowlarr indexer</TableHead>
                  <TableHead>Privacy</TableHead>
                  <TableHead>Here</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.map((f) => (
                  <TableRow key={f.id} data-feed={f.id} data-status={f.status}>
                    <TableCell>
                      {f.status === "linkable" ? (
                        <input type="checkbox" className="size-4 accent-[var(--accent-hover)]" checked={picked[f.id] ?? true} disabled={write.isPending}
                          onChange={(e) => setPicked((x) => ({ ...x, [f.id]: e.target.checked }))} aria-label={`Link ${f.name} as ${f.key}`} />
                      ) : null}
                    </TableCell>
                    <TableCell className="min-w-[10rem] whitespace-normal">
                      <span className="font-medium [overflow-wrap:anywhere]">{f.name}</span> <span className="text-[12px] text-muted-foreground">#{f.id}</span>
                    </TableCell>
                    <TableCell><Badge tone={f.privacy === "public" ? "positive" : "warning"}>{f.privacy}</Badge></TableCell>
                    <TableCell className="whitespace-normal text-[12.5px] text-muted-foreground" data-testid="feed-here">{HERE[f.status]?.(f) ?? f.status}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
          {list.some((f) => f.status === "linkable") ? (
            <>
              <IndexerLibraries libraries={libraries} value={versions} onChange={setChosen} disabled={write.isPending} />
              {error ? <ExplainBox error={error} /> : null}
              <Button type="button" size="sm" disabled={write.isPending || ticked.length === 0 || !librariesReady(libraries)} onClick={() => void link()} data-testid="link-feeds">
                {write.isPending ? <Loader2 className="animate-spin" /> : null} Link {ticked.length} feeds
              </Button>
            </>
          ) : null}
        </>
      )}
      <p className="text-[12.5px] text-muted-foreground" data-testid="link-reminder">
        Keep DMM and SeaDex added directly (Torznab &amp; built-in sources): through Prowlarr they lose which debrid service
        has a release cached. Prowlarr sends the tracker requests itself, so Theatarr's VPN check does not cover them: run
        Prowlarr behind your VPN.
      </p>
    </div>
  );
}
