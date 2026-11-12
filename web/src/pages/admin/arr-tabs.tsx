import { useLocation } from "react-router";
import { RouteTabs } from "@/components/page";
export function ArrTabs() {
  const { pathname } = useLocation();
  const tabs = [
    { id: "instances", label: "Instances", to: "/admin/advanced/arr" },
    { id: "comparison", label: "Comparison", to: "/admin/advanced/arr/comparison" },
    { id: "imports", label: "Import runs", to: "/admin/advanced/arr/imports" },
  ];
  const active = tabs.slice(1).find(t => pathname === t.to || pathname.startsWith(t.to + "/"))?.id ?? "instances";
  return <RouteTabs tabs={tabs} active={active} />;
}
