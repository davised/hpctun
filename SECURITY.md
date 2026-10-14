# Security policy

## Reporting a vulnerability

Please report security problems privately: on GitHub, open the repository's
**Security** tab and choose **Report a vulnerability**. Do not open a public
issue for them. Say what you found, how to reproduce it, and which hpctun
version (`hpctun --version`) and operating system you used.

hpctun is maintained on a best-effort basis; reports are read and answered
as soon as possible, and fixes go out as a new release.

## Supported versions

Only the latest release gets fixes. `hpctun self-update` installs it.

## Scope

hpctun runs your own OpenSSH, and `hpctun code` runs Microsoft's VS Code
CLI. Problems in OpenSSH, VS Code, the VS Code tunnel service, or your
cluster's configuration belong to those projects or to your site's
administrators; hpctun issues are about what hpctun itself does.
