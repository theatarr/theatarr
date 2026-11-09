import { settingHref } from "@/lib/settings-map";
// Admin › Users & access (docs/design/users.md): the setting for new Plex accounts, accounts waiting for approval, and per
// account the role, enable or disable, auto-approve, own API keys, its Discord user id, the versions its requests ask for, its keys and
// "sign out everywhere"; then every API key. Laid out after Seerr's user list and user settings (MIT,
// github.com/seerr-team/seerr src/components/UserList and UserProfile/UserSettings @ c167958): avatar, name, role
// and status per row, permissions behind an edit panel.
import { ChevronDown, LogOut, ShieldCheck, UserCheck, UserX } from "lucide-react";
import { useState } from "react";
import { ApiError } from "@/api/client";
import { useAuth } from "@/api/auth";
import { useEndSessions, usePatchUser, useSetUserSettings, useUsers, type User, type UserPatch, type UsersPage } from "@/api/users";
import { Empty, ErrorState, Loading, PageHeader, Section } from "@/components/page";
import { Avatar, KeysPanel } from "@/components/users/keys";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { fmtAgo } from "@/lib/format";
import { cn } from "@/lib/utils";

const ROLES: { id: "admin" | "manager" | "user"; label: string; hint: string }[] = [
  { id: "user", label: "User", hint: "Browses and requests; sees availability only" },
  { id: "manager", label: "Manager", hint: "Runs the library: adds, approves requests, sees files" },
  { id: "admin", label: "Admin", hint: "Everything, with the admin panel and users" },
];

function Segmented<T extends string>({ value, options, onChange, label }: { value: T; options: { id: T; label: string }[]; onChange: (v: T) => void; label: string }) {
  return (
    <div role="radiogroup" aria-label={label} className="inline-flex flex-wrap gap-0.5 rounded-[5px] border border-[var(--line)] bg-[var(--bg)] p-0.5">
      {options.map((o) => (
        <button
          key={o.id}
          type="button"
          role="radio"
          aria-checked={value === o.id}
          onClick={() => onChange(o.id)}
          className={cn("h-7 rounded-[3px] px-3 text-[13px] font-medium text-[var(--ink-3)] transition-colors outline-none hover:text-[var(--ink)] focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[var(--line-focus)]", value === o.id && "bg-[var(--surface)] text-[var(--ink)] shadow-[inset_0_0_0_1px_var(--line-2)]")}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function UsersAdmin() {
  const users = useUsers();
  const settings = useSetUserSettings();
  const data = users.data;
  const pending = (data?.items ?? []).filter((u) => u.status === "pending");
  return (
    <div>
      <PageHeader crumb={[{ label: "Admin", to: "/admin" }]} title="Users & access" description="Household members sign in with Plex; the local admin account stays as the way in when Plex is down." />
      {users.isPending ? <Loading /> : users.error ? <ErrorState error={users.error} /> : data ? (
        <>
          <Card>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="min-w-0">
                <div className="font-medium">New Plex accounts</div>
                <div className="text-sm text-muted-foreground">
                  {data.settings.plex_enabled
                    ? "Anyone with access to our Plex server can sign in. A first sign-in creates a user account."
                    : <>Plex sign-in is off: it needs the <a href={settingHref("metadata", "sources")}>plex.tv metadata service</a> and <a href={settingHref("library", "plex")}>your Plex server</a>.</>}
                </div>
              </div>
              <Segmented
                label="New Plex accounts"
                value={data.settings.plex_new_users as "pending" | "active"}
                onChange={(v) => settings.mutate({ plex_new_users: v })}
                options={[{ id: "pending", label: "Wait for approval" }, { id: "active", label: "Let in at once" }]}
              />
            </div>
          </Card>
          {pending.length > 0 ? (
            <Section title={`Waiting for approval (${pending.length})`}>
              <div className="space-y-3" data-testid="pending-users">
                {pending.map((u) => <UserCard key={u.id} u={u} page={data} />)}
              </div>
            </Section>
          ) : null}
          <Section title="Accounts" aside={`${data.items.length - pending.length}`}>
            <div className="space-y-3" data-testid="user-list">
              {data.items.filter((u) => u.status !== "pending").map((u) => <UserCard key={u.id} u={u} page={data} />)}
              {data.items.length === 0 ? <Card><Empty>No accounts.</Empty></Card> : null}
            </div>
          </Section>
          <Section title="All API keys" aside="Sent as the X-Api-Key header; a key carries its owner's permissions. The arr views take admin keys and view keys only.">
            <KeysPanel user="all" owner create={false} idPrefix="all-keys" />
          </Section>
        </>
      ) : null}
    </div>
  );
}

function UserCard({ u, page }: { u: User; page: UsersPage }) {
  const me = useAuth();
  const patch = usePatchUser();
  const end = useEndSessions();
  const [open, setOpen] = useState(false);
  const self = me.data?.id === u.id;
  const set = (p: UserPatch) => patch.mutate({ id: u.id, patch: p });
  const extra = u.extra_permissions ?? [];
  const toggleExtra = (name: "own_keys", on: boolean) =>
    set({ extra_permissions: on ? [...extra.filter((x) => x !== name), name] : extra.filter((x) => x !== name) });
  const name = u.display_name || u.username;
  const statusTone = u.status === "active" ? "positive" : u.status === "pending" ? "warning" : "negative";
  return (
    <Card className="p-0" data-testid="user-row" data-username={u.username}>
      <div className="flex flex-wrap items-center gap-3 p-3 sm:p-4">
        <Avatar name={name} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="font-medium text-[var(--ink)] [overflow-wrap:anywhere]">{name}</span>
            {self ? <Badge tone="accent">you</Badge> : null}
            <Badge tone={statusTone}>{u.status}</Badge>
            <Badge tone="outline">{u.plex_id ? "Plex" : "local"}</Badge>
          </div>
          <div className="text-xs text-muted-foreground [overflow-wrap:anywhere]">
            {u.username}
            {u.email ? ` · ${u.email}` : ""} · {u.last_login_at ? `last sign-in ${fmtAgo(u.last_login_at)}` : "never signed in"}
          </div>
        </div>
        <div className="flex w-full flex-wrap items-center gap-2 pl-[52px] sm:w-auto sm:pl-0">
          {u.status === "pending" ? (
            <>
              <Button size="sm" onClick={() => set({ status: "active" })} disabled={patch.isPending} data-testid="user-approve">
                <UserCheck /> Let in
              </Button>
              <Button size="sm" variant="ghost" className="text-[var(--bad-ink)]" onClick={() => set({ status: "disabled" })} disabled={patch.isPending}>
                <UserX /> Refuse
              </Button>
            </>
          ) : (
            <label className="flex items-center gap-2 text-sm">
              <span className="sr-only">Role</span>
              <Select
                value={u.role}
                disabled={patch.isPending}
                onChange={(e) => set({ role: e.target.value as User["role"] })}
                data-testid="user-role"
                className="w-auto"
              >
                {ROLES.map((r) => <option key={r.id} value={r.id}>{r.label}</option>)}
              </Select>
            </label>
          )}
          <Button size="sm" variant="outline" onClick={() => setOpen((o) => !o)} aria-expanded={open} data-testid="user-edit">
            Settings <ChevronDown className={cn("transition-transform", open && "rotate-180")} />
          </Button>
        </div>
      </div>
      {patch.error ? <p className="px-4 pb-3 text-xs text-[var(--bad-ink)]">{patch.error instanceof ApiError ? patch.error.message : String(patch.error)}</p> : null}
      {open ? (
        <div className="space-y-5 border-t border-[var(--line)] p-3 sm:p-4" data-testid="user-settings">
          <p className="text-xs text-muted-foreground">{ROLES.find((r) => r.id === u.role)?.hint}. Permissions: {(u.permissions ?? []).join(", ")}.</p>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="flex items-center justify-between gap-3 rounded-[5px] border border-[var(--line)] px-3 py-2.5">
              <div>
                <div className="text-sm font-medium">Auto-approve</div>
                <div className="text-xs text-muted-foreground">Requests apply at once{u.role !== "user" ? " (always, for this role)" : ""}</div>
              </div>
              <Switch checked={u.auto_approve || u.role !== "user"} disabled={u.role !== "user" || patch.isPending} onCheckedChange={(v) => set({ auto_approve: v })} aria-label="Auto-approve" data-testid="user-auto-approve" />
            </div>
            <div className="flex items-center justify-between gap-3 rounded-[5px] border border-[var(--line)] px-3 py-2.5">
              <div>
                <div className="text-sm font-medium">Own API keys</div>
                <div className="text-xs text-muted-foreground">For apps on their phone{u.role !== "user" ? " (always, for this role)" : ""}</div>
              </div>
              <Switch checked={extra.includes("own_keys") || u.role !== "user"} disabled={u.role !== "user" || patch.isPending} onCheckedChange={(v) => toggleExtra("own_keys", v)} aria-label="Own API keys" data-testid="user-own-keys" />
            </div>
          </div>
          <DiscordID u={u} busy={patch.isPending} onSave={(v) => set({ discord_id: v })} />
          <RequestVersions u={u} page={page} onChange={(v) => set({ request_versions: v })} />
          <div>
            <div className="mb-2 text-[13px] font-semibold text-[var(--ink)]">API keys ({u.keys})</div>
            <KeysPanel user={u.id} idPrefix={`user-${u.id}-key`} />
          </div>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" disabled={end.isPending || u.sessions === 0} onClick={() => { if (window.confirm(`Sign ${name} out everywhere?`)) end.mutate(u.id); }}>
              <LogOut /> Sign out everywhere ({u.sessions})
            </Button>
            {u.status === "active" && !self ? (
              <Button size="sm" variant="ghost" className="text-[var(--bad-ink)]" onClick={() => { if (window.confirm(`Disable ${name}? Their sessions end and their keys stop working.`)) set({ status: "disabled" }); }} data-testid="user-disable">
                <UserX /> Disable
              </Button>
            ) : u.status === "disabled" ? (
              <Button size="sm" variant="outline" onClick={() => set({ status: "active" })} data-testid="user-enable">
                <ShieldCheck /> Enable
              </Button>
            ) : null}
          </div>
        </div>
      ) : null}
    </Card>
  );
}

/** The account's Discord user id: request-ready messages to a Discord notification channel @mention it. */
export function DiscordID({ u, busy, onSave }: { u: User; busy: boolean; onSave: (v: string) => void }) {
  const stored = u.discord_id ?? "";
  const [draft, setDraft] = useState(stored);
  const value = draft.trim();
  const valid = value === "" || /^\d{17,20}$/.test(value);
  return (
    <div data-testid="user-discord">
      <label className="mb-1 block text-[13px] font-semibold text-[var(--ink)]" htmlFor={`discord-${u.id}`}>Discord user id</label>
      <p className="mb-2 text-xs text-muted-foreground">When a request is ready, the Discord message mentions this person. In Discord: Settings › Advanced › Developer Mode, then right-click the user › Copy User ID. Empty: no mention.</p>
      <div className="flex flex-wrap items-center gap-2">
        <Input id={`discord-${u.id}`} inputMode="numeric" value={draft} onChange={(e) => setDraft(e.target.value)} placeholder="123456789012345678"
          className="w-56 max-w-full font-mono" />
        <Button size="sm" variant="outline" disabled={busy || !valid || value === stored} onClick={() => onSave(value)}>Save</Button>
      </div>
      {!valid ? <p role="alert" className="mt-1 text-xs text-[var(--bad-ink)]">A Discord user id is 17 to 20 digits.</p> : null}
    </div>
  );
}

/** The versions this account's requests ask for: every version that fits (the default), or the ones ticked. */
function RequestVersions({ u, page, onChange }: { u: User; page: UsersPage; onChange: (v: string[] | null) => void }) {
  const set = u.request_versions ?? null;
  const toggle = (k: string) => {
    const cur = set ?? [];
    const next = cur.includes(k) ? cur.filter((x) => x !== k) : [...cur, k];
    onChange(next.length === 0 ? null : next);
  };
  const kinds: { id: string; label: string }[] = [
    { id: "movie", label: "Movies" },
    { id: "series", label: "Series and anime" },
  ];
  return (
    <div data-testid="user-request-versions">
      <div className="mb-1 text-[13px] font-semibold text-[var(--ink)]">Requests ask for</div>
      <p className="mb-2 text-xs text-muted-foreground">
        {set === null
          ? "Every library that fits the title (the default)."
          : "Only the ticked libraries; a kind with none ticked gets every library that fits."}
        {set !== null ? (
          <button type="button" className="ml-2 font-medium text-[var(--accent-ink)] hover:underline" onClick={() => onChange(null)}>
            Back to every library
          </button>
        ) : null}
      </p>
      <div className="space-y-2">
        {kinds.map((k) => (
          <div key={k.id} className="flex flex-wrap items-center gap-1.5">
            <span className="w-full text-xs text-[var(--ink-3)] sm:w-32">{k.label}</span>
            {page.versions.filter((v) => v.kind === k.id).map((v) => {
              const on = set === null || set.includes(v.key);
              return (
                <button
                  key={v.key}
                  type="button"
                  aria-pressed={set !== null && set.includes(v.key)}
                  data-vkey={v.key}
                  onClick={() => toggle(v.key)}
                  className={cn(
                    "h-7 rounded-[3px] px-2.5 text-xs font-medium text-[var(--ink-3)] shadow-[inset_0_0_0_1px_var(--line-2)] transition-colors outline-none focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--line-focus)]",
                    on && "bg-[var(--accent-soft)] text-[var(--accent-ink)] shadow-[inset_0_0_0_1px_var(--accent)]",
                    set === null && "opacity-75",
                  )}
                >
                  {v.label}
                  {v.anime === true && !/anime/i.test(v.label) ? " · anime" : ""}
                </button>
              );
            })}
          </div>
        ))}
      </div>
    </div>
  );
}
