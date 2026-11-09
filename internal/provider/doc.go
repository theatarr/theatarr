// Package provider reads debrid provider accounts (Real-Debrid, AllDebrid, TorBox) and keeps the content registry in
// step with them (docs/design/providers.md, docs/design/library.md). It is read-only.
//
// # Read-only by construction
//
// [Provider] has no method that adds, selects, restarts, deletes or unrestricts anything. The clients reach a
// provider only through getter, which builds GET requests and nothing else, and every request goes through the
// gateway (internal/gateway) as Target class provider: host allowlist, budget, back-off and, when the
// policy asks for it, the VPN guard. Tests assert there is no other HTTP method in the package and no write-like
// method on the interface. Debrid torrents are shared by every library, so the writes (add a magnet, select files)
// live in their own package, internal/provider/adder.
//
// # Secrets and links
//
// The API key goes in the Authorization header, never in a URL; the gateway masks it in logs and errors. Download
// links in provider answers are decoded only as a count (linkCount, linkPresent), so they never reach a struct that
// is logged or stored. Account answers keep no e-mail or username.
//
// # Registry sync
//
// [Syncer] lists each provider's torrents and updates the registry (migration 00006):
//   - content_provider: one row per content and provider, state present (torrent id, raw status, progress, size,
//     link count, added date) or absent (absent_at) once the torrent leaves the provider's list;
//   - content: a provider torrent with no content row gets one; then every content's state is recomputed:
//     present (on a provider, referenced by history, an arr file or a link), unreferenced (on a provider, referenced
//     by nothing), absent (on no provider, and every configured provider listed completely), unknown (not every
//     provider listed yet), gone (left alone unless a provider has it);
//   - provider_sync_run: one row per provider and run, with the JSON summary, like import_run.
//
// A re-run with nothing changed upstream changes no row besides its run rows. A list that fails part way changes
// nothing for that provider. [JobKind] runs a sync from the job queue.
//
// # Deviations
//
//   - content.state values: the design's registry has present and gone; absent and unreferenced are added here
//     (migration 00006 rebuilds the table to widen the CHECK). "Unreferenced" counts history and arr files as
//     references until the library scan writes links.
//   - Cache checks: only TorBox still has one. Real-Debrid's instantAvailability answers disabled_endpoint since
//     November 2024, and AllDebrid removed magnet/instant in 2024. See docs/design/providers.md.
//   - AllDebrid's magnet list has no paging (one answer per account); RD and TorBox page by offset.
package provider
