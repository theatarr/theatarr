// /title/:id/releases: a title's versions (on/off, add, search now) and its "Last search" panel (the stored decision,
// read-only) on one page, for managers and admins.
import { Link, useParams, useSearchParams } from "react-router";
import { usePerms, useTitle } from "@/api/library";
import { Empty, ErrorState, Loading, PageHeader } from "@/components/page";
import { TitleThumb } from "@/components/poster-card";
import { ShowReleases } from "@/components/title/show-releases";
import { TitleAddActions } from "@/components/title/title-add-actions";
import { Card } from "@/components/ui/card";
import { Forbidden } from "./library";
import { titleReleaseAccess } from "@/lib/title-layout";

export function TitleReleasesPage() {
  const rawID = useParams().id ?? "";
  const perms = usePerms();
  const access = titleReleaseAccess(rawID, perms.manage);
  const q = useTitle(rawID, access.kind === "page");
  const [params] = useSearchParams();
  if (access.kind === "not-found") {
    return (
      <Card className="mt-6">
        <Empty>There is no title {rawID}. <Link to="/movies" className="text-primary hover:underline">Library</Link></Empty>
      </Card>
    );
  }
  if (access.kind === "forbidden") {
    return <Forbidden description="Release management is for managers and admins." to={`/title/${access.id}`} label="Title availability" />;
  }
  const id = access.id;
  if (q.isPending) return <Loading />;
  if (q.error) return <ErrorState error={q.error} />;
  const t = q.data;
  const name = t.name ?? `Title ${id}`;
  return (
    <div>
      <PageHeader
        crumb={[{ label: "Library" }, { label: name, to: `/title/${id}` }]}
        title="Last search"
        description={
          <span className="flex items-center gap-3">
            <TitleThumb id={id} className="w-10" />
            <span>
              {name}
              {t.year ? ` (${t.year})` : ""}: the last search Theatarr decided for each library. Interactive search searches the indexers now.
            </span>
          </span>
        }
      />
      <div className="space-y-5">
        <TitleAddActions titleId={id} name={name} year={t.year} kind={t.kind === "series" ? "series" : "movie"} poster={t.poster} anime={t.anime} />
        <ShowReleases titleId={id} kind={t.kind === "series" ? "series" : "movie"} defaultOpen initialVersion={params.get("version") ?? undefined} focus={params.get("hash") ?? undefined} />
      </div>
    </div>
  );
}
