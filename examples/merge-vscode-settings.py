#!/usr/bin/env python3
"""Add default VS Code settings to a settings.json without replacing any.

Usage: merge-vscode-settings.py [--dry-run] SETTINGS DEFAULTS [DEFAULTS...]

SETTINGS is the file to update, for example the remote machine settings
~/.vscode-server/data/Machine/settings.json; it is created if missing.
Each DEFAULTS file is JSONC (comments and trailing commas allowed), and a
later one wins over an earlier one, so a site file can follow a generic one.
Only keys missing from SETTINGS are added; inside object settings such as
files.watcherExclude, only missing entries. The user's values always win.
A null in a DEFAULTS file removes that setting, or that entry of an object
setting, from what earlier files add; it never removes anything from
SETTINGS. Only top-level settings and entries of object settings are added,
as hpctun vscode-settings does.

SETTINGS is backed up to SETTINGS.bak-<time> before it is changed, and is
written back as plain JSON, so its comments are only kept in the backup.
Tested with Python 3.9; needs nothing outside the standard library.
"""

import json
import os
import sys
import time


class JSONCError(ValueError):
    pass


def strip_jsonc(text):
    """Return text as plain JSON, and whether it had comments.

    Removes a leading BOM, // and /* */ comments, and trailing commas,
    leaving string contents alone.
    """
    if text.startswith("﻿"):
        text = text[1:]
    out = []
    had_comments = False
    i, n = 0, len(text)
    while i < n:
        c = text[i]
        if c == '"':
            j = i + 1
            while j < n and text[j] != '"':
                j += 2 if text[j] == "\\" else 1
            out.append(text[i:j + 1])
            i = j + 1
        elif text.startswith("//", i):
            had_comments = True
            j = text.find("\n", i)
            i = n if j < 0 else j
        elif text.startswith("/*", i):
            had_comments = True
            j = text.find("*/", i + 2)
            if j < 0:
                raise JSONCError("unterminated /* comment")
            i = j + 2
        else:
            out.append(c)
            i += 1
    # Drop commas that only whitespace separates from a closing bracket.
    # Strings are copied whole, so scan the same way again.
    plain = "".join(out)
    res = []
    i, n = 0, len(plain)
    while i < n:
        c = plain[i]
        if c == '"':
            j = i + 1
            while j < n and plain[j] != '"':
                j += 2 if plain[j] == "\\" else 1
            res.append(plain[i:j + 1])
            i = j + 1
            continue
        if c == ",":
            j = i + 1
            while j < n and plain[j] in " \t\r\n":
                j += 1
            if j < n and plain[j] in "}]":
                i += 1
                continue
        res.append(c)
        i += 1
    return "".join(res), had_comments


def load(path, missing_ok=False):
    """Read path as a JSONC object; return (dict, text, had_comments)."""
    try:
        with open(path, encoding="utf-8") as f:
            text = f.read()
    except FileNotFoundError:
        if missing_ok:
            return {}, None, False
        raise
    plain, had_comments = strip_jsonc(text)
    if not plain.strip():
        return {}, text, had_comments
    try:
        value = json.loads(plain)
    except json.JSONDecodeError as e:
        raise JSONCError(str(e))
    if not isinstance(value, dict):
        raise JSONCError("not a JSON object")
    return value, text, had_comments


def overlay(base, top):
    """Return base with top's values on top, merging objects entry by entry.

    A None (JSON null) in top removes that key; the result holds no None.
    """
    res = dict(base)
    for k, v in top.items():
        if v is None:
            res.pop(k, None)
        elif isinstance(v, dict):
            res[k] = overlay(res[k] if isinstance(res.get(k), dict) else {}, v)
        else:
            res[k] = v
    return res


def add_missing(user, defaults):
    """Add to user what defaults has and user lacks: top-level settings, and
    entries of an object setting the user has. Return the added paths."""
    added = []
    for k, v in defaults.items():
        if k not in user:
            user[k] = v
            added.append(k)
        elif isinstance(v, dict) and isinstance(user[k], dict):
            for ek, ev in v.items():
                if ek not in user[k]:
                    user[k][ek] = ev
                    added.append(k + " > " + ek)
    return added


def main(argv):
    dry_run = "--dry-run" in argv
    args = [a for a in argv if a != "--dry-run"]
    if len(args) < 2 or any(a.startswith("-") for a in args):
        print(__doc__.strip(), file=sys.stderr)
        return 2
    target, sources = args[0], args[1:]

    defaults = {}
    for src in sources:
        try:
            value, _, _ = load(src)
        except (OSError, JSONCError) as e:
            print("%s: %s" % (src, e), file=sys.stderr)
            return 1
        defaults = overlay(defaults, value)

    try:
        user, text, had_comments = load(target, missing_ok=True)
    except (OSError, JSONCError) as e:
        print("%s: %s; nothing changed" % (target, e), file=sys.stderr)
        return 1

    added = add_missing(user, defaults)
    if not added:
        print("%s already has every setting; nothing changed" % target)
        return 0
    verb = "would add" if dry_run else "added"
    print("%s %s:" % (verb, target))
    for a in added:
        print("  " + a)
    if dry_run:
        return 0

    if text is not None:
        backup = "%s.bak-%s" % (target, time.strftime("%Y%m%d-%H%M%S"))
        with open(backup, "w", encoding="utf-8") as f:
            f.write(text)
        print("backup: " + backup)
        if had_comments:
            print("note: comments in %s were not kept; they are in the backup" % target)
    else:
        os.makedirs(os.path.dirname(target) or ".", exist_ok=True)
    tmp = target + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(user, f, indent="\t", ensure_ascii=False)
        f.write("\n")
    os.replace(tmp, target)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
