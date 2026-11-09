// The not-found and not-allowed pages. Home, the grids and the title page live in home.tsx, catalog.tsx and
// title.tsx.
import { Link } from "react-router";
import { PageHeader } from "@/components/page";
import { Button } from "@/components/ui/button";

export function NotFound() {
  return (
    <div>
      <PageHeader title="Not found" description="There is no page at this address." />
      <Button asChild>
        <Link to="/">Home</Link>
      </Button>
    </div>
  );
}

export function Forbidden({
  description = "This page is for admins. Ask one if you need something from it.",
  to = "/",
  label = "Home",
}: { description?: string; to?: string; label?: string } = {}) {
  return (
    <div data-testid="forbidden">
      <PageHeader title="Not for your account" description={description} />
      <Button asChild>
        <Link to={to}>{label}</Link>
      </Button>
    </div>
  );
}
