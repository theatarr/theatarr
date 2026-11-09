// Adapted from Scryer (github.com/scryer-media/scryer) apps/scryer-web/components/ui/input.tsx @ 2ef7757c, GPL-3.0.
// Changes: the numeric-input helpers are left out; the theme's 32 px control, focus as a border plus a soft ring.
import * as React from "react"

import { cn } from "@/lib/utils"

type InputProps = React.ComponentProps<"input"> & {
  "data-1p-ignore"?: string
  "data-lpignore"?: string
  "data-bwignore"?: string
  "data-form-type"?: string
  "data-protonpass-ignore"?: string
  /**
   * Force the password-manager opt-out even for credential-shaped fields.
   * Use for third-party credentials that are not the user's Scryer login, so
   * managers do not offer (or offer to save) the wrong password.
   */
  ignorePasswordManagers?: boolean
}

const credentialAutocompleteValues = new Set([
  "current-password",
  "new-password",
  "one-time-code",
  "username",
])

function shouldSuppressPasswordManager(
  type: React.HTMLInputTypeAttribute | undefined,
  autoComplete: string | undefined,
  force: boolean | undefined,
): boolean {
  if (force) {
    return true
  }

  if (type === "password") {
    return false
  }

  const normalizedAutoComplete = autoComplete?.toLowerCase()
  return !(
    normalizedAutoComplete &&
    credentialAutocompleteValues.has(normalizedAutoComplete)
  )
}

function Input({
  className,
  type,
  autoComplete,
  ignorePasswordManagers,
  "data-1p-ignore": onePasswordIgnore,
  "data-lpignore": lastPassIgnore,
  "data-bwignore": bitwardenIgnore,
  "data-form-type": formType,
  "data-protonpass-ignore": protonPassIgnore,
  ...props
}: InputProps) {
  const suppressPasswordManager = shouldSuppressPasswordManager(
    type,
    autoComplete,
    ignorePasswordManagers,
  )
  // An explicit opt-out overrides any credential autocomplete token the caller
  // passed, so the field cannot advertise itself as fillable.
  const resolvedAutoComplete = ignorePasswordManagers ? "off" : autoComplete

  return (
    <input
      type={type}
      data-slot="input"
      autoComplete={resolvedAutoComplete ?? (suppressPasswordManager ? "off" : undefined)}
      data-1p-ignore={onePasswordIgnore ?? (suppressPasswordManager ? "true" : undefined)}
      data-lpignore={lastPassIgnore ?? (suppressPasswordManager ? "true" : undefined)}
      data-bwignore={bitwardenIgnore ?? (suppressPasswordManager ? "true" : undefined)}
      data-form-type={formType ?? (suppressPasswordManager ? "other" : undefined)}
      data-protonpass-ignore={protonPassIgnore ?? (suppressPasswordManager ? "true" : undefined)}
      className={cn(
        "file:text-foreground placeholder:text-[var(--ink-3)] selection:bg-[var(--select)] bg-field text-foreground border-input h-8 w-full min-w-0 rounded-[5px] border px-2.5 py-1 text-base transition-[color,box-shadow,border-color] outline-none file:inline-flex file:h-6 file:border-0 file:bg-transparent file:text-sm file:font-medium disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 md:text-[13.5px]",
        "focus-visible:border-[var(--line-focus)] focus-visible:ring-[3px] focus-visible:ring-[var(--accent-soft)]",
        "aria-invalid:border-[var(--bad)] aria-invalid:ring-[3px] aria-invalid:ring-[var(--bad-soft)]",
        className
      )}
      {...props}
    />
  )
}

export { Input }
