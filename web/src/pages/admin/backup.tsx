import { Link } from "react-router";
import { PageHeader, Section } from "@/components/page";
import { AppBackups } from "@/components/backups/app-backups";
import { TorrentBackups } from "@/components/backups/torrent-backups";
import { ShowAdvancedToggle } from "@/lib/settings-map";
import { Database } from "./advanced";
export function BackupPage() {
  return <div>
    <PageHeader crumb={[{ label: "System", to: "/admin" }]} title="Backup" actions={<ShowAdvancedToggle />} />
    <AppBackups />
    <TorrentBackups />
    <Section title="Settings export"><p>A review copy of your settings without credentials. Not a backup. <Link className="text-primary underline" to="/admin/advanced/config/import">Import &amp; export</Link></p></Section>
    <Section title="Database details"><Database /></Section>
  </div>;
}
