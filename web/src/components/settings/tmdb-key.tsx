// The one-field TMDb step of Admin › Setup (docs/quickstart.md): a TMDb API key is all a fresh install has to type.
// Saving creates the metadata sources tmdb and tmdb-images with TMDb's public addresses where they are missing and
// stores the key, through the metadata section's ordinary save with its confirmation (two new hosts). A source that
// already has an address keeps it.
import { useState, type FormEvent } from "react";
import { METADATA_SERVICES } from "@/lib/settings";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useSectionPatch } from "./use-section-patch";

const record = (v: unknown): Record<string, unknown> => v && typeof v === "object" && !Array.isArray(v) ? v as Record<string, unknown> : {};

/** Adds the missing TMDb sources to a metadata section config; never changes an address or a budget that is set. */
export function withTMDbSources(config: Record<string, unknown>) {
  const sources = { ...record(config.sources) };
  for (const [name, url] of Object.entries(METADATA_SERVICES)) {
    const source = record(sources[name]);
    if (!source.url) sources[name] = { ...source, url };
  }
  config.sources = sources;
}

export function TMDbKey({ onSaved }: { onSaved: () => void }) {
  const [key, setKey] = useState("");
  const patch = useSectionPatch("metadata", () => { setKey(""); onSaved(); });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (key.trim()) void patch.save(withTMDbSources, { "metadata.sources.tmdb.api_key": key.trim() });
  };
  return <form className="grid max-w-md gap-2" onSubmit={submit} data-testid="tmdb-key">
    <label className="flex flex-col gap-1 text-sm"><span>TMDb API key (v3)</span>
      <Input type="password" autoComplete="off" value={key} disabled={patch.busy} onChange={(e) => setKey(e.target.value)} />
    </label>
    <p className="text-[12.5px] text-muted-foreground">The short “API Key” from your TMDb account’s API settings (<a className="underline" href="https://www.themoviedb.org/settings/api" target="_blank" rel="noreferrer">themoviedb.org › Settings › API</a>), not the long “API Read Access Token”. Saving adds the sources <code>tmdb</code> and <code>tmdb-images</code> with TMDb’s public addresses and asks you to confirm the two hosts.</p>
    <Button type="submit" className="w-fit" disabled={patch.busy || !key.trim()}>Save TMDb key</Button>
    {patch.feedback}
  </form>;
}
