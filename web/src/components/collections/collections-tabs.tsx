// The tabs of Collections (managers and admins): Collections and Lists, both things that add movies by themselves.
import { RouteTabs } from "@/components/page";

export function CollectionsTabs({ active }: { active: "collections" | "lists" }) {
  return <RouteTabs active={active} tabs={[
    { id: "collections", label: "Collections", to: "/collections" },
    { id: "lists", label: "Lists", to: "/collections/lists" },
  ]} />;
}
