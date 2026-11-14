// Activity (docs/design/ui.md): Queue, every download in hand (components/activity/queue-tab.tsx); History, what
// happened to downloads (components/activity/history-tab.tsx); Blocklist, the releases a version never routes again
// (components/activity/blocklist-tab.tsx, accounts that see files). The Decision journal is a page of System
// (/admin/system/journal, pages/admin/journal.tsx); /activity/journal redirects there (lib/legacy-routes.ts).
// Users see titles, versions and plain words; admins also see release names, providers, attempts and cache evidence.
// Live: the download events refresh it (api/events.tsx).
import { Eye } from "lucide-react";
import { Link, Navigate, useParams, useSearchParams } from "react-router";
import { useAuth } from "@/api/auth";
import { usePerms } from "@/api/library";
import { BlocklistTab } from "@/components/activity/blocklist-tab";
import { HistoryTab } from "@/components/activity/history-tab";
import { QueueTab } from "@/components/activity/queue-tab";
import { Empty, Loading, PageHeader, RouteTabs } from "@/components/page";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { parseShow, type ActivityShow } from "@/lib/activity";

export function ActivityPageView() {
  const { tab = "queue" } = useParams();
  const me = useAuth();
  const perms = usePerms();
  const [sp, setSp] = useSearchParams();
  // The file detail (release names, providers, attempts) and the blocked list are for accounts that see files.
  const isAdmin = perms.seeFiles;
  const asUser = isAdmin && sp.get("as") === "user";
  const admin = isAdmin && !asUser;
  const suffix = asUser ? "?as=user" : "";
  const show = parseShow(sp.get("show"));
  const setShow = (v: ActivityShow) => {
    const next = new URLSearchParams(sp);
    if (v === "all") next.delete("show");
    else next.set("show", v);
    setSp(next, { replace: true });
  };
  const tabs = [
    { id: "queue", label: "Queue", to: `/activity${suffix}` },
    { id: "history", label: "History", to: `/activity/history${suffix}` },
    ...(isAdmin && !asUser ? [{ id: "blocklist", label: "Blocklist", to: "/activity/blocklist" }] : []),
  ];
  // Who is looking decides the table or the simple rows: wait for it rather than draw one and swap.
  if (me.isPending) return <Loading />;
  // The user view has Queue and History only.
  if (tab === "blocklist" && asUser) return <Navigate to={`/activity${suffix}`} replace />;
  if (!["queue", "history", "blocklist"].includes(tab) || (tab === "blocklist" && !isAdmin)) {
    return (
      <div data-testid="activity-unknown-tab">
        <PageHeader title="Activity" description={`There is no Activity tab “${tab}”.`} />
        <RouteTabs tabs={tabs} active="" />
        <Card>
          <Empty>
            Activity has {tabs.map((t) => t.label).join(", ")}.{" "}
            <Link to={`/activity${suffix}`} className="text-primary hover:underline">Open Queue</Link>
          </Empty>
        </Card>
      </div>
    );
  }
  return (
    <div>
      <PageHeader
        title="Activity"
        description={
          perms.seeFiles
            ? "Downloads in progress and what happened to them, in plain words. Updates live."
            : "Where the titles you asked for are: downloads in progress and what happened, in plain words. Updates live."
        }
        actions={
          isAdmin ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                const next = new URLSearchParams(sp);
                if (asUser) next.delete("as");
                else next.set("as", "user");
                setSp(next, { replace: true });
              }}
              aria-pressed={asUser}
            >
              <Eye />
              {asUser ? "Back to the admin view" : "View as a user"}
            </Button>
          ) : null
        }
      />
      <RouteTabs tabs={tabs} active={tab} />
      {tab === "history" ? <HistoryTab admin={admin} as={asUser ? "user" : undefined} /> : tab === "blocklist" && admin ? <BlocklistTab /> : (
        <QueueTab admin={admin} as={asUser ? "user" : undefined} show={show} onShow={setShow} />
      )}
    </div>
  );
}
