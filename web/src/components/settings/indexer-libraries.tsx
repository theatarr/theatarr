// The Libraries checklist of every way of adding an indexer (docs/design/settings.md, "Adding an indexer"): the ticked
// libraries search the new indexer, from the same save. The caller ticks every library that suits it
// (matchingLibraries in lib/cardigann.ts). Until the library list has loaded, the add waits: its button is disabled
// (librariesReady), and a failed read says so with Try again.
import { Loader2 } from "lucide-react";
import type { VersionRecord } from "@/api/settings-versions";
import { Button } from "@/components/ui/button";
import { NamedChoices } from "./parts";

/** The library list as read for an add form: versions once loaded, error when the read failed. */
export type LibraryList = { versions?: VersionRecord[]; error?: unknown; retry: () => void };

/** The add may be sent: the library list has loaded (or the form has no Libraries list). */
export const librariesReady = (l?: LibraryList) => !l || !!l.versions;

export function IndexerLibraries({ libraries, value, onChange, disabled }: {
  libraries: LibraryList;
  value: string[];
  onChange: (next: string[]) => void;
  disabled?: boolean;
}) {
  const list = libraries.versions;
  if (!list) {
    return libraries.error ? (
      <div className="flex flex-wrap items-center gap-2 text-[12.5px] text-[var(--bad-ink)] sm:col-span-2" role="alert" data-testid="indexer-libraries-error">
        The libraries could not be read: {libraries.error instanceof Error ? libraries.error.message : String(libraries.error)}.
        <Button type="button" size="sm" variant="outline" onClick={libraries.retry}>Try again</Button>
      </div>
    ) : (
      <p className="text-[12.5px] text-muted-foreground sm:col-span-2" data-testid="indexer-libraries-loading">
        <Loader2 className="mr-1 inline size-4 animate-spin" aria-hidden />Reading the libraries…
      </p>
    );
  }
  if (list.length === 0) {
    return (
      <p className="text-[12.5px] text-muted-foreground sm:col-span-2" data-testid="indexer-libraries">
        No libraries yet. A library you create later lists the indexers it searches.
      </p>
    );
  }
  const options = list.map((v) => ({
    key: v.key,
    label: (
      <>
        {v.config.name || v.key}{" "}
        <span className="text-muted-foreground">
          {v.config.kind === "movie" ? "Movies" : "Series"}
          {v.config.anime === true ? " · anime" : ""}
          {v.config.mode === "shadow" ? " · Watch only" : ""}
        </span>
      </>
    ),
  }));
  return (
    <NamedChoices
      legend="Libraries"
      help="The ticked libraries search it. Every library that suits its content is ticked."
      options={options}
      value={value}
      onChange={onChange}
      disabled={disabled}
      testid="indexer-libraries"
    />
  );
}
