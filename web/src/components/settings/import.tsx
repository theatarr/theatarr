// Import & export: preview what importing the policy directory's indexers, providers and budgets would change (the
// diff, including credential_changed), then apply it with an explicit confirmation; the review export; and what
// importing does and does not do to authority. ImportView is the plain view; ImportPanel holds the state.
import { Download, Eye, Loader2, Upload } from "lucide-react";
import { useReducer, useState } from "react";
import { importApply, importConfirm, importPreview, useReloadSettings, type SettingsOverview } from "@/api/settings";
import type { Confirmation } from "@/api/settings-versions";
import { API_BASE } from "@/api/client";
import { Mono, Section } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Choice, ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  canApply,
  explainError,
  importCounts,
  importReducer,
  importTouchesProviders,
  initialImport,
  type ImportState,
} from "@/lib/settings";
import { ExplainBox } from "./parts";
import { OrphanChoice } from "./record-form";

const OP_TONE = { create: "positive", update: "info", delete: "negative" } as const;

export type ImportViewProps = {
  overview: SettingsOverview;
  state: ImportState;
  readOnly?: string;
  onPreview: () => void;
  onConfirm: (v: boolean) => void;
  onOrphan: (v: boolean) => void;
  onApply: () => void;
  onReset: () => void;
  onReload?: () => void;
};

export function ImportView(p: ImportViewProps) {
  const { overview: o, state: s } = p;
  const yaml = !!p.readOnly;
  const preview = s.preview;
  const counts = preview ? importCounts(preview.changes) : undefined;
  return (
    <div data-testid="import-panel" data-step={s.step}>
      <Card className="mb-4 space-y-2 text-sm" data-testid="import-authority">
        <h3 className="font-semibold text-[var(--ink)]">What an import does</h3>
        <ul className="list-disc space-y-1 pl-5 text-[13px] text-muted-foreground">
          <li>
            It reads indexers, providers, budgets, quality profiles, custom formats, format sets, library naming, libraries and views (with their API keys) from <Mono>{o.policy_dir || "the policy directory"}</Mono>, resolves each <Mono>*_env</Mono>{" "}
            credential from Theatarr's environment, and replaces those settings as one new generation. Custom format JSON is stored with its specifications.
          </li>
          <li>The database is always the live settings authority. This uses the same validated import as <Mono>theatarr settings import</Mono>.</li>
          <li>An import does not change the global switches or the sections already stored (library and Plex, notifications, metadata, egress and the rest); those are edited in Settings. Any import fills the sections the database lacks from the files; the preview names them. Write-enabling library changes require a separate confirmation describing the roots and providers affected.</li>
          <li>Startup never imports policy files. Use theatarr settings import for an explicit seed, or preview and import here.</li>
        </ul>
      </Card>

      {yaml ? (
        <p className="mb-4 rounded-lg border border-[var(--th-warning-border)] bg-[var(--warn-soft)] p-3 text-[13px] text-[var(--warn-ink)]" data-testid="import-readonly">
          {p.readOnly} Import, export and the settings audit need theatarr serve on DB settings.
        </p>
      ) : null}

      <Section title="1. Preview" className="mt-0" aside="Changes nothing">
        <div className="flex flex-wrap items-center gap-3">
          <Button variant="outline" onClick={p.onPreview} disabled={yaml || s.step === "previewing" || s.step === "applying"} data-testid="import-preview">
            {s.step === "previewing" ? <Loader2 className="animate-spin" /> : <Eye />} Preview import of generation {o.generation}
          </Button>
          {s.step === "applied" ? (
            <span className="text-sm text-[var(--ok-ink)]" data-testid="import-applied">
              Imported: generation {s.appliedGeneration} is stored and running.
            </span>
          ) : null}
        </div>
        {s.error ? <ExplainBox error={s.error} onReload={p.onReload} className="mt-3" /> : null}
        {preview && counts ? (
          <Card className="mt-3 p-0" data-testid="import-diff">
            <p
              className={`border-b border-border px-4 py-2.5 text-sm font-medium ${preview.sections_from_files.length ? "bg-[var(--warn-soft)] text-[var(--warn-ink)]" : "text-[var(--ink)]"}`}
              data-testid="import-sections-note"
            >
              {preview.sections_note}
            </p>
            <div className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-2.5 text-[13px]">
              <span className="font-medium text-[var(--ink)]">Against generation {preview.generation}:</span>
              <Badge tone="positive">{counts.create} new</Badge>
              <Badge tone="info">{counts.update} changed</Badge>
              <Badge tone="negative">{counts.delete} removed</Badge>
              {counts.credential ? <Badge tone="warning" data-testid="import-credential-count">{counts.credential} {counts.credential === 1 ? "credential" : "credentials"} replaced</Badge> : null}
            </div>
            {preview.changes.length === 0 ? (
              <p className="px-4 py-3 text-sm text-muted-foreground" data-testid="import-nochange">
                {preview.sections_from_files.length
                  ? "No record changes: the import only fills the settings sections named above."
                  : "No record changes. Settings sections stay as stored."}
              </p>
            ) : (
              <Table density="dense">
                <TableHeader>
                  <TableRow>
                    <TableHead>Change</TableHead>
                    <TableHead>Record</TableHead>
                    <TableHead>Credential</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {preview.changes.map((c) => (
                    <TableRow key={`${c.entity}/${c.key}`} data-change={`${c.entity}/${c.key}`} data-op={c.operation}>
                      <TableCell><Badge tone={OP_TONE[c.operation]}>{c.operation}</Badge></TableCell>
                      <TableCell><span className="text-muted-foreground">{c.entity.replace(/s$/, "")}</span> <Mono>{c.key}</Mono></TableCell>
                      <TableCell className="text-[13px]">
                        {c.credential_changed ? (
                          <Badge tone="warning" data-testid="credential-changed">replaced by the import</Badge>
                        ) : c.entity === "indexers" || c.entity === "providers" ? (
                          <span className="text-muted-foreground">{c.operation === "delete" ? "deleted with it" : "unchanged"}</span>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
            {preview.yaml ? (
              <details className="border-t border-border">
                <summary className="cursor-pointer px-4 py-2 text-[13px] text-muted-foreground">Review YAML of the result (credentials stripped)</summary>
                <pre className="max-h-80 overflow-auto px-4 pb-3 text-[12px] leading-relaxed">{preview.yaml}</pre>
              </details>
            ) : null}
            {preview.files && Object.keys(preview.files).length ? <details className="border-t border-border">
              <summary className="cursor-pointer px-4 py-2 text-[13px]">Review custom format JSON files</summary>
              <pre className="max-h-80 overflow-auto px-4 pb-3 text-xs">{JSON.stringify(preview.files, null, 2)}</pre>
            </details> : null}
          </Card>
        ) : null}
      </Section>

      <Section title="2. Apply" aside="Takes effect at once">
        {preview ? (
          <Card className="space-y-3 text-sm" data-testid="import-apply-box">
            <Choice checked={s.confirmed} onChange={p.onConfirm} disabled={yaml || s.step !== "preview"} testid="import-confirm">
              I have reviewed the changes above. Replace the database settings listed above with the policy files' (generation{" "}
              {preview.generation} → {preview.generation + 1}). New operations use these settings; operations already running keep their snapshot. Existing links keep their names.
            </Choice>
            {importTouchesProviders(preview.changes) ? (
              <OrphanChoice checked={s.orphan} onChange={p.onOrphan} disabled={yaml || s.step !== "preview"}>
                This import changes or removes providers. Leave this off and the import is refused if that would fail active downloads; nothing
                changes. Tick it to import anyway: the removed or disabled providers' active downloads are marked failed (not blocklisted).
              </OrphanChoice>
            ) : null}
            <div className="flex flex-wrap gap-2">
              <Button onClick={p.onApply} disabled={yaml || !canApply(s)} data-testid="import-apply">
                {s.step === "applying" ? <Loader2 className="animate-spin" /> : <Upload />} Import now
              </Button>
              <Button variant="ghost" onClick={p.onReset} disabled={s.step === "applying"}>Discard the preview</Button>
            </div>
          </Card>
        ) : (
          <p className="text-[13px] text-muted-foreground" data-testid="import-apply-waiting">Preview first: the apply step needs a preview of the current generation.</p>
        )}
      </Section>

      <Section title="Backup and export">
        <Card className="space-y-2 text-[13px] text-muted-foreground">
          <p>
            The settings live in Theatarr's database, so the nightly database backup (System › Backup) holds them, credentials included; keep the policy
            directory <Mono>{o.policy_dir}</Mono> as well. The cutover runbook is in <Mono>docs/design/settings.md</Mono>.
          </p>
          <p>
            The export below is for review: credentials and <Mono>*_env</Mono> names are stripped, so it is not a credential backup and cannot be
            imported as it is.
          </p>
          <Button asChild={!yaml} variant="outline" size="sm" disabled={yaml}>
            {yaml ? (
              <span><Download /> Download review YAML</span>
            ) : (
              <a href={`${API_BASE}/settings/export`} download="settings-export.yaml" data-testid="export-link">
                <Download /> Download review YAML
              </a>
            )}
          </Button>
        </Card>
      </Section>
    </div>
  );
}

export function ImportPanel({ overview, readOnly }: { overview: SettingsOverview; readOnly?: string }) {
  const [s, dispatch] = useReducer(importReducer, initialImport);
  const [confirmation, setConfirmation] = useState<Confirmation>();
  const reload = useReloadSettings();
  const preview = async () => {
    dispatch({ type: "preview-start" });
    try {
      dispatch({ type: "preview-done", preview: await importPreview(overview.generation) });
    } catch (e) {
      dispatch({ type: "preview-error", error: explainError(e) });
    }
  };
  const apply = async (token?: string) => {
    if (!canApply(s) || !s.preview) return;
    dispatch({ type: "apply-start" });
    try {
      if (!token) {
        const proof = await importConfirm(s.preview.generation, s.orphan);
        if (proof.effects?.length) {
          setConfirmation(proof);
          dispatch({ type: "apply-review" });
          return;
        }
      }
      const w = await importApply(s.preview.generation, s.orphan, token);
      setConfirmation(undefined);
      dispatch({ type: "apply-done", generation: w.generation ?? s.preview.generation + 1 });
    } catch (e) {
      setConfirmation(undefined);
      dispatch({ type: "apply-error", error: explainError(e) });
    }
    void reload();
  };
  return (
    <><ImportView
      overview={overview}
      state={s}
      readOnly={readOnly}
      onPreview={() => void preview()}
      onConfirm={(value) => dispatch({ type: "confirm", value })}
      onOrphan={(value) => dispatch({ type: "orphan", value })}
      onApply={() => void apply()}
      onReset={() => dispatch({ type: "reset" })}
      onReload={() => {
        dispatch({ type: "reset" });
        void reload();
      }}
    />{confirmation ? <ConfirmDialog scope={{ title: "Confirm library changes in this import", does: (confirmation.effects ?? []).map((effect) => effect.effect), keeps: ["Global switches retain their DB values; environment force-off overrides stay in force.", "Links, mount files and trash entries stay in place."], confirm: "Confirm and import" }} busy={s.step === "applying"} onClose={() => setConfirmation(undefined)} onConfirm={() => void apply(confirmation.token)} /> : null}</>
  );
}
