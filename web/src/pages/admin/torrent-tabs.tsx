import { useLocation } from "react-router";
import { RouteTabs } from "@/components/page";
export function TorrentTabs() {
  const { pathname } = useLocation();
  return <RouteTabs active={pathname.includes("/inventory") ? "inventory" : "providers"} tabs={[
    { id: "providers", label: "Provider torrents", to: "/admin/torrents" },
    { id: "inventory", label: "Saved inventory", to: "/admin/torrents/inventory" },
  ]} />;
}
