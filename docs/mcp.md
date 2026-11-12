# MCP

Theatarr serves MCP revision **2025-06-18**, JSON-RPC 2.0, at `/api/theatarr/v1/mcp`. Agents can read an install,
run preflight, inspect an arr stack, save and apply migration plans, edit settings sections and view bindings, and,
through `api_request`, reach every other API route. Everything the UI can do, an API key and MCP can do: the tools
use the native API's handlers, permissions, validation and confirmations, and a key carries its owner's permissions.

Initialization answers with `2025-06-18` even when the client offers a different revision, following
[MCP lifecycle version negotiation](https://modelcontextprotocol.io/specification/2025-06-18/basic/lifecycle#version-negotiation).
The client accepts that revision or disconnects. Subsequent HTTP requests still reject an unsupported
`MCP-Protocol-Version` header with HTTP 400.

## Connect

Create an API key in Theatarr under your account. Setup, settings, migration, health, the journal and the error inbox require an
admin account's key. A viewer's key gets library versions, library search and its permitted activity only.
Store the key in a local file readable only by you, or supply it to the agent process as `THEATARR_MCP_KEY`.
Never put a literal key in a command line or a checked-in configuration.

The stdio bridge works with clients that launch MCP commands:

```sh
theatarr mcp --url http://localhost:7700 --key-file /path/to/theatarr-api-key
# Or, when the process already has THEATARR_MCP_KEY:
theatarr mcp --url http://localhost:7700 --key-env THEATARR_MCP_KEY
```

It forwards newline-delimited MCP messages to the running service; it does not open the database or load policy
files. It accepts exactly one key source, refuses HTTP redirects, writes only protocol responses to stdout and
keeps keys out of errors. A non-2xx HTTP answer (a 500 during a settings swap, a proxy's 502) fails only the
request in flight: the client gets a JSON-RPC error `-32603` naming the status and the bridge keeps reading. It exits
only when it cannot reach the service or read its input. `--url` takes the service base URL (including a reverse proxy's path prefix), or the
complete MCP endpoint. Like the migration CLI, the bridge sends its key only to loopback or the LAN (RFC 1918 or
IPv6 ULA), checking every resolved connection and using no proxy. Use HTTPS across an untrusted LAN; direct HTTP
MCP clients can also reach a remote HTTPS deployment without the bridge.

For Claude Code, register the bridge (replace paths and the URL):

```sh
claude mcp add --transport stdio theatarr -- /path/to/theatarr mcp --url http://localhost:7700 --key-file /path/to/theatarr-api-key
```

For Codex, add this to your local `~/.codex/config.toml`:

```toml
[mcp_servers.theatarr]
command = "/path/to/theatarr"
args = ["mcp", "--url", "http://localhost:7700", "--key-file", "/path/to/theatarr-api-key"]
```

For the environment alternative, replace the last two arguments with `"--key-env", "THEATARR_MCP_KEY"` and allow
that variable through the client's subprocess environment settings.

Clients supporting Streamable HTTP can connect directly. For example, a local Claude Code `.mcp.json` entry can
resolve the key from its process environment:

```json
{
  "mcpServers": {
    "theatarr": {
      "type": "http",
      "url": "http://localhost:7700/api/theatarr/v1/mcp",
      "headers": { "X-Api-Key": "${THEATARR_MCP_KEY}" }
    }
  }
}
```

The equivalent direct HTTP configuration for Codex is:

```toml
[mcp_servers.theatarr]
url = "http://localhost:7700/api/theatarr/v1/mcp"
env_http_headers = { "X-Api-Key" = "THEATARR_MCP_KEY" }
```

These are alternatives to the stdio entry, not additional entries with the same name. See the client's current
MCP documentation for version-specific configuration: [Codex MCP](https://developers.openai.com/codex/mcp).

HTTP clients send `Content-Type: application/json`, `Accept: application/json, text/event-stream`, and
`X-Api-Key`. After initialization they send `MCP-Protocol-Version: 2025-06-18`. The endpoint is stateless: there is
no session ID header, SSE stream, server push, resource list or resource reader. Requests answer JSON;
notifications (including `notifications/initialized`) answer HTTP 202 with no body and never execute a tool.
GET on the endpoint is unsupported. A browser session works too, with its normal `X-CSRF-Token` on every POST.
An Origin, when present, must match the listener's scheme and host; foreign origins and `null` are refused. A
reverse proxy terminating TLS must arrange for the backend to see the same scheme for browser-origin requests;
the stdio bridge and ordinary nonbrowser HTTP clients send no Origin.

## Tools

`tools/list` returns the tools permitted by the caller, with precise input schemas. Every tool result carries
JSON text content and matching `structuredContent`: `{ "status": 200, "data": ... }`. Status is the native API's
HTTP status; API errors, including a 422 confirmation challenge, set `isError: true`. JSON-RPC errors cover bad
arguments, unknown methods and unknown tools. Secrets are write-only; reads show set/unset markers and references.

| Tools | What they read or change |
|---|---|
| `health`, `status`, `setup` | Health checks, service/build version, setup checks and missing pieces |
| `versions`, `indexers`, `providers` | Versions and configured sources |
| `settings_sections`, `settings_views` | Settings generation, sections and arr view identities/bindings |
| `journal`, `activity` | Recent decisions/grabs and queue/download activity; normal account filtering applies |
| `library_search` | Library titles by `q`, with optional `limit` and `offset`; no metadata/indexer search |
| `errors_list`, `errors_get`, `errors_resolve` | Admin: the error inbox ([design/errors.md (internals)](design/errors.md)); list by `status` (`open` for new and seen), `source`, `since`, `limit`, `offset`; get by `id` with stack; resolve `id` with an optional `note` |
| `preflight` | Read-only install checks; `sources: false` skips source probes and Plex |
| `settings_section_update` | Section `key`, expected `generation`, whole `config` or `yaml`, optional write-only `secrets` and `clear_secrets` |
| `settings_view_create`, `settings_view_update` | View `key`, expected `generation`, identity and version/profile bindings, optional write-only key |
| `api_request` | Any other API route: `method`, `path` under `/api/theatarr/v1/` (a query is fine), optional JSON `body` and `confirmation_token` |

The write tool that the API may challenge (`settings_section_update`) and `api_request` take an
optional `confirmation_token`; see below.

`api_request` runs the request through the same routes as a direct API call, as the caller (the same key or session,
so the same permissions and confirmations). Its result is `{ "status", "data" }` like any tool, with the route's
answer as `data`. It refuses absolute URLs, `..`, percent escapes, paths outside `/api/theatarr/v1`, `/auth/*`, `/events` and the MCP
endpoint itself. Use it for the global switches (`PUT /api/theatarr/v1/settings/switches`; `settings_section_update` refuses
them), versions (create, activate, grab), providers, indexers, users, API keys,
torrent backups and restores, jobs and the rest; [openapi.json](../internal/web/openapi.json) lists the routes.
`confirmation_token` is sent as the `X-Confirmation-Token` header, which settings routes read; torrent delete and
restore take their token in the body, so put it in `body` there. A torrent delete or restore
token is the preview's, bound to the caller: preview with the same key that confirms (another key's, a session's or a
self-computed digest of the set is refused with 422), and preview again after a service restart. A GET is a read (not audited);
other methods are writes, audited like the other write tools.

## Confirmation and limits

A user's API key, and MCP on one, confirms a risky change the way a signed-in user does in the UI. Only these changes
need confirmation: making a library Live (or creating one Live), turning a Live library's downloads on, turning Allow
new downloads or Write library links on (Pause off), deletes, an arr import into a library with downloads on (or a Live
library starting to follow an arr library), and changing a provider's or indexer's address while keeping its stored
key. Everything else, views, profiles, formats, folders and other addresses included, saves at once after validation.
A change whose effects need confirmation answers 422 with the effects and a `confirmation.token`; over MCP that is the tool result
(status 422, `isError: true`). Show the effects to the user and, once they agree, call the same tool again with the
same arguments and `confirmation_token`. The token is bound to the key that got it: another key's token, a signed-in
user's token, or a revoked key's token is refused (the change answers 422 again with a token of its own, or 401 for
the revoked key). Over the API directly, send the token as `confirmation_token` in the body or the
`X-Confirmation-Token` header. Ordinary writes which the API does not challenge
need no confirmation. Removing a provider with active downloads takes `orphan_active_downloads: true`, as in the UI.

Permissions still decide: a key carries exactly its owner's permissions, so a manager's or viewer's key gets 403 on
admin routes whatever token it has. This covers global switches (`router.enabled`, `library.enabled`,
`notify.enabled`), version activation and grab, view bindings, deletes and torrent delete and restore: an admin's
key previews and confirms them like the admin in the UI.


Every authorized write attempt is recorded in the settings audit with the caller and **via MCP** (for
`api_request`, its method and path only); successful settings changes carry that actor too, and a key-confirmed
change adds ", confirmed by api key NAME". Arguments, bodies and credentials are not stored in those call records.
On a fresh install, before the first settings save, there is no settings generation to record a call at: the write
goes ahead without that call record, as the same call to the API would (a settings save writes its own audit).
MCP does not change the old services or perform the manual cutover steps; see [migration.md](migration.md).

## Setting Theatarr up with an AI agent

1. Sign in as an admin and create a key for the agent on your profile page (Your API keys), or on the
   host: `theatarr admin create-key -user admin -name agent` prints a new key once on stdout. Store it in a file only
   you can read.
2. Register the stdio bridge or a direct HTTP entry as in [Connect](#connect).
3. Ask the agent to run `preflight` and `setup` first, then to work through the missing pieces. It edits settings with
   the settings tools and reaches everything else with `api_request`.
4. When a change needs confirmation, the agent shows you its effects; agree and it sends the token back. Nothing turns
   grabbing on unless you confirm that change.
5. Revoke the key when the agent is done (`DELETE /api/theatarr/v1/keys/{id}` or the UI). A key can do whatever its
   owner can, including making and revoking keys, so give an agent the least privileged account that covers the job.
