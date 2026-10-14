# Agent instructions

hpctun is a Go command-line tool (and menu-bar app) that manages ssh
tunnels, ssh config and file copies for HPC clusters. It runs the user's
own OpenSSH; it never stores credentials.

## Build and test

    CGO_ENABLED=0 scripts/check.sh

That is the whole gate: gofmt, `go vet`, `go test ./...`, and a Windows and
macOS cross-vet. Nothing needs cgo, the macOS tray included.

## Layout

- `cmd/hpctun`: the CLI. `cmd/hpctun-tray`: the menu-bar app.
- `internal/`: one package per concern (`tunnel`, `sshsetup`, `sshkey`,
  `copycmd`, `slurmjob`, `vscode`, `selfupdate`, …).
- `scripts/`: `check.sh`, `dist.sh` (release archives), `sign` (release signing key: keygen, sign, verify), `changelog` (release notes from CHANGELOG.md), `publiccheck` (leak check), `macapp`, `mkzip`.
- `sites/`: site presets for `hpctun init --from` (e.g. `osu.toml`).
- `site/`: the project web page (plain HTML, no build step).

## Conventions

- Tests never contact real hosts or run real ssh against them: use the
  fakes in `cmd/hpctun` tests (`a.run`, `a.interact`) and `httptest`.
  OpenSSH resolves `~/.ssh` from the password database, not `$HOME`, so a
  temporary HOME does not sandbox it.
- Run gofmt; keep comments explaining why, in full sentences.
- User-facing text (help, README, `site/`, `CHANGELOG.md`) is plain and
  factual, written for researchers who use a cluster, not for network
  specialists.

## Changelog

Every change a user would notice (a new command or option, changed
behavior or output, a bug fix, a security fix) adds a bullet under
`## Unreleased` in `CHANGELOG.md`, in the same commit as the change.
Changes users cannot see (tests, refactors, CI, internal docs) do not.

- Group entries under `### Added`, `### Changed`, `### Fixed` or
  `### Security`, creating a group when its first entry arrives.
- Write each entry as one or two plain sentences about what the user
  sees, not how the code changed: "`hpctun down` no longer waits for a
  tunnel that already closed", not "fix race in tunnel.Stop".
- Never edit a released section; a later fix gets its own entry.

## Releases

`.github/workflows/release.yml` builds in a read-only job; a publish job
in the protected `release` environment signs `SHA256SUMS` with
`go run ./scripts/sign -sign` (secret `HPCTUN_SIGNING_KEY`) and creates
the release. The release notes are `CHANGELOG.md`'s Unreleased section
(`go run ./scripts/changelog -notes`); the workflow fails before the
approval step if that section has no entries. To release:

1. Read the Unreleased section as a user would and tidy it.
2. Run the release workflow on main (Actions > release > Run workflow),
   and approve the publish job once the build passes.
3. Stamp the changelog with the version the release got:
   `go run ./scripts/changelog -stamp <version>`, committed as
   `changelog: <version>`. This starts a new, empty Unreleased section.

`hpctun self-update` refuses a release whose
`SHA256SUMS.sig` does not verify against `selfupdate.ReleasePublicKey`.
Actions are pinned by full commit SHA; update a pin with
`git ls-remote https://github.com/actions/<name> refs/tags/<tag>`.
