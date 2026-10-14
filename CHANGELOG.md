# Changelog

What changed in each hpctun release, newest first. The section for a
release is also its notes on the
[releases page](https://github.com/davised/hpctun/releases).

## Unreleased

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
