// Sign in: with Plex (household members; docs/design/users.md) or with a local account (the break-glass admin), through
// the JSON login (POST /api/theatarr/v1/auth/login). An install with no account at all shows "Create your admin
// account" (create-admin.tsx) instead. Card layout after Scryer's login page (GPL-3.0,
// github.com/scryer-media/scryer apps/scryer-web/src/pages/login.tsx @ 2ef7757c); the Plex button and its popup flow
// after Seerr's login page and PlexLoginButton (MIT, github.com/seerr-team/seerr src/components/Login @ c167958).
import { ChevronDown, Clock, KeyRound, Loader2 } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { PlexSignInError, useFirstAccount, useLogin, usePlexEnabled, usePlexLogin } from "@/api/auth";
import { ApiError } from "@/api/client";
import { useMe } from "@/api/hooks";
import { PRODUCT_NAME } from "@/brand";
import { Logo } from "@/components/shell/app-shell";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";
import { CreateAdmin } from "./create-admin";

/** A post-login target on this site: a path, never a scheme or host (mirrors the server's safeNext). */
export function safeNext(n: string | null): string {
  if (!n || !n.startsWith("/") || n.startsWith("//") || n.startsWith("/\\") || n.startsWith("/login")) return "/";
  return n;
}

const SERVER_PAGES = ["/api/", "/radarr", "/sonarr", "/v/"];

export function Login() {
  const [params] = useSearchParams();
  const next = safeNext(params.get("next"));
  const navigate = useNavigate();
  const login = useLogin();
  const me = useMe();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [plexBusy, setPlexBusy] = useState(false);
  const first = useFirstAccount();
  const plexEnabled = usePlexEnabled();
  const plexLogin = usePlexLogin();
  const plex = plexEnabled.data?.enabled === true;
  const [localOpen, setLocalOpen] = useState(false);
  const showLocal = !plex || localOpen;

  // The first account, just created here, goes to /admin unless a page was asked for: AdminHome leads a fresh install
  // to Setup instead of the empty Home.
  const [creating, setCreating] = useState(false);
  if (first.data?.needs_account && !creating) setCreating(true);
  const to = creating && next === "/" ? "/admin" : next;
  const go = () => {
    if (SERVER_PAGES.some((p) => to.startsWith(p))) window.location.assign(to);
    else navigate(to, { replace: true });
  };
  const signedIn = me.data?.kind === "session";
  useEffect(() => {
    // Already signed in (a bookmark to /login): go on.
    if (signedIn && !busy) go();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [signedIn]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    setBusy(true);
    try {
      await login(username, password);
      go();
    } catch (err) {
      setBusy(false);
      if (err instanceof ApiError && err.status === 401) setError("Wrong username or password.");
      else if (err instanceof ApiError && err.status === 429) setError("Too many failed sign-ins. Try again in a while.");
      else setError(err instanceof Error ? err.message : "Sign-in failed.");
    }
  }

  async function signInWithPlex() {
    setError("");
    setNotice("");
    setPlexBusy(true);
    try {
      await plexLogin();
      go();
    } catch (err) {
      setPlexBusy(false);
      if (err instanceof PlexSignInError && err.status === "pending") setNotice("Your account is waiting for an admin to let you in. Sign in again once they have.");
      else if (err instanceof PlexSignInError && err.status === "denied") setError("This Plex account has no access to our Plex server.");
      else if (err instanceof PlexSignInError && err.status === "disabled") setError("This account is disabled.");
      else setError(err instanceof Error ? err.message : "Plex sign-in failed.");
    }
  }

  // A new install with no account: create the admin first. Until the status is in, nothing (an older server, or an
  // error, gets the sign-in form).
  if (first.isPending) return null;
  if (first.data?.needs_account) return <CreateAdmin onDone={go} onTaken={setError} />;

  return (
    <div className="grid min-h-dvh place-items-center px-4 py-10">
      <div data-slot="card" className="w-full max-w-[384px] rounded-[8px] border border-[var(--line)] bg-[var(--surface)] px-7 py-9">
        <div className="mb-7 flex flex-col items-center gap-5">
          <Logo className="scale-125" />
          <h1 className="font-heading text-[28px] font-medium leading-tight tracking-[-0.015em] text-[var(--ink)]">Sign in</h1>
          {first.data?.password_pending ? (
            <p data-testid="initial-password-hint" className="text-center text-sm text-[var(--ink-2)]">
              The first admin password is in <code>&lt;data dir&gt;/initial-admin-password</code>.
            </p>
          ) : null}
        </div>
        {error ? (
          <div role="alert" className="mb-5 rounded-[5px] border border-l-[3px] border-[var(--line)] border-l-[var(--bad)] bg-[var(--bad-soft)] px-3 py-2.5 text-sm text-[var(--bad-ink)]">
            {error}
          </div>
        ) : null}
        {notice ? (
          <div role="status" data-testid="login-notice" className="mb-5 flex items-start gap-2 rounded-[5px] border border-l-[3px] border-[var(--line)] border-l-[var(--accent)] bg-[var(--accent-soft)] px-3 py-2.5 text-sm text-[var(--accent-ink)]">
            <Clock className="mt-0.5 size-4 shrink-0" />
            {notice}
          </div>
        ) : null}
        {plex ? (
          <>
            <Button
              type="button"
              size="lg"
              onClick={() => void signInWithPlex()}
              disabled={plexBusy}
              data-testid="plex-sign-in"
              className="h-10 w-full text-[14px]"
            >
              {plexBusy ? <Loader2 className="size-4 animate-spin" /> : <PlexMark />}
              {plexBusy ? "Waiting for Plex…" : "Sign in with Plex"}
            </Button>
            <p className="mt-2.5 text-center text-xs text-[var(--ink-3)]">Use the Plex account you watch with. A window opens at plex.tv.</p>
            <div className="my-6 flex items-center gap-3 text-[11px] font-medium uppercase tracking-[0.12em] text-[var(--ink-3)]">
              <span className="h-px flex-1 bg-[var(--line)]" />
              or
              <span className="h-px flex-1 bg-[var(--line)]" />
            </div>
            <button
              type="button"
              onClick={() => setLocalOpen((o) => !o)}
              aria-expanded={localOpen}
              data-testid="local-toggle"
              className="flex w-full items-center justify-center gap-2 rounded-[3px] text-sm font-medium text-[var(--ink-2)] outline-none hover:text-[var(--ink)] focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--line-focus)]"
            >
              <KeyRound className="size-4" />
              Local account (admin)
              <ChevronDown className={cn("size-4 transition-transform", localOpen && "rotate-180")} />
            </button>
          </>
        ) : null}
        <form onSubmit={submit} className={cn("space-y-5", plex && "mt-5", !showLocal && "hidden")} aria-label={`Sign in to ${PRODUCT_NAME}`}>
          <div className="space-y-2">
            <Label htmlFor="username">Username</Label>
            <Input id="username" name="username" autoComplete="username" required autoFocus={!plex} value={username} onChange={(e) => setUsername(e.target.value)} placeholder="Username" />
          </div>
          <div className="space-y-2">
            <Label htmlFor="password">Password</Label>
            <Input id="password" name="password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} placeholder="Password" />
          </div>
          <Button type="submit" variant={plex ? "outline" : "default"} className="h-9 w-full" disabled={busy}>
            {busy ? <Loader2 className="animate-spin" /> : null}
            Sign in
          </Button>
        </form>
      </div>
    </div>
  );
}

/** A plain chevron in the Plex button (not Plex's logo). */
function PlexMark() {
  return (
    <svg viewBox="0 0 24 24" className="size-4" aria-hidden="true">
      <path d="M7 3h5l7 9-7 9H7l7-9z" fill="currentColor" />
    </svg>
  );
}

/** The popup's first page while the server asks plex.tv for a pin, and its last after Plex sends it back. */
export function PlexPopup({ done }: { done?: boolean }) {
  useEffect(() => {
    if (done) window.close();
  }, [done]);
  return (
    <div className="grid min-h-dvh place-items-center px-4 text-sm text-muted-foreground">
      <div className="flex items-center gap-2">
        {done ? null : <Loader2 className="size-4 animate-spin" />}
        {done ? "Signed in with Plex. You can close this window." : "Connecting to Plex…"}
      </div>
    </div>
  );
}
