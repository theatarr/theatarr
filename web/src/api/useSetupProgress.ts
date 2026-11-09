import { useMutationState } from "@tanstack/react-query";
import { useSetup, setupUnfinished, type SetupState, type PreflightReport } from "./setup";
import { useVersions, type VersionList } from "./settings-versions";

/** Readiness is a full preflight without failures in this browser session, for the current settings generation.
 * Warnings remain visible; activation is a separate, optional admin decision. */
export function setupSteps(s?: SetupState, versions?: VersionList, readiness = false) {
 return [
  !!s && !s.admin_password_generated,
  !!s && s.providers > 0 && s.indexers > 0,
  !!s && s.versions_with_roots > 0 && !(s.missing ?? []).some(m => /no debrid mount/i.test(m)),
  readiness,
  !!versions?.switches.router_enabled && versions.switches.sources?.router !== "forced off by environment" && versions.switches.versions.some(v => v.grab_state?.mode === "active" && v.grab_state.grab === true),
 ];
}
export function useSetupProgress(enabled = true) {
 const setup = useSetup(enabled);
 const versions = useVersions(enabled);
 const reports = useMutationState({ filters: { mutationKey: ["setup-preflight", versions.data?.generation], exact: true }, select: m => ({ status: m.state.status, sources: m.state.variables, report: m.state.data as PreflightReport | undefined }) });
 const latest = reports.at(-1);
 const readiness = latest?.status === "success" && latest.sources === true && latest.report?.fail === 0;
 const steps = setupSteps(setup.data, versions.data, readiness);
 return { setup, versions, steps, done: steps.filter(Boolean).length, unfinished: enabled && setupUnfinished(setup.data), report: latest?.report };
}
