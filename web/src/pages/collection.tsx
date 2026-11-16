// /collections/{id}: one TMDb collection's films, as the last list refresh stored them (GET /collections/{id}, no
// TMDb call). A film in the library links to its title page with its status; one that is not opens Add (or Request).
// Edit holds the collection's settings (monitoring, libraries, search on add, minimum availability), which save at
// once; "Add missing movies now" is here as on the list. Managers and admins.
import { useState } from "react";
import { useParams } from "react-router";
import { Library, Pencil } from "lucide-react";
import { refreshWords, useCollection, useRefreshCollection, type CollectionPart } from "@/api/collections";
import { useAuth } from "@/api/auth";
import { permsOf } from "@/api/library";
import { CollectionSettings, collectionCounts, collectionName } from "@/components/collections/collection-settings";
import { Empty, ErrorState, Loading, PageHeader } from "@/components/page";
import { PosterCard } from "@/components/poster-card";
import { useSearchUI } from "@/components/search/search-context";
import { Chip } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { AVAILABILITY } from "@/components/collections/add-collection";
import { posterStatus } from "@/lib/library";
import { addMissingLabel } from "./collections";
import { Forbidden } from "./library";

function PartCard({ p }: { p: CollectionPart }) {
  const { openAdd, canAdd, canRequest } = useSearchUI();
  if (p.title_id) return <PosterCard to={`/title/${p.title_id}`} title={p.title} year={p.year || undefined} image={p.poster} overview={p.overview}
    status={posterStatus(p.availability)} />;
  const action = canAdd ? "Add" : canRequest ? "Request" : "";
  const open = action ? () => openAdd({ kind: "movie", name: p.title, year: p.year || undefined, poster: p.poster, overview: p.overview, tmdb_id: p.tmdb_id }) : undefined;
  // A film removed from every library is added back from here like any other (Add missing movies now skips it).
  const where = p.removed ? "Removed from the library" : "Not in the library";
  return <PosterCard onClick={open} title={p.title} year={p.year || undefined} image={p.poster} overview={p.overview}
    extraLabel={`${where.toLowerCase()}${action ? `, ${action.toLowerCase()}` : ""}`}
    caption={<span data-testid="part-not-held">{where}{action ? <> · <span className="text-[var(--accent-ink)]">{action}</span></> : null}</span>} />;
}

export function CollectionPage() {
  const id = Number(useParams().id);
  const me = useAuth();
  const perms = permsOf(me.data?.permissions);
  const q = useCollection(id, perms.manage);
  const refresh = useRefreshCollection();
  const [editing, setEditing] = useState(false);
  const [done, setDone] = useState("");
  if (me.isPending) return <Loading />;
  if (!perms.manage) return <Forbidden description="Collections are for managers and admins." />;
  const d = q.data;
  const c = d?.collection;
  const name = c ? collectionName(c) : "Collection";
  const libs = c ? c.versions.map(k => d?.libraries[k] ?? k) : [];
  const availability = AVAILABILITY.find(([v]) => v === c?.minimum_availability)?.[1] ?? "TBA";
  return <div>
    <PageHeader crumb={[{ label: "Collections", to: "/collections" }]} title={name}
      description={c ? (c.name ? collectionCounts(c) : "Name and movies arrive with the next list refresh.") : undefined}
      actions={c ? <>
        {c.monitored && d?.can_add ? <Button variant="outline" disabled={refresh.isPending}
          onClick={() => refresh.mutate(c.tmdb_id, { onSuccess: r => setDone(refreshWords(r)) })}>{addMissingLabel(c)}</Button> : null}
        <Button variant="outline" onClick={() => setEditing(true)}><Pencil aria-hidden />Edit</Button>
      </> : null} />
    {q.isLoading ? <Loading /> : q.error ? <ErrorState error={q.error} /> : d && c ? <div className="space-y-4">
      <p className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground" data-testid="collection-summary">
        <Chip tone={c.monitored ? "ok" : "ghost"}>{c.monitored ? "Monitored" : "Not monitored"}</Chip>
        {c.monitored && libs.length === 0 ? <span>No library is chosen, so no movies are added; choose one under Edit.</span>
          : c.monitored ? <span>Adds missing movies to {libs.join(", ")}{c.search_on_add ? " and searches for them" : ""}; minimum availability {availability}.</span>
          : <span>Missing movies are not added by themselves.</span>}
      </p>
      {done ? <p role="status" className="text-sm text-[var(--ink)]" data-testid="collections-done">{done}</p> : null}
      {refresh.error ? <ErrorState error={refresh.error} /> : null}
      {d.parts.length === 0 ? <Card><Empty icon={Library}>No movies known yet. They arrive with the next list refresh.</Empty></Card> :
        <ul className="grid grid-cols-[repeat(auto-fill,minmax(132px,1fr))] gap-4 sm:grid-cols-[repeat(auto-fill,minmax(150px,1fr))]" aria-label={`Movies in ${name}`}>
          {d.parts.map(p => <li key={p.tmdb_id} data-testid={`part-${p.tmdb_id}`}><PartCard p={p} /></li>)}
        </ul>}
      {editing ? <CollectionSettings c={c} libraries={d.libraries} onClose={() => setEditing(false)} /> : null}
    </div> : null}
  </div>;
}
