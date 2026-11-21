// /title/:id/releases: a title's versions (on/off, add, search now) and its "Last search" panel (the stored decision,
// read-only) on one page, for managers and admins.
import { Link, useParams, useSearchParams } from "react-router";
import { usePerms, useTitle } from "@/api/library";
import { Empty, ErrorState, Loading, PageHeader } from "@/components/page";
import { TitleThumb } from "@/components/poster-card";
import { ShowReleases } from "@/components/title/show-releases";
import { TitleToolbar } from "@/components/title/title-toolbar";
import { Card } from "@/components/ui/card";
import { Forbidden } from "./library";
import { currentLibraries } from "@/lib/library-rows";
import { titleReleaseAccess } from "@/lib/title-layout";
import { fmtDay, titleWithYear } from "@/lib/format";

export function TitleReleasesPage() {
  const rawID = useParams().id ?? "";
  const perms = usePerms();
  const access = titleReleaseAccess(rawID, perms.manage);
  const q = useTitle(rawID, access.kind === "page");
  // The library shown is the address's `version`: a tab replaces it, so the heading, the panel (closed and opened
  // again too) and Back all read the same one.
  const [params, setParams] = useSearchParams();
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
  const labels = Object.fromEntries((t.versions ?? []).flatMap((v) => (v.key ? [[v.key, v.label ?? v.key]] : [])));
  // A season's Last search (?season=N, from that season's links): its own stored search in each library.
  const rawSeason = params.get("season");
  const season = t.kind === "series" && rawSeason !== null && /^\d+$/.test(rawSeason) ? Number(rawSeason) : undefined;
  const seasonName = season === undefined ? "" : season === 0 ? "Specials" : `Season ${season}`;
  // One run's stored search (?run=R, the Libraries row's link to the run whose counts it shows).
  const rawRun = params.get("run");
  const run = rawRun !== null && /^\d+$/.test(rawRun) && Number(rawRun) > 0 ? Number(rawRun) : undefined;
  // The run belongs to the library the link named; picking another drops it (below), so that tab is headed as its own
  // Last search.
  const pinned = run !== undefined;
  const version = params.get("version") ?? undefined;
  // Another library: the run, its day and episode and the followed release were the linked library's; a season stays.
  const pick = (k: string) => {
    if (k === version) return;
    const next = new URLSearchParams({ version: k });
    if (rawSeason !== null) next.set("season", rawSeason);
    setParams(next, { replace: true });
  };
  // A pinned run is that search, not the library's last one (a later search may exist): its season and day.
  const rawAt = params.get("at");
  const ranOn = rawAt && /^\d{4}-\d{2}-\d{2}$/.test(rawAt) ? fmtDay(rawAt) : "";
  // An episode run (?episode=S02E05, for the heading only) is that episode's search, not a whole-series one.
  const rawEpisode = params.get("episode");
  const episode = !seasonName && rawEpisode && /^S\d+E\d+$/i.test(rawEpisode) ? rawEpisode.toUpperCase() : "";
  const searched = seasonName || episode;
  const subject = pinned
    ? `: the search${searched ? ` of ${searched}` : ""}${ranOn ? ` on ${ranOn}` : ""} that the Libraries row reports. `
    : seasonName ? `, ${seasonName}: the last search of this season Theatarr decided for each library. ` : ": the last search Theatarr decided for each library. ";
  return (
    <div>
      <PageHeader
        crumb={[{ label: "Library" }, { label: name, to: `/title/${id}` }]}
        title="Last search"
        description={
          <span className="flex items-center gap-3">
            <TitleThumb id={id} className="w-10" />
            <span>
              {titleWithYear(name, t.year)}
              {subject}
              {t.kind !== "series"
                ? "Interactive search searches the indexers now."
                : seasonName
                  ? "Interactive search searches the indexers now for this season."
                  : "Interactive search here searches the indexers now for packs of several seasons; a season or an episode has its own on the title page."}
            </span>
          </span>
        }
      />
      <div className="space-y-5">
        <TitleToolbar titleId={id} name={name} year={t.year} kind={t.kind === "series" ? "series" : "movie"} poster={t.poster} anime={t.anime} versions={currentLibraries(t.versions ?? [])} page="releases" />
        <ShowReleases titleId={id} kind={t.kind === "series" ? "series" : "movie"} labels={labels} defaultOpen initialVersion={version} focus={params.get("hash") ?? undefined} season={season} run={run} version={version} onVersion={pick} />
      </div>
    </div>
  );
}
