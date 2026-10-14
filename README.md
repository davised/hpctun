# hpctun

`hpctun` opens, closes and monitors ssh port forwards through a gateway host.
Instead of hand-typing `ssh -Nf -L ...` and hunting for stray `lsof` output,
you name your tunnels once in a config file (or open them ad-hoc) and then get
`hpctun up`, `hpctun down` and `hpctun status` — one table that says what is open, where
each port goes, and whether anything is connected to it. `hpctun` wraps your own
`ssh`: every connection is made with the OpenSSH client you already have, so
your keys, your ssh-agent and everything in `~/.ssh/config` apply unchanged,
and `hpctun` stores no passwords or keys (see [Security notes](#security-notes)).

## Contents

- [Install](#install)
- [Updating](#updating)
- [Quick start](#quick-start)
- [Config](#config)
- [Routes: which one does my service need?](#routes-which-one-does-my-service-need)
- [Tunnels on their own connection](#tunnels-on-their-own-connection)
- [Ad-hoc tunnels](#ad-hoc-tunnels)
- [Opening a tunnel's page](#opening-a-tunnels-page)
- [Scripting with --json](#scripting-with---json)
- [Menu-bar app](#menu-bar-app)
- [Logging in and copying files](#logging-in-and-copying-files)
  - [Tab completion](#tab-completion)
- [VS Code on a compute node](#vs-code-on-a-compute-node)
- [Sharing a team config](#sharing-a-team-config)
- [How it works](#how-it-works)
- [Recommended ssh settings](#recommended-ssh-settings)
- [Troubleshooting](#troubleshooting)
- [Example: OSU HPC](#example-osu-hpc)
- [Logging in with a key](#logging-in-with-a-key)
- [Windows](#windows)
  - [Tunnels on Windows](#tunnels-on-windows)
- [Security notes](#security-notes)
- [How hpctun is made](#how-hpctun-is-made)
- [No warranty](#no-warranty)
- [License](#license)

# Install

<sub>[↑ Contents](#contents)</sub>

Download the archive for your computer from the
[releases page](https://github.com/davised/hpctun/releases):
`hpctun-<version>-darwin-arm64.tar.gz` (Apple silicon Mac),
`-darwin-amd64.tar.gz` (Intel Mac), `-linux-amd64.tar.gz`, and
`-windows-amd64.zip` (`-windows-arm64.zip` on ARM laptops). Unpack it and
put `hpctun` (`hpctun.exe` on Windows) somewhere on your PATH, such as
`~/.local/bin`. `SHA256SUMS` on the same page lists each file's checksum,
and `SHA256SUMS.sig` is the release key's signature of that list;
[Security notes](#security-notes) shows how to check a download.

On a Mac, download with `curl` rather than a browser: macOS refuses to run
a program a browser downloaded unless it is signed by a registered Apple
developer, and `hpctun` is not.

```sh
v=$(curl -fsSL https://github.com/davised/hpctun/releases/latest/download/VERSION)
curl -fLO "https://github.com/davised/hpctun/releases/download/$v/hpctun-$v-darwin-arm64.tar.gz"
```

If you did download it in a browser, check it against `SHA256SUMS` first (see
[Security notes](#security-notes)), then clear the flag with
`xattr -d com.apple.quarantine ~/.local/bin/hpctun`, or allow it once under
System Settings > Privacy & Security > Open Anyway.

`hpctun-tray-<version>-darwin-universal.zip` holds the menu-bar app with
`hpctun` inside it, for Apple silicon and Intel alike; take it instead of
the tarball if you want the menu (see [Menu-bar app](#menu-bar-app)).
The command-line archives hold the site presets, such as `osu.toml`, next
to `hpctun` in the unpacked folder; in the menu-bar app they are in
`hpctun-tray.app/Contents/`, so tray users run
`hpctun init --from /Applications/hpctun-tray.app/Contents/osu.toml` (see
[Quick start](#quick-start)).

Or build from a checkout with Go 1.26.9 or newer:

```sh
go build -o ~/.local/bin/hpctun ./cmd/hpctun
```

`hpctun --version` prints a version like `v0.20260924-1dac96b`: the date (UTC)
and short hash of the commit it was built from, with `-dirty` when the
checkout had uncommitted changes.

Requirements: an OpenSSH client and `lsof`. Both are standard on
macOS and Linux. On Windows, each tunnel signs in by itself and needs a key in
the ssh agent: see [Windows](#windows).

# Updating

<sub>[↑ Contents](#contents)</sub>

    hpctun self-update          # update to the latest release
    hpctun self-update --check  # see what would happen

hpctun downloads the latest release from GitHub over HTTPS (no login
needed). Before replacing anything it checks that the release's SHA256SUMS
is signed with hpctun's release key (the public half is built into
hpctun), that the download matches SHA256SUMS, and that the new hpctun
runs. A release without a valid signature is refused, and nothing is
changed. With `hpctun-tray` next to
`hpctun`, both are updated; on a Mac with the tray app, the whole app is.
If the latest release is older than yours (it was rolled back),
self-update moves you back to it and says so. A build from a checkout is
only replaced with `--force`.

# Quick start

<sub>[↑ Contents](#contents)</sub>

At OSU, run this from the unpacked folder: the wizard starts with
`shell.hpc.oregonstate.edu` as the gateway and `files.hpc.oregonstate.edu`
as the transfer host, and asks for your username.

```sh
hpctun init --from osu.toml
```

In the menu-bar app the preset is inside the app bundle:
`hpctun init --from /Applications/hpctun-tray.app/Contents/osu.toml`.

Elsewhere, `hpctun init` asks for your own gateway. Then:

```sh
hpctun add         # add a tunnel (or Yes at "Set up tunnels? (advanced)" during init)
hpctun up --all    # open every tunnel (safe to re-run any time)
hpctun status      # what is open, where it goes, who is using it
hpctun down --all  # close them again
```

`hpctun status` prints a gateway header and one row per port:

```
gateway gw.example.net: running (pid 86660, ssh-config)

NAME      PORT   ROUTE   TARGET           STATE     CONNS
jupyter   4001   jump    dev1:4001        up        0
dash      8099   master  dev1:8099        up        2
socks     5005   socks   -                down      -
(ad-hoc)  4002   master  dev1:4002        up        1
-         43723  ?       ssh pid 25301    external  0
```

States: `up` (listening, owned as expected), `down` (not listening),
`conflict` (held by something that should not hold it — the note under the
table names the owner), and `external` (an ssh forward `hpctun` did not open;
`hpctun` never touches those). `CONNS` counts established connections to the
local port.

# Config

<sub>[↑ Contents](#contents)</sub>

`hpctun` reads its config from the first of:

1. `--config <file>`
2. `$HPCTUN_CONFIG`
3. `$XDG_CONFIG_HOME/hpctun/config.toml`
4. `~/.config/hpctun/config.toml`

An annotated example:

```toml
# Any ssh destination: an alias from ~/.ssh/config, a hostname, or user@host.
gateway = "gw.example.net"

# Target host for tunnels that do not name one. Optional.
default_host = "dev1"

# A service reachable directly from the gateway: forwarded on the
# gateway's own connection (the default route).
[tunnel.dash]
port = 8099

# A service that listens only on localhost on the remote host needs its
# own ssh connection to that host, through the gateway.
[tunnel.jupyter]
port = 4001
route = "jump"

# A tunnel aimed at a different host than default_host, on a different
# remote port.
[tunnel.web]
port = 43723
host = "web1"
remote_port = 8080

# A dynamic SOCKS proxy, added to the gateway connection.
[tunnel.socks]
socks = 5005
```

Top-level fields:

| Field | Required | Meaning |
|---|---|---|
| `gateway` | For `up`; not for `status` | ssh destination of the gateway. |
| `tunnel_gateway` | No | ssh destination tunnels go through (up, down, status); defaults to gateway. See "Tunnels on their own connection". |
| `default_host` | No | Target host when a tunnel has no `host`. |

Per-tunnel fields (`[tunnel.<name>]`; names match `[a-z0-9_-]+`):

| Field | Default | Meaning |
|---|---|---|
| `port` | — | Local port. Required unless `socks` is set. |
| `remote_port` | `port` | Port on the target host. |
| `host` | `default_host` | Target host, as resolved from the gateway. Required if there is no `default_host`. |
| `route` | `master` | `master` or `jump`. |
| `socks` | — | Local port for a SOCKS proxy. Mutually exclusive with `port`. |
| `url` | — | Page `hpctun browse` opens: an `http` or `https` URL. |
| `browse` | `true` | `false` for a tunnel with no page (an API for tools): no "Open in browser" item, and `hpctun browse` needs a URL argument. Not combined with `url`. |

Bad configs are rejected at load with an error naming the tunnel and field
(duplicate local ports, unknown `route`, `route`/`host`/`remote_port` on a
socks tunnel, ports outside 1–65535, and so on), and `hpctun` exits 2.

A missing config file is not an error for `hpctun status`, which then shows only
the ssh listeners it can observe. `up`, `add` and `save` without a gateway
exit 2 with "no gateway configured; run `hpctun init`".

`hpctun` appends to your config and never rewrites it: `add`, `save` and
`up --save` append a `[tunnel.<name>]` block as text, so your comments and
formatting always survive. `hpctun init` writes a new file from a commented
template. Anything that writes parses the combined text first, so an invalid
change is refused and nothing is written.

# Routes: which one does my service need?

<sub>[↑ Contents](#contents)</sub>

| Route | Use when | How |
|---|---|---|
| `master` (default) | The remote service listens on more than loopback | A forward is added to the gateway's own (multiplexed) connection: `ssh -O forward`. No extra ssh process. |
| `jump` | The remote service listens on loopback **only** | A dedicated `ssh` process connects to the target *through* the gateway (as a ProxyCommand) and forwards from there. |
| `socks` | You want a dynamic SOCKS proxy (browser, `curl --socks5`) | A dynamic `-D` forward on the gateway connection. |

A forward added on the gateway can only reach an address the gateway itself
can connect to — and a service bound to `127.0.0.1` on the target is not
reachable from outside that machine. That is the whole difference between
`master` and `jump`.

To tell which one a service needs, look at its listen address on the remote
host:

```sh
ssh dev1 ss -ltn | grep 4001
```

- `127.0.0.1:4001` (or `[::1]:4001`) — loopback-only. Use `route = "jump"`
  in config, or `--loopback` on an ad-hoc `hpctun up`. Jupyter and most
  dev-server tools are like this.
- `0.0.0.0:4001` or `*:4001` — reachable from the gateway. The default
  `master` route works and is cheaper (no per-tunnel ssh process). But
  every other machine and user on the cluster's network can reach it
  too, so use it only for services that ask for their own password or
  token.

For an app you start yourself on a compute node (a dashboard, Shiny,
TensorBoard, a development server), start it on `127.0.0.1` and use the
jump route: then only programs on that node can reach it, not the whole
cluster.

Two consequences worth knowing: jump tunnels ride their own ssh connection,
so they keep their target's settings rather than the gateway's; and master
and socks forwards live on the gateway connection, so they close when that
connection ends — `hpctun status` shows this, and `hpctun up --all` restores them.

# Tunnels on their own connection

<sub>[↑ Contents](#contents)</sub>

By default tunnels ride the gateway's shared connection, together with your
logins (`ssh shell`, `ssh node1`) and anything else using it. Give them a
connection of their own so logging in and out, a connection that drops, or a
big copy never disturbs them:

```toml
gateway        = "shell"
tunnel_gateway = "shell-tunnels"

[ssh]
user                = "me"
gateway_host        = "shell.example.org"
tunnel_gateway_host = "shell.example.org"
```

With `tunnel_gateway_host`, hpctun defines `shell-tunnels` in `~/.ssh/hpctun_config`
with its own control socket (`~/.ssh/sockets/tunnels-%C`), so it is a second
connection even to the same host. `hpctun init` and `hpctun ssh-config setup` ask
about this under "Set up tunnels? (advanced)". `hpctun up` opens the tunnel
connection when needed; `hpctun login shell-tunnels` opens it ahead of time
(for example to answer a Duo prompt now), and `hpctun status` shows both.
`tunnel_gateway` may also be any ssh destination you configure yourself;
leave out `tunnel_gateway_host` then.

# Ad-hoc tunnels

<sub>[↑ Contents](#contents)</sub>

Need a forward once, without editing the config? Give `hpctun up` a bare port:

```sh
hpctun up 4002 --host web1                    # web1:4002 on localhost:4002
hpctun up 4002 --host web1 --loopback         # web1 listens only on localhost there
hpctun up 4002 --host web1 --remote-port 8080 # web1:8080 on localhost:4002
hpctun up --socks 5005                        # a SOCKS proxy
```

Ad-hoc tunnels are recorded in the state file, so `hpctun status` shows them as
`(ad-hoc)` and `hpctun up --all` restores them. When you open one from a
terminal, `hpctun` then asks:

```
Save as a named tunnel? Name (blank to skip):
```

Typing a name appends the tunnel to your config, so it becomes permanent;
blank keeps it ad-hoc. Skip the question entirely with `--save <name>` (save
directly) or `--no-save`; in scripts (no terminal) `hpctun` never asks. To keep
an ad-hoc tunnel you already opened, use `hpctun save <port> <name>`. To define
a tunnel without opening it, use `hpctun add`:

```sh
hpctun add jupyter2 --port 4003 --host web1 --loopback
hpctun add proxy --socks 5006
```

`hpctun add` with no flags asks the same questions `hpctun init` asks per tunnel.
In both, Esc goes back to the previous question, Tab completes host names,
and Ctrl-C quits without writing anything. A bare number in `hpctun up`/`hpctun down` means the named tunnel with that local
port if one exists, otherwise the ad-hoc tunnel on it.

# Opening a tunnel's page

<sub>[↑ Contents](#contents)</sub>

    hpctun browse jupyter          # http://localhost:<port>/, or the tunnel's url
    hpctun browse proxy            # the tunnel's url, or an empty window
    hpctun browse proxy https://intranet.example.org

`hpctun browse` opens the tunnel first if it is not up. A port forward
opens in your default browser. A SOCKS tunnel opens in a Chrome, Edge,
Brave or Chromium window of its own (a separate profile under hpctun's
state directory) that sends its web traffic, host names included, through the
proxy (Chromium still connects to `localhost` and link-local addresses
directly); your other browser windows are not affected, and the profile keeps
its logins between runs. `HPCTUN_BROWSER` names another Chromium browser.
Firefox users set its SOCKS proxy instead (SOCKS v5, with "Proxy DNS when
using SOCKS v5"). On Windows it finds Edge, Chrome, Brave or Chromium in
their usual install folders.

# Scripting with --json

<sub>[↑ Contents](#contents)</sub>

`hpctun status`, `hpctun up`, `hpctun down` and `hpctun browse` take `--json` for programs
(a menu-bar app, an editor extension, a script) that would rather not read
tables. With it, stdout holds exactly one line of JSON; everything else
(`-v` logs, notes, the error message) still goes to stderr, and the exit
status is the same as without it: 0 ok, 1 something failed, 2 bad usage or
config.

```sh
hpctun status --json | jq '.tunnels[] | select(.state == "up") | .port'
```

A status document, trimmed:

```json
{
  "version": 1,
  "config": "/Users/me/.config/hpctun/config.toml",
  "gateway": {"dest": "hpc", "tunnels_only": false, "mode": "managed", "running": true,
              "pid": 4242, "stale": false, "error": null, "hints": []},
  "login": null,
  "transfer": null,
  "code": null,
  "tunnels": [
    {"kind": "named", "name": "jupyter", "port": 4001, "route": "jump", "target": "node1:4001",
     "state": "up", "conns": 0, "owner": null, "warning": null, "url": null,
     "browse": true}
  ]
}
```

A tunnel row's `url` is the `url` configured for that named tunnel, or null;
`browse` is false for a tunnel with `browse = false`.

A `url` is saved in `config.toml` exactly as you wrote it, and printed back
verbatim here and by `browse --json`. A URL that carries a token or password —
a Jupyter URL with `?token=...`, for example — is therefore written to disk and
appears in that output: do not save such a URL in a config you share, and treat
`--json` output that contains one as a secret.

`up` and `down` print one result per tunnel, with an `outcome` of `opened`,
`already_up`, `closed`, `already_down`, `skipped` or `failed` (and a
`detail` saying why); `up` adds `saved` when `--save` saved the tunnel, or
`save_error` when it could not (the tunnel stays up; hpctun exits 2).
When some tunnels fail, every result is still printed and hpctun exits 1.
`up --json` never asks to save an ad-hoc tunnel.

`hpctun browse --json` prints one document: `version`, `result` (like one
`up` result), `browser` (the browser that was started), `url` (the page it
was given) and `warning` (for example, a window already open on another
proxy port). Each is null when it does not apply: when the tunnel failed to
open, `browser` and `url` are null, and a blank proxied window has no `url`.

When a command fails before it has anything to report (a broken config, a
missing argument), stdout holds
`{"version": 1, "error": {"message": "...", "exit": 2}}`. Mistakes caught
while reading the command line come before that and leave stdout empty
(exit 2): a flag hpctun does not know, a flag value it cannot read, or an
argument to `status`, which takes none. Treat "exit non-zero, stdout empty"
as a usage error and show stderr.

A field that does not apply is `null`, never missing. `version` stays 1
while fields are only added; read unknown fields and values as "something
newer" rather than failing.

`up` may still start the gateway connection, and ssh asks for your password
or second factor on the terminal. A program with no terminal passes
`--askpass` (or sets `HPCTUN_ASKPASS=1`): ssh then asks in a small window
instead, through `osascript` on a Mac and `zenity` or `kdialog` on Linux.
For a second factor such as Duo, the window shows the menu the server sent
(`1. Duo Push to ...`) above the question. hpctun never stores what you
type; a key in your ssh agent still saves you typing a passphrase.

```sh
hpctun --askpass login
hpctun --askpass up --json jupyter
```

# Menu-bar app

<sub>[↑ Contents](#contents)</sub>

`hpctun-tray` puts your tunnels in the macOS menu bar or the Linux system
tray. Its icon says whether you are logged in to the gateway: a filled
disc when every tunnel is up, half filled when some are down, an empty
ring when you are not logged in, struck through when something is wrong.
Its menu has:

- Your transfer host (such as `files`), and the login gateway when tunnels
  have their own: connected or not.
- **Log in** / **Log out**: your password or second factor is asked in a
  window (see `--askpass` above). Log in also shows while the gateway is
  up but the transfer host is not, and opens just what is missing.
- One line per tunnel, ticked when it is up. Click it to open or close
  it. A tunnel whose port another program holds is greyed out with that
  program's name.
- **Open all** and **Close all**: `hpctun up --all` and `hpctun down --all`.
- **Open <name> in browser**, one for each port forward and each SOCKS tunnel
  with a `url`: runs `hpctun browse`, opening the tunnel first if needed.
- The `hpctun code` job, when you have `[code]`.
- The last error, when something failed; click it to dismiss it.
- **Open config**, **Start at login** and **Quit**. Quitting leaves your
  tunnels open.

It runs `hpctun` for everything, so it shows exactly what `hpctun status`
shows, and a tunnel you open in a terminal appears in the menu within a
few seconds. Setting up (`hpctun init`, `hpctun add`) is still done in a
terminal.

On Linux, `hpctun-tray` is in the linux-amd64 archive next to `hpctun`;
run it once and tick **Start at login**. GNOME shows tray icons only with
the AppIndicator extension (Ubuntu has it on by default); KDE and most
other desktops show them as they are. The password window needs `zenity`
or `kdialog`.

On a Mac, unzip `hpctun-tray-<version>-darwin-universal.zip`, put `hpctun-tray.app`
in Applications and open it. It is not signed by a registered Apple
developer, so if a browser downloaded it, check the zip against `SHA256SUMS`
(see [Security notes](#security-notes)) and then clear the flag:
`xattr -dr com.apple.quarantine /Applications/hpctun-tray.app` (downloads
made with `curl` have no flag). To use the same hpctun from a terminal,
link it onto your PATH, for example
`ln -s /Applications/hpctun-tray.app/Contents/MacOS/hpctun
~/.local/bin/hpctun`; `hpctun self-update` then updates the app.

On Windows, `hpctun-tray.exe` is in the zip next to `hpctun.exe`; it sits
in the notification area, and "Start at login" adds it to the programs
Windows starts when you log in.

# Logging in and copying files

<sub>[↑ Contents](#contents)</sub>

`hpctun init` can also set up ssh so the hosts behind your gateway work with
plain `ssh`, `scp`, `sftp` and `rsync`. It asks for:

- the **gateway** (as for tunnels),
- an optional **transfer host**, such as a data-transfer node,
- optional **node patterns** such as `web*` or `node??`: hosts you log in to
  through the gateway.

and records them in the `[ssh]` table:

```toml
gateway = "shell"

[ssh]
user          = "you"
gateway_host  = "login.example.edu"   # hpctun defines "shell"; omit if it is your own alias
transfer      = "files"
transfer_host = "files.example.edu"   # omit if "files" is your own alias
nodes         = ["web*", "node??"]
```

Tunnel questions come after "Set up tunnels? (advanced)", which defaults to
No: most people only need logins and copies.

`[ssh]` section fields:

| Field | Required | Meaning |
|---|---|---|
| `user` | When hpctun writes ssh entries (`gateway_host`, `transfer_host`, `tunnel_gateway_host` or `nodes`) | Your login name, shared with every host hpctun defines. |
| `gateway_host` | No | The gateway's `HostName` when hpctun should define it. Omit if you have your own `gateway` alias in `~/.ssh/config`. |
| `transfer` | No | Name of the transfer host alias; with this, `hpctun cp` and `hpctun rsync` route through it. |
| `transfer_host` | No | The transfer host's `HostName` when hpctun should define the transfer alias. Omit if you configured it yourself. |
| `nodes` | No | Node name patterns (e.g. `web*`, `node??`): hosts `ssh` reaches through the gateway (`ProxyJump`). Aliases in your own `~/.ssh/config` that match are left alone. |
| `tunnel_gateway_host` | No | When set, hpctun defines `tunnel_gateway` as an alias with this `HostName` and its own `ControlPath`. Requires `tunnel_gateway`. |

From it hpctun writes `~/.ssh/hpctun_config` and adds one line, `Include
hpctun_config`, near the top of `~/.ssh/config`, with a backup in
`~/.ssh/config.bak-hpctun`. It always shows these changes as a diff first and
writes nothing until you approve; `hpctun ssh-config sync` with no terminal
prints the diffs and stops unless given `--yes`. Every host it defines shares one login
(`ControlMaster`), keeps it open (`ControlPersist yes`), and hands a key
passphrase to your ssh agent (`AddKeysToAgent yes`). Hosts you configured
yourself are left alone: the node rule excludes every alias in your own
config that matches a pattern.

```sh
ssh web1                    # through the gateway
scp files:/lab/data.csv .   # straight to the transfer host
hpctun cp node7:/scratch/run.log .
# → scp -J files node7:/scratch/run.log .
hpctun rsync -av results/ node7:/scratch/you/
# → rsync -e 'ssh -J files' -av results/ node7:/scratch/you/
hpctun sftp node7
```

`hpctun cp`, `hpctun rsync` and `hpctun sftp` take the real tool's arguments. For a
path on a node they hop through the transfer host and print the command they
ran; anything else runs unchanged.

`hpctun login` opens the gateway and transfer host connections up front, so
passphrase, password and 2FA prompts happen once rather than mid-copy
(`hpctun login files` opens just one). `hpctun logout` closes them; it will not
close the gateway while tunnels use it, and closing either one also ends any
ssh sessions and copies (`ssh`, `scp`, `sftp`, `rsync`, `hpctun cp`) riding that
connection. `hpctun status` shows both.

`hpctun ssh-config` shows what hpctun generated and checks, with `ssh -G`, that
ssh sees it that way. `hpctun ssh-config setup` asks the questions again;
after editing `[ssh]` by hand, `hpctun ssh-config sync` shows and applies the
changes to `hpctun_config`. To remove the setup, delete the `Include hpctun_config` line and
`~/.ssh/hpctun_config`.

## Tab completion

<sub>[↑ Contents](#contents)</sub>

`hpctun completion zsh|bash|fish --help` shows how to enable completion. Then
`hpctun cp node7:/scratch/<Tab>` lists remote files over the shared
connection, and `hpctun code proj<Tab>` lists folders in your cluster home.
Completion never prompts: run `hpctun login` (or any `ssh`) first
if your login needs a password or 2FA. zsh needs its completion system on
(`autoload -Uz compinit && compinit` in `~/.zshrc`), which macOS's default
zsh does not do; zsh's own `scp` completion needs it too.

# VS Code on a compute node

<sub>[↑ Contents](#contents)</sub>

`hpctun code` gives you VS Code running on a compute node, inside your own
Slurm job, so your editor, terminals, notebooks and language servers use
the job's CPUs instead of a shared machine. The job runs VS Code's tunnel on
the node, and VS Code connects through its Remote - Tunnels extension;
nothing connects to the node over ssh. hpctun itself needs `[ssh]` set up
and key login to the gateway (`hpctun ssh-config key`), and a `[code]` table
in your config. `hpctun init` asks "Set up VS Code on a compute node?" and
writes one for you (Yes is the default with `--from osu.toml`, which carries
an empty `[code]`); otherwise add it by hand. An empty one is enough:

```toml
[code]
# cpus = 4                 # defaults shown
# time = "24:00:00"        # sbatch --time
# start_within = "30m"     # a job that has not started by then is removed
# partition = "core"       # default: Slurm's default partition
# sbatch = ["--gres=gpu:1"]
# server_dirs = ["/scratch", "/data"]   # node-local disks for VS Code's server; else your home
```

Then:

```sh
hpctun code project            # ~/project on the cluster (Tab lists folders)
hpctun code /scratch/you/run1  # an absolute path is checked on the job's node, so node-local folders work
hpctun code                    # asks: a recent folder, your home, another, or an empty window
hpctun code -w chrom1          # on a node you choose (--node; Tab lists them)
hpctun code -A lpi             # a Slurm account (--account; default: one the partition takes)
hpctun code -- --gres=gpu:1    # anything after -- goes to sbatch
hpctun code --status           # the job, its node and time left
hpctun code --stop             # end the job
```

`hpctun code` checks the key, VS Code and its **Remote - Tunnels** extension
(offering to install it) before it submits anything, then waits for a node
and the tunnel, and opens VS Code on it. The job keeps running when VS Code
closes, until its time limit or `hpctun code --stop`; `hpctun status` shows
it. Running `hpctun code` again reuses the job, and a VS Code window still
open on that folder reconnects by itself: hpctun brings it forward rather
than opening a second one. A new job may be on another
node: files stay, terminals, running processes and node-local `/scratch` do
not.

One-time steps:

- The first time VS Code opens a tunnel, it asks you to sign in with
  Microsoft. Use your ONID (`you@oregonstate.edu`).
- The first time a job lands on a node, hpctun shows a device code: open
  the link, enter the code, and sign in with your ONID. Later jobs on that
  node need nothing. hpctun waits 20 minutes for you to finish.

Worth knowing:

- **One tunnel per node.** Each node's tunnel is named `<node>-<you>`
  (e.g. `chrom1-you`) and appears under Remote Explorer → Tunnels. A
  window stays with its node: a job elsewhere does not wake it. hpctun
  keeps the 8 most recently used nodes' tunnels registered.
- **Other architectures and accounts.** The job sets up its shell as a login
  on the node would, so aarch64 nodes get their own tools (no x86 paths from
  the login node). A partition that takes only some accounts gets one of
  yours that it takes, or name one: `hpctun code -p grace -A grace`.
- **After the job ends.** At the time limit or `--stop`, an open window can
  still look connected. When it tries to reconnect it says the tunnel
  cannot be found and to check you are signed in as the right user. That
  means the job is gone, not a sign-in problem: run `hpctun code` again
  (`hpctun code --status` shows `no job running`).
- **Notebooks.** Jupyter notebooks opened in VS Code run their kernel on the
  node, with no tunnel.
- **Web apps.** VS Code's Ports tab links (`*.devtunnels.ms`) fail with "431
  Request Header Fields Too Large" for OSU accounts. Start the app on
  `127.0.0.1` in your job (Jupyter does this by default) and open it with
  `hpctun up <port> --host <node> --loopback`. Avoid `0.0.0.0` (all
  addresses, such as Jupyter's `--ip=0.0.0.0`): other cluster users can
  then connect to the app directly, so it would need its own token or
  password. (A later version handles this for you.)
- **What hpctun keeps on the cluster.** `~/.local/state/hpctun/vscode`
  (sign-ins per node, the tunnel record, job logs) and
  `~/.cache/hpctun/vscode` (VS Code's CLI), following `XDG_STATE_HOME` and
  `XDG_CACHE_HOME`. VS Code's server cache goes on the node's own disk
  (`/scratch/<you>/hpctun-vscode`, else `/data/<you>/hpctun-vscode`, else
  your home); `server_dirs` changes the list.
- **License.** hpctun starts the tunnel with `--accept-server-license-terms`,
  which accepts the VS Code Server License Terms
  (https://aka.ms/vscode-server-license) and the Microsoft Privacy
  Statement (https://privacy.microsoft.com/en-US/privacystatement). The
  first `hpctun code` on each computer shows both and asks whether you
  accept them; hpctun keeps your answer in its state directory on that
  computer. With no recorded answer and no terminal to ask in,
  `hpctun code` stops and says so.
- **Microsoft's service.** VS Code on your computer reaches the job
  through Microsoft's dev tunnels service, so your files, terminals and
  edits pass through it, and the tunnel's sign-in is kept in your home
  directory on the cluster. Check your institution's rules before
  working with restricted data (for example health records or
  export-controlled data) this way.
- **Cluster side.** Compute nodes need outbound HTTPS to Microsoft's tunnel
  service, which the site proxy provides.

### VS Code settings for the cluster

`hpctun vscode-settings` adds the VS Code settings that spare the cluster's
shared file systems (search limits, file watcher excludes) to
`~/.vscode-server/data/Machine/settings.json` on the cluster. VS Code
shows them under the Remote tab of its settings editor.

```sh
hpctun vscode-settings --dry-run   # show what would be added
hpctun vscode-settings             # show it, ask, then write
```

Your own settings are never changed: hpctun only adds the ones you lack,
keeps your comments, and saves the old file as `settings.json.bak-<time>`
next to it. It talks only to the gateway. `--yes` writes without asking.

A site can add its own settings, or change or remove hpctun's, with a
`[vscode]` table:

```toml
[vscode]
settings = "/path/on/the/cluster/vscode.jsonc"   # absolute, or ~/...
# defaults = false    # use only the site's settings (needs settings)
```

`hpctun init --from` copies this table. `examples/` in the hpctun source
has a site file and the rules for writing one.

# Sharing a team config

<sub>[↑ Contents](#contents)</sub>

The gateway, tunnels and node patterns are not user-specific, so a team can
keep one `config.toml` in a shared repo. The `[ssh]` table is personal,
though: `ssh.user` is your login name, and when it defines the gateway or
transfer host's `HostName` itself, every host hpctun writes uses it too. A
shared file with no `[ssh]` table can be used straight:

```sh
export HPCTUN_CONFIG=~/work/team-infra/hpctun/config.toml
```

A shared config is trusted like code: the hosts it names are what ssh
connects to and runs through. hpctun refuses values that ssh would read as
options, but point `HPCTUN_CONFIG` only at a repository whose committers you
trust, and review changes to it as you would changes to a script.

A shared file also carries each tunnel's `url` as written, tokens included, so
keep passwords and tokens out of the URLs you save (see
[Scripting with --json](#scripting-with---json)).

A team file that does carry `[ssh]` (hosts and node patterns, for example)
should instead seed a personal config, keeping its gateway and tunnels as
the wizard's defaults:

```sh
hpctun init --from ~/work/team-infra/hpctun/config.toml
```

`--from` always asks for your own username rather than taking it from the
file, so the seeded `[ssh]` table is yours, not the file's.
A seed file may therefore leave `ssh.user` out, as the shipped `osu.toml`
does, even with a `[code]` table; a `config.toml` hpctun reads day to day
may not. A seed's `[code]` is kept only if you answer Yes to the hpctun
code question, and its tunnels only if you answer Yes to "Set up tunnels?
(advanced)", which defaults to No even when the seed has tunnels.

# How it works

<sub>[↑ Contents](#contents)</sub>

Every tunnel rides one persistent ssh connection — the **gateway master**.
On each run `hpctun` resolves your gateway with `ssh -G` and picks one of two
modes, which `hpctun status` shows in its header:

- **`ssh-config`** — your `~/.ssh/config` already sets `ControlMaster` and
  `ControlPath` for the gateway, so `hpctun` uses that master as-is and your
  settings apply unchanged.
- **`managed`** — it does not, so `hpctun` runs its own master with its own
  options (`ControlMaster=auto`, its own `ControlPath` in the state
  directory, `ControlPersist`, and keepalives). You do not have to touch
  your ssh config at all.

State lives in `$XDG_STATE_HOME/hpctun/`, else `~/.local/state/hpctun/`:
`state.json` (what cannot be observed from outside: which ssh pid owns each
jump tunnel, the exact forward specs needed to cancel them), its lock file,
the managed control socket, and the stderr logs of backgrounded ssh
(`master.log`, `jump-<port>.log`). Everything else — what is listening, who
owns it, who is connected — is observed fresh with `lsof` on every command,
so the table cannot drift from reality.

hpctun creates the state directory private (mode 700) and refuses to use one
that another user owns or can write to, because the control socket and the
password-window helper (`askpass.sh`) live there.

`hpctun up` is idempotent and safe to re-run: already-open tunnels are left
alone, and after sleep, a dropped VPN or a dead network, `hpctun up --all`
restarts the master if needed and restores every tunnel. A stale control
socket (a socket file whose master is gone) is detected and removed
automatically. Concurrent `hpctun` runs serialise on a lock file.

# Recommended ssh settings

<sub>[↑ Contents](#contents)</sub>

If your gateway is a `Host` alias from your own `~/.ssh/config`, these
settings make it a good tunnel gateway. (Pointing `hpctun` at a plain hostname
with no matching block gets you managed mode, where `hpctun` passes all of this
itself.)

- **Keep the tunnel master separate from your interactive one.** Give
  tunnels their own alias with its own `ControlPath`, so logging out of — or
  restarting — either connection never drops the other:

  ```
  Host hpc-interactive
      HostName gw.example.net
      User you
      ControlMaster auto
      ControlPath ~/.ssh/sockets/%C
      ServerAliveInterval 60
      ServerAliveCountMax 30     # long window: ride out stalls
  Host hpc-tunnel
      HostName gw.example.net
      User you
      ControlMaster auto
      ControlPath ~/.ssh/sockets/tunnel-%C
      ControlPersist yes
      ServerAliveInterval 60
      ServerAliveCountMax 10     # ~10 minute window: die faster
  ```

  Use `hpc-tunnel` as `gateway` in your hpctun config. Managed mode does this
  separation for you automatically.

- **Use `ControlMaster auto`, never `yes`.** With `auto`, plain `ssh`
  connections to the alias reuse the master. With `yes`, every later
  connection (including `ssh -J`) *sees* the socket, disables multiplexing
  and opens its own connection — the opposite of what you set it for.

- **The keepalive window (`ServerAliveInterval` × `ServerAliveCountMax`) is
  a tradeoff.** A long window rides out cellular and CGNAT stalls, which
  suits interactive sessions. A short window — around 5–10 minutes total —
  lets a dead tunnel master actually exit, so `hpctun up --all` can start a
  fresh one; with a long window a hung master keeps tunnels dead until the
  window finally trips. A `Host *` default of `ServerAliveInterval 30`
  covers hosts with no block of their own.

- **`Host` aliases do not match full hostnames.** `Host shell.hpc` does not
  apply to `ssh shell.hpc.oregonstate.edu`; a pattern matches the name you
  typed, so list both spellings on the `Host` line if you use both.

- **Use `%C` in `ControlPath`** (`ControlPath ~/.ssh/sockets/%C`) to keep
  socket paths short: macOS `unix` socket paths top out at 104 bytes, and
  `%r@%h-%p` with a long hostname can exceed that.

- **Windows has no ControlMaster.** OpenSSH for Windows does not support
  `ControlMaster`/`ControlPath` and ignores them. Keep multiplexing settings
  in their own block (or file) so a config you share with Windows users can
  leave those blocks out. On Windows, `hpctun` writes its entries without them;
  see [Windows](#windows).

- **To check a master by hand**, run `ssh -o ControlMaster=no -O check
  <alias>`. Without `-o ControlMaster=no`, checking an alias configured with
  `ControlMaster yes` may open a new session instead of reporting on the
  master.

`hpctun status` helps you audit this: in ssh-config mode it prints `hint:`
lines under the gateway header when it sees `ControlMaster yes`, no
`ServerAliveInterval`, or a keepalive window over 30 minutes.

# Troubleshooting

<sub>[↑ Contents](#contents)</sub>

- **See what `hpctun` is doing:** run it with `-v` — every `ssh` and `lsof`
  command line is printed to stderr before it runs.

- **Exit codes:** `0` every requested action succeeded or was already
  satisfied; `1` at least one action failed (the others were still
  attempted); `2` usage or config errors — bad flags, invalid config, no
  gateway configured.

- **`conflict` on a port** means your local port is held by something other
  than the process `hpctun` expects: another program, or an ssh you did not
  open with `hpctun`. A note under the table names the owning pid and command.
  Free the port (or move the tunnel) and re-run.

- **Master or jump tunnel fails to open:** the backgrounded `ssh` wrote its
  stderr to a log — read `~/.local/state/hpctun/master.log` for the gateway
  connection or `~/.local/state/hpctun/jump-<port>.log` for a jump tunnel.
  (`hpctun` prints the log for you when the command it launched fails. Prompts
  like passphrases still reach your terminal because `ssh` writes them to
  `/dev/tty`, which is why you may see a prompt and *then* a log-based
  error.)

- **Stale control socket:** if `ssh -O check` reports `Connection refused`
  against a socket file that still exists, `hpctun` removes the socket and
  starts a fresh master — no manual cleanup needed. If `hpctun` instead refuses
  with "control socket path too long", set `XDG_STATE_HOME` to a short
  directory of your own in your home, for example
  `export XDG_STATE_HOME=~/.hst` (the socket path must stay under macOS's
  104-byte limit). Do not use `/tmp`: other users can create files there.

- **`state directory ... can be changed by other users`** (or `belongs to
  another user`): hpctun keeps its control sockets and password-window
  helper there, so it must be yours alone. Run `chmod 700` on it, or set
  `XDG_STATE_HOME` to a directory of your own in your home.

- **Known limitation — ssh-config mode without `ControlPersist`:** if your
  ssh config sets `ControlMaster` but no `ControlPersist`, the master may be
  an ordinary interactive session you opened earlier, and every master- or
  socks-route tunnel on it closes when that session ends. Add
  `ControlPersist yes` (or let `hpctun` manage the master) to make forwards
  independent of any shell.

- **Missing `lsof`:** `hpctun` exits with an error naming it; install `lsof`.

- **`waiting for another hpctun command to finish...`** another `hpctun` is
  running (they serialise); let it finish.

# Example: OSU HPC

<sub>[↑ Contents](#contents)</sub>

> To start an OSU config, use the `osu.toml` shipped in the release
> (`hpctun init --from osu.toml`), then add tunnels with `hpctun add`. The
> example below is a different, hand-written setup (your own `hpc-tunnel`
> alias as the gateway); use it as a shape, not on top of `osu.toml`.

Oregon State's HPC login node is `shell.hpc.oregonstate.edu`; compute jobs
run on nodes like `aspen1`. Jupyter on a compute node binds to loopback,
so it needs the `jump` route; start other web apps, such as a dashboard, on
loopback too and give them the same route (an app listening on all
interfaces can be reached by everyone on the cluster). An ssh alias keeps
the username out of the hpctun config:

```
# ~/.ssh/config
Host hpc-tunnel
    HostName shell.hpc.oregonstate.edu
    User yourusername
    ControlMaster auto
    ControlPath ~/.ssh/sockets/tunnel-%C
    ControlPersist yes
    ServerAliveInterval 60
    ServerAliveCountMax 10
```

```toml
# ~/.config/hpctun/config.toml
gateway = "hpc-tunnel"
default_host = "aspen1"

[tunnel.dash]        # a dashboard started on 127.0.0.1 on aspen1
port = 8099
route = "jump"

[tunnel.jupyter]     # jupyter binds 127.0.0.1 only: needs the jump route
port = 4001
route = "jump"

[tunnel.web]
port = 43723
host = "web1"
route = "jump"

[ssh]
user          = "you"
transfer      = "files"
transfer_host = "files.hpc.oregonstate.edu"
nodes         = ["web*"]
```

```sh
hpctun up --all
hpctun status
```

Then `http://localhost:4001` reaches Jupyter on your compute node and
`http://localhost:8099` the dashboard. After your lease ends or the network
flaps, `hpctun up --all` puts everything back.

# Logging in with a key

<sub>[↑ Contents](#contents)</sub>

`hpctun ssh-config key` sets up password-free logins to your gateway:

1. It uses `~/.ssh/id_ed25519`, or creates it; `ssh-keygen` asks you for a
   passphrase, which protects the key if your computer is lost.
2. It adds the key to the ssh agent; `ssh-add` asks for the passphrase
   once. On macOS the passphrase also goes into the Keychain, and for the
   hosts hpctun defines in `~/.ssh/hpctun_config`, ssh reads it from there, so it
   is not asked again, even after a reboot. (For a gateway you defined
   yourself, add `UseKeychain yes` under its `Host` line.)
3. It installs the public key on the gateway (you type your password there
   one last time) and checks that the gateway now accepts the key.

hpctun does not handle your passphrase or password here: `ssh-keygen`, `ssh-add`
and `ssh` ask for them themselves. Running the command again is safe; it skips
what is already done. `hpctun init` and `hpctun ssh-config setup` offer it at the
end when the gateway does not accept a key yet. Because nodes and the
transfer host share your home directory, they accept the key too. For
another host, `hpctun ssh-config key <host>`.

On Windows, enable the ssh-agent service first (see [Windows](#windows)).

The Keychain is Apple's ssh only. When another ssh comes first on your
PATH (from conda or Homebrew, say), it asks for the passphrase once after
each reboot, and the agent keeps it from then on. hpctun_config starts with
`IgnoreUnknown UseKeychain` so that such an ssh skips the Keychain setting,
but ssh uses only the first `IgnoreUnknown` it reads: if `~/.ssh/config`
has its own above the `Include hpctun_config` line, add `UseKeychain` to
it. `hpctun ssh-config` warns when this is needed.

# Windows

<sub>[↑ Contents](#contents)</sub>

Download `hpctun-<version>-windows-amd64.zip` (or `-arm64` on ARM laptops) as in
[Install](#install), unzip it, and put `hpctun.exe` somewhere on your PATH.
Windows may warn that the program is unrecognized; choose "More info" and
"Run anyway".

Then open a new PowerShell (one opened before you changed PATH does not
see the change), go to the unzipped folder, and run
`hpctun.exe init --from osu.toml` (elsewhere, `hpctun init`). It asks for your gateway, an optional
transfer host and the hosts you reach through the gateway, shows the
changes to `%USERPROFILE%\.ssh`, and writes them once you approve. hpctun's
own config lives in `%APPDATA%\hpctun\config.toml`. Afterwards `ssh node1`
and VS Code Remote-SSH reach your nodes through the gateway, and
`hpctun cp node1:/path .` copies through the transfer host.
`hpctun code` works too: it needs no shared connection.

OpenSSH for Windows cannot share one login between connections, so every
new connection signs in on its own. A key in the ssh agent spares you the
password; only a second factor, if your site asks for one, remains. The
agent is a Windows service that starts disabled: enable it once in an
administrator PowerShell,

    Get-Service ssh-agent | Set-Service -StartupType Automatic; Start-Service ssh-agent

and `AddKeysToAgent` (which hpctun writes) keeps your key in it across
reboots. `hpctun ssh-config` notes when the agent is not running.

## Tunnels on Windows

<sub>[↑ Contents](#contents)</sub>

OpenSSH for Windows has no connection sharing, so each tunnel is its own
ssh, running hidden in the background; closing the terminal you opened it
from leaves it up. It signs in with no terminal to ask on, so it needs a
key in the ssh agent (see [Logging in with a key](#logging-in-with-a-key))
and the gateway's host key already accepted (connect once with
`ssh <gateway>`). `hpctun login` checks both:

    hpctun login

says, for the gateway and the transfer host, whether ssh signs in without
asking for anything, and what to do if not. `hpctun up`, `down`, `status`,
`save` and `add` then work as elsewhere. `hpctun logout` closes the tunnels
(there is no shared connection to end). When the network drops or the
laptop sleeps, a tunnel's ssh ends within about a minute and a half;
`hpctun status` shows it down and `hpctun up --all` opens it again.

`hpctun browse` and `hpctun-tray` work there too.

# Security notes

<sub>[↑ Contents](#contents)</sub>

- **Passwords and keys.** hpctun stores no passwords or keys. Logins are
  made by your own OpenSSH, with your keys and ssh-agent. With the
  menu-bar app or `--askpass`, ssh asks for a password or second factor
  through `hpctun askpass`, which shows a window and passes what you type
  straight to ssh; it is not written anywhere.
- **An open connection can be reused.** The shared connection stays open
  in the background (`ControlPersist`). While it is open, any program
  running as you on your computer can use it to reach the cluster
  without a password or second factor. `hpctun logout` closes it.
- **Local ports have no password of their own.** A tunnel's local port
  (`localhost:<port>`) and the SOCKS proxy can be used by any program on
  your computer, and by other people logged in to the same computer. Web
  pages you visit can also try to reach them. Keep your apps' own logins
  on (Jupyter's token, for example), and close tunnels you no longer need.
- **A shared config is trusted like code.** See
  [Sharing a team config](#sharing-a-team-config).
- **VS Code (`hpctun code`).** hpctun runs Microsoft's VS Code CLI in your
  Slurm job. Your editor reaches it through Microsoft's dev tunnels
  service, so your files, terminals and edits pass through that service,
  and the CLI's sign-in is kept in your home directory on the cluster
  (`~/.local/state/hpctun/vscode`). The first `hpctun code` on each
  computer asks you to accept the VS Code Server License Terms. Check
  your institution's rules before working with restricted data this way.
- **Signed releases.** Each release's `SHA256SUMS` is signed with
  hpctun's release key, and `hpctun self-update` refuses a release whose
  signature does not match the public key built into hpctun. To check a
  download by hand, compare its checksum with the line in `SHA256SUMS`
  (`shasum -a 256 <file>` on a Mac, `sha256sum <file>` on Linux,
  `Get-FileHash <file>` in PowerShell). From a source checkout,
  `go run ./scripts/sign -verify SHA256SUMS` checks the signature of that
  list (with `SHA256SUMS.sig` next to it).
- **No Apple or Microsoft signature.** The macOS and Windows programs are
  not signed with an Apple or Microsoft developer certificate, so macOS
  and Windows warn about them, or block them when a browser downloaded
  them. Download with `curl` as shown in [Install](#install), or check the
  file as above before you clear the warning.

# How hpctun is made

<sub>[↑ Contents](#contents)</sub>

hpctun is written with AI coding agents, working under its maintainer's
direction:

- **Claude Code** (Anthropic) drafts the designs and implementation plans,
  writes most of the code and tests, and coordinates the work.
- **Codex** (OpenAI), along with separate Claude sessions, reviews each
  change against its plan before it lands. Some changes are implemented by
  **OpenCode** running DeepSeek models.
- **The maintainer** sets the requirements, approves every design and plan,
  rules on review findings, and tests releases on Oregon State's HPC
  cluster.

Every change passes the test suite (`scripts/check.sh`), and the public tree
is checked for private information (`scripts/publiccheck`). Before the first
public release, the code had a security review: two independent AI reviews
plus automated vulnerability scanning, with every finding fixed or
documented.

AI-written code can still contain mistakes. Please report problems as GitHub
issues, and report security problems privately (see
[SECURITY.md](SECURITY.md)).

# No warranty

<sub>[↑ Contents](#contents)</sub>

hpctun is provided under the BSD 3-Clause License, "as is", without warranty
of any kind (see [LICENSE](LICENSE)). It is not an official service of Oregon
State University. It runs your own OpenSSH with your own credentials and
changes files only in your home directory. You are responsible for using it
within your cluster's and institution's policies.

# License

<sub>[↑ Contents](#contents)</sub>

BSD 3-Clause; see [LICENSE](LICENSE).
