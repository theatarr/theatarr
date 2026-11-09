// /me, the profile (docs/design/ui.md; docs/design/users.md): display name, the theme, a password for local accounts
// (Plex accounts sign in with Plex and have none), and your own API keys when your account may have them.
import { Check, Loader2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useAuth } from "@/api/auth";
import { ApiError } from "@/api/client";
import { usePerms } from "@/api/library";
import { useChangePassword, usePatchMe } from "@/api/users";
import { PageHeader, Section } from "@/components/page";
import { ThemeSwitch } from "@/components/shell/account-menu";
import { Avatar, KeysPanel } from "@/components/users/keys";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function Profile() {
  const me = useAuth();
  const perms = usePerms();
  const d = me.data;
  if (!d) return null;
  return (
    <div>
      <PageHeader title="Profile" />
      <Card>
        <div className="flex flex-wrap items-center gap-4">
          <Avatar name={d.display_name || d.username} size="lg" />
          <div className="min-w-0 flex-1">
            <div className="text-lg font-semibold text-[var(--ink)] [overflow-wrap:anywhere]">{d.display_name || d.username}</div>
            <div className="text-sm text-muted-foreground">
              {d.username} · {d.plex ? "signs in with Plex" : "local account"}
            </div>
          </div>
          <Badge tone="accent" className="capitalize">{d.role}</Badge>
        </div>
        <NameForm current={d.display_name || ""} />
      </Card>
      <Section title="Theme">
        <Card className="max-w-sm">
          <ThemeSwitch />
        </Card>
      </Section>
      {d.local ? (
        <Section title="Password">
          <PasswordForm />
        </Section>
      ) : null}
      {perms.ownKeys ? (
        <Section title="Your API keys" aside="For apps and scripts. A key can do what your account can.">
          <KeysPanel idPrefix="my-key" />
        </Section>
      ) : null}
    </div>
  );
}

function NameForm({ current }: { current: string }) {
  const patch = usePatchMe();
  const [name, setName] = useState(current);
  const [saved, setSaved] = useState(false);
  return (
    <form
      className="mt-5 flex flex-wrap items-end gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        patch.mutate({ display_name: name.trim() }, { onSuccess: () => setSaved(true) });
      }}
    >
      <div className="min-w-0 flex-1 basis-56 space-y-1.5 sm:flex-none">
        <Label htmlFor="display-name">Display name</Label>
        <Input id="display-name" className="w-full sm:w-72" maxLength={64} value={name} onChange={(e) => { setName(e.target.value); setSaved(false); }} />
      </div>
      <Button type="submit" variant="outline" disabled={patch.isPending || name.trim() === current}>
        {patch.isPending ? <Loader2 className="animate-spin" /> : saved ? <Check /> : null} Save
      </Button>
      {patch.error ? <span className="text-sm text-[var(--bad-ink)]">{String((patch.error as Error).message)}</span> : null}
    </form>
  );
}

function PasswordForm() {
  const change = useChangePassword();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setMsg(null);
    if (next !== again) return setMsg({ ok: false, text: "The new passwords differ." });
    try {
      await change.mutateAsync({ current, new: next });
      setCurrent("");
      setNext("");
      setAgain("");
      setMsg({ ok: true, text: "Password changed. Your other sessions were signed out." });
    } catch (err) {
      setMsg({ ok: false, text: err instanceof ApiError ? err.message : String(err) });
    }
  };
  return (
    <Card className="max-w-md">
      <form onSubmit={submit} className="space-y-4">
        <div className="space-y-1.5">
          <Label htmlFor="pw-current">Current password</Label>
          <Input id="pw-current" type="password" autoComplete="current-password" required value={current} onChange={(e) => setCurrent(e.target.value)} />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="pw-new">New password</Label>
          <Input id="pw-new" type="password" autoComplete="new-password" minLength={12} required value={next} onChange={(e) => setNext(e.target.value)} />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="pw-again">New password again</Label>
          <Input id="pw-again" type="password" autoComplete="new-password" minLength={12} required value={again} onChange={(e) => setAgain(e.target.value)} />
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Button type="submit" disabled={change.isPending}>{change.isPending ? <Loader2 className="animate-spin" /> : null} Change password</Button>
          {msg ? <span className={msg.ok ? "text-sm text-[var(--ok-ink)]" : "text-sm text-[var(--bad-ink)]"}>{msg.text}</span> : null}
        </div>
      </form>
    </Card>
  );
}
