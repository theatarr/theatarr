// Home's "Getting ready" row (docs/design/ui.md; the API route is still GET /activity/continue): titles with a
// download in hand, as poster cards in a sideways row, each with its state in plain words and a progress bar. Under
// the card, the title (two lines at most) and the year, as every poster card has them; the version too for those who
// see files (a version key is jargon to a family account). Self-contained (its own query, refreshed live by the
// download events), so Home mounts it with one line: <ContinueRow />. It renders nothing while nothing is in
// progress. Card shape from Seerr's TitleCard via poster-card.tsx (MIT).
import { Link } from "react-router";
import { useContinue } from "@/api/activity";
import { usePerms } from "@/api/library";
import { kindLabel, ProgressBar, toneOf } from "@/components/activity-bits";
import { Section } from "@/components/page";
import { Slider } from "@/components/poster-card";
import { cn } from "@/lib/utils";

export function ContinueRow({ title = "Getting ready", limit = 20 }: { title?: string; limit?: number }) {
  const q = useContinue(limit);
  const perms = usePerms();
  const items = q.data?.items ?? [];
  if (items.length === 0) return null;
  return (
    <Section
      title={title}
      aside={
        <Link className="hover:text-foreground" to={q.data?.more ?? "/activity"}>
          See all ›
        </Link>
      }
    >
      <Slider label={title}>
        {items.map((it) => {
          const tone = toneOf({ state: it.state, step: it.step });
          return (
            <div role="listitem" key={it.id} data-testid="continue-item">
              <Link to="/activity" className="group block outline-none" aria-label={`${it.name}: ${it.words}`}>
                <div className="relative overflow-hidden rounded-xl bg-[var(--bg)] pb-[150%] shadow ring-1 ring-[var(--line)] transition duration-300 group-hover:scale-[1.04] group-hover:ring-[var(--line-2)] group-focus-visible:ring-2 group-focus-visible:ring-[var(--line-focus)]">
                  <img src={it.poster} alt="" loading="lazy" decoding="async" className="absolute inset-0 size-full object-cover" />
                  <span
                    className={cn(
                      "pointer-events-none absolute left-2 top-2 rounded-[3px] bg-[var(--art-scrim)] px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-white",
                    )}
                  >
                    {kindLabel(it.kind)}
                  </span>
                  <div className="dark absolute inset-x-0 bottom-0 bg-[linear-gradient(180deg,transparent_0%,var(--art-scrim-strong)_45%)] p-2.5 pt-8 text-white">
                    <div className="line-clamp-2 text-[13px] font-semibold leading-tight">{it.words}</div>
                    <ProgressBar pct={it.state === "queued" ? 0 : it.progress} tone={tone} className="mt-2 bg-white/20" />
                  </div>
                </div>
                <div className="mt-2 px-0.5">
                  <div className="line-clamp-2 text-[13px] font-semibold leading-snug text-[var(--ink)] [overflow-wrap:anywhere]">{it.name}</div>
                  <div className="truncate text-[11.5px] text-[var(--ink-3)]">
                    {[it.year || null, perms.seeFiles ? it.version : null, it.downloads > 1 ? `${it.downloads} downloads` : null].filter(Boolean).join(" · ")}
                  </div>
                </div>
              </Link>
            </div>
          );
        })}
      </Slider>
    </Section>
  );
}
