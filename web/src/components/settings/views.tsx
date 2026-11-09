// Admin › Settings › Connected apps (docs/design/settings.md "Views"): the arr-compatible views in the settings database,
// the one place their library bindings and keys are edited. Create, edit and delete a view (key, flavour, path,
// libraries and profiles as named choices, alias_from, API key) in the side sheet. A new view, a moved path or flavour, other aliases or a new key answer 422 with their effects; the shared
// confirmation dialog sends the same request again with the token. The API key is write-only. Routes apply at once.
// The sheet keeps the view and generation it opened with: a save or delete after someone else's change answers 409
// and offers an explicit reload (discard or keep the draft), never a silent overwrite.
import { useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { Link } from "react-router";
import { qs } from "@/api/client";
import { useSettingsViews, viewsQuery, type SettingsView } from "@/api/settings";
import { useVersions, type VersionList } from "@/api/settings-versions";
import { ErrorState, Loading, Mono } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { VersionStatus } from "@/components/version-status";
import { Sheet, SheetLayout } from "@/components/ui/sheet";
import { type Explained, type SecretAction } from "@/lib/settings";
import { useSettingsEditor } from "./use-settings-editor";
import { ExplainBox, Field, NamedChoices, SecretField, SELECT_CLASS } from "./parts";

const list = (text: string) => text.split(",").map((s) => s.trim()).filter(Boolean);


/** The library choices: every library in the settings, or (before they load) just the saved ones. */
const libraryOptions = (data: VersionList | undefined, saved: string[]) => (data ? data.versions.map((v) => v.key) : saved).map((key) => ({ key }));

/** A library's state for the list; the same fallback as the Libraries page when the switches lack it. */
const stateOf = (data: VersionList, key: string) => data.switches.versions.find((v) => v.key === key)?.grab_state ?? { mode: "off", grab: false, library_enabled: data.switches.library_enabled, router_enabled: data.switches.router_enabled, router_forced_off: data.switches.sources?.router === "forced off by environment" };

export function ViewsCard({ readOnly }: { readOnly: boolean }) {
  const views = useSettingsViews();
  const libraries = useVersions();
  const [open, setOpen] = useState<string | "new">();
  const [notice, setNotice] = useState<string>();
  if (views.isPending) return <Loading />;
  if (views.error) return <ErrorState error={views.error} />;
  const data = views.data;
  const writable = !readOnly && data.source === "db";
  const current = open === "new" ? undefined : data.views.find((v) => v.key === open);
  return (
    <Card className="mt-6" data-testid="settings-views">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-lg font-semibold">Arr apps</h2>
        {writable ? <Button className="ml-auto" size="xs" onClick={() => { setNotice(undefined); setOpen("new"); }}>Add view</Button> : null}
      </div>
      <p className="mt-1 text-sm text-muted-foreground">
        Each app's libraries, profiles and API key. Changes apply without a restart.
      </p>
      {notice ? <p role="status" className="mt-3 text-sm">{notice}</p> : null}
      <ul className="mt-4 divide-y divide-[var(--line)]">
        {data.views.map((v) => (
          <li key={v.key} className="flex flex-wrap items-center gap-3 py-2.5" data-view={v.key}>
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2 font-medium">
                {v.key}
                {v.identity ? <><Mono>{v.identity.path}</Mono><Badge tone="outline">{v.identity.flavour}</Badge></> : null}
                {data.source === "db" ? <Badge tone={v.secret_set ? "positive" : "neutral"}>{v.secret_set ? "API key set" : "no API key"}</Badge> : null}
              </div>
              {v.versions.length ? (
                <ul className="mt-1 flex flex-col gap-1">
                  {v.versions.map((key) => (
                    <li key={key} className="flex flex-wrap items-center gap-2 text-[12.5px]">
                      <span className="font-mono [overflow-wrap:anywhere]">{key}</span>
                      {libraries.data?.versions.some((l) => l.key === key) ? <VersionStatus showMode why={false} state={stateOf(libraries.data, key)} /> : null}
                    </li>
                  ))}
                </ul>
              ) : <p className="text-[12.5px] text-muted-foreground">no libraries</p>}
            </div>
            <Button type="button" size="xs" variant="outline" onClick={() => { setNotice(undefined); setOpen(v.key); }}>
              {writable ? "Edit" : "View"} <span className="sr-only">{v.key}</span>
            </Button>
          </li>
        ))}
      </ul>
      {open ? (
        <ViewSheet
          key={open}
          view={current}
          generation={data.generation}
          readOnly={!writable}
          onClose={() => setOpen(undefined)}
          onSaved={(text) => { setOpen(undefined); setNotice(text); }}
        />
      ) : null}
    </Card>
  );
}

/** The view is gone from a fresh read: nothing to reload into. */
const VIEW_GONE: Explained = { kind: "other", title: "View deleted elsewhere", message: "The settings no longer have this view. Close the editor; add it again if it is still wanted." };

export function ViewSheet({ view: opened, generation: openedAt, readOnly, onClose, onSaved }: {
  /** undefined: a new view. */
  view?: SettingsView;
  /** The generation the editor opened at; later reads do not move it (only a reload after a conflict does). */
  generation: number;
  readOnly: boolean;
  onClose: () => void;
  onSaved: (notice: string) => void;
}) {
  const qc = useQueryClient();
  const heading = useRef<HTMLHeadingElement>(null);
  // The view and generation the draft was made from, fixed when the sheet opens: a view deleted or changed
  // elsewhere meanwhile neither turns this editor into a new view nor moves the generation its save sends.
  const editor = useSettingsEditor({
    initial: { view: opened, generation: openedAt },
    rebase: async (base) => {
      const fresh = await qc.fetchQuery({ ...viewsQuery, staleTime: 0 });
      const view = base.view ? fresh.views.find((v) => v.key === base.view!.key) : undefined;
      return base.view && !view ? undefined : { view, generation: fresh.generation };
    },
    gone: VIEW_GONE,
    onReload: (next, keep) => {
      const view = next.view;
      if (!view) return true;
      if (keep) return;
      setFlavour(view.identity?.flavour ?? "radarr");
      setPath(view.identity?.path ?? "");
      setVersions(view.versions);
      setProfiles(view.profiles ?? {});
      setAliases((view.identity?.alias_from ?? []).join(", "));
      setAction("keep");
      setApiKey("");
    },
  });
  const { base, error, setError, busy, reload, confirm } = editor;
  const kept = editor.kept;
  const view = base.view;
  const generation = base.generation;
  const create = !view;
  // A view the first start of this version has not seeded yet keeps its identity and key in the policy files.
  const seeded = create || !!view.identity;
  const [key, setKey] = useState(view?.key ?? "");
  const [flavour, setFlavour] = useState(view?.identity?.flavour ?? "radarr");
  const [path, setPath] = useState(view?.identity?.path ?? "");
  const [versions, setVersions] = useState<string[]>(view?.versions ?? []);
  const [profiles, setProfiles] = useState<Record<string, string[]>>(view?.profiles ?? {});
  const [newProfile, setNewProfile] = useState("");
  const libraries = useVersions();
  const [aliases, setAliases] = useState((view?.identity?.alias_from ?? []).join(", "));
  const [action, setAction] = useState<SecretAction>(create ? "replace" : "keep");
  const [apiKey, setApiKey] = useState("");
  const [deleting, setDeleting] = useState(false);
  const save = () => {
    if (action === "replace" && !apiKey) {
      setError({ kind: "other", title: "Enter the API key", message: create ? "A new view needs its API key." : "Enter the new key, or keep the stored one." });
      return;
    }
    const body: Record<string, unknown> = { generation, versions, profiles };
    if (seeded) Object.assign(body, { flavour, path, alias_from: list(aliases) });
    if (create) body.key = key;
    if (action === "replace") body.api_key = apiKey;
    if (action === "clear") body.clear_api_key = true;
    void editor.save(
      { path: create ? "/settings/view-bindings" : `/settings/view-bindings/${encodeURIComponent(view.key)}`, method: create ? "POST" : "PUT", body },
      () => onSaved(`View ${key} saved.`),
    );
  };
  const remove = () => editor.save(
    { path: `/settings/view-bindings/${encodeURIComponent(key)}` + qs({ generation }), method: "DELETE" },
    () => onSaved(`View ${key} deleted.`),
  );
  const off = readOnly || busy;
  return (
    <Sheet label={create ? "New view" : `View ${view.key}`} onClose={onClose} initialFocus={heading} testid="view-sheet">
      <SheetLayout
        title={create ? "New view" : `View ${view.key}`}
        subtitle="Applies without a restart."
        headingRef={heading}
        onClose={onClose}
        testid="view-editor"
        footer={readOnly ? <Button variant="outline" onClick={onClose}>Close</Button> : <>
          <Button onClick={() => void save()} disabled={busy}>Save</Button>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          {!create ? <Button className="ml-auto" variant="destructive" onClick={() => setDeleting(true)} disabled={busy}>Delete view</Button> : null}
        </>}
      >
        {error ? <ExplainBox error={error} onReload={() => void reload(false)} onReloadKeep={() => void reload(true)} className="mb-4" /> : null}
        {kept !== undefined && !error ? (
          <p className="mb-4 text-[13px] text-[var(--warn-ink)]" data-testid="draft-kept">
            Your changes are kept over the reloaded settings (generation {kept}). Saving them replaces what changed elsewhere in this view.
          </p>
        ) : null}
        {!seeded ? <p className="mb-4 text-sm text-muted-foreground">Path, flavour, aliases and key come from the policy files until Theatarr restarts once on this release.</p> : null}
        <div className="grid gap-4 sm:grid-cols-2">
          {create ? <Field label="Key" help="A name for the view; it cannot be renamed later."><Input value={key} onChange={(e) => setKey(e.target.value)} disabled={off} /></Field> : null}
          <Field label="Flavour">
            <select className={SELECT_CLASS} value={flavour} onChange={(e) => setFlavour(e.target.value)} disabled={off || !seeded}>
              <option value="radarr">radarr</option>
              <option value="sonarr">sonarr</option>
            </select>
          </Field>
          <Field label="Path" help="Where the view answers, e.g. /radarr."><Input value={path} onChange={(e) => setPath(e.target.value)} disabled={off || !seeded} /></Field>
          <NamedChoices legend="Libraries" help="What the app sees, in the order ticked." testid="view-libraries"
            options={libraryOptions(libraries.data, versions)} value={versions} onChange={setVersions} disabled={off} />
          <div className="flex min-w-0 flex-col gap-3 sm:col-span-2" data-testid="view-profiles">
            <p className="text-sm font-medium text-[var(--ink)]">Profiles <span className="font-normal text-muted-foreground">(clients see them as quality profiles)</span></p>
            {Object.entries(profiles).map(([name, keys]) => (
              <NamedChoices key={name} legend={`Profile: ${name}`} testid={`view-profile-${name}`}
                options={libraryOptions(libraries.data, keys)} value={keys} onChange={(next) => setProfiles({ ...profiles, [name]: next })} disabled={off}>
                {off ? null : <Button type="button" variant="ghost" size="xs" className="self-start" onClick={() => { const next = { ...profiles }; delete next[name]; setProfiles(next); }}>Remove profile <span className="sr-only">{name}</span></Button>}
              </NamedChoices>
            ))}
            {off ? null : (
              <div className="flex flex-wrap items-end gap-2">
                <Field label="New profile name"><Input value={newProfile} onChange={(e) => setNewProfile(e.target.value)} /></Field>
                <Button type="button" variant="outline" disabled={!newProfile.trim() || Object.hasOwn(profiles, newProfile.trim())}
                  onClick={() => { setProfiles({ ...profiles, [newProfile.trim()]: [] }); setNewProfile(""); }}>Add profile</Button>
              </div>
            )}
          </div>
          <Field label="Alias from" help="Comma-separated arr instances whose movie or series ids this view still answers." wide>
            <Input value={aliases} onChange={(e) => setAliases(e.target.value)} disabled={off || !seeded} />
          </Field>
          {seeded ? (
            <SecretField label="API key" set={!!view?.secret_set} action={action} value={apiKey} onAction={setAction} onValue={setApiKey} disabled={off}
              hint={<>Clients send it as X-Api-Key. Keys made under <Link className="underline" to="/admin/users">Users &amp; access</Link> work too.</>} />
          ) : null}
        </div>
      </SheetLayout>
      {confirm.dialog}
      {deleting ? (
        <ConfirmDialog
          testid="view-delete"
          scope={{
            title: `Delete view ${key}`,
            does: [`${path || "The view"} stops answering arr clients at once; its stored API key is removed.`],
            keeps: ["Libraries, links and library files stay as they are."],
            confirm: "Delete view",
          }}
          onClose={() => setDeleting(false)}
          onConfirm={() => { setDeleting(false); void remove(); }}
        />
      ) : null}
    </Sheet>
  );
}
