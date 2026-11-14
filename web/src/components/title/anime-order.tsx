// The anime title page's Watch order tab: each franchise that maps into the title (GET /titles/{id} franchises),
// with its orders as GET /anime/franchises/{slug} has them. Admins get a link to the franchise in Metadata review.
import { ListOrdered } from "lucide-react";
import { Link } from "react-router";
import { useFranchise } from "@/api/hooks";
import { ErrorState, Loading, Mono } from "@/components/page";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";
import { animeFormatWords, mappingWords, orderWords, roleWords } from "@/lib/anime-words";
import { dayFormatter } from "@/lib/format";


function Franchise({ slug, admin }: { slug: string; admin: boolean }) {
  const q = useFranchise(slug);
  if (q.isPending) return <Loading />;
  if (q.error) return <ErrorState error={q.error} />;
  const f = q.data;
  const orders = f.orders ?? [];
  return (
    <Card className="p-0" data-franchise={slug}>
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-4 py-3">
        <div className="flex min-w-0 items-center gap-2.5">
          <ListOrdered className="size-4 shrink-0 text-primary" />
          <span className="truncate font-semibold">{f.name}</span>
          <span className="text-xs text-muted-foreground">{f.entries} entries</span>
        </div>
        {admin ? (
          <Link to={`/admin/reviews/anime/${encodeURIComponent(slug)}`} className="text-sm text-primary hover:underline">
            Mappings and review
          </Link>
        ) : null}
      </div>
      {orders.length === 0 ? (
        <p className="p-4 text-sm text-muted-foreground">No order is built for this franchise yet.</p>
      ) : (
        <Tabs defaultValue={orders[0].kind} className="p-3">
          <TabsList className="flex-wrap">
            {orders.map((o) => (
              <TabsTrigger key={o.kind} value={o.kind ?? ""}>{orderWords(o.kind)}</TabsTrigger>
            ))}
          </TabsList>
          {orders.map((o) => {
            // One day format per order: with the year on every date once the order spans more than this year.
            const day = dayFormatter((o.items ?? []).map((it) => it.date));
            return (
            <TabsContent key={o.kind} value={o.kind ?? ""} className="mt-3">
              <ol className="divide-y divide-border rounded-lg border border-border">
                {(o.items ?? []).map((it) => {
                  return (
                    <li key={`${it.n}-${it.key}`} data-order-item={it.key} className={cn("flex items-start gap-3 px-3 py-2.5", it.optional && "opacity-80")}>
                      <span className="w-7 shrink-0 pt-0.5 text-right text-sm font-semibold tabular-nums text-muted-foreground">{it.n ?? "·"}</span>
                      <span className="min-w-0 flex-1">
                        <span className="block font-medium">{it.title}</span>
                        <span className="flex flex-wrap gap-x-2 text-xs text-muted-foreground">
                          {it.format ? <span title={it.format}>{animeFormatWords(it.format)}</span> : null}
                          {it.date ? <span>{day(it.date)}</span> : null}
                          {it.part ? <span>part {it.part}</span> : null}
                          {it.tvdb ? <Mono>{it.tvdb}</Mono> : null}
                        </span>
                      </span>
                      <span className="flex shrink-0 flex-wrap justify-end gap-1">
                        {it.optional ? <Badge>optional</Badge> : null}
                        {it.role && it.role !== "main" ? <Badge title={it.role}>{roleWords(it.role)}</Badge> : null}
                        {admin && it.mapping ? <Badge title={`Mapping status: ${it.mapping}`}>{mappingWords(it.mapping)}</Badge> : null}
                      </span>
                    </li>
                  );
                })}
              </ol>
              {(o.alternates ?? []).length > 0 ? (
                <p className="mt-2 text-xs text-muted-foreground">Alternates: {(o.alternates ?? []).map((a) => a.title).join(", ")}</p>
              ) : null}
            </TabsContent>
            );
          })}
        </Tabs>
      )}
    </Card>
  );
}

export function AnimeOrder({ franchises, admin }: { franchises: { slug: string; name: string }[]; admin: boolean }) {
  return (
    <div className="space-y-4">
      {franchises.map((f) => <Franchise key={f.slug} slug={f.slug} admin={admin} />)}
    </div>
  );
}
