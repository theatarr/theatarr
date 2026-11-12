import { useMutationState } from "@tanstack/react-query";
import { useSetup, setupLinkShown, type SetupState, type PreflightReport } from "./setup";
import { useVersions, type VersionList } from "./settings-versions";

/** The six steps of Admin › Setup, in order: admin password, downloads, TMDb, library, readiness, activation.
 * TMDb is done when GET /setup says its key resolves, from the environment or from settings.
 * Readiness is a full preflight without failures for the current settings generation: the latest run in this browser
 * session, else the server's latest run (GET /setup last_preflight, kept until the server restarts), so a reload keeps
 * the step. Warnings remain visible; activation is a separate, optional admin decision. */
export function setupSteps(s?: SetupState, versions?: VersionList, readiness = false) {
 return [
  !!s && !s.admin_password_generated,
  !!s && s.providers > 0 && s.indexers > 0,
  !!s && s.tmdb === true,
  !!s && s.versions_with_roots > 0 && !(s.missing ?? []).some(m => /no debrid mount/i.test(m)),
  readiness,
  !!versions && !versions.switches.paused && !versions.switches.pause_forced && versions.switches.versions.some(v => v.grab_state?.mode === "active"),
 ];
}
/** A library is Active, as saved or as running: the install went live at some point, whatever Pause
 * says now and whether or not a startup fallback runs older settings. */
export const anyActive = (versions: VersionList) => versions.versions.some(v => v.config.mode === "active") || versions.switches.versions.some(v => v.grab_state?.mode === "active");
export function useSetupProgress(enabled = true) {
 const setup = useSetup(enabled);
 const versions = useVersions(enabled);
 const reports = useMutationState({ filters: { mutationKey: ["setup-preflight", versions.data?.generation], exact: true }, select: m => ({ status: m.state.status, sources: m.state.variables, report: m.state.data as PreflightReport | undefined }) });
 const latest = reports.at(-1);
 const server = setup.data?.last_preflight;
 const readiness = latest
  ? latest.status === "success" && latest.sources === true && latest.report?.fail === 0
  : !!server && server.sources && server.fail === 0 && versions.data !== undefined && server.generation === versions.data.generation;
 const steps = setupSteps(setup.data, versions.data, readiness);
 return { setup, versions, steps, done: steps.filter(Boolean).length, total: steps.length, unfinished: enabled && setupLinkShown(setup.data, versions.data && anyActive(versions.data), readiness), report: latest?.report, last: !latest && server && server.generation === versions.data?.generation ? server : undefined };
}
