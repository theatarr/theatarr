// The tabs of Wanted (admins): Missing, Cutoff unmet and Manual import. The first two keep the library filter
// (query, "?version=..."); Manual import has its own library picker.
import { RouteTabs } from "@/components/page";

export function WantedTabs({ active, query = "" }: { active: "missing" | "cutoff" | "manual-import"; query?: string }) {
  return (
    <RouteTabs
      active={active}
      tabs={[
        { id: "missing", label: "Missing", to: `/wanted${query}` },
        { id: "cutoff", label: "Cutoff unmet", to: `/wanted/cutoff${query}` },
        { id: "manual-import", label: "Manual import", to: "/wanted/manual-import" },
      ]}
    />
  );
}
