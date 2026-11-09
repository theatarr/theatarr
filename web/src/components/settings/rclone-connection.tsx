import { useState } from "react";
import { api } from "@/api/client";
import { useSettingsSections, type SettingsSection } from "@/api/settings";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card } from "@/components/ui/card";
import { ErrorState, Loading } from "@/components/page";
import { objectValue } from "./field-input";
import { useSectionPatch } from "./use-section-patch";

export function RcloneConnection() {
  const settings = useSettingsSections();
  if (settings.isPending) return <Loading />;
  if (settings.error) return <ErrorState error={settings.error} />;
  const section = settings.data.sections.find((s) => s.key === "library");
  return section ? <RcloneConnectionForm key={settings.data.generation} section={section} readOnly={settings.data.source !== "db"} /> : null;
}
export function RcloneConnectionForm({ section, readOnly }: { section: SettingsSection; readOnly: boolean }) {
  const rc = objectValue(objectValue(objectValue(section.config).mount).rclone_rc);
  const storedURL = typeof rc.url === "string" ? rc.url : "";
  const [editing, setEditing] = useState(!storedURL);
  const [url, setURL] = useState(storedURL);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [removeCredentials, setRemoveCredentials] = useState(false);
  const storedUsername = section.secrets.some((s) => s.path === "mount.rclone_rc.username" && s.set);
  const storedPassword = section.secrets.some((s) => s.path === "mount.rclone_rc.password" && s.set);
  const changedURL = url !== storedURL;
  const needsCredentials = changedURL && !removeCredentials && ((storedUsername && !username) || (storedPassword && !password));
  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState("");
  const patch = useSectionPatch("library", () => { setUsername(""); setPassword(""); setResult("Connection saved."); setEditing(false); });
  const test = async () => {
    setTesting(true); setResult("");
    try {
      const result = await api<{ ok: boolean; version?: string; error?: string }>("/nowplaying/rclone/test", { method: "POST", body: { url, username: removeCredentials ? "" : username, password: removeCredentials ? "" : password, use_stored_username: !changedURL && !removeCredentials && storedUsername && !username, use_stored_password: !changedURL && !removeCredentials && storedPassword && !password } });
      setResult(result.ok ? `Connected${result.version ? ` · ${result.version}` : ""}` : result.error || "Connection failed.");
    } catch { setResult("Connection test failed."); }
    finally { setTesting(false); }
  };
  let host = "";
  try { host = new URL(storedURL).host; } catch { /* An unset connection has no host. */ }
  if (readOnly && !storedURL) return null;
  if ((readOnly || !editing) && storedURL) return <p className="mb-4 text-sm">rclone: connected to {host}{!readOnly ? <> · <Button variant="ghost" size="xs" onClick={() => setEditing(true)}>Edit</Button></> : null}</p>;
  const disabled = readOnly || patch.busy || testing;
  return <Card className="mb-5 grid gap-3"><h2 className="font-semibold">Connect rclone</h2><p className="text-sm text-muted-foreground">Adds fetch speed and cache state of the playing file.</p>
    {readOnly ? <p>Read-only: these settings cannot be edited here.</p> : null}
    <label>URL<Input aria-label="rclone URL" placeholder="http://HOST:5572" value={url} disabled={disabled} onChange={(e) => { setURL(e.target.value); setResult(""); }} /></label>
    <label>Username<Input aria-label="rclone username" autoComplete="off" value={username} placeholder={section.secrets.some((s) => s.path === "mount.rclone_rc.username" && s.set) ? "Stored (leave blank to keep)" : "Optional"} disabled={disabled} onChange={(e) => setUsername(e.target.value)} /></label>
    <label>Password<Input aria-label="rclone password" type="password" autoComplete="new-password" value={password} placeholder={section.secrets.some((s) => s.path === "mount.rclone_rc.password" && s.set) ? "Stored (leave blank to keep)" : "Optional"} disabled={disabled} onChange={(e) => setPassword(e.target.value)} /></label>
    {storedUsername || storedPassword ? <label><input type="checkbox" checked={removeCredentials} disabled={disabled} onChange={(e) => setRemoveCredentials(e.target.checked)} />Remove saved credentials</label> : null}
    {needsCredentials ? <p>Changing the address needs its credentials again, or remove the saved credentials.</p> : null}
    <div className="flex gap-2"><Button variant="outline" disabled={disabled || !url || needsCredentials} onClick={() => void test()}>Test connection</Button><Button disabled={disabled || !url || needsCredentials} onClick={() => {
      const secrets: Record<string, string> = {};
      if (username && !removeCredentials) secrets["library.mount.rclone_rc.username"] = username;
      if (password && !removeCredentials) secrets["library.mount.rclone_rc.password"] = password;
      void patch.save((config) => { const mount = objectValue(config.mount); config.mount = { ...mount, rclone_rc: { ...objectValue(mount.rclone_rc), url, ...(removeCredentials ? { username_env: "", password_env: "" } : {}) } }; }, secrets, removeCredentials ? ["library.mount.rclone_rc.username", "library.mount.rclone_rc.password"] : undefined);
    }}>Save</Button></div>{result ? <p role="status">{result}</p> : null}{patch.feedback}
  </Card>;
}
