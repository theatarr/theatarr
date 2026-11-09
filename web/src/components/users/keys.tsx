// API keys: a list with revoke and a create form that shows the new key once. Used by the profile (your own keys)
// and the users screen (one account's, or everyone's). Keys carry their owner's permissions. Copy waits for the
// clipboard and says when it failed: navigator.clipboard is missing on a plain-HTTP LAN address (not a secure context),
// so the key stays in a selected field with a warning to copy it by hand.
import { AlertTriangle, Check, Copy, KeyRound } from "lucide-react";
import { useRef, useState, type FormEvent } from "react";
import { ApiError } from "@/api/client";
import { useCreateKeyFor, useKeysOf, useRevokeKeyAny } from "@/api/users";
import { Empty, ErrorState, Loading, Mono } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmtAgo, fmtTime } from "@/lib/format";

export function KeysPanel({ user, owner = false, create = true, idPrefix = "key" }: {
  /** Whose keys: undefined = yours, a user id, or "all". */
  user?: number | "all";
  owner?: boolean;
  create?: boolean;
  idPrefix?: string;
}) {
  const keys = useKeysOf(user);
  const make = useCreateKeyFor();
  const revoke = useRevokeKeyAny();
  const [name, setName] = useState("");
  const [created, setCreated] = useState<{ name: string; key: string } | null>(null);
  const [err, setErr] = useState("");
  const [copy, setCopy] = useState<"idle" | "copied" | "failed">("idle");
  const keyField = useRef<HTMLInputElement>(null);
  const selectKey = () => {
    keyField.current?.focus();
    keyField.current?.select();
  };
  const copyKey = async (key: string) => {
    try {
      if (!navigator.clipboard?.writeText) throw new Error("no clipboard");
      await navigator.clipboard.writeText(key);
      setCopy("copied");
    } catch {
      setCopy("failed");
      selectKey();
    }
  };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setErr("");
    try {
      const r = await make.mutateAsync({ name: name.trim(), user_id: typeof user === "number" ? user : undefined });
      setCreated({ name: name.trim(), key: r.key });
      setName("");
      setCopy("idle");
      window.setTimeout(selectKey, 0);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : String(e));
    }
  };
  const items = keys.data?.items ?? [];
  return (
    <div>
      {create ? (
        <form onSubmit={submit} className="mb-4 flex flex-wrap items-end gap-2">
          <div className="min-w-0 flex-1 basis-48 space-y-1.5 sm:flex-none">
            <Label htmlFor={`${idPrefix}-name`}>New key</Label>
            <Input id={`${idPrefix}-name`} className="w-full sm:w-64" placeholder="name, e.g. phone" value={name} onChange={(e) => setName(e.target.value)} required />
          </div>
          <Button type="submit" disabled={make.isPending || !name.trim()} data-testid={`${idPrefix}-create`}><KeyRound />Create key</Button>
          {err ? <span className="text-sm text-[var(--bad-ink)]">{err}</span> : null}
        </form>
      ) : null}
      {created ? (
        <Card className="mb-4 border-[var(--th-success-border)]" data-testid="new-key">
          <div className="flex items-start gap-2 text-sm font-medium">
            <AlertTriangle className="mt-0.5 size-4 shrink-0 text-[var(--warn)]" />
            <span>Key "{created.name}": copy it now, it won't be shown again.</span>
          </div>
          <div className="mt-2 flex items-center gap-2">
            <Input
              ref={keyField}
              readOnly
              value={created.key}
              className="font-mono text-xs"
              onFocus={(e) => e.currentTarget.select()}
              onClick={(e) => e.currentTarget.select()}
              aria-label="New API key"
              data-testid="new-key-value"
            />
            <Button type="button" variant="outline" size="icon" aria-label="Copy to clipboard" onClick={() => void copyKey(created.key)} data-testid="new-key-copy">
              {copy === "copied" ? <Check /> : <Copy />}
            </Button>
          </div>
          <p role="status" className="mt-2 text-xs" data-testid="new-key-copy-state">
            {copy === "copied" ? (
              <span className="text-[var(--ok-ink)]">Copied to the clipboard.</span>
            ) : copy === "failed" ? (
              <span className="text-[var(--bad-ink)]">
                Could not copy: the browser has no clipboard here (plain HTTP). The key is selected: press Ctrl-C (Cmd-C), or
                copy it by hand, before you leave this page.
              </span>
            ) : (
              <span className="text-muted-foreground">Select the field and copy it, or use the copy button.</span>
            )}
          </p>
        </Card>
      ) : null}
      {keys.isPending ? <Loading /> : keys.error ? <ErrorState error={keys.error} /> : items.length === 0 ? <Card><Empty>No keys.</Empty></Card> : (
        <Card className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead className="hidden sm:table-cell">Key</TableHead>
                <TableHead className="hidden md:table-cell">Created</TableHead>
                <TableHead className="hidden sm:table-cell">Last used</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((k) => (
                <TableRow key={k.id}>
                  <TableCell className="font-medium">
                    {k.name}
                    {owner && k.created_by ? <div className="text-xs font-normal text-muted-foreground">{k.created_by}</div> : null}
                  </TableCell>
                  <TableCell className="hidden sm:table-cell"><Mono>{k.prefix}…</Mono></TableCell>
                  <TableCell className="hidden whitespace-nowrap md:table-cell">{fmtTime(k.created_at)}</TableCell>
                  <TableCell className="hidden whitespace-nowrap sm:table-cell">{k.last_used_at ? fmtAgo(k.last_used_at) : "never"}</TableCell>
                  <TableCell className="text-right">
                    {k.revoked_at ? <Badge>revoked</Badge> : (
                      <Button size="sm" variant="ghost" className="text-[var(--bad-ink)]" disabled={revoke.isPending}
                        onClick={() => { if (window.confirm(`Revoke key ${k.name}? Clients using it stop working.`)) revoke.mutate(k.id ?? 0); }}>
                        Revoke
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </div>
  );
}

/** An account's initials in a ruled circle (avatars from plex.tv are not loaded: the CSP keeps images on this origin). */
export function Avatar({ name, size = "md" }: { name: string; size?: "sm" | "md" | "lg" }) {
  const initials = name.split(/[\s._-]+/).filter(Boolean).slice(0, 2).map((w) => w[0]).join("") || "?";
  const cls = size === "lg" ? "size-14 text-lg" : size === "sm" ? "size-8 text-xs" : "size-10 text-sm";
  return (
    <span className={`grid shrink-0 place-items-center rounded-full border border-[var(--line)] bg-[var(--sunken)] font-semibold uppercase text-[var(--ink-2)] ${cls}`}>
      {initials}
    </span>
  );
}
