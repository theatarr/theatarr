// The tabs of Wanted (admins): Missing, Cutoff unmet and Manual import. The first two keep the library filter
// (query, "?version=..."); Manual import has its own library picker. In "View as a user" (?as=user) Manual import is
// not offered (the router does not open it either) and the other two tabs keep that view.
import { useSearchParams } from "react-router";
import { RouteTabs } from "@/components/page";

export function WantedTabs({ active, query = "" }: { active: "missing" | "cutoff" | "manual-import"; query?: string }) {
  const [sp] = useSearchParams();
  const asUser = sp.get("as") === "user";
  const q = new URLSearchParams(query);
  if (asUser) q.set("as", "user");
  const suffix = q.size ? `?${q}` : "";
  return (
    <RouteTabs
      active={active}
      tabs={[
        { id: "missing", label: "Missing", to: `/wanted${suffix}` },
        { id: "cutoff", label: "Cutoff unmet", to: `/wanted/cutoff${suffix}` },
        ...(asUser ? [] : [{ id: "manual-import", label: "Manual import", to: "/wanted/manual-import" }]),
      ]}
    />
  );
}
