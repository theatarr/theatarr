// Shared pieces of the settings pages (docs/design/settings.md): the source-of-authority banner, where the
// switches are managed, a refusal explained, a test result, the tests' live region and the write-only credential field. Each is
// a plain view over its props, so the tests render them directly.
import { AlertTriangle, Database, FileText, KeyRound, Loader2, RotateCcw } from "lucide-react";
import { cloneElement, Fragment, isValidElement, useCallback, useId, useState, type ReactNode } from "react";
import type { SettingsOverview, SettingsTestResult } from "@/api/settings";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { authority, testSummary, type Explained, type SecretAction } from "@/lib/settings";
import { cn } from "@/lib/utils";

/** Where indexers, providers and budgets come from right now, on every settings tab. */
export function AuthorityBanner({ overview, onReload, reloading, compact = false, phoneDisclosure = true }: { phoneDisclosure?: boolean; compact?: boolean; overview: SettingsOverview; onReload?: () => void; reloading?: boolean }) {
  const [details, setDetails] = useState(false);
  const a = authority(overview);
  if (compact && a.mode === "db" && !a.fallback) return <div className="mb-4 text-sm text-muted-foreground"><span className="hidden sm:inline">Settings are saved in Theatarr and apply at once.</span><details><summary><span className="sm:hidden">Saved in Theatarr · </span>Details</summary><AuthorityBanner overview={overview} onReload={onReload} reloading={reloading} phoneDisclosure={false} /></details></div>;
  const unavailable = a.mode === "unavailable";
  const Icon = unavailable ? FileText : Database;
  return (
    <div
      data-testid="settings-authority"
      data-mode={a.mode}
      className={cn(
        "mb-4 flex items-start gap-2 rounded-xl border p-2 sm:gap-3 sm:p-4 text-sm",
        unavailable
          ? "border-[var(--th-warning-border)] bg-[var(--warn-soft)] text-[var(--warn-ink)]"
          : "border-[var(--th-info-border)] bg-[var(--accent-soft)] text-[var(--accent-ink)]",
      )}
    >
      <Icon className="mt-0.5 hidden size-5 shrink-0 sm:block" aria-hidden />
      <div className="min-w-0 flex-1 space-y-1">
        <div className={cn("items-center gap-2 sm:hidden", phoneDisclosure ? "flex" : "hidden")}>
          <span className="truncate">{unavailable ? "Unavailable · read-only" : `Database · generation ${overview.generation}`}{a.fallback ? " · fallback" : ""}</span>
          {phoneDisclosure ? <button type="button" className="ml-auto shrink-0 underline" aria-expanded={details} onClick={() => setDetails(!details)}>Details</button> : null}
        </div>
        <div className={cn(details || !phoneDisclosure ? "block" : "hidden", "sm:block")}>
        <div className="flex flex-wrap items-center gap-2 font-semibold">
          {a.title}
          <Badge tone={unavailable ? "warning" : "info"}>{unavailable ? "read-only" : `source: db · generation ${overview.generation}`}</Badge>
        </div>
        {a.lines.map((l, i) => <p key={i} className="[overflow-wrap:anywhere]">{l}</p>)}
        {a.fallback ? (
          <div className="mt-2 flex flex-wrap items-center gap-2" data-testid="settings-fallback">
            <span>
              Running generation {a.fallback.active}, not the stored {a.fallback.stored}: the stored one could not be prepared at start (startup fallback).
            </span>
            {onReload ? (
              <Button type="button" size="xs" variant="outline" onClick={onReload} disabled={reloading}>
                {reloading ? <Loader2 className="animate-spin" /> : <RotateCcw />} Load generation {a.fallback.stored} again
              </Button>
            ) : null}
          </div>
        ) : null}
        </div>
      </div>
    </div>
  );
}

/** Where the switches and the other policy sections are managed, for the source tabs. */
export function ManagedInSettings() {
  return <p className="mb-4 text-sm text-muted-foreground">Global switches, arr views and the other policy sections (library and Plex, notifications, metadata, egress and the rest) are managed in <a className="text-primary hover:underline" href="/admin/settings">Settings</a>.</p>;
}

/** A refusal in plain words, with the server's own text and a reload when settings changed elsewhere. With
 * onReloadKeep (an open editor), the reload offers to discard or keep the draft. */
export function ExplainBox({
  error,
  onReload,
  onReloadKeep,
  className,
}: {
  error: Explained;
  onReload?: () => void;
  onReloadKeep?: () => void;
  className?: string;
}) {
  return (
    <div
      role="alert"
      tabIndex={-1}
      data-testid="settings-error"
      data-kind={error.kind}
      className={cn(
        "flex items-start gap-3 rounded-xl border p-3 text-sm outline-none",
        error.kind === "cooldown" || error.kind === "busy"
          ? "border-[var(--th-warning-border)] bg-[var(--warn-soft)] text-[var(--warn-ink)]"
          : "border-[var(--th-danger-border)] bg-[var(--bad-soft)] text-[var(--bad-ink)]",
        className,
      )}
    >
      <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden />
      <div className="min-w-0 flex-1 space-y-1 [overflow-wrap:anywhere]">
        <div className="font-medium">{error.title}</div>
        <p>{error.message}</p>
        {error.server ? <p className="text-xs opacity-80">Server: {error.server}</p> : null}
        {error.detail && error.detail.length > 1 ? (
          <ul className="list-disc pl-5 text-xs opacity-90">{error.detail.map((d, i) => <li key={i}>{d}</li>)}</ul>
        ) : null}
        {error.reload && onReload && onReloadKeep ? (
          <div className="flex flex-wrap gap-2 pt-1">
            <Button type="button" size="xs" variant="outline" onClick={onReload} data-testid="settings-reload">
              <RotateCcw /> Reload, discard my changes
            </Button>
            <Button type="button" size="xs" variant="outline" onClick={onReloadKeep} data-testid="settings-reload-keep">
              <RotateCcw /> Reload, keep my changes
            </Button>
          </div>
        ) : error.reload && onReload ? (
          <Button type="button" size="xs" variant="outline" onClick={onReload} data-testid="settings-reload" className="mt-1">
            <RotateCcw /> Reload settings
          </Button>
        ) : null}
      </div>
    </div>
  );
}

/** One test result: a status badge and what it found (never upstream text or a credential). */
export function TestResult({ result, now, compact }: { result: SettingsTestResult; now?: number; compact?: boolean }) {
  const s = testSummary(result, now);
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-1.5 text-[13px]" data-testid="test-result" data-status={result.status} data-key={result.key}>
      <Badge tone={s.tone}>{s.label}</Badge>
      {compact ? null : <span className="min-w-0 text-muted-foreground [overflow-wrap:anywhere]">{s.line}</span>}
      {compact || !s.next ? null : <span className="min-w-0 basis-full text-muted-foreground [overflow-wrap:anywhere]" data-testid="test-next">{s.next}</span>}
    </div>
  );
}

/** One announcement for a live region; seq changes with each new one, even when the text repeats. */
export type Announcement = { seq: number; text: string };

/** The latest announcement, and a setter that numbers each new one. */
export function useAnnouncement(): [Announcement | undefined, (text: string | undefined) => void] {
  const [a, setA] = useState<Announcement>();
  const announce = useCallback((text: string | undefined) => {
    if (text) setA((p) => ({ seq: (p?.seq ?? 0) + 1, text }));
  }, []);
  return [a, announce];
}

/** A polite live region that stays mounted, so screen readers hear what lands in it. Each announcement is a new
 * node (keyed by seq): a repeated outcome is spoken again, and a render that brings no new announcement says
 * nothing. */
export function Announcer({ announcement, testid }: { announcement?: Announcement; testid: string }) {
  return (
    <div role="status" aria-live="polite" aria-atomic="true" className="sr-only" data-testid={testid}>
      {announcement ? <span key={announcement.seq}>{announcement.text}</span> : null}
    </div>
  );
}

/** A write-only credential: the stored value never reaches the browser, only whether one is set. Keep sends
 * nothing, Replace sends the new value typed here, Clear removes the stored one. */
export function SecretField({
  set,
  action,
  value,
  onAction,
  onValue,
  disabled,
  needsAgain,
  error,
  label = "Credential",
  hint,
}: {
  set: boolean;
  action: SecretAction;
  value: string;
  onAction: (a: SecretAction) => void;
  onValue: (v: string) => void;
  disabled?: boolean;
  /** The address changed: the stored credential must be entered again. */
  needsAgain?: boolean;
  error?: string;
  /** What the credential is, when a form has several. */
  label?: string;
  /** A line under the state, e.g. the environment fallback while none is stored. */
  hint?: ReactNode;
}) {
  return (
    <div className="space-y-2 sm:col-span-2" data-testid="secret-field" data-action={action}>
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <KeyRound className="size-4 text-muted-foreground" aria-hidden />
        <span className="font-medium">{label}</span>
        <Badge tone={set ? "positive" : "neutral"} data-testid="secret-state">{set ? "set" : "not set"}</Badge>
        <span className="text-xs text-muted-foreground">write-only: the stored value is never shown</span>
      </div>
      {hint ? <p className="text-xs text-muted-foreground">{hint}</p> : null}
      {action === "keep" ? (
        <div className="flex flex-wrap gap-2">
          <Button type="button" size="xs" variant="outline" disabled={disabled} onClick={() => onAction("replace")} data-testid="secret-replace">
            {set ? "Replace" : "Set"}
          </Button>
          {set ? (
            <Button type="button" size="xs" variant="ghost" disabled={disabled} onClick={() => onAction("clear")} data-testid="secret-clear">
              Clear
            </Button>
          ) : null}
        </div>
      ) : action === "replace" ? (
        <div className="flex flex-wrap items-center gap-2">
          <Input
            type="password"
            aria-label="New credential"
            autoComplete="new-password"
            ignorePasswordManagers
            placeholder={set ? "new credential (replaces the stored one)" : "credential"}
            value={value}
            onChange={(e) => onValue(e.target.value)}
            disabled={disabled}
            className="h-8 min-w-0 flex-1 sm:max-w-sm"
            data-testid="secret-input"
          />
          <Button type="button" size="xs" variant="ghost" disabled={disabled} onClick={() => onAction("keep")}>
            {set ? "Keep the stored one" : "Cancel"}
          </Button>
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-2 text-[13px] text-[var(--warn-ink)]">
          The stored credential will be removed on save.
          <Button type="button" size="xs" variant="ghost" disabled={disabled} onClick={() => onAction("keep")}>Undo</Button>
        </div>
      )}
      {needsAgain ? (
        <p className="text-[13px] text-[var(--warn-ink)]" data-testid="secret-needs-again">
          The address changed (kind, scheme, host, port, definition or hosts). A test only sends the stored credential to the
          saved address: choose Replace and enter it to test the new one. Saving keeps the stored credential and asks you to
          confirm that it goes to the new address.
        </p>
      ) : null}
      {error ? <p className="text-[13px] text-[var(--bad-ink)]">{error}</p> : null}
    </div>
  );
}

/** A labelled field row for the forms. */
/** A labelled settings control (docs/design/ui.md "Settings forms"): the label wraps the control; the validation message
 * and the help text sit beside it, tied to the control with aria-describedby; a changed field is marked, with Undo
 * back to its saved value. */
export function Field({ label, help, error, children, wide, changed, onUndo, disabled, name }: {
  label: ReactNode; help?: ReactNode; error?: string; children: ReactNode; wide?: boolean;
  /** The value differs from the saved one. */
  changed?: boolean;
  /** Puts the saved value back (shown with the changed mark). */
  onUndo?: () => void;
  disabled?: boolean;
  /** The plain field name for the Undo button when label is markup. */
  name?: string;
}) {
  const id = useId();
  const described = [error ? `${id}-error` : "", help ? `${id}-help` : ""].filter(Boolean).join(" ") || undefined;
  const control = described && isValidElement<{ "aria-describedby"?: string; "aria-invalid"?: boolean }>(children) && children.type !== Fragment
    ? cloneElement(children, { "aria-describedby": [children.props["aria-describedby"], described].filter(Boolean).join(" "), ...(error ? { "aria-invalid": true } : {}) })
    : children;
  return (
    <div className={cn("flex min-w-0 flex-col gap-1 text-sm", wide && "sm:col-span-2")} data-changed={changed || undefined}>
      <label className="flex min-w-0 flex-col gap-1">
        <span className="font-medium text-[var(--ink)]">{label}</span>
        {control}
      </label>
      {error ? <span id={`${id}-error`} className="text-[12.5px] text-[var(--bad-ink)]">{error}</span> : null}
      {help ? <span id={`${id}-help`} className="text-[12.5px] text-muted-foreground">{help}</span> : null}
      {changed ? <ChangedMark name={typeof label === "string" ? label : name} onUndo={onUndo} disabled={disabled} /> : null}
    </div>
  );
}

/** "Changed" under an edited field, with Undo back to the saved value. */
export function ChangedMark({ name, onUndo, disabled }: { name?: string; onUndo?: () => void; disabled?: boolean }) {
  return (
    <span className="flex items-center gap-2 text-[12px] text-[var(--warn)]" data-testid="field-changed">
      <span className="inline-block size-1.5 rounded-full bg-[var(--warn)]" aria-hidden />
      Changed
      {onUndo ? (
        <button type="button" disabled={disabled} onClick={onUndo} aria-label={name ? `Undo change to ${name}` : undefined}
          className="rounded-[3px] px-1 font-medium text-[var(--accent-ink)] underline-offset-2 hover:underline disabled:opacity-50">
          Undo{name ? null : " change"}
        </button>
      ) : null}
    </span>
  );
}

/** The native select's class, from the select primitive (components/ui/select.tsx). */
export { SELECT_CLASS } from "@/components/ui/select";

/** A list of keys as named checkboxes instead of a comma-separated field. The saved order is kept; a newly ticked key
 * goes last. A saved key no option names stays listed (ticked), so nothing is dropped unseen. */
export function NamedChoices({ legend, help, options, value, onChange, disabled, testid, children }: {
  legend: ReactNode; help?: ReactNode; options: { key: string; label?: ReactNode }[]; value: string[];
  onChange: (next: string[]) => void; disabled?: boolean; testid?: string;
  /** Shown above the checkboxes (for example a choice that makes them unnecessary). */
  children?: ReactNode;
}) {
  const id = useId();
  const known = new Set(options.map((o) => o.key));
  const all = [...options, ...value.filter((k) => !known.has(k)).map((key) => ({ key, label: <>{key} <span className="text-muted-foreground">(not found)</span></> }))];
  return (
    <fieldset className="flex min-w-0 flex-col gap-1.5 text-sm sm:col-span-2" data-testid={testid} aria-describedby={help ? `${id}-help` : undefined}>
      <legend className="mb-1 font-medium text-[var(--ink)]">{legend}</legend>
      {children}
      <div className="grid gap-x-4 gap-y-1.5 sm:grid-cols-2">
        {all.map((o) => (
          <label key={o.key} className="flex min-w-0 cursor-pointer items-start gap-2 has-[:disabled]:cursor-not-allowed has-[:disabled]:opacity-70">
            <input type="checkbox" className="mt-0.5 size-4 shrink-0 accent-[var(--accent)]" checked={value.includes(o.key)} disabled={disabled}
              onChange={(e) => onChange(e.target.checked ? [...value, o.key] : value.filter((k) => k !== o.key))} />
            <span className="min-w-0 [overflow-wrap:anywhere]">{o.label ?? o.key}</span>
          </label>
        ))}
      </div>
      {help ? <span id={`${id}-help`} className="text-[12.5px] text-muted-foreground">{help}</span> : null}
    </fieldset>
  );
}
