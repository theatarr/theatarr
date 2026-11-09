# Security policy

## Supported versions

Theatarr is an alpha. Only the latest `main` gets security fixes. Upgrade before you report, and say which commit
you run (`theatarr version`).

## Reporting a vulnerability

Report vulnerabilities privately through GitHub's private vulnerability reporting: open the repository's
**Security** tab and choose **Report a vulnerability**. Don't open a public issue, discussion or pull request for a
security problem.

Include what is affected, how to reproduce it, and what an attacker gains. Don't include real credentials; if a
report needs a log or a diagnostics bundle, remove keys, tokens and private addresses first.

The maintainers answer once they have looked at the report. Allow time for a fix to ship before you disclose
the problem publicly.

## In scope

- Authentication, sessions, roles and permissions, and API keys, including the arr-compatible views and MCP.
- Leaks of stored credentials (debrid, indexer, Plex and arr keys) through the UI, the API, logs, errors, the
  diagnostics bundle or outbound requests.
- Ways around the confirmation of risky changes, the global switches or `THEATARR_FORCE_OFF`.
- Filesystem writes outside the configured library roots and trash, and writes into the debrid mount.
- Ways around the optional VPN egress check for the traffic classes it is set to guard.
- Server-side request forgery through settings, indexer definitions or the API.
- Cross-site scripting, CSRF and similar flaws in the web UI.

## Out of scope

- Problems in the services Theatarr connects to (debrid providers, indexers, Plex, Seerr, the arrs, zurg, rclone).
- Attacks that need an admin account or shell access to the host; an admin can change every setting by design.
- The backups and the database holding credentials in plaintext: protect the data directory and backup copies.
- `/metrics` being readable without a key when `THEATARR_METRICS` is `open` (the default); set `lan` or `key`.
- Running Theatarr on a public network without TLS. Put it behind an HTTPS reverse proxy if it is reachable from
  outside your network.
