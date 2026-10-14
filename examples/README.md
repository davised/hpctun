# Examples for sites

Files a site can copy to the cluster and hand out. hpctun does not use
them.

## VS Code remote settings

VS Code keeps "machine" settings on the remote side, in
`~/.vscode-server/data/Machine/settings.json`. They apply to every window
connected there, whichever laptop it runs on, and the settings editor
shows them under the "Remote [SSH: <host>]" tab. That makes them the place
for a site's advice on search and file watching, which is about the
cluster's file systems rather than the laptop.

`hpctun vscode-settings` does this for hpctun users. It adds hpctun's
built-in settings (`internal/vscode/defaults.jsonc` in the source), then
the site's file named by `[vscode] settings` in the site's hpctun config,
to the user's file on the cluster. `hpctun init --from` hands that config
to every user. hpctun works through the gateway, shows a diff and keeps a
backup. The built-in settings are generic ones that cut NFS load (search
limits, no search on type, no terminal file links on remotes, and file
watcher and search excludes for environments, caches and workflow
directories). Nothing in them is site specific.

For what each setting does, see VS Code's settings documentation:
https://code.visualstudio.com/docs/configure/settings

- `site-vscode-settings.jsonc`: an example site file. It overrides one of
  hpctun's settings, uses `null` to remove a setting and an entry of an
  object setting, and adds a setting of the site's own. Put the file on
  the cluster and name it in `[vscode] settings`. With `defaults = false`
  only the site's file is used.
- `merge-vscode-settings.py`: for users who do not run hpctun. It follows
  the same rules and needs only Python 3.

The rules, for both tools: the user's values always win. A later source
wins over an earlier one. `null` in a source removes that setting (or that
entry of an object setting) from what earlier sources add; it never
removes anything from the user's file. Only top-level settings, and
entries of an object setting the user already has, are added.

VS Code has no way to include a shared settings file, and a symlink to one
would be replaced the first time the user changes a setting in the editor.
So each user gets their own copy, merged into what they already have:

```sh
python3 merge-vscode-settings.py --dry-run \
    ~/.vscode-server/data/Machine/settings.json \
    ../internal/vscode/defaults.jsonc site.jsonc
```

Drop `--dry-run` to write the file. The script backs the user's file up to
`settings.json.bak-<time>` before changing it and writes it back as plain
JSON: comments are only kept in the backup. (`hpctun vscode-settings` keeps
them.) When nothing is missing, it changes nothing.
