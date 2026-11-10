import { Link } from "react-router";
import { usePerms } from "@/api/library";
import { useSetupProgress } from "@/api/useSetupProgress";
import { Card } from "@/components/ui/card";
export function SetupReminder() {
 const { admin } = usePerms();
 const progress = useSetupProgress(admin);
 if (!progress.unfinished) return null;
 return <Card className="mb-5 flex flex-wrap items-center justify-between gap-2"><p>Finish setting up Theatarr: {progress.done} of {progress.total}</p><Link className="text-primary underline" to="/admin/setup">Continue setup</Link></Card>;
}
