# Changelog

What changed in each hpctun release, newest first. The section for a
release is also its notes on the
[releases page](https://github.com/davised/hpctun/releases).

## Unreleased

### Added

- `hpctun-tray` checks for a new release once a day and offers
  **Update to v…** in its menu; one click installs it and restarts the
  tray, with tunnels left open. `update_check = false` in the config
  turns the check off.
- `hpctun self-update --json` prints the result as JSON, and
  `--expect <version>` updates only to that version.

### Changed

- `hpctun self-update` refuses to run while another update of the same
  install is running, and Ctrl-C no longer interrupts it while it swaps
  files.

### Fixed

- On Windows, `hpctun self-update` now also updates `hpctun-tray.exe`;
  before, the tray stayed on the version first installed.

## v0.20261028-188e3df (2026-10-28)

### Fixed

- Lists to choose from, such as the ssh hosts in `hpctun init`, now use
  the height of the terminal instead of stopping at ten. When a list is
  still too long, a line under it says how many there are and that you
  can scroll for more.

## v0.20261014-b54f165 (2026-10-14)

First public release.

hpctun opens, closes and monitors ssh tunnels to services on an HPC
cluster (Jupyter, RStudio, web apps on a compute node), sets up ssh
config and keys, copies files, and starts VS Code in a Slurm job. It
runs your own OpenSSH and stores no passwords or keys. See the
[README](https://github.com/davised/hpctun#readme) to get started.

### Security

- Releases are signed. `hpctun self-update` installs only a release whose
  `SHA256SUMS` is signed with hpctun's release key; see
  [Security notes](https://github.com/davised/hpctun#security-notes) for
  checking a download by hand.
- Tunnels listen only on your own computer (`localhost`).
- A shared team config cannot pass extra options to ssh.
- Downloads use HTTPS only and are limited in size.
- The VS Code CLI that `hpctun code` downloads to the cluster is checked
  against its published checksum, and checked again before each use.
- hpctun's state and config files are private to you, and hpctun refuses
  to use them when another user could change them.
