// The Libraries checklist of every way of adding an indexer (docs/design/settings.md, "Adding an indexer"): the ticked
// libraries search the new indexer, from the same save. The caller ticks every library that suits it
// (matchingLibraries in lib/cardigann.ts).
import type { VersionRecord } from "@/api/settings-versions";
import { NamedChoices } from "./parts";

export function IndexerLibraries({ libraries, value, onChange, disabled }: {
  /** Undefined while they load: nothing is shown. */
  libraries?: VersionRecord[];
  value: string[];
  onChange: (next: string[]) => void;
  disabled?: boolean;
}) {
  if (!libraries) return null;
  if (libraries.length === 0) {
    return (
      <p className="text-[12.5px] text-muted-foreground sm:col-span-2" data-testid="indexer-libraries">
        No libraries yet. A library you create later lists the indexers it searches.
      </p>
    );
  }
  const options = libraries.map((v) => ({
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
