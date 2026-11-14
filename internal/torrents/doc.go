// Package torrents is the provider torrent manager (docs/design/torrents.md): for every configured debrid account
// (Real-Debrid, AllDebrid, TorBox) it reports each torrent with what uses it, backs the lists up, restores them, and
// deletes the torrents a person picks, like Debrid Media Manager's library view.
//
// # Usage
//
// A torrent is used when any of its files is referenced: an active or dead library link of Theatarr (by the content's or
// the download's hash, or by the mount folder its target names), a current arr file (arr import), a download that
// is queued or in flight, or a symlink into the debrid mount under one of torrents.scan_roots (libraries other tools
// manage). It is unknown when Theatarr lacks the data: the provider was never listed completely, the hash is not in the
// registry yet, a scan root could not be read, or library links could not be tied to a torrent. Only the rest is
// unused, and only unused torrents are offered for cleanup without an override.
//
// # The record
//
// Reports read the record (record.go: provider_torrent, written by the provider sync), never the providers; Refresh
// runs the sync now. Sources and Saved inventory take their "used" from the same record and reference index
// (RecordUsage).
//
// # Writes
//
// Reports read the record; backups only read (provider.Provider, GET through the gateway). The writes go through
// internal/provider/adder, the only package besides the router allowed to import it (boundary_test.go):
//   - a restore re-adds every hash of a backup with adder.Restore (a bare magnet), paced by the router's pacing rows (router.ReserveAdd) and the gateway budgets;
//   - a delete removes only the torrents a person chose and confirmed (adder.ChooseForDelete,
//     adder.UserDeleter.DeleteUserChosen), after a fresh list (same id, same hash), a fresh usage check, the 24-hour
//     and still-downloading guards, and a full backup; no backup, no delete. The confirmation is bound to the exact
//     set (count, size and a digest of the ids, hashes, usage and overrides). Every delete is journaled
//     (torrent_journal: who, when, hash, name, backup).
//
// Nothing here deletes on its own, and none of it needs router.enabled: a restore is not a grab.
package torrents
