// An invite link (/invite#<token>, docs/design/users.md "Invites"): one page where the invited person chooses a
// username and password, or signs in with Plex when it is set up, and lands signed in on Home. The token is read from
// the fragment, which the browser never sends, and goes to the server in request bodies only. An expired, used or
// unknown link says so and offers nothing else. Laid out like the sign-in and create-admin cards.
import { Loader2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { PlexSignInError, useAcceptInvite, useInvite, usePlexLogin } from "@/api/auth";
import { ApiError } from "@/api/client";
import { PRODUCT_NAME } from "@/brand";
import { Logo } from "@/components/shell/app-shell";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { MIN_PASSWORD_LEN } from "./create-admin";

/** The invite page's card: the logo, a heading, an optional line under it, then the body. */
function Frame({ title, line, children }: { title: string; line?: string; children?: React.ReactNode }) {
  return (
    <div className="grid min-h-dvh place-items-center px-4 py-10">
      <div data-slot="card" className="w-full max-w-[384px] rounded-[8px] border border-[var(--line)] bg-[var(--surface)] px-7 py-9">
        <div className={children ? "mb-7 flex flex-col items-center gap-5" : "flex flex-col items-center gap-5"}>
          <Logo className="scale-125" />
          <h1 className="font-heading text-[28px] font-medium leading-tight tracking-[-0.015em] text-[var(--ink)]">{title}</h1>
          {line ? <p className="text-center text-sm text-[var(--ink-2)]" data-testid="invite-line">{line}</p> : null}
        </div>
        {children}
      </div>
    </div>
  );
}

const deadMessage = (err: unknown) =>
  err instanceof ApiError && (err.status === 404 || err.status === 410) ? err.message
    : err instanceof ApiError && err.status === 429 ? "Too many tries. Try again later."
      : err instanceof Error ? err.message : "This invite link does not work.";

export function InvitePage() {
  const [token] = useState(() => window.location.hash.replace(/^#/, ""));
  const invite = useInvite(token);
  const accept = useAcceptInvite();
  const plexLogin = usePlexLogin();
  const navigate = useNavigate();
  const [username, setUsername] = useState<string | null>(null);
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [plexBusy, setPlexBusy] = useState(false);
  const [dead, setDead] = useState("");

  if (!token) return <Frame title="Invite link" line="This invite link is not valid or has already been used." />;
  if (invite.isPending) return null;
  if (invite.error || dead) return <Frame title="Invite link" line={dead || deadMessage(invite.error)} />;
  const info = invite.data;
  // The name the admin gave, as a starting username when it has no spaces.
  const name = username ?? (/\s/.test(info.name.trim()) ? "" : info.name.trim().toLowerCase());
  const done = () => navigate("/", { replace: true });
  const refused = (err: unknown) => {
    if (err instanceof ApiError && (err.status === 404 || err.status === 410)) setDead(err.message);
    else setError(err instanceof ApiError && err.status === 429 ? "Too many tries. Try again later." : err instanceof Error ? err.message : "That did not work.");
  };

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!name.trim()) return setError("Choose a username.");
    if (/\s/.test(name.trim())) return setError("The username cannot contain spaces.");
    if (password.length < MIN_PASSWORD_LEN) return setError(`The password must be at least ${MIN_PASSWORD_LEN} characters.`);
    if (password !== again) return setError("The passwords do not match.");
    setError("");
    setBusy(true);
    try {
      await accept(token, name.trim(), password);
      done();
    } catch (err) {
      setBusy(false);
      refused(err);
    }
  }

  async function withPlex() {
    setError("");
    setPlexBusy(true);
    try {
      await plexLogin(undefined, token);
      done();
    } catch (err) {
      setPlexBusy(false);
      if (err instanceof PlexSignInError && (err.status === "expired" || err.status === "error") && /invite/i.test(err.message)) setDead(err.message);
      else if (err instanceof PlexSignInError && err.status === "denied") setError("This Plex account has no access to our Plex server.");
      else refused(err);
    }
  }

  return (
    <Frame title={`Join ${PRODUCT_NAME}`} line={`${info.name ? `${info.name}, you` : "You"} have been invited. Choose a username and password${info.plex ? ", or sign in with Plex" : ""}.`}>
      {error ? (
        <div role="alert" className="mb-5 rounded-[5px] border border-l-[3px] border-[var(--line)] border-l-[var(--bad)] bg-[var(--bad-soft)] px-3 py-2.5 text-sm text-[var(--bad-ink)]">
          {error}
        </div>
      ) : null}
      {info.plex ? (
        <>
          <Button type="button" size="lg" onClick={() => void withPlex()} disabled={plexBusy || busy} data-testid="invite-plex" className="h-10 w-full text-[14px]">
            {plexBusy ? <Loader2 className="size-4 animate-spin" /> : null}
            {plexBusy ? "Waiting for Plex…" : "Sign in with Plex"}
          </Button>
          <div className="my-6 flex items-center gap-3 text-[11px] font-medium uppercase tracking-[0.12em] text-[var(--ink-3)]">
            <span className="h-px flex-1 bg-[var(--line)]" />
            or
            <span className="h-px flex-1 bg-[var(--line)]" />
          </div>
        </>
      ) : null}
      <form onSubmit={submit} noValidate className="space-y-5" aria-label="Choose a username and password">
        <div className="space-y-2">
          <Label htmlFor="invite-username">Username</Label>
          <Input id="invite-username" name="username" autoComplete="username" value={name} onChange={(e) => setUsername(e.target.value)} autoFocus={!info.plex} />
        </div>
        <div className="space-y-2">
          <Label htmlFor="invite-password">Password</Label>
          <Input id="invite-password" name="password" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
          <p className="text-xs text-[var(--ink-3)]">At least {MIN_PASSWORD_LEN} characters.</p>
        </div>
        <div className="space-y-2">
          <Label htmlFor="invite-password-again">Confirm password</Label>
          <Input id="invite-password-again" name="password-again" type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} />
        </div>
        <Button type="submit" variant={info.plex ? "outline" : "default"} className="h-9 w-full" disabled={busy || plexBusy}>
          {busy ? <Loader2 className="animate-spin" /> : null}
          Create account
        </Button>
      </form>
    </Frame>
  );
}
