// Package sshkey sets up key login to a host: an ed25519 key with a
// passphrase, held by the ssh agent, installed on the host and proven by a
// key-only login. hpctun never sees the passphrase or password: the tools that
// ask for them run on the user's terminal.
package sshkey

// InstallScript runs on the host as sh -c '...' (so any login shell, tcsh
// included, passes it through: it contains no single quote). It reads one
// public key line on stdin and appends it to ~/.ssh/authorized_keys unless
// that exact line is there. It creates ~/.ssh (0700) and the file (0600),
// removes group and world write from both (sshd's StrictModes), and ends a
// last line that lacks its newline so the key does not merge into it.
const InstallScript = `umask 077; mkdir -p ~/.ssh && chmod go-w ~/.ssh && touch ~/.ssh/authorized_keys && ` +
	`chmod go-w ~/.ssh/authorized_keys && IFS= read -r k && f=~/.ssh/authorized_keys && ` +
	`if [ -s "$f" ] && [ -n "$(tail -c1 "$f")" ]; then echo >>"$f"; fi && ` +
	`{ grep -qxF -- "$k" "$f" || printf "%s\n" "$k" >>"$f"; }`
