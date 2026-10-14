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
- `scripts/`: `check.sh`, `dist.sh` (release archives), `sign` (release signing key: keygen, sign, verify), `publiccheck` (leak check), `macapp`, `mkzip`.
- `sites/`: site presets for `hpctun init --from` (e.g. `osu.toml`).
- `site/`: the project web page (plain HTML, no build step).

## Conventions

- Tests never contact real hosts or run real ssh against them: use the
  fakes in `cmd/hpctun` tests (`a.run`, `a.interact`) and `httptest`.
  OpenSSH resolves `~/.ssh` from the password database, not `$HOME`, so a
  temporary HOME does not sandbox it.
- Run gofmt; keep comments explaining why, in full sentences.
- User-facing text (help, README, `site/`) is plain and factual, written
  for researchers who use a cluster, not for network specialists.

## Releases

`.github/workflows/release.yml` builds in a read-only job; a publish job
in the protected `release` environment signs `SHA256SUMS` with
`go run ./scripts/sign -sign` (secret `HPCTUN_SIGNING_KEY`) and creates
the release. `hpctun self-update` refuses a release whose
`SHA256SUMS.sig` does not verify against `selfupdate.ReleasePublicKey`.
Actions are pinned by full commit SHA; update a pin with
`git ls-remote https://github.com/actions/<name> refs/tags/<tag>`.
