// Debrid provider pills (docs/design/ui.md "Interactive search", "Library views"): one green pill per provider cache
// code (rd → RD, ad → AD, tb → TB, anything else upper-cased); "—" or the given empty node when there is none.
import type { ReactNode } from "react";
import { Badge } from "@/components/ui/badge";

const LABELS: Record<string, string> = { rd: "RD", ad: "AD", tb: "TB" };

export function providerLabel(code: string): string {
  return LABELS[code.toLowerCase()] ?? code.toUpperCase();
}

export function ProviderPills({ codes, empty, testid }: { codes?: readonly string[] | null; empty?: ReactNode; testid?: string }) {
  if (!codes?.length) return <>{empty ?? <span className="text-[var(--ink-3)]">—</span>}</>;
  return (
    <span className="flex flex-wrap gap-1">
      {codes.map((c) => (
        <Badge key={c} tone="ok" data-testid={testid}>
          {providerLabel(c)}
        </Badge>
      ))}
    </span>
  );
}
