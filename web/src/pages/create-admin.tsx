// "Create your admin account": what the sign-in page shows while the install has no account at all (GET
// auth/first-account says needs_account). The server accepts it only while no account exists (docs/design/web.md).
import { Loader2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useCreateFirstAdmin } from "@/api/auth";
import { ApiError } from "@/api/client";
import { PRODUCT_NAME } from "@/brand";
import { Logo } from "@/components/shell/app-shell";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

/** The server's minimum (auth.MinPasswordLen). */
export const MIN_PASSWORD_LEN = 12;

export function CreateAdmin({ onDone }: { onDone: () => void }) {
  const create = useCreateFirstAdmin();
  const qc = useQueryClient();
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!username.trim()) return setError("Enter a username.");
    if (/\s/.test(username.trim())) return setError("The username cannot contain spaces.");
    if (password.length < MIN_PASSWORD_LEN) return setError(`The password must be at least ${MIN_PASSWORD_LEN} characters.`);
    if (password !== again) return setError("The passwords do not match.");
    setError("");
    setBusy(true);
    try {
      await create(username.trim(), password);
      onDone();
    } catch (err) {
      setBusy(false);
      if (err instanceof ApiError && err.status === 409) {
        setError("This install already has an account. Sign in instead.");
        // The sign-in page takes over once the status says so.
        void qc.invalidateQueries({ queryKey: ["auth", "first-account"] });
      } else setError(err instanceof Error ? err.message : "Could not create the account.");
    }
  }

  return (
    <div className="grid min-h-dvh place-items-center px-4 py-10">
      <div data-slot="card" className="w-full max-w-[384px] rounded-[8px] border border-[var(--line)] bg-[var(--surface)] px-7 py-9">
        <div className="mb-7 flex flex-col items-center gap-5">
          <Logo className="scale-125" />
          <h1 className="font-heading text-[28px] font-medium leading-tight tracking-[-0.015em] text-[var(--ink)]">Create your admin account</h1>
          <p className="text-center text-sm text-[var(--ink-2)]">{PRODUCT_NAME} has no accounts yet. This one manages everything; you can let others in later.</p>
        </div>
        {error ? (
          <div role="alert" className="mb-5 rounded-[5px] border border-l-[3px] border-[var(--line)] border-l-[var(--bad)] bg-[var(--bad-soft)] px-3 py-2.5 text-sm text-[var(--bad-ink)]">
            {error}
          </div>
        ) : null}
        <form onSubmit={submit} noValidate className="space-y-5" aria-label="Create your admin account">
          <div className="space-y-2">
            <Label htmlFor="new-username">Username</Label>
            <Input id="new-username" name="username" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="new-password">Password</Label>
            <Input id="new-password" name="password" type="password" autoComplete="new-password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} />
            <p className="text-xs text-[var(--ink-3)]">At least {MIN_PASSWORD_LEN} characters.</p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="new-password-again">Confirm password</Label>
            <Input id="new-password-again" name="password-again" type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} />
          </div>
          <Button type="submit" className="h-9 w-full" disabled={busy}>
            {busy ? <Loader2 className="animate-spin" /> : null}
            Create account
          </Button>
        </form>
      </div>
    </div>
  );
}
