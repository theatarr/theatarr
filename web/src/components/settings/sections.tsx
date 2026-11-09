// Admin › Settings › Sections (docs/design/settings.md "Sections"): the policy sections the settings database owns,
// each edited whole as the YAML it was in the policy files, in the side sheet. The server validates it as the policy
// files are validated; a change that moves traffic or file writes, or the VPN guard, answers 422 with its effects and
// the shared confirmation dialog sends it again with the token. Credentials are write-only, by path. Every section
// applies when saved (the server answers live and restart_required false); a section the server marks otherwise says
// that a restart is needed. The sheet keeps the generation and section it opened with: a save after someone else's
// change answers 409 and offers an explicit reload (discard or keep the draft), never a silent overwrite.
import { useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { sectionsQuery, useSettingsSections, type SettingsSection } from "@/api/settings";
import { ErrorState, Loading, Mono } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Sheet, SheetLayout } from "@/components/ui/sheet";
import { type Explained, type SecretAction } from "@/lib/settings";
import { useSettingsEditor } from "./use-settings-editor";
import { ExplainBox, SecretField } from "./parts";

export const SECTION_LABELS: Record<string, { label: string; help: string }> = {
  metadata: { label: "Metadata sources", help: "TMDb, TVDB, image hosts and their keys." },
  library: { label: "Library and Plex", help: "Mount, link path, trash, dangling roots, zurg log and API, Plex. Library roots are set per library; the on/off switch is in Libraries." },
  notify: { label: "Notifications", help: "Targets, events and formats. The on/off switch is in Notifications." },
  clients: { label: "Download clients", help: "Download clients: decypharr, which calls a view back and takes the grabs of libraries that send them to a download client, and qBittorrent for seedbox libraries." },
  egress: { label: "VPN guard", help: "The check URL, VPN addresses and what must go through the VPN." },
  torrents: { label: "Torrents", help: "Scan roots and scheduled backups." },
  cardigann: { label: "Indexer catalog and Prowlarr", help: "Catalog on/off, FlareSolverr and the Prowlarr reader." },
  shadow: { label: "RSS and arr comparison", help: "RSS feed checks and comparison with Sonarr/Radarr." },
  import: { label: "Sonarr/Radarr instances", help: "The Sonarr and Radarr instances imports and aliases read." },
  playlists: { label: "Playlists", help: "Plex playlists Theatarr keeps." },
  ui: { label: "Artwork cache", help: "Artwork cache size, folder and image quality." },
  provider_sync: { label: "Provider account sync", help: "How often provider state is read." },
  router: { label: "Download behaviour", help: "Automatic searches, cache checks, retries and own-add cleanup." },
};

const sectionLabel = (key: string) => SECTION_LABELS[key]?.label ?? key;

const TEXTAREA_CLASS =
  "min-h-[18rem] w-full rounded-[5px] border border-[var(--control)] bg-[var(--surface)] px-3 py-2 font-mono text-[12.5px] outline-none focus-visible:ring-2 focus-visible:ring-[var(--line-focus)] disabled:opacity-60";

export function SectionsCard({ readOnly }: { readOnly: boolean }) {
  const sections = useSettingsSections();
  const [open, setOpen] = useState<string>();
  const [notice, setNotice] = useState<string>();
  if (sections.isPending) return <Loading />;
  if (sections.error) return <ErrorState error={sections.error} />;
  const data = sections.data;
  const current = data.sections.find((s) => s.key === open);
  return (
    <Card className="mt-6" data-testid="settings-sections">
      <h2 className="text-lg font-semibold">Sections</h2>
      <p className="mt-1 text-sm text-muted-foreground">
        Stored in the settings database. Changes marked live apply at once: new operations use them, and loops they schedule restart on the saved values. A section marked restart needed applies at the next restart.
      </p>
      {notice ? <p role="status" className="mt-3 text-sm" data-testid="sections-notice">{notice}</p> : null}
      <ul className="mt-4 divide-y divide-[var(--line)]">
        {data.sections.map((s) => (
          <li key={s.key} className="flex flex-wrap items-center gap-3 py-2.5" data-section={s.key}>
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2 font-medium">
                {sectionLabel(s.key)} <Mono>{s.key}</Mono>
                <Badge tone={s.live ? "positive" : "neutral"}>{s.live ? "live" : "restart needed"}</Badge>
                {s.config === null ? <Badge tone="outline">not configured</Badge> : null}
              </div>
              <p className="text-[12.5px] text-muted-foreground">{SECTION_LABELS[s.key]?.help}</p>
            </div>
            <Button type="button" size="xs" variant="outline" onClick={() => { setNotice(undefined); setOpen(s.key); }}>
              {readOnly ? "View YAML" : "Edit as YAML"} <span className="sr-only">{sectionLabel(s.key)}</span>
            </Button>
          </li>
        ))}
      </ul>
      {current ? (
        <SectionSheet
          key={current.key}
          section={current}
          generation={data.generation}
          readOnly={readOnly || data.source !== "db"}
          onClose={() => setOpen(undefined)}
          onSaved={(restart) => {
            setOpen(undefined);
            setNotice(restart ? `${sectionLabel(current.key)} saved. Restart Theatarr for the change to take effect.` : `${sectionLabel(current.key)} saved and applied.`);
          }}
        />
      ) : null}
    </Card>
  );
}

/** The section is gone from a fresh read: nothing to reload into. */
const SECTION_GONE: Explained = { kind: "other", title: "Section not found", message: "The settings no longer list this section. Close the editor." };

export function SectionSheet({ section: opened, generation: openedAt, readOnly, onClose, onSaved }: {
  section: SettingsSection;
  /** The generation the editor opened at; later reads do not move it (only a reload after a conflict does). */
  generation: number;
  readOnly: boolean;
  onClose: () => void;
  onSaved: (restartRequired: boolean) => void;
}) {
  const qc = useQueryClient();
  const heading = useRef<HTMLHeadingElement>(null);
  // The section and generation the draft was made from, fixed when the sheet opens.
  const editor = useSettingsEditor({
    initial: { section: opened, generation: openedAt },
    rebase: async (base) => {
      const fresh = await qc.fetchQuery({ ...sectionsQuery, staleTime: 0 });
      const section = fresh.sections.find((s) => s.key === base.section.key);
      return section ? { section, generation: fresh.generation } : undefined;
    },
    gone: SECTION_GONE,
    onReload: (next, keep) => {
      if (keep) return;
      setText(next.section.yaml);
      setActions({});
      setValues({});
    },
  });
  const { base, error, setError, kept, busy, reload, confirm } = editor;
  const section = base.section;
  const generation = base.generation;
  const [text, setText] = useState(opened.yaml);
  const [actions, setActions] = useState<Record<string, SecretAction>>({});
  const [values, setValues] = useState<Record<string, string>>({});
  const save = () => {
    const secrets: Record<string, string> = {};
    const clear: string[] = [];
    for (const s of section.secrets) {
      const action = actions[s.path] ?? "keep";
      if (action === "replace") {
        if (!values[s.path]) {
          setError({ kind: "other", title: "Enter the credential", message: `${s.path}: enter a value, or keep the stored one.` });
          return;
        }
        secrets[s.path] = values[s.path];
      } else if (action === "clear") clear.push(s.path);
    }
    const body: Record<string, unknown> = { generation, yaml: text };
    if (Object.keys(secrets).length) body.secrets = secrets;
    if (clear.length) body.clear_secrets = clear;
    void editor.save<{ restart_required?: boolean }>(
      { path: `/settings/sections/${encodeURIComponent(section.key)}`, method: "PUT", body },
      (saved) => onSaved(saved.restart_required ?? !section.live),
    );
  };
  return (
    <Sheet label={sectionLabel(section.key)} onClose={onClose} initialFocus={heading} testid="section-sheet">
      <SheetLayout
        title={sectionLabel(section.key)}
        subtitle={<>{SECTION_LABELS[section.key]?.help} {section.live ? "Applies without a restart." : <strong>Restart needed: the parts of Theatarr built at startup keep the old value until it restarts.</strong>}</>}
        headingRef={heading}
        onClose={onClose}
        testid="section-editor"
        data-section={section.key}
        footer={readOnly ? <Button variant="outline" onClick={onClose}>Close</Button> : <>
          <Button onClick={() => void save()} disabled={busy}>Save</Button>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
        </>}
      >
        {error ? <ExplainBox error={error} onReload={() => void reload(false)} onReloadKeep={() => void reload(true)} className="mb-4" /> : null}
        {kept !== undefined && !error ? (
          <p className="mb-4 text-[13px] text-[var(--warn-ink)]" data-testid="draft-kept">
            Your YAML is kept over the reloaded settings (generation {kept}). Saving it replaces what changed elsewhere in this section.
          </p>
        ) : null}
        <label className="flex flex-col gap-1 text-sm">
          <span className="font-medium">YAML</span>
          <textarea
            className={TEXTAREA_CLASS}
            value={text}
            onChange={(e) => setText(e.target.value)}
            disabled={readOnly || busy}
            spellCheck={false}
            aria-describedby={`section-help-${section.key}`}
            data-testid="section-yaml"
          />
          <span id={`section-help-${section.key}`} className="text-[12.5px] text-muted-foreground">
            The <Mono>{section.key}:</Mono> section as in the policy files, without the key. Empty leaves it out.
            Credentials are set below, not here; a <Mono>*_env</Mono> field names the environment variable used while none is stored.
          </span>
        </label>
        {section.secrets.length ? (
          <div className="mt-5 grid gap-4">
            {section.secrets.map((s) => (
              <SecretField
                key={s.path}
                label={s.path}
                set={s.set}
                hint={s.env ? <>Without a stored value: the environment variable <Mono>{s.env}</Mono>.</> : undefined}
                action={actions[s.path] ?? "keep"}
                value={values[s.path] ?? ""}
                onAction={(a) => setActions((prev) => ({ ...prev, [s.path]: a }))}
                onValue={(v) => setValues((prev) => ({ ...prev, [s.path]: v }))}
                disabled={readOnly || busy}
              />
            ))}
          </div>
        ) : null}
      </SheetLayout>
      {confirm.dialog}
    </Sheet>
  );
}
