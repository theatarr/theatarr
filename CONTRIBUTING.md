# Contributing to Theatarr

Theatarr is an alpha. Bug reports, fixes and small focused changes are welcome. For a larger change, open an issue
first to agree on the approach before you spend time on it.

## Build and test

You need Go (the version in `go.mod`) and Node 20 for the web UI. [docs/dev.md](docs/dev.md) has the details.

```sh
make check      # go vet, the Go tests and gofmt
make lint       # staticcheck
make ui-check   # web UI: typecheck, lint and unit tests (Node 20)
make e2e        # the end-to-end pipeline tests
make race       # the Go tests under the race detector (needs cgo)
make build      # bin/theatarr (run `make ui` first to embed the UI)
make image      # a local Docker image
```

Run `make check`, `make lint` and `make ui-check` before you open a pull request, and `make e2e` and `make race` when
you change acquisition, import or concurrency code. Tests never touch the network or a live service: use fixtures
and fakes.

## Code style

- Match the code around your change: naming, comment density, error style. Run `gofmt`.
- Keep a change to one topic. No unrelated refactors in the same pull request.
- Name test files and test functions after the behaviour they cover.
- `internal/web/openapi.json` is maintained by hand. Change it, then run `cd web && npm run gen:api`.
- Generated files are regenerated with their generator, never edited by hand.
- A migration is a new numbered file in `internal/store/migrations/`. Never edit the SQL of a released migration.

## Ported code and licences

Parts of Theatarr are ports of Radarr and Sonarr (GPL-3.0) and of Mediarium (AGPL-3.0). Every ported file starts
with a header naming its source file and commit, for example
`// Port of Radarr src/NzbDrone.Core/Parser/QualityParser.cs @ a96bf7c.` Keep those headers when you edit a file. A
new port gets a header of its own and a line in [NOTICE](NOTICE).

Theatarr is licensed under AGPL-3.0 ([LICENSE](LICENSE)). By sending a contribution you agree that it is licensed
under the same terms.

## No secrets

Never commit credentials of any kind: API keys, tokens, passwords, Plex tokens, webhook URLs or `.env` files. Test
fixtures use obviously fake values (`test-key`, `0123456789abcdef...`), documentation addresses (`192.0.2.x`,
`example.com`) and invented names. Don't copy configuration, logs or databases from a real install into the
repository; trim and anonymise a sample first.

## Issues

Search the existing issues first. A good bug report says:

- the Theatarr version (`theatarr version`) and how you run it (Docker, from source);
- what you did, what you expected and what happened;
- the relevant log lines or a diagnostics bundle (`theatarr debug bundle -out FILE`, which is redacted). Check it for
  anything private before you attach it.

Report security problems privately, as [SECURITY.md](SECURITY.md) describes, not in a public issue.

## Pull requests

- Branch from `main` and keep the pull request focused.
- Say what the change does and why, and how you tested it.
- Add or update tests for behaviour you change. Don't weaken an existing test to make it pass.
- Update the docs that describe the behaviour you change.
